package gatewaysync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	infraerrors "github.com/infrapi/lib/pkg/errors"
	infragateway "github.com/infrapi/lib/pkg/gateway"
)

// fakeAdmin stores what it is given as JSON, as APISIX does, and counts writes.
type fakeAdmin struct {
	store  map[string][]byte
	writes []string
	fail   error
}

func newFakeAdmin() *fakeAdmin { return &fakeAdmin{store: map[string][]byte{}} }

func get[T any](f *fakeAdmin, kind, id string) (*T, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	b, ok := f.store[kind+"/"+id]
	if !ok {
		return nil, infraerrors.New(http.StatusNotFound, "fake.Get", "%s/%s not found", kind, id)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func put[T any](f *fakeAdmin, kind, id string, v T) (*T, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	f.store[kind+"/"+id] = b
	f.writes = append(f.writes, kind+"/"+id)
	return &v, nil
}

func (f *fakeAdmin) GetUpstream(_ context.Context, id string) (*infragateway.Upstream, error) {
	return get[infragateway.Upstream](f, "upstreams", id)
}
func (f *fakeAdmin) NewUpstream(_ context.Context, id string, u infragateway.Upstream) (*infragateway.Upstream, error) {
	return put(f, "upstreams", id, u)
}
func (f *fakeAdmin) GetService(_ context.Context, id string) (*infragateway.Service, error) {
	return get[infragateway.Service](f, "services", id)
}
func (f *fakeAdmin) NewService(_ context.Context, id string, s infragateway.Service) (*infragateway.Service, error) {
	return put(f, "services", id, s)
}
func (f *fakeAdmin) GetRoute(_ context.Context, id string) (*infragateway.Route, error) {
	return get[infragateway.Route](f, "routes", id)
}
func (f *fakeAdmin) NewRoute(_ context.Context, id string, r infragateway.Route) (*infragateway.Route, error) {
	return put(f, "routes", id, r)
}

type fakeRing struct {
	members []string
	leader  string
}

func (r *fakeRing) Members() ([]string, error) { return r.members, nil }
func (r *fakeRing) Leader() (string, string, bool, error) {
	if r.leader == "" {
		return "", "", false, errors.New("no leader")
	}
	return "id-" + r.leader, r.leader, false, nil
}

func testConfig() Config {
	cfg := DefaultConfig("orders-api", "/readyz")
	cfg.Prefixes, cfg.LeaderWrites = []string{"/orders/v1/"}, true
	return cfg
}

func TestSyncWritesOnlyWhatChanged(t *testing.T) {
	admin := newFakeAdmin()
	ring := &fakeRing{members: []string{"10.0.0.2:8080", "10.0.0.1:8080"}, leader: "10.0.0.2:8080"}
	s := New(admin, ring, testConfig())
	ctx := context.Background()

	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"upstreams/orders-api-read", "upstreams/orders-api-write", "services/orders-api-api",
		"routes/orders-api-orders-v1-read", "routes/orders-api-orders-v1-write", "routes/orders-api-docs",
	}
	if !reflect.DeepEqual(admin.writes, want) {
		t.Fatalf("first sync wrote %v, want %v", admin.writes, want)
	}

	// Nothing moved: nothing written.
	admin.writes = nil
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(admin.writes) != 0 {
		t.Fatalf("an unchanged sync wrote %v", admin.writes)
	}

	// A replica joins: the read upstream only.
	ring.members = append(ring.members, "10.0.0.3:8080")
	admin.writes = nil
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(admin.writes, []string{"upstreams/orders-api-read"}) {
		t.Fatalf("a join wrote %v", admin.writes)
	}

	// Leadership moves: the write upstream only.
	ring.leader = "10.0.0.1:8080"
	admin.writes = nil
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(admin.writes, []string{"upstreams/orders-api-write"}) {
		t.Fatalf("a new leader wrote %v", admin.writes)
	}

	read, err := admin.GetUpstream(ctx, "orders-api-read")
	must(t, err)
	write, err := admin.GetUpstream(ctx, "orders-api-write")
	must(t, err)
	if got := []string{read.Nodes[0].Host, read.Nodes[1].Host, read.Nodes[2].Host}; !reflect.DeepEqual(got,
		[]string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}) {
		t.Errorf("read nodes %v, want every replica, sorted", got)
	}
	if len(write.Nodes) != 1 || write.Nodes[0].Host != "10.0.0.1" || write.Nodes[0].Port != 8080 {
		t.Errorf("write nodes %+v, want the leader alone", write.Nodes)
	}
	if read.Checks == nil || read.Checks.Active.HTTPPath != "/readyz" || read.Timeout.Read != 120 {
		t.Errorf("read upstream checks or timeout: %+v", read)
	}
}

