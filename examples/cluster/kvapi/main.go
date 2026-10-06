// A small CRUD API over the cluster's key-value store, with the backend chosen
// by one line of configuration.
//
//	cd examples/cluster/kvapi
//
//	INFRAPI_CONFIG_DOTENV_FILE=memberlist-1.env  go run .
//	INFRAPI_CONFIG_DOTENV_FILE=memberlist-2.env  go run .   # second terminal
//
//	podman compose up -d                                    # etcd on :2379
//	INFRAPI_CONFIG_DOTENV_FILE=etcd-1.env        go run .
//	INFRAPI_CONFIG_DOTENV_FILE=etcd-2.env        go run .   # second terminal
//
// The program does not know which store it is on. Only
// INFRAPI_CLUSTER_KV_STORE changes, and the same four routes behave the same
// way — which is the point of the key-value abstraction underneath.
//
// There is deliberately no in-memory variant here. That store is a map inside
// one process: a second replica gets its own, nothing is shared, and an API
// demonstrating a cluster over it would demonstrate nothing. It earns its place
// in tests and in the single-process examples next door, not here.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gogo/protobuf/proto"
	"github.com/grafana/dskit/kv"
	"github.com/grafana/dskit/kv/codec"

	"github.com/infrapi/lib/examples/cluster/internal/demo"
	"github.com/infrapi/lib/pkg/cluster"
	infraerrors "github.com/infrapi/lib/pkg/errors"
	"github.com/infrapi/lib/pkg/listener"
	infraserver "github.com/infrapi/lib/pkg/server"
)

const (
	// codecID travels with every value, so once a deployment is running it is
	// as good as frozen.
	codecID = "kvapi-items"

	// storeKey is the single key the whole collection lives under. See the
	// comment on Items for why it is one key and not one per item.
	storeKey = "items"
)

func main() {
	appConfig, clusterConfig := demo.Config("memberlist-1.env")

	itemsCodec := codec.NewProtoCodec(codecID, func() proto.Message { return NewItems() })

	c, stop := demo.Start(appConfig, clusterConfig, cluster.Options{
		Roles:  []cluster.RoleSpec{{Name: "api", Mode: cluster.ModeBroadcast}},
		Codecs: []codec.Codec{itemsCodec},
	})

	client, err := c.KVClient(codecID)
	if err != nil {
		log.Fatalf("KVClient: %v", err)
	}

	api := &itemsAPI{cluster: c, client: client, prefix: clusterConfig.KvPrefix}

	gin.SetMode(gin.ReleaseMode)
	engine, err := infraserver.NewServerGin(appConfig)
	if err != nil {
		log.Fatalf("NewServerGin: %v", err)
	}
	if err := c.RegisterGin(engine, cluster.HandlerOptions{}); err != nil {
		log.Fatalf("RegisterGin: %v", err)
	}
	api.routes(engine)

	socket, err := listener.Listen(appConfig)
	if err != nil {
		log.Fatalf("Listen: %v", err)
	}
	srv := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(socket); !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Serve: %v", err)
		}
	}()

	api.banner(socket.Addr().String())

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	select {
	case <-signals:
		fmt.Printf("\n%s leaving the cluster...\n", c.InstanceID())

		shutCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Printf("Shutdown: %v", err)
		}

		stop()
		fmt.Printf("%s left cleanly\n", c.InstanceID())

	// With etcd this is where losing the store surfaces; with gossip, a
	// transport that died.
	case err := <-c.Err():
		log.Fatalf("a cluster service failed: %v", err)
	}
}

// itemsAPI is four routes over one key-value key.
type itemsAPI struct {
	cluster *cluster.Cluster
	client  kv.Client
	prefix  string
}

// routes mounts the API next to the status pages the cluster package serves.
func (a *itemsAPI) routes(engine *gin.Engine) {
	items := engine.Group("/api/v1/items")
	items.GET("", a.list)
	items.GET("/:id", a.get)
	items.PUT("/:id", a.put)
	items.DELETE("/:id", a.delete)
}

// banner prints what to curl, which is the whole interface of this example.
func (a *itemsAPI) banner(addr string) {
	fmt.Printf("%s up, store %s, serving on http://%s\n\n", a.cluster.InstanceID(), a.cluster.Store(), addr)
	fmt.Printf("  curl -X PUT http://%s/api/v1/items/one -d '{\"value\":\"hello\"}'\n", addr)
	fmt.Printf("  curl        http://%s/api/v1/items\n", addr)
	fmt.Printf("  curl        http://%s/api/v1/items/one\n", addr)
	fmt.Printf("  curl -X DELETE http://%s/api/v1/items/one\n\n", addr)
	fmt.Printf("  http://%s%s\n", addr, cluster.StatusPath)
	fmt.Printf("  the stored key is %q\n\n", a.prefix+storeKey)
	fmt.Println("Run the second replica against the same store and the collection")
	fmt.Println("is shared: write on either address, read it from both.")
	fmt.Println()
	fmt.Println("Ctrl+C to leave the cluster.")
}

// itemBody is what a write carries.
type itemBody struct {
	Value string `json:"value" binding:"required"`
}

