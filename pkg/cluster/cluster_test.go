package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/grafana/dskit/flagext"
	"github.com/grafana/dskit/kv/codec"
	"github.com/grafana/dskit/kv/memberlist"
	"github.com/grafana/dskit/ring"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/config"
)

func TestMode_String(t *testing.T) {
	assert.Equal(t, "leader", ModeLeader.String())
	assert.Equal(t, "broadcast", ModeBroadcast.String())
	assert.Equal(t, "unknown", Mode(99).String())
}

func TestRoleSpec_ringKey(t *testing.T) {
	assert.Equal(t, "scheduler", RoleSpec{Name: "scheduler"}.ringKey())
	assert.Equal(t, "ring", RoleSpec{Name: "acme-manager", Key: "ring"}.ringKey())
}

func TestNew_rejects_nil_config(t *testing.T) {
	_, err := New(nil, &config.ClusterConfig{}, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil *config.AppConfig")

	_, err = New(testAppConfig(), nil, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil *config.ClusterConfig")
}

// Every one of these would otherwise surface as a panic from inside promauto,
// because two rings of the same name register the same collectors.
func TestNew_rejects_bad_role_declarations(t *testing.T) {
	for name, tc := range map[string]struct {
		roles   []RoleSpec
		wantErr error
		wantMsg string
	}{
		"no name": {
			roles:   []RoleSpec{{Mode: ModeLeader}},
			wantErr: ErrDuplicateRole,
			wantMsg: "has no name",
		},
		"duplicate name": {
			roles: []RoleSpec{
				{Name: "scheduler", Mode: ModeLeader},
				{Name: "scheduler", Mode: ModeBroadcast},
			},
			wantErr: ErrDuplicateRole,
			wantMsg: "declared twice",
		},
		"duplicate ring key": {
			roles: []RoleSpec{
				{Name: "scheduler", Mode: ModeLeader, Key: "shared"},
				{Name: "collector", Mode: ModeBroadcast, Key: "shared"},
			},
			wantErr: ErrDuplicateRole,
			wantMsg: `under key "shared"`,
		},
		"a name colliding with another role's key": {
			roles: []RoleSpec{
				{Name: "scheduler", Mode: ModeLeader},
				{Name: "collector", Mode: ModeBroadcast, Key: "scheduler"},
			},
			wantErr: ErrDuplicateRole,
			wantMsg: `under key "scheduler"`,
		},
		"no mode": {
			roles:   []RoleSpec{{Name: "scheduler"}},
			wantErr: ErrBadMode,
			wantMsg: "has mode 0",
		},
		"unknown mode": {
			roles:   []RoleSpec{{Name: "scheduler", Mode: Mode(99)}},
			wantErr: ErrBadMode,
			wantMsg: "has mode 99",
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": `["scheduler"]`})

			_, err := New(testAppConfig(), cfg, Options{Roles: tc.roles, Logger: testLogger(), Registerer: prometheus.NewRegistry()})
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.wantErr)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

// A role named in the dotenv that the code does not declare is always a typo or
// a bad rollout. Accepting it silently would mean a scheduler that never
// schedules, with nothing in the logs to say why.
func TestNew_rejects_a_role_the_code_does_not_declare(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": `["scheduler","typo"]`})

	_, err := New(testAppConfig(), cfg, Options{
		Roles:      []RoleSpec{{Name: "scheduler", Mode: ModeLeader}},
		Logger:     testLogger(),
		Registerer: prometheus.NewRegistry(),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRoleUndeclared)
	assert.Contains(t, err.Error(), `"typo"`)
	assert.Contains(t, err.Error(), "scheduler")
}

func TestNew_rejects_roles_when_none_are_declared(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": `["scheduler"]`})

	_, err := New(testAppConfig(), cfg, Options{Logger: testLogger(), Registerer: prometheus.NewRegistry()})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRoleUndeclared)
	assert.Contains(t, err.Error(), "declared no roles")
}

// A cluster that joins no ring coordinates nothing, so a service that declares
// roles and selects none is misconfigured rather than degenerate.
func TestNew_rejects_an_empty_selection(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": "[]"})

	_, err := New(testAppConfig(), cfg, Options{
		Roles:      []RoleSpec{{Name: "scheduler", Mode: ModeLeader}},
		Logger:     testLogger(),
		Registerer: prometheus.NewRegistry(),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoRoles)
}

// A service may legitimately want the shared store and no coordination at all.
func TestNew_allows_a_store_with_no_rings(t *testing.T) {
	c := startTestCluster(t, map[string]string{"INFRAPI_CLUSTER_ROLES": "[]"})
	assert.Empty(t, c.Roles())
}

func TestNew_rejects_two_codecs_sharing_an_id(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": "[]"})

	_, err := New(testAppConfig(), cfg, Options{
		Codecs:     []codec.Codec{testCodec(), testCodec()},
		Logger:     testLogger(),
		Registerer: prometheus.NewRegistry(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "share the ID")
}

// dns_lookups_total carries no label that would tell two clusters apart, so
// sharing a registerer must be refused rather than left to panic.
func TestNew_refuses_to_share_a_registerer(t *testing.T) {
	registry := prometheus.NewRegistry()
	overrides := map[string]string{"INFRAPI_CLUSTER_ROLES": "[]"}

	first, err := New(testAppConfig(), testClusterConfig(t, overrides), Options{Logger: testLogger(), Registerer: registry})
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Stop(context.Background()) })

	_, err = New(testAppConfig(), testClusterConfig(t, overrides), Options{Logger: testLogger(), Registerer: registry})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered on this registerer")
}

// A registerer is handed back when the cluster stops, so a process that
// restarts its cluster is not locked out of its own registry.
func TestStop_releases_the_registerer(t *testing.T) {
	registry := prometheus.NewRegistry()
	overrides := map[string]string{"INFRAPI_CLUSTER_ROLES": "[]"}

	first, err := New(testAppConfig(), testClusterConfig(t, overrides), Options{Logger: testLogger(), Registerer: registry})
	require.NoError(t, err)
	require.NoError(t, first.Start(context.Background()))
	require.NoError(t, first.Stop(context.Background()))

	second, err := New(testAppConfig(), testClusterConfig(t, overrides), Options{Logger: testLogger(), Registerer: registry})
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Stop(context.Background()) })
}

// Clustering off must still give a usable object, so a service can wire it in
// unconditionally and switch it on by dotenv.
func TestDisabled_cluster_is_inert(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ENABLED": "false"})

	c, err := New(testAppConfig(), cfg, Options{
		Roles:  []RoleSpec{{Name: "scheduler", Mode: ModeLeader}},
		Logger: testLogger(),
	})
	require.NoError(t, err)

	assert.False(t, c.Enabled())
	require.NoError(t, c.Start(context.Background()))
	assert.Empty(t, c.Roles())
	assert.Empty(t, c.InstanceAddr())
	assert.Nil(t, c.Err())

	_, err = c.Role("scheduler")
	assert.ErrorIs(t, err, ErrDisabled)

	_, err = c.KVClient("anything")
	assert.ErrorIs(t, err, ErrDisabled)

	_, err = c.GossipPort()
	assert.ErrorIs(t, err, ErrDisabled)

	_, err = c.MemberlistKV()
	assert.ErrorIs(t, err, ErrDisabled)

	require.NoError(t, c.Stop(context.Background()))
}

func TestRole_before_start(t *testing.T) {
	c := newTestCluster(t, nil, uniqueRole(t, "a", ModeLeader))

	_, err := c.Role(c.Roles()[0])
	assert.ErrorIs(t, err, ErrNotStarted)
}

func TestLifecycle_guards(t *testing.T) {
	role := uniqueRole(t, "a", ModeLeader)
	c := startTestCluster(t, nil, role)

	assert.ErrorIs(t, c.Start(context.Background()), ErrAlreadyStarted)
	assert.NotNil(t, c.Err())

	require.NoError(t, c.Stop(context.Background()))

	// Idempotent, and every query reports the cluster is gone rather than
	// reading a half-torn-down ring.
	require.NoError(t, c.Stop(context.Background()))
	assert.ErrorIs(t, c.Start(context.Background()), ErrStopped)

	_, err := c.Role(role.Name)
	assert.ErrorIs(t, err, ErrStopped)
}

func TestStop_without_start(t *testing.T) {
	c := newTestCluster(t, nil, uniqueRole(t, "a", ModeLeader))
	require.NoError(t, c.Stop(context.Background()))
}

// A binary may know more roles than a deployment runs.
func TestRole_declared_but_not_selected(t *testing.T) {
	joined := uniqueRole(t, "joined", ModeLeader)
	idle := uniqueRole(t, "idle", ModeBroadcast)

	c := startTestCluster(t, map[string]string{
		"INFRAPI_CLUSTER_ROLES": `["` + joined.Name + `"]`,
	}, joined, idle)

	assert.Equal(t, []string{joined.Name}, c.Roles())

	_, err := c.Role(idle.Name)
	assert.ErrorIs(t, err, ErrRoleNotJoined)
}

// Roles are reported in declaration order, not in the order the dotenv happened
// to list them.
func TestRoles_are_in_declaration_order(t *testing.T) {
	first := uniqueRole(t, "aaa", ModeLeader)
	second := uniqueRole(t, "bbb", ModeBroadcast)

	c := startTestCluster(t, map[string]string{
		"INFRAPI_CLUSTER_ROLES": `["` + second.Name + `","` + first.Name + `"]`,
	}, first, second)

	assert.Equal(t, []string{first.Name, second.Name}, c.Roles())
}

func TestCluster_accessors(t *testing.T) {
	role := uniqueRole(t, "a", ModeLeader)
	c := startTestCluster(t, nil, role)

	assert.Equal(t, storeInmemory, c.Store())
	assert.NotEmpty(t, c.InstanceID())
	assert.Contains(t, c.InstanceAddr(), "127.0.0.1:")

	// The gossip accessors belong to the memberlist store only.
	_, err := c.GossipPort()
	assert.ErrorIs(t, err, ErrWrongStore)

	_, err = c.MemberlistKV()
	assert.ErrorIs(t, err, ErrWrongStore)
}

// testCodec is a real dskit codec over the ring's own message type, so a
// key-value round trip can be tested without inventing a protobuf.
func testCodec() codec.Codec {
	return codec.NewProtoCodec("infrapi-test", func() proto.Message { return &ring.Desc{} })
}

func TestKVClient(t *testing.T) {
	c := startTestCluster(t, map[string]string{"INFRAPI_CLUSTER_ROLES": "[]"})

	_, err := c.KVClient("nope")
	assert.ErrorIs(t, err, ErrCodecUnknown)
}

func TestKVClient_round_trip(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": "[]"})

	c, err := New(testAppConfig(), cfg, Options{
		Codecs:     []codec.Codec{testCodec()},
		Logger:     testLogger(),
		Registerer: prometheus.NewRegistry(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	require.NoError(t, c.Start(context.Background()))

	client, err := c.KVClient("infrapi-test")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := "state-" + c.InstanceID()
	err = client.CAS(ctx, key, func(any) (any, bool, error) {
		return &ring.Desc{Ingesters: map[string]ring.InstanceDesc{"a": {Id: "a", Addr: "127.0.0.1:1"}}}, true, nil
	})
	require.NoError(t, err)

	got, err := client.Get(ctx, key)
	require.NoError(t, err)
	desc, ok := got.(*ring.Desc)
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1:1", desc.Ingesters["a"].Addr)
}

// Two roles must each get their own ring and their own key-value clients. This
// is the direct test of the collision risk: ring metrics are told apart by a
// "name" const label and key-value metrics by a "kv_name" one, and a mistake
// there is a panic, not an error.
func TestTwoRoles_have_distinct_rings_and_metrics(t *testing.T) {
	leader := uniqueRole(t, "sched", ModeLeader)
	broadcast := uniqueRole(t, "coll", ModeBroadcast)

	registry := prometheus.NewRegistry()
	cfg := testClusterConfig(t, map[string]string{
		"INFRAPI_CLUSTER_ROLES": `["` + leader.Name + `","` + broadcast.Name + `"]`,
	})

	c, err := New(testAppConfig(), cfg, Options{
		Roles:      []RoleSpec{leader, broadcast},
		Logger:     testLogger(),
		Registerer: registry,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	require.NoError(t, c.Start(context.Background()))

	leaderRole, err := c.Role(leader.Name)
	require.NoError(t, err)
	broadcastRole, err := c.Role(broadcast.Name)
	require.NoError(t, err)

	assert.NotSame(t, leaderRole.Ring(), broadcastRole.Ring())
	assert.NotEqual(t, leaderRole.Key(), broadcastRole.Key())
	assert.Equal(t, ModeLeader, leaderRole.Mode())
	assert.Equal(t, ModeBroadcast, broadcastRole.Mode())

	families, err := registry.Gather()
	require.NoError(t, err)

	ringNames := map[string]bool{}
	kvNames := map[string]bool{}
	for _, family := range families {
		switch family.GetName() {
		case MetricsPrefix + "ring_members":
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "name" {
						ringNames[label.GetValue()] = true
					}
				}
			}
		case MetricsPrefix + "kv_request_duration_seconds":
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "kv_name" {
						kvNames[label.GetValue()] = true
					}
				}
			}
		}
	}

	assert.True(t, ringNames[leader.Name], "the leader ring must label its metrics with its own name")
	assert.True(t, ringNames[broadcast.Name], "the broadcast ring must label its metrics with its own name")

	// One client for the ring and one for the lifecycler, per role.
	for _, want := range []string{
		leader.Name + "-ring", leader.Name + "-lifecycler",
		broadcast.Name + "-ring", broadcast.Name + "-lifecycler",
	} {
		assert.True(t, kvNames[want], "expected a key-value client named %q", want)
	}
}

// A store that cannot be reached must fail Start, and the unwind must leave
// nothing running: Stop afterwards has to be clean and immediate.
func TestStart_unwinds_a_failure(t *testing.T) {
	role := uniqueRole(t, "a", ModeLeader)
	cfg := testClusterConfig(t, map[string]string{
		"INFRAPI_CLUSTER_KV_STORE":          storeEtcd,
		"INFRAPI_CLUSTER_ETCD_ENDPOINTS":    `["127.0.0.1:1"]`,
		"INFRAPI_CLUSTER_ETCD_DIAL_TIMEOUT": "300ms",
		"INFRAPI_CLUSTER_ETCD_MAX_RETRIES":  "1",
		"INFRAPI_CLUSTER_START_TIMEOUT":     "3s",
		"INFRAPI_CLUSTER_ROLES":             `["` + role.Name + `"]`,
	})

	c, err := New(testAppConfig(), cfg, Options{
		Roles:      []RoleSpec{role},
		Logger:     testLogger(),
		Registerer: prometheus.NewRegistry(),
	})
	require.NoError(t, err)

	err = c.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), role.Name)

	// Already unwound, so this is a no-op rather than a second teardown.
	require.NoError(t, c.Stop(context.Background()))
	assert.ErrorIs(t, c.Start(context.Background()), ErrStopped)
}

func TestNewDskitConfig_translates_the_dotenv(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{
		"INFRAPI_CLUSTER_KV_STORE":             storeMemberlist,
		"INFRAPI_CLUSTER_KV_PREFIX":            "rings/",
		"INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL": "prod-eu",
		"INFRAPI_CLUSTER_JOIN_MEMBERS":         `["10.0.0.1:7946","10.0.0.2:7946"]`,
		"INFRAPI_CLUSTER_GOSSIP_BIND_ADDRS":    `["127.0.0.1"]`,
		"INFRAPI_CLUSTER_GOSSIP_BIND_PORT":     "0",
		"INFRAPI_CLUSTER_INSTANCE_PORT":        "9000",
		"INFRAPI_CLUSTER_ROLES":                "[]",
	})

	d, err := newDskitConfig(testAppConfig(), cfg, []codec.Codec{testCodec()}, "127.0.0.1")
	require.NoError(t, err)

	assert.Equal(t, storeMemberlist, d.kv.Store)
	assert.Equal(t, "rings/", d.kv.Prefix)
	assert.Equal(t, "prod-eu", d.memberlist.ClusterLabel)
	assert.Equal(t, []string{"10.0.0.1:7946", "10.0.0.2:7946"}, []string(d.memberlist.JoinMembers))
	assert.Equal(t, []string{"127.0.0.1"}, []string(d.memberlist.TCPTransport.BindAddrs))
	assert.Equal(t, "127.0.0.1:9000", d.lifecycler.Addr)
	assert.Equal(t, "dc1", d.lifecycler.Zone)
	assert.Equal(t, cfg.InstanceID, d.memberlist.NodeName)

	// The node name is the identity operators grep for, so dskit's random
	// suffix is turned off.
	assert.False(t, d.memberlist.RandomizeNodeName)

	// The ring codec always comes first, then the caller's.
	require.Len(t, d.memberlist.Codecs, 2)
	assert.Equal(t, "infrapi-test", d.memberlist.Codecs[1].CodecID())

	// Per-mode parameters come from the mode, not from the dotenv.
	assert.Equal(t, 1, d.ringConfigFor(modeSpecs[ModeLeader]).ReplicationFactor)
	assert.Equal(t, 1, d.lifecyclerConfigFor(modeSpecs[ModeLeader]).NumTokens)
	assert.True(t, d.ring.SubringCacheDisabled)
}

// dskit's own validation is a guard against a future refactor, not something a
// dotenv can trip, so it is tested where it lives rather than through New.
func TestValidateDskitConfig(t *testing.T) {
	sound := ring.Config{HeartbeatTimeout: time.Minute}

	var soundGossip memberlist.KVConfig
	flagext.DefaultValues(&soundGossip)

	require.NoError(t, validateDskitConfig(sound, soundGossip, storeMemberlist))
	require.NoError(t, validateDskitConfig(sound, soundGossip, storeInmemory))

	err := validateDskitConfig(ring.Config{}, soundGossip, storeInmemory)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ring configuration")

	// A zero-value gossip configuration has empty message queues, which dskit
	// rejects.
	err = validateDskitConfig(sound, memberlist.KVConfig{}, storeMemberlist)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gossip configuration")

	// The gossip check applies to the gossip store only.
	require.NoError(t, validateDskitConfig(sound, memberlist.KVConfig{}, storeEtcd))
}

func TestJoinedRoles_declaration_order_is_stable(t *testing.T) {
	specs := []RoleSpec{
		{Name: "zeta", Mode: ModeLeader},
		{Name: "alpha", Mode: ModeBroadcast},
	}
	declared, err := declaredRoles(specs)
	require.NoError(t, err)

	order, err := joinedRoles(specs, declared, []string{"alpha", "zeta"})
	require.NoError(t, err)
	assert.Equal(t, []string{"zeta", "alpha"}, order)
}

func TestSortedNames(t *testing.T) {
	declared, err := declaredRoles([]RoleSpec{
		{Name: "zeta", Mode: ModeLeader},
		{Name: "alpha", Mode: ModeBroadcast},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "zeta"}, sortedNames(declared))
}

// An error from New must not leave the registerer claimed, or a service that
// fixes its configuration and retries would be told the registry is taken.
func TestNew_releases_the_registerer_on_failure(t *testing.T) {
	registry := prometheus.NewRegistry()
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": "[]"})
	cfg.InstanceAddr = ""
	cfg.InstanceInterfaceNames = []string{"no-such-interface0"}

	_, err := New(testAppConfig(), cfg, Options{Logger: testLogger(), Registerer: registry})
	require.Error(t, err)

	cfg = testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": "[]"})
	second, err := New(testAppConfig(), cfg, Options{Logger: testLogger(), Registerer: registry})
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Stop(context.Background()) })
}