// What the routes enforce is the contract's: the apikey header, 429 per
// consumer, 413 over the body limit; reads and writes go apart.
func TestSyncRoutesAndPlugins(t *testing.T) {
	admin := newFakeAdmin()
	s := New(admin, &fakeRing{members: []string{"10.0.0.1:8080"}, leader: "10.0.0.1:8080"}, testConfig())
	ctx := context.Background()
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	svc, err := admin.GetService(ctx, "orders-api-api")
	must(t, err)
	b, err := json.Marshal(svc.Plugins)
	must(t, err)
	var plugins map[string]map[string]any
	must(t, json.Unmarshal(b, &plugins))
	if plugins["key-auth"]["header"] != "apikey" || plugins["key-auth"]["hide_credentials"] != true {
		t.Errorf("key-auth %v", plugins["key-auth"])
	}
	if lc := plugins["limit-count"]; lc["rejected_code"] != float64(429) || lc["key"] != "consumer_name" ||
		lc["count"] != float64(600) || lc["time_window"] != float64(60) {
		t.Errorf("limit-count %v", lc)
	}
	if plugins["client-control"]["max_body_size"] != float64(1<<20) {
		t.Errorf("client-control %v", plugins["client-control"])
	}

	read, err := admin.GetRoute(ctx, "orders-api-orders-v1-read")
	must(t, err)
	write, err := admin.GetRoute(ctx, "orders-api-orders-v1-write")
	must(t, err)
	docs, err := admin.GetRoute(ctx, "orders-api-docs")
	must(t, err)
	if read.UpstreamID != "orders-api-read" || !reflect.DeepEqual(read.Methods, []string{"GET", "HEAD"}) ||
		!reflect.DeepEqual(read.URIs, []string{"/orders/v1/*"}) || read.ServiceID != "orders-api-api" {
		t.Errorf("read route %+v", read)
	}
	if write.UpstreamID != "orders-api-write" || !reflect.DeepEqual(write.Methods,
		[]string{"POST", "PUT", "PATCH", "DELETE"}) || write.ServiceID != "orders-api-api" {
		t.Errorf("write route %+v", write)
	}
	if !reflect.DeepEqual(docs.URIs, []string{"/-/docs", "/-/openapi/*"}) {
		t.Errorf("documentation route URIs %v", docs.URIs)
	}
	if docs.ServiceID != "" || docs.Plugins != nil {
		t.Errorf("the documentation route requires a key: %+v", docs)
	}
}

