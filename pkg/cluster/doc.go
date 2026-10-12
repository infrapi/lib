// Package cluster runs an InfraPI service as several coordinated replicas.
//
// It wraps Grafana's dskit: a key-value store shared by every replica, one
// hash ring per role, and a lifecycler that registers this replica and keeps
// its heartbeat fresh. What a service gets out of it is a single predicate,
// [Role.ShouldRun], answering "must I act right now?".
//
// # Roles
//
// A role is one ring. A service declares its roles in Go, because the
// coordination mode decides what the application code does, and the dotenv
// lists the roles this particular replica joins — the equivalent of Mimir's
// -target flag.
//
//	c, err := cluster.New(appCfg, clusterCfg, cluster.Options{
//		Roles: []cluster.RoleSpec{
//			{Name: "scheduler", Mode: cluster.ModeLeader},
//			{Name: "collector", Mode: cluster.ModeAll},
//		},
//	})
//
// Several roles share one key-value store and, with the memberlist store, one
// gossip mesh: they are separated by the key their ring is stored under. That
// is the Mimir layout, and it means different services can join one mesh and
// each register in the ring of its own role.
//
// # Two separations not to confuse
//
// The gossip cluster label (INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL) decides which
// mesh a replica belongs to. memberlist discards any packet whose label does
// not match, so a wrong label makes a replica invisible rather than noisy.
//
// The ring key (INFRAPI_CLUSTER_KV_PREFIX plus the role name) decides which
// ring inside that mesh. Replicas with the same label and different role names
// gossip with each other and ignore each other's rings.
//
// # The ring is not a distributed lock
//
// Leader election here is ownership of a ring token, refreshed by heartbeat.
// There is no quorum and no lease: during a network partition two replicas can
// each believe they are the leader, and the only bound on that overlap is
// INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT. An action gated on [Role.ShouldRun] must
// therefore be idempotent, or fenced by whatever it writes to. See [Role.Run],
// which states the same thing where it matters most.
//
// # The key-value store is a membership store, not a cache
//
// Every key costs a broadcast worker and a slot in the state every replica
// gossips. Keys must be few and long-lived: a key per request, per job or per
// challenge is how this turns into an out-of-memory incident.
//
// # Dependency surface
//
// This package lives in its own Go module so that a service importing only
// config, http, listener or server does not inherit dskit's dependency tree.
// Because [Role.Ring], [Role.Lifecycler] and [Cluster.KVClient] return dskit
// types, this package's public API is pinned to one dskit pseudo-version: a
// breaking change there is a breaking change here.
package cluster
