package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clusterVariables builds a *Variables over the supplied dotenv values, with
// an instance ID so that a host with no resolvable hostname cannot fail the
// test for the wrong reason.
func clusterVariables(values map[string]string) *Variables {
	if values == nil {
		values = map[string]string{}
	}
	if _, ok := values["INFRAPI_CLUSTER_INSTANCE_ID"]; !ok {
		values["INFRAPI_CLUSTER_INSTANCE_ID"] = "node-a"
	}
	return &Variables{
		Values:        values,
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}
}

func TestGetClusterConfig_defaults(t *testing.T) {
	cfg, err := clusterVariables(nil).GetClusterConfig()
	require.NoError(t, err)

	assert.False(t, cfg.Enabled)
	assert.Equal(t, "memberlist", cfg.KvStore)
	assert.Equal(t, "collectors/", cfg.KvPrefix)
	assert.Empty(t, cfg.Roles)
	assert.Equal(t, "node-a", cfg.InstanceID)
	assert.Empty(t, cfg.InstanceAddr)
	assert.Equal(t, []string{"eth0", "en0"}, cfg.InstanceInterfaceNames)
	assert.Equal(t, 7946, cfg.InstancePort)
	assert.Equal(t, 30*time.Second, cfg.StartTimeout)
	assert.Equal(t, 45*time.Second, cfg.StopTimeout)

	assert.Equal(t, 15*time.Second, cfg.HeartbeatPeriod)
	assert.Equal(t, time.Minute, cfg.HeartbeatTimeout)
	assert.True(t, cfg.KeepInstanceInRingOnShutdown)
	assert.Equal(t, 10, cfg.AutoForgetUnhealthyPeriods)
	assert.Zero(t, cfg.TokensObservePeriod)

	assert.Empty(t, cfg.Zone)
	assert.False(t, cfg.ZoneAwarenessEnabled)
	assert.Empty(t, cfg.ExcludedZones)
	assert.False(t, cfg.GossipZoneAwareRoutingEnabled)
	assert.Equal(t, "member", cfg.GossipZoneRole)

	assert.Equal(t, "infrapi", cfg.GossipClusterLabel)
	assert.Equal(t, []string{"0.0.0.0"}, cfg.GossipBindAddrs)
	assert.Equal(t, 7946, cfg.GossipBindPort)
	assert.Zero(t, cfg.GossipAdvertisePort)
	assert.Equal(t, 200*time.Millisecond, cfg.GossipInterval)
	assert.Equal(t, 3, cfg.GossipNodes)
	assert.True(t, cfg.GossipCompressionEnabled)
	assert.Equal(t, 20*time.Second, cfg.LeaveTimeout)
	assert.Equal(t, 10*time.Second, cfg.BroadcastTimeoutOnShutdown)
	assert.Equal(t, 5*time.Minute, cfg.LeftInstancesTimeout)
	assert.Empty(t, cfg.JoinMembers)
	assert.Equal(t, 10, cfg.JoinMaxRetries)
	assert.False(t, cfg.AbortIfJoinFails)
	assert.False(t, cfg.GossipTLSEnabled)

	assert.Empty(t, cfg.EtcdEndpoints)
	assert.Equal(t, 10*time.Second, cfg.EtcdDialTimeout)
	assert.Empty(t, cfg.EtcdUsername)
	assert.Empty(t, cfg.EtcdPassword)
}

// The instance identity falls back to the host name, the way AppFqdn does.
func TestGetClusterConfig_instance_id_defaults_to_hostname(t *testing.T) {
	defaults := NewDefaultConfig()
	require.NotEmpty(t, defaults.Cluster.InstanceID.Default, "this host has no resolvable hostname")

	cfg, err := (&Variables{Values: map[string]string{}, Path: ".env", DefaultConfig: defaults}).GetClusterConfig()
	require.NoError(t, err)
	assert.Equal(t, defaults.Cluster.InstanceID.Default, cfg.InstanceID)
}

