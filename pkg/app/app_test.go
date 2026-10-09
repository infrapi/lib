package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/server"

	"github.com/infrapi/lib/pkg/app/problem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ordersV1 is a contract small enough to read: one create, one read.
var ordersV1 = []byte(`openapi: 3.0.3
info: {title: Orders, version: "1"}
paths:
  /orders/v1/tenants/{tenant}/orders:
    post:
      parameters:
        - {name: tenant, in: path, required: true, schema: {type: string}}
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              additionalProperties: false
              required: [item]
              properties: {item: {type: string}}
      responses: {"201": {description: created}}
  /orders/v1/tenants/{tenant}/orders/{id}:
    get:
      parameters:
        - {name: tenant, in: path, required: true, schema: {type: string}}
        - {name: id, in: path, required: true, schema: {type: string}}
      responses: {"200": {description: found}}
`)

// serviceConfig is what the orders service reads from its section.
type serviceConfig struct {
	Greeting string `yaml:"greeting"`
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

const required = `
app:
  platform: sandbox
  region: eu-west-1
  location: dc1
  url: http://orders.example.com
  fqdn: orders-1.example.com
`

func TestLoadConfig(t *testing.T) {
	var svc serviceConfig
	spec := Spec{Name: "orders-api", Service: &svc}
	path := writeFile(t, required+`
cluster:
  enabled: false
  join_members: [a:7946, b:7946]
gateway:
  admin_url: http://apisix:9180
shutdown:
  drain_delay: 0s
service:
  greeting: hello
`)
	t.Setenv("INFRAPI_APP_LOG_LEVEL", "debug") // the environment wins
	t.Setenv("INFRAPI_GATEWAY_ADMIN_KEY", "from-env")

	cfg, err := loadConfig(spec, path)
	require.NoError(t, err)
	assert.Equal(t, "orders-api", cfg.app.AppName, "the spec names the app")
	assert.Equal(t, "0.0.0.0:8080", cfg.app.AppListenAddress)
	assert.Equal(t, "json", cfg.app.AppLogFormat)
	assert.Equal(t, "debug", cfg.app.AppLogLevel.String())
	assert.Equal(t, []string{"a:7946", "b:7946"}, cfg.cluster.JoinMembers, "a YAML list reaches the library")
	assert.Equal(t, 8080, cfg.cluster.InstancePort, "the ring address is the API's port")
	assert.Empty(t, cfg.cluster.InstanceAddr, "on a wildcard, the address comes from the interfaces")
	assert.Equal(t, []string{"0.0.0.0"}, cfg.cluster.GossipBindAddrs)
	assert.Equal(t, "orders-api/", cfg.cluster.KvPrefix)
	assert.Empty(t, cfg.cluster.Roles, "a disabled cluster elects no leader")
	assert.Equal(t, "from-env", cfg.Gateway.AdminKey)
	assert.Equal(t, "orders-api", cfg.Gateway.Name)
	assert.Equal(t, 15*time.Second, cfg.Gateway.Interval)
	assert.Equal(t, time.Duration(0), cfg.drainDelay)
	assert.Equal(t, "hello", svc.Greeting)
}

// An API bound to one address is reached there: the ring advertises it, with
// no interface to look up, and gossip binds it; a configured one still wins.
func TestTheRingAdvertisesTheAPIsAddress(t *testing.T) {
	cfg, err := loadConfig(Spec{Name: "orders-api"}, writeFile(t, required+"  listen_address: 127.0.0.2:9090\n"))
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.2", cfg.cluster.InstanceAddr)
	assert.Equal(t, 9090, cfg.cluster.InstancePort)
	assert.Equal(t, []string{"127.0.0.2"}, cfg.cluster.GossipBindAddrs, "two replicas on one host gossip apart")

	cfg, err = loadConfig(Spec{Name: "orders-api"},
		writeFile(t, required+"  listen_address: 127.0.0.2:9090\ncluster: {instance_addr: 10.0.0.1}\n"))
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", cfg.cluster.InstanceAddr)
}

// A leader is elected only for what needs one: writes through it, or the
// gateway's configuration. Otherwise there is no ring to join, and leader
// checks, which would decide nothing, are refused.
func TestALeaderOnlyWhenSomethingNeedsOne(t *testing.T) {
	const clustered = `
cluster:
  enabled: true
  kv_store: inmemory
  instance_addr: 127.0.0.1
`
	for name, c := range map[string]struct {
		extra string
		roles []string
	}{
		"nothing":       {"", nil},
		"leader writes": {"  leader_writes: true\n", []string{writerRole}},
		"gateway":       {"gateway: {admin_url: http://apisix:9180}\n", []string{writerRole}},
	} {
		cfg, err := loadConfig(Spec{Name: "orders-api"}, writeFile(t, required+clustered+c.extra))
		require.NoError(t, err, name)
		assert.ElementsMatch(t, c.roles, cfg.cluster.Roles, name)
		var declared []string
		for _, r := range clusterOptions(cfg).Roles {
			declared = append(declared, r.Name)
		}
		assert.Equal(t, c.roles, declared, name)
	}

	_, err := loadConfig(Spec{Name: "orders-api"},
		writeFile(t, required+clustered+"  leader_checks: [\"tcp://db:5432\"]\n"))
	assert.ErrorContains(t, err, "no leader is elected")
}

// The service declares its rings; cluster.roles chooses which a replica
// joins, all by default; writer is added when something needs a leader.
func TestTheServicesRoles(t *testing.T) {
	spec := Spec{Name: "orders-api", Roles: []cluster.RoleSpec{
		{Name: "purger", Mode: cluster.ModeLeader},
		{Name: "cache", Mode: cluster.ModeBroadcast},
	}}
	const clustered = `
cluster:
  enabled: true
  kv_store: inmemory
  instance_addr: 127.0.0.1
`
	for name, c := range map[string]struct {
		extra    string
		joined   []string
		declared []string
	}{
		"all by default":     {"", []string{"purger", "cache"}, []string{"purger", "cache"}},
		"chosen":             {"  roles: [cache]\n", []string{"cache"}, []string{"purger", "cache"}},
		"none":               {"  roles: []\n", nil, nil},
		"with a leader":      {"  roles: [cache]\n  leader_writes: true\n", []string{"cache", writerRole}, []string{"purger", "cache", writerRole}},
		"checks per role":    {"  roles: [purger]\n  leader_writes: true\n  role_leader_checks:\n    purger: [\"exec:/bin/is-active-dc\"]\n    writer: [\"tcp://db:5432\"]\n", []string{"purger", writerRole}, []string{"purger", "cache", writerRole}},
		"checks, own leader": {"  roles: [purger]\n  leader_checks: [\"tcp://db:5432\"]\n", []string{"purger"}, []string{"purger", "cache"}},
	} {
		cfg, err := loadConfig(spec, writeFile(t, required+clustered+c.extra))
		require.NoError(t, err, name)
		assert.ElementsMatch(t, c.joined, cfg.cluster.Roles, name)
		var declared []string
		for _, r := range clusterOptions(cfg).Roles {
			declared = append(declared, r.Name)
		}
		assert.Equal(t, c.declared, declared, name)
	}
	cfg, err := loadConfig(spec, writeFile(t, required+clustered+"  role_leader_checks: {purger: [\"exec:/bin/is-active-dc\"]}\n"))
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"purger": {"exec:/bin/is-active-dc"}}, cfg.cluster.RoleLeaderChecks,
		"a YAML map reaches the library")

	for name, c := range map[string]struct{ extra, err string }{
		"unknown":              {"  roles: [compactor]\n", `"compactor" is not one of the service's roles [cache purger]`},
		"writer":               {"  roles: [writer]\n", "the library's own ring"},
		"checks, nobody leads": {"  roles: [cache]\n  leader_checks: [\"tcp://db:5432\"]\n", "no leader is elected here"},
	} {
		_, err := loadConfig(spec, writeFile(t, required+clustered+c.extra))
		assert.ErrorContains(t, err, c.err, name)
	}

	assert.ErrorContains(t, Run(context.Background(), Spec{Name: "x", API: func(context.Context, *Runtime) ([]Version, error) {
		return nil, nil
	}, Roles: []cluster.RoleSpec{{Name: writerRole}}}, ""), "the library's own ring")
}