// itemView is what a read returns.
type itemView struct {
	ID        string `json:"id"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updatedAt"`
	UpdatedBy string `json:"updatedBy"`
}

// list returns every live item. Tombstones never reach here: the store hides
// them on the way out.
func (a *itemsAPI) list(ctx *gin.Context) {
	collection, err := a.read(ctx)
	if err != nil {
		a.fail(ctx, err)
		return
	}

	views := []itemView{}
	for _, id := range collection.IDs() {
		views = append(views, view(id, collection.Items[id]))
	}

	ctx.JSON(http.StatusOK, gin.H{
		"servedBy": a.cluster.InstanceID(),
		"store":    a.cluster.Store(),
		"items":    views,
	})
}

// get returns one item.
func (a *itemsAPI) get(ctx *gin.Context) {
	id := ctx.Param("id")

	collection, err := a.read(ctx)
	if err != nil {
		a.fail(ctx, err)
		return
	}

	item, found := collection.Items[id]
	if !found || item.Deleted {
		a.fail(ctx, infraerrors.New(http.StatusNotFound, "kvapi", "no item %q", id).
			WithSolution("list the collection at GET /api/v1/items"))
		return
	}

	ctx.JSON(http.StatusOK, view(id, item))
}

// put creates or replaces an item.
func (a *itemsAPI) put(ctx *gin.Context) {
	id := ctx.Param("id")

	var body itemBody
	if err := ctx.ShouldBindJSON(&body); err != nil {
		a.fail(ctx, infraerrors.Wrap(err, http.StatusBadRequest, "kvapi", "bad request body").
			WithSolution(`send {"value": "..."}`))
		return
	}

	// Compare-and-swap, never a blind write: another replica may be writing the
	// same key. Only ever call it when there is something to change — a merge
	// that reports no change is treated as a failure, retried, and finally an
	// error.
	err := a.client.CAS(ctx.Request.Context(), storeKey, func(existing any) (any, bool, error) {
		collection := asItems(existing)
		collection.Items[id] = Item{
			Value:       body.Value,
			UpdatedAtMs: time.Now().UnixMilli(),
			UpdatedBy:   a.cluster.InstanceID(),
		}
		return collection, true, nil
	})
	if err != nil {
		a.fail(ctx, infraerrors.Wrap(err, http.StatusServiceUnavailable, "kvapi", "could not write %q", id))
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"id": id, "value": body.Value, "updatedBy": a.cluster.InstanceID()})
}

// delete removes an item, which leaves a tombstone behind.
func (a *itemsAPI) delete(ctx *gin.Context) {
	id := ctx.Param("id")

	var existed bool
	err := a.client.CAS(ctx.Request.Context(), storeKey, func(existing any) (any, bool, error) {
		collection := asItems(existing)

		if item, found := collection.Items[id]; !found || item.Deleted {
			// Nothing to change. Returning retry false stops dskit from
			// spending ten seconds discovering the same thing.
			return nil, false, nil
		}
		existed = true

		// Dropping the entry is what makes the merge write a tombstone, under
		// its localCAS branch.
		delete(collection.Items, id)
		return collection, true, nil
	})
	// existed is the authority, not err: a compare-and-swap that changed nothing
	// can come back either as success or as an error depending on the store, and
	// both mean the same thing here — there was nothing to delete.
	if !existed {
		a.fail(ctx, infraerrors.New(http.StatusNotFound, "kvapi", "no item %q", id))
		return
	}
	if err != nil {
		a.fail(ctx, infraerrors.Wrap(err, http.StatusServiceUnavailable, "kvapi", "could not delete %q", id))
		return
	}

	ctx.Status(http.StatusNoContent)
}

// read fetches the collection, treating an absent key as an empty one.
func (a *itemsAPI) read(ctx *gin.Context) (*Items, error) {
	stored, err := a.client.Get(ctx.Request.Context(), storeKey)
	if err != nil {
		return nil, infraerrors.Wrap(err, http.StatusServiceUnavailable, "kvapi", "could not read the store").
			WithSolution("check the key-value store is reachable, and see " + cluster.StatusPath)
	}
	return asItems(stored), nil
}

// fail answers with the RFC 9457 payload the errors package builds.
func (a *itemsAPI) fail(ctx *gin.Context, err error) {
	var appErr *infraerrors.Error
	if !errors.As(err, &appErr) {
		appErr = infraerrors.Wrap(err, http.StatusInternalServerError, "kvapi", "unexpected failure")
	}
	ctx.JSON(infraerrors.ErrorCode(appErr), appErr)
}

// asItems is the shape every read and every compare-and-swap starts from: the
// store hands over nil the first time, and a decoded value afterwards.
func asItems(existing any) *Items {
	collection, ok := existing.(*Items)
	if !ok || collection == nil {
		return NewItems()
	}
	if collection.Items == nil {
		collection.Items = map[string]Item{}
	}
	return collection
}

// view renders one item for the API.
func view(id string, item Item) itemView {
	return itemView{
		ID:        id,
		Value:     item.Value,
		UpdatedAt: time.UnixMilli(item.UpdatedAtMs).Format(time.RFC3339Nano),
		UpdatedBy: item.UpdatedBy,
	}
}