func TestGetClusterConfig_dotenv_overrides(t *testing.T) {
	cfg, err := clusterVariables(map[string]string{
		"INFRAPI_CLUSTER_ENABLED":                       "true",
		"INFRAPI_CLUSTER_KV_STORE":                      "etcd",
		"INFRAPI_CLUSTER_KV_PREFIX":                     "infrapi/rings/",
		"INFRAPI_CLUSTER_ROLES":                         `["scheduler","collector"]`,
		"INFRAPI_CLUSTER_INSTANCE_ADDR":                 "10.0.0.7",
		"INFRAPI_CLUSTER_INSTANCE_PORT":                 "9946",
		"INFRAPI_CLUSTER_HEARTBEAT_PERIOD":              "5s",
		"INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT":             "30s",
		"INFRAPI_CLUSTER_AUTO_FORGET_UNHEALTHY_PERIODS": "4",
		"INFRAPI_CLUSTER_ZONE":                          "dc1",
		"INFRAPI_CLUSTER_ZONE_AWARENESS_ENABLED":        "true",
		"INFRAPI_CLUSTER_EXCLUDED_ZONES":                `["dc3"]`,
		"INFRAPI_CLUSTER_GOSSIP_ZONE_ROLE":              "bridge",
		"INFRAPI_CLUSTER_ETCD_ENDPOINTS":                `["127.0.0.1:2379","127.0.0.2:2379"]`,
		"INFRAPI_CLUSTER_ETCD_USERNAME":                 "infrapi",
		"INFRAPI_CLUSTER_ETCD_PASSWORD":                 "s3cret",
		"INFRAPI_CLUSTER_MESSAGE_HISTORY_BUFFER_BYTES":  "1048576",
	}).GetClusterConfig()
	require.NoError(t, err)

	assert.True(t, cfg.Enabled)
	assert.Equal(t, "etcd", cfg.KvStore)
	assert.Equal(t, "infrapi/rings/", cfg.KvPrefix)
	assert.Equal(t, []string{"scheduler", "collector"}, cfg.Roles)
	assert.Equal(t, "10.0.0.7", cfg.InstanceAddr)
	assert.Equal(t, 9946, cfg.InstancePort)
	assert.Equal(t, 5*time.Second, cfg.HeartbeatPeriod)
	assert.Equal(t, 30*time.Second, cfg.HeartbeatTimeout)
	assert.Equal(t, 4, cfg.AutoForgetUnhealthyPeriods)
	assert.Equal(t, []string{"127.0.0.1:2379", "127.0.0.2:2379"}, cfg.EtcdEndpoints)
	assert.Equal(t, "infrapi", cfg.EtcdUsername)
	assert.Equal(t, "s3cret", cfg.EtcdPassword)
	assert.Equal(t, 1048576, cfg.MessageHistoryBufferBytes)
	assert.Equal(t, "dc1", cfg.Zone)
	assert.True(t, cfg.ZoneAwarenessEnabled)
	assert.Equal(t, []string{"dc3"}, cfg.ExcludedZones)
	assert.Equal(t, "bridge", cfg.GossipZoneRole)
}

func TestGetClusterConfig_nil_variables(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "cluster_test.env")

	var v *Variables
	cfg, err := v.GetClusterConfig()
	require.NoError(t, err)
	assert.True(t, cfg.Enabled)
	assert.Equal(t, "inmemory", cfg.KvStore)
	assert.Equal(t, []string{"scheduler", "collector"}, cfg.Roles)
	assert.Equal(t, "cluster_test.env", cfg.path)
}

func TestGetClusterConfig_newconfig_fails(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "does_not_exist.env")

	var v *Variables
	_, err := v.GetClusterConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unable to load dotenv")
}