func TestLoadConfigRefuses(t *testing.T) {
	for name, content := range map[string]string{
		"a key the library does not know": required + "  listen_adress: 0.0.0.0:1\n",
		"a misspelled section":            required + "clustr: {}\n",
		"a role the service has not":      required + "cluster: {roles: [writer]}\n",
		"a key the service does not know": required + "service: {greting: x}\n",
		"a value the library refuses":     strings.Replace(required, "dc1", "mars", 1),
	} {
		_, err := loadConfig(Spec{Name: "orders-api", Service: &serviceConfig{}}, writeFile(t, content))
		assert.Error(t, err, name)
	}
	_, err := loadConfig(Spec{Name: "orders-api"}, writeFile(t, required+"service: {greeting: x}\n"))
	assert.ErrorContains(t, err, "takes none")
}

// syncBuffer is the access log, written by the engine while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// The whole service through Run: the contract validates and documents, the
// errors are problem documents, the access log carries the call's fields, and
// the service stops when its context does.
func TestRun(t *testing.T) {
	addr := freePort(t)
	path := writeFile(t, required+"  listen_address: "+addr+"\nshutdown: {drain_delay: 0s}\nservice: {greeting: hi}\n")

	var logs syncBuffer
	previous := gin.DefaultWriter
	gin.DefaultWriter = &logs
	t.Cleanup(func() { gin.DefaultWriter = previous })

	var svc serviceConfig
	background, stopped := make(chan struct{}), make(chan struct{})
	spec := Spec{
		Name: "orders-api", Service: &svc,
		Background: func(ctx context.Context, rt *Runtime) error {
			close(background)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		},
		API: func(_ context.Context, rt *Runtime) ([]Version, error) {
			assert.Equal(t, "orders-api", rt.App.AppName)
			return []Version{{
				OpenAPI: server.OpenAPISpec{Name: "orders v1", Path: "orders/v1.yaml", Content: ordersV1},
				Routes: func(r Router) {
					r.POST("/orders/v1/tenants/{tenant}/orders", func(c *Context) {
						var in struct {
							Item string `json:"item"`
						}
						if err := c.Bind(&in); err != nil {
							c.Error(err)
							return
						}
						c.JSON(http.StatusCreated, map[string]string{"greeting": svc.Greeting, "item": in.Item})
					})
					r.GET("/orders/v1/tenants/{tenant}/orders/{id}", func(c *Context) {
						if c.Param("id") == "missing" {
							c.Error(problem.NotFound("no order %q", c.Param("id")))
							return
						}
						c.JSON(http.StatusOK, map[string]string{"id": c.Param("id"), "tenant": c.Param("tenant")})
					})
				},
			}}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, spec, path) }()

	base := "http://" + addr
	select {
	case <-background:
	case <-time.After(10 * time.Second):
		t.Fatal("the background did not start")
	}
	require.Eventually(t, func() bool {
		resp, err := http.Get(base + ReadyPath)
		if err != nil {
			return false
		}
		require.NoError(t, resp.Body.Close())
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 50*time.Millisecond)

	do := func(method, p, body string) (int, string, http.Header) {
		req, err := http.NewRequest(method, base+p, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Correlation-ID", "corr-1")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		return resp.StatusCode, string(b), resp.Header
	}

	code, body, _ := do(http.MethodPost, "/orders/v1/tenants/acme/orders", `{"item":"book"}`)
	assert.Equal(t, http.StatusCreated, code)
	assert.JSONEq(t, `{"greeting":"hi","item":"book"}`, body)

	code, body, _ = do(http.MethodGet, "/orders/v1/tenants/acme/orders/o-1", "")
	assert.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{"id":"o-1","tenant":"acme"}`, body, "path parameters by their contract names")
	code, body, _ = do(http.MethodGet, "/orders/v1/tenants/acme/orders/missing", "")
	assert.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, body, `"code":"not_found"`)

	code, body, hdr := do(http.MethodPost, "/orders/v1/tenants/acme/orders", `{"item":"book","colour":"red"}`)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "application/problem+json", hdr.Get("Content-Type"))
	var p map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &p))
	assert.Equal(t, "invalid_request", p["code"])
	assert.Equal(t, false, p["retryable"])
	assert.Equal(t, "corr-1", p["correlation_id"])
	assert.True(t, strings.HasPrefix(p["detail"].(string), "request body has an error"), p["detail"])

	code, body, _ = do(http.MethodGet, "/nowhere", "")
	assert.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, body, `"code":"not_found"`)

	code, body, _ = do(http.MethodGet, "/-/openapi/orders/v1.yaml", "")
	assert.Equal(t, http.StatusOK, code)
	assert.True(t, strings.HasPrefix(body, "openapi: 3.0.3\ninfo: {title: Orders"), "the author's contract, in its order")
	assert.Contains(t, body, `default: {$ref: "#/components/responses/Problem"}`, "completed with what every contract shares")
	code, _, _ = do(http.MethodGet, "/-/openapi.json", "")
	assert.Equal(t, http.StatusNotFound, code, "the contract is the only OpenAPI description")
	_, body, _ = do(http.MethodGet, "/", "")
	assert.Contains(t, body, "<h1>orders-api</h1>")
	assert.Contains(t, body, `href="/-/openapi/orders/v1.yaml"`)

	// The access line of the create carries the call's fields.
	var line map[string]any
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, `"route":"/orders/v1/tenants/:tenant/orders"`) && strings.Contains(l, `"status":201`) {
			require.NoError(t, json.Unmarshal([]byte(l), &line))
		}
	}
	require.NotNil(t, line, "no access line for the create in:\n%s", logs.String())
	assert.Equal(t, "acme", line["tenant"])
	assert.Equal(t, "corr-1", line["correlation_id"])

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
	select {
	case <-stopped:
	default:
		t.Error("Run returned before its background did")
	}
}

// A background that fails stops the service with its error.
func TestABackgroundErrorStopsTheService(t *testing.T) {
	path := writeFile(t, required+"  listen_address: "+freePort(t)+"\nshutdown: {drain_delay: 0s}\n")
	err := Run(context.Background(), Spec{Name: "orders-api",
		API: func(context.Context, *Runtime) ([]Version, error) {
			return []Version{{OpenAPI: server.OpenAPISpec{Name: "orders v1", Path: "orders/v1.yaml", Content: ordersV1},
				Routes: func(Router) {}}}, nil
		},
		Background: func(context.Context, *Runtime) error { return errors.New("the purge failed") },
	}, path)
	assert.ErrorContains(t, err, "app: background: the purge failed")
}

func TestRunRefusesAnIncompleteSpec(t *testing.T) {
	assert.ErrorContains(t, Run(context.Background(), Spec{API: func(context.Context, *Runtime) ([]Version, error) {
		return nil, nil
	}}, ""), "Name")
	assert.ErrorContains(t, Run(context.Background(), Spec{Name: "x"}, ""), "API")
}

// cluster.leader_checks is a key like any other: it reaches the cluster, and a
// check that does not parse stops the service before it serves.
func TestLeaderChecksFromTheFile(t *testing.T) {
	path := writeFile(t, required+`
cluster:
  enabled: true
  kv_store: inmemory
  instance_addr: 127.0.0.1
  leader_checks: ["!file:///etc/infrapi/maintenance", "tcp://db:5432"]
  leader_check_interval: 3s
  leader_writes: true
`)
	cfg, err := loadConfig(Spec{Name: "orders-api"}, path)
	require.NoError(t, err)
	assert.True(t, cfg.cluster.LeaderWrites, "the operator's choice, from the file")
	assert.Equal(t, []string{"!file:///etc/infrapi/maintenance", "tcp://db:5432"}, cfg.cluster.LeaderChecks)
	assert.Equal(t, 3*time.Second, cfg.cluster.LeaderCheckInterval)

	bad := writeFile(t, required+`
cluster:
  enabled: true
  kv_store: inmemory
  instance_addr: 127.0.0.1
  leader_checks: ["ping://db"]
  leader_writes: true
`)
	cfg, err = loadConfig(Spec{Name: "orders-api"}, bad)
	require.NoError(t, err)
	_, err = newCluster(cfg)
	assert.ErrorContains(t, err, "ping://db")
}
