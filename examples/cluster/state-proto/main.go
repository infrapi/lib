// A key-value value of your own: a protobuf message that implements dskit's
// Mergeable, stored in the same gossip store the rings use.
//
//	cd examples/cluster/state-proto && go run .
//
// fleet.proto is the wire format, fleet.go is the type, and fleet_test.go is
// the part worth copying: a Mergeable whose merge is not idempotent,
// commutative and associative will silently leave replicas in different states,
// and only property tests catch that.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gogo/protobuf/proto"
	"github.com/grafana/dskit/kv/codec"

	"github.com/infrapi/lib/examples/cluster/internal/demo"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/config"
	"github.com/infrapi/lib/pkg/listener"
	infraserver "github.com/infrapi/lib/pkg/server"
)

// codecID is what the client is looked up by, and what travels with every
// value: once a deployment is running, it is as good as frozen.
const codecID = "fleet-state"

func main() {
	appConfig, clusterConfig := demo.Config("app.env")

	// The factory hands the decoder an empty value to unmarshal into.
	fleetCodec := codec.NewProtoCodec(codecID, func() proto.Message { return NewFleetState() })

	c, stop := demo.Start(appConfig, clusterConfig, cluster.Options{
		Codecs: []codec.Codec{fleetCodec},
	})
	defer stop()

	client, err := c.KVClient(codecID)
	if err != nil {
		log.Fatalf("KVClient: %v", err)
	}

	// No deadline: this replica stays up until it is interrupted, so the stored
	// key can be looked at. The compare-and-swaps below are quick either way.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const key = "fleet"
	me := c.InstanceID()

	fmt.Printf("store %s, codec %q, replica %s\n\n", c.Store(), codecID, me)

	// --- Publishing our own entry.
	//
	// Compare-and-swap, never a blind write: every replica may be writing, and
	// the store is the only arbiter. A replica writes only its own entry, which
	// is what makes two copies of this value mergeable without a coordinator.
	//
	// Each successful write bumps the key's version, which is node-local: it
	// counts the updates this replica applied, its own included. That is why
	// the version climbs below even with nobody else in the cluster.
	//
	// And a write has to change something. dskit calls Merge with the state the
	// function returned; if the merge reports no change, the whole CAS is
	// treated as a failure, slept on for a second and retried, ten times, and
	// then returns an error. So decide before calling rather than calling to
	// find out: read, compare, and only CAS when there is something to write.
	publish := func(payload string) {
		err := client.CAS(ctx, key, func(existing any) (any, bool, error) {
			state := asFleetState(existing)
			state.Entries[me] = Entry{Payload: payload, UpdatedAtMs: time.Now().UnixMilli()}
			return state, true, nil
		})
		if err != nil {
			log.Fatalf("CAS: %v", err)
		}
	}

	publish("starting")
	show(ctx, client, key, "after publishing once")

	publish("ready")
	show(ctx, client, key, "after publishing again, newer timestamp wins")

	// --- Another replica's entry, written here so the example has two.
	err = client.CAS(ctx, key, func(existing any) (any, bool, error) {
		state := asFleetState(existing)
		state.Entries["replica-2"] = Entry{Payload: "from elsewhere", UpdatedAtMs: time.Now().UnixMilli()}
		return state, true, nil
	})
	if err != nil {
		log.Fatalf("CAS: %v", err)
	}
	show(ctx, client, key, "with a second owner")

	// --- Deleting one.
	//
	// The compare-and-swap returns a state without the entry. Merge is called
	// with localCAS true, so instead of disappearing the entry becomes a
	// tombstone: gossiped as gone, because a replica that has not heard yet
	// would otherwise hand it back.
	err = client.CAS(ctx, key, func(existing any) (any, bool, error) {
		state := asFleetState(existing)
		delete(state.Entries, "replica-2")
		return state, true, nil
	})
	if err != nil {
		log.Fatalf("CAS: %v", err)
	}
	show(ctx, client, key, "after deleting the second owner")

	fmt.Println("The deleted owner is absent from every read above, because the")
	fmt.Println("store calls RemoveTombstones with a zero limit before handing the")
	fmt.Println("value over. It is still in the store, still being gossiped, until")
	fmt.Println("INFRAPI_CLUSTER_OBSOLETE_ENTRIES_TIMEOUT has passed.")
	fmt.Println()

	// The replica stays up from here, serving its own status pages, so that the
	// key can be looked at as the store actually holds it.
	serve(ctx, c, appConfig, clusterConfig.KvPrefix+key, func() { publish("still here") })
}