func TestNew_reports_an_unresolvable_instance_address(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": "[]"})
	cfg.InstanceAddr = ""
	cfg.InstanceInterfaceNames = []string{"no-such-interface0"}

	_, err := New(testAppConfig(), cfg, Options{Logger: testLogger(), Registerer: prometheus.NewRegistry()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve instance address")
	assert.True(t, errors.Is(err, err), "the error is returned, never fatal")
}

// Zones reach dskit in two unrelated places, which is the whole reason they are
// resolved once: the ring spreads replicas across them, the gossip layer keeps
// traffic inside them.
func TestNewDskitConfig_zones(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{
		"INFRAPI_CLUSTER_KV_STORE":                          storeMemberlist,
		"INFRAPI_CLUSTER_ROLES":                             "[]",
		"INFRAPI_CLUSTER_ZONE":                              "dc2",
		"INFRAPI_CLUSTER_ZONE_AWARENESS_ENABLED":            "true",
		"INFRAPI_CLUSTER_EXCLUDED_ZONES":                    `["dc3"]`,
		"INFRAPI_CLUSTER_GOSSIP_ZONE_AWARE_ROUTING_ENABLED": "true",
		"INFRAPI_CLUSTER_GOSSIP_ZONE_ROLE":                  "bridge",
	})

	d, err := newDskitConfig(testAppConfig(), cfg, nil, "127.0.0.1")
	require.NoError(t, err)

	// The ring side: spreading replicas, and filtering a zone out of the view.
	assert.True(t, d.ring.ZoneAwarenessEnabled)
	assert.Equal(t, []string{"dc3"}, []string(d.ring.ExcludedZones))

	// The gossip side: who talks to whom.
	assert.True(t, d.memberlist.ZoneAwareRouting.Enabled)
	assert.Equal(t, "dc2", d.memberlist.ZoneAwareRouting.Zone)
	assert.Equal(t, "bridge", d.memberlist.ZoneAwareRouting.Role)

	// And the replica's own zone, which is what every peer reads off its ring
	// entry and its gossip metadata.
	assert.Equal(t, "dc2", d.lifecycler.Zone)
}

// Without a zone of its own, a replica takes the one the service says it runs
// in, so a deployment that already sets INFRAPI_APP_LOCATION needs nothing more.
func TestNewDskitConfig_zone_falls_back_to_the_app_location(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": "[]"})

	d, err := newDskitConfig(testAppConfig(), cfg, nil, "127.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, "dc1", d.lifecycler.Zone, "testAppConfig runs in dc1")
}

// Zone-aware routing without a zone would silently do nothing, so it is refused
// rather than accepted and ignored.
func TestNewDskitConfig_rejects_zone_routing_without_a_zone(t *testing.T) {
	app := testAppConfig()
	app.AppLocation = ""

	cfg := testClusterConfig(t, map[string]string{
		"INFRAPI_CLUSTER_KV_STORE":                          storeMemberlist,
		"INFRAPI_CLUSTER_ROLES":                             "[]",
		"INFRAPI_CLUSTER_GOSSIP_ZONE_AWARE_ROUTING_ENABLED": "true",
	})

	_, err := newDskitConfig(app, cfg, nil, "127.0.0.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFRAPI_CLUSTER_GOSSIP_ZONE_AWARE_ROUTING_ENABLED")
	assert.Contains(t, err.Error(), "INFRAPI_APP_LOCATION")
}

// A role may raise its replication factor, which is what makes the ring's zone
// awareness do anything: it spreads those replicas across zones.
func TestRoleSpec_replication_factor_override(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": "[]"})

	d, err := newDskitConfig(testAppConfig(), cfg, nil, "127.0.0.1")
	require.NoError(t, err)

	// The modes shipped here both use one replica, so zone awareness has
	// nothing to spread until a role asks for more.
	assert.Equal(t, 1, d.ringConfigFor(modeSpecs[ModeLeader]).ReplicationFactor)
	assert.Equal(t, 1, d.ringConfigFor(modeSpecs[ModeBroadcast]).ReplicationFactor)
}
