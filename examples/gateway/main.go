// Command gateway is a worked example of pkg/gateway: it starts a local stand-in
// for an APISIX Admin API, then drives it through a Client's route, service,
// upstream and consumer methods.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/infrapi/lib/pkg/errors"
	infragateway "github.com/infrapi/lib/pkg/gateway"
)

// apiKey is APISIX's documented default admin key, which is also what
// conf/apisix.yaml hands to the local stack this example talks to. A published
// default rather than a credential, hence the annotation.
const apiKey = "edd1c9f034335f136f87ad84b625c8f1" // #nosec G101 -- APISIX public default, see conf/apisix.yaml

func main() {
	fmt.Println("=== InfraPi Gateway Package Examples ===")
	fmt.Println()

	srv := startAdminAPI()
	defer func() { _ = srv.Close() }()
	time.Sleep(100 * time.Millisecond)

	client, err := infragateway.NewClient(&infragateway.ClientOptions{
		Server:   "127.0.0.1",
		Port:     19180,
		Protocol: "http",
		APIKey:   apiKey,
	})
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}

	ctx := context.Background()

	example1(ctx, client) // Upstream and route, created with a chosen id
	example2(ctx, client) // Reading one route and listing the collection
	example3(ctx, client) // Partial update with Update
	example4(ctx, client) // Service shared by plugins, and a consumer with key-auth
	example5(ctx, client) // Deleting a route and handling the *infraerrors.Error that follows
	example6()            // Error handling: a wrong API key and an unreachable server
}

// Example 1: an upstream created standalone, then referenced by a route
// created at a chosen id.
func example1(ctx context.Context, client *infragateway.Client) {
	fmt.Println("--- Example 1: Create an Upstream and a Route ---")

	upstream, err := client.NewUpstream(ctx, "1", infragateway.Upstream{
		Name:  "hello",
		Type:  "roundrobin",
		Nodes: []infragateway.Node{{Host: "httpbin", Port: 80, Weight: 1}},
	})
	if err != nil {
		log.Fatalf("NewUpstream: %v", err)
	}

	route, err := client.NewRoute(ctx, "1", infragateway.Route{
		Name:     "hello",
		URI:      "/hello",
		Methods:  []string{"GET"},
		Status:   1,
		Upstream: upstream, // *Upstream, plugs straight in
	})
	if err != nil {
		log.Fatalf("NewRoute: %v", err)
	}

	fmt.Printf("created route: uri=%s port=%d upstream=%s\n\n", route.URI, route.Upstream.Nodes[0].Port, route.Upstream.Nodes[0].Host)
}

// Example 2: Get and List.
func example2(ctx context.Context, client *infragateway.Client) {
	fmt.Println("--- Example 2: Read a Route and List the Collection ---")

	route, err := client.GetRoute(ctx, "1")
	if err != nil {
		log.Fatalf("GetRoute: %v", err)
	}
	fmt.Printf("fetched route: name=%s methods=%v\n", route.Name, route.Methods)

	all, err := client.ListRoutes(ctx)
	if err != nil {
		log.Fatalf("ListRoutes: %v", err)
	}
	fmt.Printf("routes in the collection: %d\n\n", len(all))
}

// Example 3: Update sends a partial document; fields left out keep their
// current value.
func example3(ctx context.Context, client *infragateway.Client) {
	fmt.Println("--- Example 3: Partial Update ---")

	updated, err := client.UpdateRoute(ctx, "1", map[string]any{
		"desc": "says hello",
	})
	if err != nil {
		log.Fatalf("UpdateRoute: %v", err)
	}
	fmt.Printf("updated route: uri=%s desc=%s\n\n", updated.URI, updated.Desc)
}

// Example 4: a Service centralising plugin config, and a Consumer authenticated
// through key-auth.
func example4(ctx context.Context, client *infragateway.Client) {
	fmt.Println("--- Example 4: Service and Consumer ---")

	id, service, err := client.CreateService(ctx, infragateway.Service{
		Name: "public-api",
		Plugins: map[string]any{
			"key-auth": map[string]any{},
		},
	})
	if err != nil {
		log.Fatalf("CreateService: %v", err)
	}
	fmt.Printf("created service: id=%s name=%s\n", id, service.Name)

	consumer, err := client.NewConsumer(ctx, infragateway.Consumer{
		Username: "alice",
		Plugins: map[string]any{
			"key-auth": map[string]any{"key": "alice-secret"},
		},
	})
	if err != nil {
		log.Fatalf("NewConsumer: %v", err)
	}
	fmt.Printf("created consumer: username=%s\n\n", consumer.Username)
}

// Example 5: Delete, then handle the *errors.Error a follow-up Get returns -
// pkg/gateway reports Admin API failures through pkg/errors, like the rest of
// this library.
func example5(ctx context.Context, client *infragateway.Client) {
	fmt.Println("--- Example 5: Delete and the Resulting Not-Found Error ---")

	if err := client.DeleteRoute(ctx, "1"); err != nil {
		log.Fatalf("DeleteRoute: %v", err)
	}
	fmt.Println("deleted route 1")

	_, err := client.GetRoute(ctx, "1")
	var apiErr *infraerrors.Error
	if infraerrors.As(err, &apiErr) {
		fmt.Printf("expected error after delete: code=%d message=%s\n\n", apiErr.Code, apiErr.Message)
	}
	fmt.Printf("infraerrors.ErrorCode(err) = %d\n\n", infraerrors.ErrorCode(err))
}