// serve mounts the status pages, keeps republishing so the stored value visibly
// moves, and waits for an interrupt.
func serve(ctx context.Context, c *cluster.Cluster, appConfig *config.AppConfig, storedKey string, republish func()) {
	gin.SetMode(gin.ReleaseMode)

	engine, err := infraserver.NewServerGin(appConfig)
	if err != nil {
		log.Fatalf("NewServerGin: %v", err)
	}
	if err := c.RegisterGin(engine, cluster.HandlerOptions{}); err != nil {
		log.Fatalf("RegisterGin: %v", err)
	}

	socket, err := listener.Listen(appConfig)
	if err != nil {
		log.Fatalf("Listen: %v", err)
	}
	srv := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(socket) }()

	// The key as the store holds it: the configured prefix, then ours.
	escaped := url.QueryEscape(storedKey)
	base := fmt.Sprintf("http://%s", socket.Addr())

	fmt.Printf("The store holds it under %q. Still up, so you can look:\n\n", storedKey)
	fmt.Printf("  %s%s\n", base, cluster.MemberlistPath)
	fmt.Printf("      the Key-Value Store table: codec, node-local version, encoded size\n\n")
	fmt.Printf("  %s%s?viewKey=%s&format=json-pretty\n", base, cluster.MemberlistPath, escaped)
	fmt.Printf("      the decoded value, as JSON through this type's own tags\n\n")
	fmt.Printf("  %s%s?viewKey=%s&format=struct\n", base, cluster.MemberlistPath, escaped)
	fmt.Printf("      the same, as the Go value the codec produced\n\n")
	fmt.Printf("  %s%s?downloadKey=%s\n", base, cluster.MemberlistPath, escaped)
	fmt.Printf("      the bytes on the wire: snappy over protobuf\n\n")
	fmt.Println("Republishing every 5s with a fresh timestamp, so each write is a")
	fmt.Println("real change and the version climbs. The version is node-local and")
	fmt.Println("counts the updates this replica applied, its own included: it moves")
	fmt.Println("with nobody else in the cluster, and two replicas disagreeing on it")
	fmt.Println("is normal.")
	fmt.Println()
	fmt.Println("Ctrl+C to leave the cluster.")

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-signals:
			fmt.Println("\nleaving...")

			shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutCancel()
			_ = srv.Shutdown(shutCtx)
			return

		case <-ctx.Done():
			return

		case <-ticker.C:
			republish()
		}
	}
}

// asFleetState is the shape every compare-and-swap starts with: the store hands
// over nil the first time, and a decoded value afterwards.
func asFleetState(existing any) *FleetState {
	state, ok := existing.(*FleetState)
	if !ok || state == nil {
		return NewFleetState()
	}

	// The store hands out a copy, so this is ours to modify. Taking it rather
	// than building a fresh one is what keeps other replicas' entries.
	if state.Entries == nil {
		state.Entries = map[string]Entry{}
	}
	return state
}

// show reads the value back and prints it, which is all the example needs to
// make the merging visible.
func show(ctx context.Context, client interface {
	Get(ctx context.Context, key string) (any, error)
}, key, label string) {
	stored, err := client.Get(ctx, key)
	if err != nil {
		log.Fatalf("Get: %v", err)
	}

	state := asFleetState(stored)

	owners := state.MergeContent()
	sort.Strings(owners)

	fmt.Printf("%-42s %d owner(s)\n", label, len(owners))
	for _, owner := range owners {
		entry := state.Entries[owner]
		fmt.Printf("  %-12s %-16q updated %s\n", owner, entry.Payload,
			time.UnixMilli(entry.UpdatedAtMs).Format("15:04:05.000"))
	}
	fmt.Println()
}