// A bad key must name itself, its rule and the dotenv it came from, the way
// every other getter in this package does.
func TestGetClusterConfig_key_errors(t *testing.T) {
	for name, tc := range map[string]struct {
		values  map[string]string
		wantMsg string
	}{
		"unknown store": {
			values:  map[string]string{"INFRAPI_CLUSTER_KV_STORE": "consul"},
			wantMsg: "INFRAPI_CLUSTER_KV_STORE",
		},
		"prefix without trailing slash": {
			values:  map[string]string{"INFRAPI_CLUSTER_KV_PREFIX": "collectors"},
			wantMsg: "INFRAPI_CLUSTER_KV_PREFIX",
		},
		"enabled not a bool": {
			values:  map[string]string{"INFRAPI_CLUSTER_ENABLED": "yes"},
			wantMsg: "INFRAPI_CLUSTER_ENABLED",
		},
		"instance addr not an ip": {
			values:  map[string]string{"INFRAPI_CLUSTER_INSTANCE_ADDR": "not-an-ip"},
			wantMsg: "INFRAPI_CLUSTER_INSTANCE_ADDR",
		},
		"port not a number": {
			values:  map[string]string{"INFRAPI_CLUSTER_INSTANCE_PORT": "seven"},
			wantMsg: "not parsable as int",
		},
		"duration not parsable": {
			values:  map[string]string{"INFRAPI_CLUSTER_HEARTBEAT_PERIOD": "15 seconds"},
			wantMsg: "not parsable as time.Duration",
		},
		"roles not a json array": {
			values:  map[string]string{"INFRAPI_CLUSTER_ROLES": "scheduler,collector"},
			wantMsg: "not parsable as string slice",
		},
		"unknown gossip zone role": {
			values:  map[string]string{"INFRAPI_CLUSTER_GOSSIP_ZONE_ROLE": "relay"},
			wantMsg: "INFRAPI_CLUSTER_GOSSIP_ZONE_ROLE",
		},
		"instance id not a hostname": {
			values:  map[string]string{"INFRAPI_CLUSTER_INSTANCE_ID": "not a hostname"},
			wantMsg: "INFRAPI_CLUSTER_INSTANCE_ID",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := clusterVariables(tc.values).GetClusterConfig()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
			assert.Contains(t, err.Error(), "dotenv located at")
		})
	}
}

// The password is masked in the error message, the rest of the value is not.
func TestGetClusterConfig_password_is_masked(t *testing.T) {
	v := clusterVariables(map[string]string{"INFRAPI_CLUSTER_ETCD_PASSWORD": "s3cret"})
	v.DefaultConfig.Cluster.EtcdPassword.Validator = "ipv4"

	_, err := v.GetClusterConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[masked]")
	assert.NotContains(t, err.Error(), "s3cret")
}

// validClusterConfig is a configuration that passes every cross-key rule, so a
// test can break exactly one of them.
func validClusterConfig() *ClusterConfig {
	return &ClusterConfig{
		path:                       "test.env",
		KvStore:                    "memberlist",
		InstancePort:               7946,
		GossipBindPort:             7946,
		GossipNodes:                3,
		HeartbeatPeriod:            15 * time.Second,
		HeartbeatTimeout:           time.Minute,
		AutoForgetUnhealthyPeriods: 10,
		JoinMinBackoff:             time.Second,
		JoinMaxBackoff:             time.Minute,
		LeaveTimeout:               20 * time.Second,
		BroadcastTimeoutOnShutdown: 10 * time.Second,
		StopTimeout:                45 * time.Second,
	}
}

func TestClusterConfig_Validate_accepts_a_sound_configuration(t *testing.T) {
	require.NoError(t, validClusterConfig().Validate())
}