// Example 6: a wrong API key and a server that is not listening.
func example6() {
	fmt.Println("--- Example 6: Error Handling ---")

	wrongKey, err := infragateway.NewClient(&infragateway.ClientOptions{
		Server:   "127.0.0.1",
		Port:     19180,
		Protocol: "http",
		APIKey:   "not-the-right-key",
	})
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}
	if _, err := wrongKey.ListRoutes(context.Background()); err != nil {
		fmt.Printf("wrong API key: code=%d %v\n", infraerrors.ErrorCode(err), err)
	}

	unreachable, err := infragateway.NewClient(&infragateway.ClientOptions{
		Server:           "127.0.0.1",
		Port:             19999, // nothing listening here
		Protocol:         "http",
		APIKey:           apiKey,
		RetryWaitTime:    10 * time.Millisecond,
		RetryMaxWaitTime: 20 * time.Millisecond,
	})
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}
	if _, err := unreachable.ListRoutes(context.Background()); err != nil {
		fmt.Printf("unreachable server: %v\n\n", err)
	}
}

// startAdminAPI launches a local stand-in for the APISIX Admin API: enough of
// its request/response shape - key/value items, an X-API-KEY check - to back
// every example above without a real APISIX deployment.
func startAdminAPI() *http.Server {
	var mu sync.Mutex
	store := map[string]map[string]json.RawMessage{
		"routes": {}, "services": {}, "upstreams": {}, "consumers": {},
	}
	var counter int64

	nextIndex := func() int64 { counter++; return counter }

	checkAuth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("X-API-KEY") != apiKey {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"error_msg":"wrong api key"}`)
			return false
		}
		return true
	}

	type itemResponse struct {
		Key           string          `json:"key"`
		Value         json.RawMessage `json:"value"`
		CreatedIndex  int64           `json:"createdIndex"`
		ModifiedIndex int64           `json:"modifiedIndex"`
	}

	writeItem := func(w http.ResponseWriter, collection, id string, value json.RawMessage) {
		w.Header().Set("Content-Type", "application/json")
		idx := nextIndex()
		_ = json.NewEncoder(w).Encode(itemResponse{
			Key:           "/apisix/" + collection + "/" + id,
			Value:         value,
			CreatedIndex:  idx,
			ModifiedIndex: idx,
		})
	}

	mux := http.NewServeMux()
	for _, collection := range []string{"routes", "services", "upstreams", "consumers"} {
		collection := collection
		mux.HandleFunc("/apisix/admin/"+collection, func(w http.ResponseWriter, r *http.Request) {
			if !checkAuth(w, r) {
				return
			}
			mu.Lock()
			defer mu.Unlock()

			switch r.Method {
			case http.MethodGet:
				items := make([]itemResponse, 0)
				for id, value := range store[collection] {
					idx := nextIndex()
					items = append(items, itemResponse{
						Key: "/apisix/" + collection + "/" + id, Value: value,
						CreatedIndex: idx, ModifiedIndex: idx,
					})
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(struct {
					List  []itemResponse `json:"list"`
					Total int            `json:"total"`
				}{List: items, Total: len(items)})
			case http.MethodPost:
				var body json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&body)
				id := fmt.Sprintf("auto-%d", nextIndex())
				store[collection][id] = body
				writeItem(w, collection, id, body)
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		})

		mux.HandleFunc("/apisix/admin/"+collection+"/", func(w http.ResponseWriter, r *http.Request) {
			if !checkAuth(w, r) {
				return
			}
			id := strings.TrimPrefix(r.URL.Path, "/apisix/admin/"+collection+"/")

			mu.Lock()
			defer mu.Unlock()

			switch r.Method {
			case http.MethodGet:
				value, ok := store[collection][id]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					_, _ = fmt.Fprint(w, `{"error_msg":"key not found"}`)
					return
				}
				writeItem(w, collection, id, value)
			case http.MethodPut:
				var body json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&body)
				store[collection][id] = body
				writeItem(w, collection, id, body)
			case http.MethodPatch:
				existing, ok := store[collection][id]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					_, _ = fmt.Fprint(w, `{"error_msg":"key not found"}`)
					return
				}
				var e, p map[string]any
				_ = json.Unmarshal(existing, &e)
				_ = json.NewDecoder(r.Body).Decode(&p)
				for k, v := range p {
					e[k] = v
				}
				merged, _ := json.Marshal(e)
				store[collection][id] = merged
				writeItem(w, collection, id, merged)
			case http.MethodDelete:
				if _, ok := store[collection][id]; !ok {
					w.WriteHeader(http.StatusNotFound)
					_, _ = fmt.Fprint(w, `{"error_msg":"key not found"}`)
					return
				}
				delete(store[collection], id)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(struct {
					Key     string `json:"key"`
					Deleted string `json:"deleted"`
				}{Key: "/apisix/" + collection + "/" + id, Deleted: "1"})
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		})
	}

	srv := &http.Server{Addr: "127.0.0.1:19180", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.ListenAndServe() //nolint:errcheck
	return srv
}
