package app

import (
	"bytes"
	"context"
	"encoding/json"
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
	assert.Equal(t, "orders-api/", cfg.cluster.KvPrefix)
	assert.Equal(t, []string{writerRole}, cfg.cluster.Roles)
	assert.Equal(t, "from-env", cfg.Gateway.AdminKey)
	assert.Equal(t, "orders-api", cfg.Gateway.Name)
	assert.Equal(t, 15*time.Second, cfg.Gateway.Interval)
	assert.Equal(t, time.Duration(0), cfg.drainDelay)
	assert.Equal(t, "hello", svc.Greeting)
}

func TestLoadConfigRefuses(t *testing.T) {
	for name, content := range map[string]string{
		"a key the library does not know": required + "  listen_adress: 0.0.0.0:1\n",
		"a misspelled section":            required + "clustr: {}\n",
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
	spec := Spec{
		Name: "orders-api", Service: &svc,
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
	assert.Equal(t, string(ordersV1), body)
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
`)
	cfg, err = loadConfig(Spec{Name: "orders-api"}, bad)
	require.NoError(t, err)
	_, err = newCluster(cfg)
	assert.ErrorContains(t, err, "ping://db")
}