func TestClusterConfig_Validate(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate  func(*ClusterConfig)
		wantMsg string
	}{
		"instance port zero": {
			mutate:  func(c *ClusterConfig) { c.InstancePort = 0 },
			wantMsg: "INFRAPI_CLUSTER_INSTANCE_PORT is 0",
		},
		"instance port above range": {
			mutate:  func(c *ClusterConfig) { c.InstancePort = 99999 },
			wantMsg: "INFRAPI_CLUSTER_INSTANCE_PORT is 99999",
		},
		"gossip bind port above range": {
			mutate:  func(c *ClusterConfig) { c.GossipBindPort = 70000 },
			wantMsg: "INFRAPI_CLUSTER_GOSSIP_BIND_PORT is 70000",
		},
		"gossip advertise port negative": {
			mutate:  func(c *ClusterConfig) { c.GossipAdvertisePort = -1 },
			wantMsg: "INFRAPI_CLUSTER_GOSSIP_ADVERTISE_PORT is -1",
		},
		"gossip nodes zero": {
			mutate:  func(c *ClusterConfig) { c.GossipNodes = 0 },
			wantMsg: "INFRAPI_CLUSTER_GOSSIP_NODES is 0",
		},
		"join max retries negative": {
			mutate:  func(c *ClusterConfig) { c.JoinMaxRetries = -1 },
			wantMsg: "INFRAPI_CLUSTER_JOIN_MAX_RETRIES is -1",
		},
		"etcd max retries negative": {
			mutate:  func(c *ClusterConfig) { c.EtcdMaxRetries = -1 },
			wantMsg: "INFRAPI_CLUSTER_ETCD_MAX_RETRIES is -1",
		},
		"message history buffer negative": {
			mutate:  func(c *ClusterConfig) { c.MessageHistoryBufferBytes = -1 },
			wantMsg: "INFRAPI_CLUSTER_MESSAGE_HISTORY_BUFFER_BYTES is -1",
		},
		"empty role name": {
			mutate:  func(c *ClusterConfig) { c.Roles = []string{"scheduler", ""} },
			wantMsg: "contains an empty role name",
		},
		"duplicate role": {
			mutate:  func(c *ClusterConfig) { c.Roles = []string{"scheduler", "scheduler"} },
			wantMsg: `lists role "scheduler" more than once`,
		},
		"heartbeat timeout equal to period": {
			mutate:  func(c *ClusterConfig) { c.HeartbeatTimeout = c.HeartbeatPeriod },
			wantMsg: "must be greater than INFRAPI_CLUSTER_HEARTBEAT_PERIOD",
		},
		"heartbeat timeout below period": {
			mutate:  func(c *ClusterConfig) { c.HeartbeatTimeout = time.Second },
			wantMsg: "must be greater than INFRAPI_CLUSTER_HEARTBEAT_PERIOD",
		},
		"auto forget after a single period": {
			mutate:  func(c *ClusterConfig) { c.AutoForgetUnhealthyPeriods = 1 },
			wantMsg: "expected 0 to disable auto forget or 2 or more",
		},
		"auto forget negative": {
			mutate:  func(c *ClusterConfig) { c.AutoForgetUnhealthyPeriods = -2 },
			wantMsg: "INFRAPI_CLUSTER_AUTO_FORGET_UNHEALTHY_PERIODS is -2",
		},
		"join backoff inverted": {
			mutate:  func(c *ClusterConfig) { c.JoinMaxBackoff = time.Millisecond },
			wantMsg: "INFRAPI_CLUSTER_JOIN_MAX_BACKOFF",
		},
		"stop budget shorter than the gossip leave": {
			mutate:  func(c *ClusterConfig) { c.StopTimeout = 5 * time.Second },
			wantMsg: "INFRAPI_CLUSTER_STOP_TIMEOUT",
		},
		// The budget has to cover the broadcast drain too, not just the leave:
		// 25s clears the 20s leave on its own and is still not enough.
		"stop budget ignoring the broadcast drain": {
			mutate:  func(c *ClusterConfig) { c.StopTimeout = 25 * time.Second },
			wantMsg: "INFRAPI_CLUSTER_BROADCAST_TIMEOUT_ON_SHUTDOWN",
		},
		"etcd without endpoints": {
			mutate:  func(c *ClusterConfig) { c.KvStore = "etcd" },
			wantMsg: "INFRAPI_CLUSTER_ETCD_ENDPOINTS is empty",
		},
		"inmemory with peers": {
			mutate: func(c *ClusterConfig) {
				c.KvStore = "inmemory"
				c.JoinMembers = []string{"127.0.0.1:7946"}
			},
			wantMsg: "cannot reach another process",
		},
		"gossip cert without key": {
			mutate:  func(c *ClusterConfig) { c.GossipTLSCertPath = "/tls/cert.pem" },
			wantMsg: "INFRAPI_CLUSTER_GOSSIP_TLS_CERT_PATH and INFRAPI_CLUSTER_GOSSIP_TLS_KEY_PATH",
		},
		"gossip key without cert": {
			mutate:  func(c *ClusterConfig) { c.GossipTLSKeyPath = "/tls/key.pem" },
			wantMsg: "INFRAPI_CLUSTER_GOSSIP_TLS_CERT_PATH and INFRAPI_CLUSTER_GOSSIP_TLS_KEY_PATH",
		},
		"zone name too long": {
			mutate:  func(c *ClusterConfig) { c.Zone = "a-zone-name-well-over-sixteen-bytes" },
			wantMsg: "the gossip node metadata allows at most 16",
		},
		"excluding our own zone": {
			mutate: func(c *ClusterConfig) {
				c.Zone = "dc1"
				c.ExcludedZones = []string{"dc2", "dc1"}
			},
			wantMsg: "this replica's own INFRAPI_CLUSTER_ZONE",
		},
		"empty excluded zone": {
			mutate:  func(c *ClusterConfig) { c.ExcludedZones = []string{""} },
			wantMsg: "contains an empty zone name",
		},
		"etcd cert without key": {
			mutate:  func(c *ClusterConfig) { c.EtcdTLSCertPath = "/tls/cert.pem" },
			wantMsg: "INFRAPI_CLUSTER_ETCD_TLS_CERT_PATH and INFRAPI_CLUSTER_ETCD_TLS_KEY_PATH",
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validClusterConfig()
			tc.mutate(cfg)

			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
			assert.Contains(t, err.Error(), "dotenv located at test.env")
		})
	}
}

