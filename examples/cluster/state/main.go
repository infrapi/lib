// Sharing application state across replicas, through the same key-value store
// the rings use.
//
//	cd examples/cluster/state && go run .
//
// This example joins no role at all: a service may legitimately want the shared
// store and no coordination.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/grafana/dskit/kv/codec"
	"github.com/grafana/dskit/ring"

	"github.com/infrapi/lib/examples/cluster/internal/demo"
	"github.com/infrapi/lib/pkg/cluster"
)

func main() {
	appConfig, clusterConfig := demo.Config("app.env")

	// A codec says how the value is serialised. ring.Desc stands in for a real
	// application message here, so that the example needs no protobuf of its
	// own.
	stateCodec := codec.NewProtoCodec("fleet-state", func() proto.Message { return &ring.Desc{} })

	// Codecs are a construction-time option, not a setter: the gossip store
	// only learns its codecs when the replica joins, and a client built later
	// for an unregistered codec is refused.
	c, stop := demo.Start(appConfig, clusterConfig, cluster.Options{
		Codecs: []codec.Codec{stateCodec},
	})
	defer stop()

	fmt.Printf("store %s, roles %v\n\n", c.Store(), c.Roles())

	client, err := c.KVClient("fleet-state")
	if err != nil {
		log.Fatalf("KVClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Compare-and-swap, not a blind write: every replica may be writing, and
	// the store is the only arbiter.
	const key = "fleet"
	err = client.CAS(ctx, key, func(existing any) (any, bool, error) {
		desc, ok := existing.(*ring.Desc)
		if !ok || desc == nil {
			desc = &ring.Desc{Ingesters: map[string]ring.InstanceDesc{}}
		}
		desc.Ingesters[c.InstanceID()] = ring.InstanceDesc{
			Id:   c.InstanceID(),
			Addr: c.InstanceAddr(),
		}
		return desc, true, nil
	})
	if err != nil {
		log.Fatalf("CAS: %v", err)
	}

	stored, err := client.Get(ctx, key)
	if err != nil {
		log.Fatalf("Get: %v", err)
	}

	desc, ok := stored.(*ring.Desc)
	if !ok {
		log.Fatalf("unexpected stored type %T", stored)
	}

	fmt.Printf("wrote and read back key %q: %d entry\n", key, len(desc.Ingesters))
	for id, entry := range desc.Ingesters {
		fmt.Printf("  %-12s %s\n", id, entry.Addr)
	}

	// A codec that was not declared is refused, rather than silently producing
	// a client nothing can read.
	fmt.Println()
	if _, err := c.KVClient("not-declared"); err != nil {
		fmt.Printf("KVClient(\"not-declared\")  %v\n", err)
	}

	fmt.Println()
	fmt.Println("One rule, and it is not optional: the store is a membership")
	fmt.Println("store, not a cache. Every key costs a broadcast worker and a")
	fmt.Println("slot in the state each replica gossips, on every push/pull.")
	fmt.Println("Keys must be few and long-lived. A key per request, per job or")
	fmt.Println("per challenge is how this becomes an out-of-memory incident.")
}
