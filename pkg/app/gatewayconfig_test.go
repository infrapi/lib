package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Nothing said: the defaults, under the service's name.
func TestGatewayDefaults(t *testing.T) {
	cfg, err := loadConfig(Spec{Name: "orders-api"}, writeFile(t, required+"gateway: {admin_url: http://apisix:9180}\n"))
	require.NoError(t, err)
	sync, err := cfg.Gateway.syncConfig([]string{"/orders/v1/"}, false)
	require.NoError(t, err)

	assert.Equal(t, "orders-api-read", sync.ReadUpstream)
	assert.Equal(t, "orders-api-write", sync.WriteUpstream)
	assert.Equal(t, "orders-api-api", sync.Service)
	assert.Equal(t, "orders-api-{version}-{kind}", sync.RouteID)
	assert.Equal(t, []string{"GET", "HEAD", "OPTIONS"}, sync.ReadMethods)
	assert.Equal(t, ReadyPath, sync.Upstream.Checks.Active.HTTPPath)
	assert.Equal(t, 120.0, sync.Upstream.Timeout.Read)
	assert.ElementsMatch(t, []string{"key-auth", "limit-count", "client-control", "request-id"}, keys(sync.Plugins))
	assert.Equal(t, "orders-api-docs", sync.DocsRoute)
}

// Everything the leader writes can be said in the file.
func TestGatewayConfigured(t *testing.T) {
	cfg, err := loadConfig(Spec{Name: "orders-api"}, writeFile(t, required+`
gateway:
  admin_url: http://apisix:9180
  name: orders
  labels: {team: orders}
  read: {upstream: "{name}-get", methods: [get]}
  write: {upstream: "{name}-set", methods: [POST, PUT, DELETE, PATCH]}
  service: "{name}-svc"
  route_id: "{name}-{kind}-{version}"
  upstream:
    type: least_conn
    timeout: {connect: 1s, send: 10s, read: 5m}
    keepalive_pool: {size: 64, idle_timeout: 30s}
    health_check:
      path: /health
      timeout: 2s
      healthy: {interval: 5s, successes: 3}
      unhealthy: {interval: 2s, http_failures: 4, http_statuses: [500, 503]}
  rate_limit: {count: 100, window_seconds: 10}
  plugins:
    request-id: null
    cors: {allow_origins: "https://console.example.com"}
  docs: {route: "{name}-swagger", uris: [/-/docs]}
`))
	require.NoError(t, err)
	sync, err := cfg.Gateway.syncConfig([]string{"/orders/v1/"}, false)
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"team": "orders"}, sync.Labels)
	assert.Equal(t, "orders-get", sync.ReadUpstream)
	assert.Equal(t, "orders-set", sync.WriteUpstream)
	assert.Equal(t, "orders-svc", sync.Service)
	assert.Equal(t, "orders-{kind}-{version}", sync.RouteID)
	assert.Equal(t, []string{"GET"}, sync.ReadMethods, "methods are upper-cased")

	up := sync.Upstream
	assert.Equal(t, "least_conn", up.Type)
	assert.Equal(t, 300.0, up.Timeout.Read)
	assert.Equal(t, 64, up.KeepalivePool.Size)
	assert.Equal(t, 30.0, *up.KeepalivePool.IdleTimeout)
	a := up.Checks.Active
	assert.Equal(t, "/health", a.HTTPPath)
	assert.Equal(t, 2.0, a.Timeout)
	assert.Equal(t, 5, a.Healthy.Interval)
	assert.Equal(t, 3, a.Healthy.Successes)
	assert.Equal(t, 4, a.Unhealthy.HTTPFailures)
	assert.Equal(t, 2, a.Unhealthy.TCPFailures, "what is not said keeps its default")
	assert.Equal(t, []int{500, 503}, a.Unhealthy.HTTPStatuses)

	assert.ElementsMatch(t, []string{"key-auth", "limit-count", "client-control", "cors"}, keys(sync.Plugins),
		"null removes a default plugin, a new name adds one")
	assert.Equal(t, 100, sync.Plugins["limit-count"].(map[string]any)["count"])
	assert.Equal(t, "orders-swagger", sync.DocsRoute)
	assert.Equal(t, []string{"/-/docs"}, sync.DocsURIs)

	// The default is untouched by a configured one.
	again, err := (&GatewayConfig{Name: "x"}).syncConfig(nil, false)
	require.NoError(t, err)
	assert.Equal(t, ReadyPath, again.Upstream.Checks.Active.HTTPPath)
}

func TestGatewayWithoutHealthCheckOrDocs(t *testing.T) {
	cfg, err := loadConfig(Spec{Name: "orders-api"}, writeFile(t, required+`
gateway:
  admin_url: http://apisix:9180
  upstream: {health_check: {enabled: false}}
  docs: {enabled: false}
`))
	require.NoError(t, err)
	sync, err := cfg.Gateway.syncConfig(nil, false)
	require.NoError(t, err)
	assert.Nil(t, sync.Upstream.Checks)
	assert.Empty(t, sync.DocsRoute)
}

// A mistake stops the service at start, named.
func TestGatewayRefuses(t *testing.T) {
	for want, section := range map[string]string{
		"whole seconds":          "upstream: {health_check: {healthy: {interval: 1500ms}}}",
		"both":                   `read: {upstream: x}` + "\n  " + `write: {upstream: x}`,
		"{version} and {kind}":   `route_id: "{name}-routes"`,
		"both read and written":  "write: {methods: [GET, POST]}",
		`"FETCH" is not an HTTP`: "read: {methods: [fetch]}",
		"read.methods is empty":  "read: {methods: []}",
		"docs.uris is empty":     "docs: {uris: []}",
	} {
		_, err := loadConfig(Spec{Name: "orders-api"}, writeFile(t, required+
			"gateway:\n  admin_url: http://apisix:9180\n  "+section+"\n"))
		if assert.Error(t, err, section) {
			assert.True(t, strings.Contains(err.Error(), want), "%q: %v", section, err)
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
