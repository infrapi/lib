package cluster

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/config"
)

// instanceCounter keeps every instance ID in a test binary unique. The
// in-memory key-value store is a process-wide singleton, so two clusters
// sharing an ID would fight over one ring entry.
var instanceCounter atomic.Int64

// testAppConfig is the application configuration a cluster needs: the location
// becomes the ring zone, which only makes the status page readable.
func testAppConfig() *config.AppConfig {
	return &config.AppConfig{
		AppName:     "testapp",
		AppPlatform: "sandbox",
		AppRegion:   "eu-west-1",
		AppLocation: "dc1",
		AppFqdn:     "testapp.local",
	}
}

// testValues is a configuration that clusters on the in-memory store with
// timings short enough for a test to watch. The address is pinned to loopback
// rather than discovered, so a host with unusual interface names cannot fail a
// test for the wrong reason.
func testValues(overrides map[string]string) map[string]string {
	values := map[string]string{
		"INFRAPI_CLUSTER_ENABLED":                       "true",
		"INFRAPI_CLUSTER_KV_STORE":                      storeInmemory,
		"INFRAPI_CLUSTER_INSTANCE_ID":                   fmt.Sprintf("node-%d", instanceCounter.Add(1)),
		"INFRAPI_CLUSTER_INSTANCE_ADDR":                 "127.0.0.1",
		"INFRAPI_CLUSTER_HEARTBEAT_PERIOD":              "100ms",
		"INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT":             "2s",
		"INFRAPI_CLUSTER_AUTO_FORGET_UNHEALTHY_PERIODS": "2",
		// Short enough that a test can watch a replica leave, and short enough
		// that the stop budget below clears the leave plus the broadcast drain.
		"INFRAPI_CLUSTER_LEAVE_TIMEOUT":                 "1s",
		"INFRAPI_CLUSTER_BROADCAST_TIMEOUT_ON_SHUTDOWN": "1s",
		"INFRAPI_CLUSTER_START_TIMEOUT":                 "15s",
		// A ceiling, not a wait: a shutdown with nothing left to say returns at
		// once. Keeping it generous stops a loaded machine from failing the
		// cleanup on a budget rather than on a defect.
		"INFRAPI_CLUSTER_STOP_TIMEOUT": "30s",
	}
	maps.Copy(values, overrides)
	return values
}

// testClusterConfig resolves a configuration through the real dotenv getters,
// so the tests exercise the same path a service does.
func testClusterConfig(t *testing.T, overrides map[string]string) *config.ClusterConfig {
	t.Helper()

	vars := &config.Variables{
		Values:        testValues(overrides),
		Path:          "test.env",
		DefaultConfig: config.NewDefaultConfig(),
	}

	cfg, err := vars.GetClusterConfig()
	require.NoError(t, err)
	return cfg
}

// testLogger keeps test output quiet unless a test asks for it.
func testLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)
	return logger
}

// newTestCluster builds a cluster on a registry of its own. That is not
// optional: dns_lookups_total carries no label that would tell two clusters
// apart, so sharing a registry panics inside promauto.
func newTestCluster(t *testing.T, overrides map[string]string, roles ...RoleSpec) *Cluster {
	t.Helper()

	names := make([]string, 0, len(roles))
	for _, role := range roles {
		names = append(names, `"`+role.Name+`"`)
	}
	if len(names) > 0 {
		overrides = withRoles(overrides, names)
	}

	cfg := testClusterConfig(t, overrides)

	c, err := New(testAppConfig(), cfg, Options{
		Roles:      roles,
		Logger:     testLogger(),
		Registerer: prometheus.NewRegistry(),
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		require.NoError(t, c.Stop(ctx), "Stop must not hang or error during cleanup")
	})

	return c
}

// withRoles selects every declared role, unless the caller already said which.
func withRoles(overrides map[string]string, names []string) map[string]string {
	if overrides == nil {
		overrides = map[string]string{}
	}
	if _, set := overrides["INFRAPI_CLUSTER_ROLES"]; !set {
		overrides["INFRAPI_CLUSTER_ROLES"] = "[" + strings.Join(names, ",") + "]"
	}
	return overrides
}

// startTestCluster builds a cluster and starts it.
func startTestCluster(t *testing.T, overrides map[string]string, roles ...RoleSpec) *Cluster {
	t.Helper()

	c := newTestCluster(t, overrides, roles...)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, c.Start(ctx))

	return c
}

// uniqueRole names a role after the test that owns it, because the in-memory
// store is shared by every test in the binary and ring keys must not collide.
func uniqueRole(t *testing.T, suffix string, mode Mode) RoleSpec {
	t.Helper()

	name := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	return RoleSpec{Name: name + "-" + suffix, Mode: mode}
}
