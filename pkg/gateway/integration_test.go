package gateway

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestIntegration_RealAdminAPI drives a Client and the Routes, Services,
// Upstreams and Consumers resources against a real, already-running APISIX
// Admin API - unlike the rest of this package's tests, which run against the
// in-process stand-in in gateway_test.go. Run this one to verify this
// package's integration with an actual deployment, or as a template while
// building your own.
//
// It is skipped unless APISIX_ADMIN_SERVER and APISIX_ADMIN_APIKEY name a
// reachable Admin API. To run it:
//
//  1. Start APISIX somewhere reachable - a local Docker Compose stack is the
//     quickest way, see https://apisix.apache.org/docs/apisix/getting-started/.
//
//  2. Note its Admin API address and admin key (apisix.admin_key in its
//     config, or the key your deployment sets).
//
//  3. Run:
//
//     APISIX_ADMIN_SERVER=127.0.0.1 APISIX_ADMIN_APIKEY=<your-admin-key> \
//     go test ./pkg/gateway/... -run TestIntegration_RealAdminAPI -v
//
// APISIX_ADMIN_PROTOCOL and APISIX_ADMIN_PORT override the http/9180
// defaults; set APISIX_ADMIN_PROTOCOL=https for a TLS Admin API.
//
// The test creates an upstream, a service, a route and a consumer, all named
// after a timestamp-based id unique to the run, and deletes every one of
// them again whether it passes or fails.
func TestIntegration_RealAdminAPI(t *testing.T) {
	server := os.Getenv("APISIX_ADMIN_SERVER")
	apiKey := os.Getenv("APISIX_ADMIN_APIKEY")
	if server == "" || apiKey == "" {
		t.Skip("set APISIX_ADMIN_SERVER and APISIX_ADMIN_APIKEY to run this test against a real APISIX Admin API; see this test's godoc")
	}

	opts := &ClientOptions{Server: server, APIKey: apiKey}

	if protocol := os.Getenv("APISIX_ADMIN_PROTOCOL"); protocol != "" {
		opts.Protocol = protocol
	}

	if port := os.Getenv("APISIX_ADMIN_PORT"); port != "" {
		p, err := strconv.Atoi(port)
		if err != nil {
			t.Fatalf("APISIX_ADMIN_PORT: %v", err)
		}
		opts.Port = p
	}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx := context.Background()
	id := fmt.Sprintf("infrapi-lib-test-%d", time.Now().UnixNano())

	// Safety net: clean up even if a step below fails partway through.
	t.Cleanup(func() {
		_ = client.DeleteRoute(ctx, id)
		_ = client.DeleteService(ctx, id)
		_ = client.DeleteUpstream(ctx, id)
		_ = client.DeleteConsumer(ctx, id)
	})

	upstream, err := client.NewUpstream(ctx, id, Upstream{
		Type:  "roundrobin",
		Nodes: []Node{{Host: "127.0.0.1", Port: 1980, Weight: 1}},
	})
	if err != nil {
		t.Fatalf("NewUpstream: %v", err)
	}
	if upstream.Nodes[0].Host != "127.0.0.1" {
		t.Fatalf("NewUpstream: unexpected nodes %+v", upstream.Nodes)
	}

	service, err := client.NewService(ctx, id, Service{
		Name:       id,
		UpstreamID: id,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if service.UpstreamID != id {
		t.Fatalf("NewService: expected upstream_id %q, got %q", id, service.UpstreamID)
	}

	route, err := client.NewRoute(ctx, id, Route{
		Name:      id,
		URI:       "/" + id,
		ServiceID: id,
		Status:    1,
	})
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}
	if route.URI != "/"+id {
		t.Fatalf("NewRoute: expected uri %q, got %q", "/"+id, route.URI)
	}

	if _, err := client.GetRoute(ctx, id); err != nil {
		t.Fatalf("GetRoute: %v", err)
	}

	all, err := client.ListRoutes(ctx)
	if err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
	found := false
	for _, r := range all {
		if r.Name == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListRoutes: created route %q not found among %d routes", id, len(all))
	}

	updated, err := client.UpdateRoute(ctx, id, map[string]any{"desc": "infrapi-lib integration test"})
	if err != nil {
		t.Fatalf("UpdateRoute: %v", err)
	}
	if updated.Desc != "infrapi-lib integration test" {
		t.Fatalf("UpdateRoute: desc not applied, got %+v", updated)
	}

	consumer, err := client.NewConsumer(ctx, Consumer{
		Username: id,
		Plugins:  map[string]any{"key-auth": map[string]any{"key": id}},
	})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	if consumer.Username != id {
		t.Fatalf("NewConsumer: expected username %q, got %q", id, consumer.Username)
	}

	if err := client.DeleteRoute(ctx, id); err != nil {
		t.Fatalf("DeleteRoute: %v", err)
	}
	if _, err := client.GetRoute(ctx, id); err == nil {
		t.Fatalf("GetRoute after Delete: expected error")
	}
}