// No leader, or an Admin API that fails: nothing is written, the error says why.
// Nothing is fixed: ids, methods, health check, plugins and the
// documentation route are whatever the configuration says.
func TestSyncWritesWhatTheConfigurationSays(t *testing.T) {
	admin := newFakeAdmin()
	cfg := testConfig()
	cfg.ReadUpstream, cfg.WriteUpstream, cfg.Service = "orders-get", "orders-set", "orders-svc"
	cfg.RouteID = "o-{kind}-{version}"
	cfg.ReadMethods = []string{http.MethodGet}
	cfg.Labels = map[string]string{"team": "orders"}
	cfg.Upstream.Type = "least_conn"
	cfg.Upstream.Checks.Active.HTTPPath = "/health"
	cfg.Upstream.Checks.Active.Unhealthy.HTTPFailures = 5
	cfg.Plugins = map[string]any{"key-auth": map[string]any{"header": "x-api-key"}}
	cfg.DocsRoute = ""
	ctx := context.Background()
	if err := New(admin, &fakeRing{members: []string{"10.0.0.1:8080"}, leader: "10.0.0.1:8080"}, cfg).Sync(ctx); err != nil {
		t.Fatal(err)
	}

	want := []string{"upstreams/orders-get", "upstreams/orders-set", "services/orders-svc",
		"routes/o-read-orders-v1", "routes/o-write-orders-v1"}
	if !reflect.DeepEqual(admin.writes, want) {
		t.Fatalf("wrote %v, want %v (and no documentation route)", admin.writes, want)
	}
	up, err := admin.GetUpstream(ctx, "orders-get")
	must(t, err)
	if up.Type != "least_conn" || up.Checks.Active.HTTPPath != "/health" ||
		up.Checks.Active.Unhealthy.HTTPFailures != 5 || up.Labels["team"] != "orders" {
		t.Errorf("upstream %+v", up)
	}
	read, err := admin.GetRoute(ctx, "o-read-orders-v1")
	must(t, err)
	if !reflect.DeepEqual(read.Methods, []string{"GET"}) || read.UpstreamID != "orders-get" || read.ServiceID != "orders-svc" {
		t.Errorf("read route %+v", read)
	}
	svc, err := admin.GetService(ctx, "orders-svc")
	must(t, err)
	if len(svc.Plugins) != 1 {
		t.Errorf("plugins %v, want only key-auth", svc.Plugins)
	}

	// The template is not shared between syncs: changing one config's
	// upstream does not change the default.
	if DefaultConfig("x", "/r").Upstream.Checks.Active.HTTPPath != "/r" {
		t.Error("the default changed")
	}
}

// Without leader writes, every replica takes writes too: both upstreams hold
// all of them, and a new leader changes nothing in APISIX.
func TestSyncWithoutLeaderWrites(t *testing.T) {
	admin := newFakeAdmin()
	cfg := testConfig()
	cfg.LeaderWrites = false
	ring := &fakeRing{members: []string{"10.0.0.1:8080", "10.0.0.2:8080"}, leader: "10.0.0.1:8080"}
	s := New(admin, ring, cfg)
	ctx := context.Background()
	must(t, s.Sync(ctx))

	write, err := admin.GetUpstream(ctx, "orders-api-write")
	must(t, err)
	if len(write.Nodes) != 2 {
		t.Errorf("write nodes %+v, want every replica", write.Nodes)
	}

	ring.leader = "10.0.0.2:8080"
	admin.writes = nil
	must(t, s.Sync(ctx))
	if len(admin.writes) != 0 {
		t.Errorf("a new leader rewrote %v", admin.writes)
	}
}

func TestSyncRefuses(t *testing.T) {
	admin := newFakeAdmin()
	if err := New(admin, &fakeRing{members: []string{"10.0.0.1:8080"}}, testConfig()).Sync(context.Background()); err == nil ||
		len(admin.writes) != 0 {
		t.Errorf("synced without a leader: %v, wrote %v", err, admin.writes)
	}

	admin.fail = infraerrors.New(http.StatusForbidden, "fake", "wrong admin key")
	err := New(admin, &fakeRing{members: []string{"10.0.0.1:8080"}, leader: "10.0.0.1:8080"}, testConfig()).Sync(context.Background())
	if err == nil || infraerrors.ErrorCode(err) != http.StatusForbidden || len(admin.writes) != 0 {
		t.Errorf("err %v, wrote %v", err, admin.writes)
	}

	if _, err := nodes([]string{"no-port"}); err == nil {
		t.Error("an address without a port was accepted")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