// A sound TLS pair, and an inmemory store with no peers, must both pass.
func TestClusterConfig_Validate_accepts_tls_pairs_and_inmemory(t *testing.T) {
	cfg := validClusterConfig()
	cfg.GossipTLSCertPath = "/tls/cert.pem"
	cfg.GossipTLSKeyPath = "/tls/key.pem"
	cfg.EtcdTLSCertPath = "/tls/cert.pem"
	cfg.EtcdTLSKeyPath = "/tls/key.pem"
	require.NoError(t, cfg.Validate())

	cfg = validClusterConfig()
	cfg.KvStore = "inmemory"
	require.NoError(t, cfg.Validate())

	// The gossip leave budget is only checked for the gossip store.
	cfg.StopTimeout = time.Second
	require.NoError(t, cfg.Validate())

	cfg = validClusterConfig()
	cfg.AutoForgetUnhealthyPeriods = 0
	require.NoError(t, cfg.Validate())

	// Excluding other zones is the point of the key; only our own is refused.
	cfg = validClusterConfig()
	cfg.Zone = "dc1"
	cfg.ExcludedZones = []string{"dc2", "dc3"}
	require.NoError(t, cfg.Validate())
}

// The cross-key rules run as part of resolving the dotenv, not only on demand.
func TestGetClusterConfig_runs_validate(t *testing.T) {
	_, err := clusterVariables(map[string]string{
		"INFRAPI_CLUSTER_KV_STORE": "etcd",
	}).GetClusterConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFRAPI_CLUSTER_ETCD_ENDPOINTS is empty")
}
