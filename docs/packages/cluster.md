# cluster

```go
import "github.com/infrapi/lib/pkg/cluster"
```

Runs a service as several coordinated replicas, on top of
[dskit](https://github.com/grafana/dskit): a key-value store every replica
shares, one hash ring per role, and a lifecycler that registers this replica and
keeps its heartbeat fresh.

What a service actually gets out of it is one predicate.

```go
should, err := role.ShouldRun()
```

!!! note "Its own Go module"

    `cluster` is a separate module, so a service that does not cluster does not
    inherit dskit's dependency tree — grpc, etcd, consul, otel,
    hashicorp/memberlist.

    ```bash
    go get github.com/infrapi/lib/pkg/cluster
    ```

    The cost of that split: because `Role.Ring`, `Role.Lifecycler` and
    `Cluster.KVClient` return dskit types, this package's API is pinned to one
    dskit pseudo-version. A breaking change there is a breaking change here.

## Roles

A role is one ring. A service declares its roles in Go, because the coordination
mode decides what the application code does; the dotenv lists which of them this
replica joins, the way Mimir's `-target` does.

```go
appConfig, _ := vars.GetAppConfig()
clusterConfig, _ := vars.GetClusterConfig()

c, err := cluster.New(appConfig, clusterConfig, cluster.Options{
	Roles: []cluster.RoleSpec{
		{Name: "scheduler", Mode: cluster.ModeLeader},
		{Name: "collector", Mode: cluster.ModeBroadcast},
	},
})
if err != nil {
	return err
}

if err := c.Start(ctx); err != nil {
	return err
}
defer func() { _ = c.Stop(context.Background()) }()
```

```bash
INFRAPI_CLUSTER_ROLES=["scheduler","collector"]
```

| Mismatch | What happens |
|----------|--------------|
| A name in the dotenv the code does not declare | `New` fails with `ErrRoleUndeclared`. It is always a typo or a bad rollout, and a silent no-op would mean a scheduler that never schedules with nothing in the logs. |
| A declared role the dotenv does not select | Accepted. A binary legitimately knows more roles than a deployment runs; `Cluster.Role` reports `ErrRoleNotJoined`. |
| Roles declared, none selected | `New` fails with `ErrNoRoles`. A cluster that joins no ring coordinates nothing. |
| No roles declared at all | Accepted: a store with no rings, for a service that wants shared state and no coordination. |

Two roles that share a name, or a ring key, are refused by `New`. They would
both register the same Prometheus collectors, and `promauto` panics rather than
returning an error.

## Modes

```go
const (
	cluster.ModeLeader    // only the owner of the leader token acts
	cluster.ModeBroadcast // every healthy member acts
)
```

| | `ModeLeader` | `ModeBroadcast` |
|---|---|---|
| `ShouldRun` is true on | the owner of the leader token, among the members [that may lead](#choosing-who-may-lead) | every healthy member |
| Ring states accepted | `ACTIVE`, `LEAVING` | `ACTIVE` |
| `Leader`, `IsLeader` | the elected replica | `ErrWrongMode` |

`ModeLeader` counts a `LEAVING` replica as eligible, which is what keeps a
rolling restart from changing leader at every step. `ModeBroadcast` does not: a
replica that is draining must stop working.

A non-nil error from `ShouldRun` means **do not act**. It says the ring view is
unusable, not that this replica was excluded.

## Choosing who may lead

By default every member of a leader role may lead, and the leader is the owner of
the leader token: chance. Leader checks decide it from the outside world — the
active data centre, the node holding a VIP, a maintenance flag, a dependency this
node can reach. Each member runs them on a timer and publishes the answer in the
ring; the leader is elected **among the members that may lead**.

```text
INFRAPI_CLUSTER_LEADER_CHECKS=["!file:///etc/infrapi/maintenance", "tcp://db.dc1:5432", "exec:/usr/local/bin/is-primary dc1"]
```

| check | passes when |
|---|---|
| `tcp://host:port`, `telnet://host:port` | a TCP connection opens |
| `http://…`, `https://…` | a GET answers 2xx — redirects are not followed |
| `exec:/path/to/cmd arg …` | the command exits 0; no shell, arguments split on spaces; its output explains a failure |
| `file:///path` | the file exists |
| `!<check>` | the check fails — `!file:///etc/infrapi/maintenance` is "no maintenance flag" |

- **Every check must pass.** The first that fails is the reason shown.
- **No member may lead: no leader.** `Leader` reports `ErrNoLeader`, `ShouldRun`
  an error, and nothing acts until a member may lead again — never a fallback
  to one that may not.
- A check that times out fails, negated or not: an unknown condition is not a pass.
- A check that does not parse is refused by `New`: a condition silently dropped is
  a node that leads when it should not.
- Code can add its own: `RoleSpec.LeaderCheck`, a `func(ctx) error` run with the
  configured ones.

| Key | Default | Notes |
|-----|---------|-------|
| `INFRAPI_CLUSTER_LEADER_CHECKS` | `[]` | The checks, all of which must pass. Empty: every member may lead. |
| `INFRAPI_CLUSTER_LEADER_WRITES` | `false` | Read by [`app`](app.md): a service's writes go through the leader of its writer ring. |
| `INFRAPI_CLUSTER_LEADER_CHECK_INTERVAL` | `10s` | How often they run. |
| `INFRAPI_CLUSTER_LEADER_CHECK_TIMEOUT` | `5s` | Each check's budget. |
| `INFRAPI_CLUSTER_LEADER_CHECK_THRESHOLD` | `2` | Identical results in a row before the published answer changes: one blip does not move the leadership. The first answer, at start, is published at once. |

The answer travels as the instance's read-only flag, dskit's own: a read-only
instance takes no writes, which is exactly a member that must not lead. dskit's
token lookup does not skip read-only instances, so `Leader` picks among the
others itself — the smallest token, as before. A failover therefore takes about
`threshold × interval` plus the gossip round that carries the flag.

The status page shows, for this replica, whether it may lead and which check
said no; the ring page has a "May lead" column; `Member.MayLead` and
`Role.LeaderEligibility` say the same to code.

### A gated periodic action

```go
err := role.Run(ctx, 30*time.Second, func(runCtx context.Context) error {
	return reconcile(runCtx)
})
```

`Run` re-checks `ShouldRun` on every tick, never overlaps two runs, and cancels
the action's context as soon as this replica stops being eligible. An error from
the action is logged and the loop continues: a transient empty ring during a
gossip partition must not kill a service's control loop.

!!! warning "The ring is not a distributed lock"

    Leader election here is ownership of a ring token, refreshed by heartbeat.
    There is no quorum and no lease. During a network partition two replicas can
    each believe they are the leader, and the cancellation `Run` performs is
    cooperative: an action that ignores its context, or is blocked in a syscall,
    keeps running.

    The overlap is bounded by `INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT` plus one
    watchdog interval. Nothing closes it. **The action must be idempotent, or
    fenced by whatever it writes to** — a compare-and-swap, a conditional write,
    a unique constraint.

## Two separations not to confuse

This is the part that costs an afternoon if it is got wrong.

| | Set by | Decides |
|---|---|---|
| Gossip cluster label | `INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL` | Which gossip mesh a replica belongs to. memberlist **discards** any packet whose label does not match, so a wrong label makes a replica invisible rather than noisy. |
| Ring key | `INFRAPI_CLUSTER_KV_PREFIX` + the role name | Which ring inside that mesh. |

Replicas sharing a label gossip with each other and ignore each other's rings.
That is what lets several different services join one mesh and each register in
the ring of its own role, which is how Mimir runs an ingester ring, a ruler ring
and a compactor ring over one cluster.

```text
gossip mesh, label "prod-eu-west-1"
  collectors/scheduler  -> the ring of role "scheduler" (leader)
  collectors/collector  -> the ring of role "collector" (broadcast)
  collectors/state      -> whatever a caller codec writes
```

Changing the label on a live cluster needs two rollouts, with
`INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL_VERIFICATION_DISABLED=true` in between.

`RoleSpec.Key` overrides the key, and exists for one reason: adopting this
package on a deployment whose ring is already written under a different key,
without resetting it.

## Backends

`INFRAPI_CLUSTER_KV_STORE` selects the store. Everything above it is
backend-agnostic.

| Store | Use |
|-------|-----|
| `memberlist` | Gossip between replicas. No external dependency, one TCP port. The default. |
| `etcd` | A central store, when one is already operated. |
| `inmemory` | A process-wide map. **Tests and single-replica runs only**: it coordinates nothing beyond the process, so two replicas on it each get their own ring and each elect themselves. `INFRAPI_CLUSTER_JOIN_MEMBERS` with it is refused, which is the one mistake it can catch for you. |

dskit also implements a `consul` store and a mirroring `multi` store for
migrating between two backends. Neither is wired up here; both would cost one
widened validator and a key block.

!!! danger "The gossip store is memory, and only memory"

    There is no persistence in it at all — no snapshot, no file, nothing on
    disk. The cluster's own memory *is* the storage, replicated across whichever
    replicas happen to be alive.

    A replica that restarts recovers by pulling the state from a live peer, so a
    rolling restart is safe. **Stopping every replica at once loses everything**:
    a bad rollout that restarts them together, a region-wide outage, a scale to
    zero. Nothing warns you, and nothing brings it back.

    So the gossip store holds coordination state that can be rebuilt — who is a
    member, who leads, a view derived from somewhere else. It is not a system of
    record. If the data has to survive the cluster, either use `etcd`, which
    persists it, or treat the gossip store as a cache each replica repopulates
    at startup from the real source of truth.

| Store | Survives a full cluster stop? |
|-------|-------------------------------|
| `memberlist` | **No.** Memory only, replicated across live replicas. |
| `etcd` | Yes, to etcd's disk. |
| `inmemory` | No, and not even a process restart. |

!!! info "The gossip transport is TCP only"

    dskit's memberlist transport uses TCP for both its packet and its stream
    traffic. One port, no UDP: a firewall rule needs TCP `7946` and nothing
    else.

## Configuration

Every key is read from the dotenv through `config.GetClusterConfig`, which stops
on the first key that is missing or fails its validator, then checks the rules
that span several keys.

### Core

| Key | Default | Notes |
|-----|---------|-------|
| `INFRAPI_CLUSTER_ENABLED` | `false` | Off means `New` still returns a usable object whose `Start` is a no-op and whose queries report `ErrDisabled`. |
| `INFRAPI_CLUSTER_KV_STORE` | `memberlist` | `memberlist`, `etcd` or `inmemory`. |
| `INFRAPI_CLUSTER_KV_PREFIX` | `collectors/` | Must end in `/`. |
| `INFRAPI_CLUSTER_ROLES` | `[]` | The roles this replica joins. |
| `INFRAPI_CLUSTER_INSTANCE_ID` | the host name | The ring identity, and the gossip node name. |
| `INFRAPI_CLUSTER_INSTANCE_ADDR` | auto-detected | Optional; otherwise found from the interface names below. |
| `INFRAPI_CLUSTER_INSTANCE_INTERFACE_NAMES` | `["eth0", "en0"]` | |
| `INFRAPI_CLUSTER_INSTANCE_PORT` | `7946` | The port advertised in the ring. |
| `INFRAPI_CLUSTER_START_TIMEOUT` | `30s` | Bounds the whole of `Start`. |
| `INFRAPI_CLUSTER_STOP_TIMEOUT` | `45s` | Must exceed the gossip leave timeout plus the broadcast drain. A ceiling, not a wait. |

### Ring and lifecycler

| Key | Default | Notes |
|-----|---------|-------|
| `INFRAPI_CLUSTER_HEARTBEAT_PERIOD` | `15s` | |
| `INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT` | `1m` | Must be greater than the period, or every replica flaps. |
| `INFRAPI_CLUSTER_KEEP_INSTANCE_IN_RING_ON_SHUTDOWN` | `true` | See the failover note below. |
| `INFRAPI_CLUSTER_AUTO_FORGET_UNHEALTHY_PERIODS` | `10` | Multiples of the heartbeat **timeout**. `0` disables it; `1` is refused. |
| `INFRAPI_CLUSTER_TOKENS_OBSERVE_PERIOD` | `0s` | |

### Zones

| Key | Default | Notes |
|-----|---------|-------|
| `INFRAPI_CLUSTER_ZONE` | `INFRAPI_APP_LOCATION` | Where this replica runs. At most 16 bytes: it travels in every gossip packet. |
| `INFRAPI_CLUSTER_ZONE_AWARENESS_ENABLED` | `false` | Ring-level: spread a key's replicas across zones. Does nothing until a role's replication factor is above 1. |
| `INFRAPI_CLUSTER_EXCLUDED_ZONES` | `[]` | Zones to filter out of this replica's view of the ring. Listing your own is refused. |
| `INFRAPI_CLUSTER_GOSSIP_ZONE_AWARE_ROUTING_ENABLED` | `false` | Gossip-level: keep gossip inside a zone. Read the next section before enabling it. |
| `INFRAPI_CLUSTER_GOSSIP_ZONE_ROLE` | `member` | `member` gossips only within its zone; `bridge` also reaches bridges in other zones. |

There is no key for the replication factor or the token count. Both are
semantics of the coordination mode, which lives in code: an operator who could
set a replication factor of 3 on a leader ring would break the election, so the
dotenv must not be able to express it. `RoleSpec.NumTokens` is the override, in
the same place as the decision it belongs to.

### Gossip

Applies only when the store is `memberlist`.

| Key | Default |
|-----|---------|
| `INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL` | `infrapi` |
| `INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL_VERIFICATION_DISABLED` | `false` |
| `INFRAPI_CLUSTER_GOSSIP_BIND_ADDRS` | `["0.0.0.0"]` |
| `INFRAPI_CLUSTER_GOSSIP_BIND_PORT` | `7946` (`0` for an ephemeral port) |
| `INFRAPI_CLUSTER_GOSSIP_ADVERTISE_ADDR` | unset (NAT traversal) |
| `INFRAPI_CLUSTER_GOSSIP_ADVERTISE_PORT` | `0` |
| `INFRAPI_CLUSTER_JOIN_MEMBERS` | `[]` |
| `INFRAPI_CLUSTER_JOIN_MIN_BACKOFF` | `1s` |
| `INFRAPI_CLUSTER_JOIN_MAX_BACKOFF` | `1m` |
| `INFRAPI_CLUSTER_JOIN_MAX_RETRIES` | `10` |
| `INFRAPI_CLUSTER_ABORT_IF_JOIN_FAILS` | `false` |
| `INFRAPI_CLUSTER_REJOIN_INTERVAL` | `0s` |
| `INFRAPI_CLUSTER_GOSSIP_INTERVAL` | `200ms` |
| `INFRAPI_CLUSTER_GOSSIP_NODES` | `3` |
| `INFRAPI_CLUSTER_PUSH_PULL_INTERVAL` | `30s` |
| `INFRAPI_CLUSTER_GOSSIP_TO_THE_DEAD_TIME` | `30s` |
| `INFRAPI_CLUSTER_LEAVE_TIMEOUT` | `20s` |
| `INFRAPI_CLUSTER_BROADCAST_TIMEOUT_ON_SHUTDOWN` | `10s` |
| `INFRAPI_CLUSTER_OBSOLETE_ENTRIES_TIMEOUT` | `30s` (a fully deleted key) |
| `INFRAPI_CLUSTER_TOMBSTONE_TIMEOUT` | `5m` (a deleted entry inside a value) |
| `INFRAPI_CLUSTER_GOSSIP_COMPRESSION_ENABLED` | `true` |
| `INFRAPI_CLUSTER_MESSAGE_HISTORY_BUFFER_BYTES` | `0` |
| `INFRAPI_CLUSTER_GOSSIP_TLS_ENABLED` | `false` |
| `INFRAPI_CLUSTER_GOSSIP_TLS_CA_PATH` | unset |
| `INFRAPI_CLUSTER_GOSSIP_TLS_CERT_PATH` | unset |
| `INFRAPI_CLUSTER_GOSSIP_TLS_KEY_PATH` | unset |
| `INFRAPI_CLUSTER_GOSSIP_TLS_SERVER_NAME` | unset |
| `INFRAPI_CLUSTER_GOSSIP_TLS_INSECURE_SKIP_VERIFY` | `false` |

`INFRAPI_CLUSTER_ABORT_IF_JOIN_FAILS=true` is worth knowing about: without it, a
replica that cannot reach its seed nodes starts anyway, forms a cluster of one,
and elects itself. Two of those are indistinguishable from a working cluster
until something goes wrong.

`INFRAPI_CLUSTER_MESSAGE_HISTORY_BUFFER_BYTES` is `0`, and should stay there.
It keeps every message the node sends and receives in memory, so it buys the
message history on the gossip page at the cost of carrying that buffer for as
long as the process lives. Turn it on while troubleshooting, turn it back off
afterwards.

### etcd

Applies only when the store is `etcd`.

| Key | Default |
|-----|---------|
| `INFRAPI_CLUSTER_ETCD_ENDPOINTS` | `[]`, and required |
| `INFRAPI_CLUSTER_ETCD_DIAL_TIMEOUT` | `10s` |
| `INFRAPI_CLUSTER_ETCD_MAX_RETRIES` | `10` |
| `INFRAPI_CLUSTER_ETCD_USERNAME` | unset |
| `INFRAPI_CLUSTER_ETCD_PASSWORD` | unset, masked in error messages |
| `INFRAPI_CLUSTER_ETCD_TLS_ENABLED` | `false` |
| `INFRAPI_CLUSTER_ETCD_TLS_CA_PATH` | unset |
| `INFRAPI_CLUSTER_ETCD_TLS_CERT_PATH` | unset |
| `INFRAPI_CLUSTER_ETCD_TLS_KEY_PATH` | unset |
| `INFRAPI_CLUSTER_ETCD_TLS_SERVER_NAME` | unset |
| `INFRAPI_CLUSTER_ETCD_TLS_INSECURE_SKIP_VERIFY` | `false` |

### What is left at dskit's defaults

Stream timeouts, retransmit factor, dead node reclaim time, the message queue
sizes, the compression algorithm, the CAS retry backoffs, the TCP transport's
packet timeouts and write concurrency, the TLS cipher suites and minimum
version, zone awareness, and the whole consul and multi-store surface.

The rule for adding a key: an operator would plausibly change it to fix a
production problem, or a test needs it to run fast. Everything above is either a
dskit internal tuning constant or a knob whose correct value is "whatever dskit
says".

## Several data centres

Two unrelated dskit features are both called zone awareness, and choosing the
wrong one is how a multi-DC cluster ends up split without anyone noticing.

| | Key | What it decides |
|---|---|---|
| Ring | `INFRAPI_CLUSTER_ZONE_AWARENESS_ENABLED` | **How many replicas share a piece of work, and where.** With a replication factor above 1, the ring puts those replicas in different zones. |
| Gossip | `INFRAPI_CLUSTER_GOSSIP_ZONE_AWARE_ROUTING_ENABLED` | **Who talks to whom.** Whether a replica gossips with every peer or only with those in its own zone. |

The replication factor is the first of those, and it is not what decides whether
replicas in different data centres talk to each other. The two modes shipped
here both use a single replica, so ring zone awareness has nothing to spread
until a role raises `RoleSpec.ReplicationFactor`.

What follows is the second one: three topologies, and they are genuinely
different deployments rather than three settings of the same one.

### Everyone talks to everyone

The default. One mesh, every replica gossiping with every other, whatever data
centre it sits in. One ring per role, spanning all of them, so a role elects one
leader across the whole deployment.

```bash
INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL="prod"
# zone-aware routing left off
```

Simple, and the right answer until cross-DC gossip traffic becomes a problem.
Every replica holds the whole state and chatters with everyone.

### One mesh, gossip kept local, bridges between zones

Still one logical cluster and one ring per role, but gossip stays inside a zone
instead of crossing between every pair of replicas. A handful of replicas are
promoted to `bridge` and carry traffic between zones.

```bash
# every replica
INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL="prod"
INFRAPI_CLUSTER_GOSSIP_ZONE_AWARE_ROUTING_ENABLED="true"
INFRAPI_CLUSTER_ZONE="dc1"           # or dc2, dc3

# most of them
INFRAPI_CLUSTER_GOSSIP_ZONE_ROLE="member"

# at least one per zone
INFRAPI_CLUSTER_GOSSIP_ZONE_ROLE="bridge"
```

A `member` selects **only** peers in its own zone. A `bridge` selects its own
zone, plus the bridges of other zones. So the bridges are the only path state
takes between data centres.

!!! note "A zone without a live bridge degrades, it does not split"

    dskit notices. When a zone has no bridge alive, it abandons zone-aware
    routing and goes back to selecting every peer, logging
    `memberlist zone-aware routing is skipped because a zone has no alive bridge`
    and counting it in
    `infrapi_cluster_memberlist_client_zone_aware_routing_select_nodes_skipped_total`.

    So losing a zone's bridges costs the traffic saving, not the cluster: the
    ring stays whole and keeps one leader. Verified by running
    `examples/cluster/zones` and killing a bridge — the remaining three replicas
    still agree on their membership and their leader.

    Run at least two bridges per zone anyway, and alert on that counter: a
    cluster silently gossiping full-mesh is doing the thing you turned this on
    to avoid.

### Independent clusters, one per data centre

If replicas genuinely should not coordinate across data centres — a leader per
DC rather than one leader overall — then do not reach for zone-aware routing at
all. Give each DC its own **gossip cluster label**:

```bash
# dc1
INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL="prod-dc1"
# dc2
INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL="prod-dc2"
```

memberlist discards any packet whose label does not match, so these are separate
meshes with separate rings, by construction and not by configuration that could
drift. No bridges, no split-brain risk, no cross-DC traffic. It is the right
answer whenever the work itself is per-DC.

Choosing between the last two comes down to one question: **is there one leader
for the whole deployment, or one per data centre?** One overall needs a single
mesh and bridges; one per DC needs separate labels. Note that the middle option
is a traffic optimisation, not an isolation mechanism: every replica still ends
up holding the whole cluster's state. If zones must not know about each other,
only separate labels give you that.

`examples/cluster/zones` runs this topology on one machine — two zones, a bridge
and a member in each — and its README walks through killing a bridge.

### Affinity: work handled in its own data centre

Zone-aware routing does **not** give you this. It decides who gossips with whom,
not who acts: the ring spans every zone and elects one leader wherever the token
lands, which may well be the other side of the country. Two ways to get affinity
instead, and they differ in whether the data centres can still see each other.

**One ring per data centre, one mesh.** A role is a ring, so a role named after
the zone gives each data centre its own leader election while every replica
stays in one cluster — and can therefore read every other zone's ring on the
status pages.

```go
zone := clusterConfig.Zone // or appConfig.AppLocation

c, err := cluster.New(appConfig, clusterConfig, cluster.Options{
	Roles: []cluster.RoleSpec{
		// Every zone the binary may run in, declared up front.
		{Name: "scheduler-dc1", Mode: cluster.ModeLeader},
		{Name: "scheduler-dc2", Mode: cluster.ModeLeader},

		// And one ring the whole deployment shares, if something does need a
		// single leader overall.
		{Name: "migrations", Mode: cluster.ModeLeader},
	},
})
```

```bash
# in dc1
INFRAPI_CLUSTER_ROLES=["scheduler-dc1","migrations"]
# in dc2
INFRAPI_CLUSTER_ROLES=["scheduler-dc2","migrations"]
```

Each `scheduler-dcN` ring holds only the replicas of that data centre and elects
one of them; `migrations` spans all of them and elects one overall. A replica
asking for a role it did not join gets `ErrRoleNotJoined`, so a misrouted
deployment fails loudly rather than acting in the wrong place.

**One cluster per data centre.** If the zones should not see each other at all,
give each its own `INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL`, as above. Separate
meshes, separate rings, nothing shared, no cross-DC traffic. Pick this when
there is nothing a replica in one data centre would ever want to know about
another.

What neither gives you is key-level affinity — "this particular job belongs to
dc1". That needs a sharded mode, where a key maps to a ring token, and it is not
implemented here.

### Keeping a zone out

`INFRAPI_CLUSTER_EXCLUDED_ZONES` filters instances out of *this* replica's view
of the ring, whatever the gossip layer does. It is a blunt instrument for
draining a data centre: the replicas there stay in the mesh and keep gossiping,
but this replica stops counting them as members or electing them leader. Listing
your own zone is refused, since it would leave this replica reading an empty
ring and never acting.

## Status pages

```go
engine, err := server.NewServerGin(appConfig)
if err != nil {
	return err
}

if err := c.RegisterGin(engine, cluster.HandlerOptions{}); err != nil {
	return err
}
```

| Path | Serves |
|------|--------|
| `GET /-/cluster` | This package's JSON summary: identity, store, label, and per role its mode, members, leader and whether this replica should act. |
| `GET /-/cluster/ring/{role}` | dskit's ring page. Honours `Accept: application/json`. |
| `GET /-/cluster/memberlist` | dskit's gossip page, or 404 when the store is not memberlist. |

The paths come from `HandlerOptions.PathPrefix`, which defaults to
`DefaultPathPrefix`.

They mount before `Start` and explain themselves until the cluster is up, so a
service can wire its routes once at startup. A role that cannot be read reports
its own error rather than failing the whole page: during a partition, which
roles are readable is the useful part.

`HandlerOptions.ForgetEnabled` also mounts the ring page's `POST` route, which
evicts a replica from the ring. That is an unauthenticated write to cluster
state, so it is off unless a deployment asks for it.

`HandlerOptions.PathPrefix` relocates the whole set, for instance under
`/debug/cluster`.

### The gossip page: dskit's data, this design

`/-/cluster/memberlist` shows what dskit's own page shows — the key-value store,
the gossip membership, and the message history — because that introspection
could not be reproduced from outside dskit anyway: the data behind it is
unexported.

What is ours is the rendering. `memberlist.NewHTTPStatusHandler` takes a
template, so the page is laid out in this package, in the same design as the
others, instead of dskit's unstyled default. dskit's own actions survive with
it: the `?viewKey=`, `?viewMsg=` and `?downloadKey=` links still work, because
the handler is still dskit's and only the template was replaced. Those responses
are text and raw dumps rather than pages, and come back untouched.

| Section | What it tells you |
|---------|-------------------|
| Health score | memberlist's own view of this node. `0` is healthy; anything higher means it is struggling to reach peers. |
| Cluster members | Who is in the **mesh**, with its gossip state — alive, suspect, dead, left. Not the same thing as a ring: a node appears here as soon as it joined, whatever rings it registered in. |
| Key-value store | One row per key, with the codec, the node-local version, the encoded size, and links to inspect or download the value. |
| Message history | What this node sent and received. Empty unless `INFRAPI_CLUSTER_MESSAGE_HISTORY_BUFFER_BYTES` is set, which is off by default because it buffers every message in memory; the page names the key rather than rendering empty tables. |

`Role.LifecyclerHandler` is the one page still rendered by dskit, so that one
gets the stylesheet injected into its head rather than a template of its own.

### Mounting them where you already look

The `/-/` prefix is this library's convention, and the ring page is per role
because there are now several rings. Other dskit-based services — Mimir, and
acme-manager — put a single ring at `/ring` and the gossip page at
`/memberlist`, and runbooks and dashboards point there.

Both handlers take no path parameter, so that layout is four lines:

```go
engine.GET("/memberlist", c.MemberlistHandler())

scheduler, err := c.Role("scheduler")
if err != nil {
	return err
}
engine.GET("/ring", scheduler.RingHandler())
```

`Role.LifecyclerHandler` renders the same page from this replica's lifecycler
instead of from the ring service. The only difference is the source: the
lifecycler reads the key-value store on every request, the ring serves the
snapshot it already watches. Prefer `RingHandler`; `LifecyclerHandler` is there
for a deployment that wants the uncached view, or that previously mounted the
lifecycler and wants the same bytes.

The `serve` mode of the example mounts both layouts side by side, so the two
can be compared on a live cluster.

## Metrics

Everything dskit registers is prefixed `infrapi_cluster_` and goes on
`prometheus.DefaultRegisterer`, which the [server](server.md) package already
exposes at `/-/metrics`. Nothing else to wire.

```text
infrapi_cluster_ring_members{name="scheduler",state="ACTIVE"}
infrapi_cluster_ring_member_heartbeats_total{name="scheduler"}
infrapi_cluster_kv_request_duration_seconds{kv_name="scheduler-ring"}
infrapi_cluster_memberlist_client_cluster_members_count
infrapi_cluster_leader_eligible{name="scheduler"}                       # 1 when this replica may lead
infrapi_cluster_leader_check_failures_total{name="scheduler",check="tcp://db.dc1:5432"}
```

The prefix is not decoration: dskit names its collectors `ring_members`,
`kv_request_duration_seconds` and `dns_lookups_total`, with no namespace of
their own.

Only one cluster per registerer. `dns_lookups_total` carries no label that would
tell two of them apart, so a second cluster on the same registerer is refused by
`New` rather than left to panic — pass `Options.Registerer` if a process really
needs two.

## Sharing application state

The store the rings use also carries application state. The codec must be
declared up front: the gossip store only learns its codecs when the replica
joins, and a client built later for an unregistered codec is refused.

```go
stateCodec := codec.NewProtoCodec("fleet-state", func() proto.Message { return &FleetState{} })

c, err := cluster.New(appConfig, clusterConfig, cluster.Options{
	Roles:  roles,
	Codecs: []codec.Codec{stateCodec},
})
...
client, err := c.KVClient("fleet-state")
```

With the **gossip** store the value must also implement
`memberlist.Mergeable`, and its `Merge` must be idempotent, commutative and
associative — a CRDT. Gossip delivers updates out of order and more than once,
and nothing reconciles afterwards, so a merge without those properties leaves
replicas in different states permanently. A value that is not `Mergeable` fails
loudly (`invalid type: …, expected Mergeable`), but a `Mergeable` whose merge is
subtly wrong fails silently: test the three properties directly.

The etcd and in-memory clients impose no such thing — they encode, decode and
compare-and-swap — so a bare protobuf message works there, and will not move to
the gossip store later.

`examples/cluster/state-proto` carries a worked example: the proto, the
hand-written type, and the property tests.

### Deleting: what actually happens, and when it is really gone

This is the part worth understanding before designing around it, because a
deletion is real but it is not immediate, and three different settings govern
three different stages.

**Deleting is an update.** Do not remove the entry — write it back with a
`deleted` flag. Two reasons, and both are hard:

- `WatchPrefix` does not report deletions. dskit skips its callback when the
  value comes back nil, which is exactly what a key whose value was deleted
  gives. (`WatchKey` does report them, with a nil value, but you can only watch
  a key you already know about.) An entry carrying a flag is never nil, so every
  watcher is told like any other change.
- On gossip, a peer that never heard about the removal would re-add the entry at
  the next merge. The flag is what lets `Merge` decide that "deleted at T2"
  supersedes "present at T1".

**Then it is gone in three stages**, and only the last one frees anything:

| Stage | Governed by | Default | What it does |
|-------|-------------|---------|--------------|
| Hidden from the application | nothing — every read | — | The store calls `RemoveTombstones` with a zero limit before handing a value over, so a deleted entry never reaches your code. |
| Still gossiped | `INFRAPI_CLUSTER_TOMBSTONE_TIMEOUT` | `5m` | The entry stays in the value and keeps propagating, so a replica that was down or partitioned still learns of the deletion when it comes back. After this it may be dropped for good. |
| The whole key removed | `INFRAPI_CLUSTER_OBSOLETE_ENTRIES_TIMEOUT` | `30s` | Only applies to a key whose *entire* value was deleted, which is what `kv.Client.Delete` does. Not your per-entry flag. |

The one to reason about is the middle row. `INFRAPI_CLUSTER_TOMBSTONE_TIMEOUT` is
how long the cluster remembers that something was deleted — so it is also the
longest a replica may be absent and still come back converged. A replica away
for longer than that can rejoin holding an entry everyone else has forgotten,
and re-introduce it. Set it longer than your worst realistic outage, not
shorter: the cost is a little state and bandwidth, and the alternative is an item
coming back from the dead.

dskit names that setting `-memberlist.left-ingesters-timeout` and documents it in
ring terms, which is why it is easy to miss: it is the limit passed to
`RemoveTombstones` on *any* gossiped value, not just on ring membership.

!!! danger "The store is a membership store, not a cache"

    Every key costs a broadcast worker and a slot in the state each replica
    gossips, on every push/pull. Keys must be **few and long-lived**.

    A key per request, per job or per challenge is how this becomes an
    out-of-memory incident: the workers accumulate, and the state each replica
    carries grows without bound. If state is per-item and short-lived, it does
    not belong here.

## Shutdown, and the failover delay

`Stop` leaves every role ring and then the store, in reverse order. It ignores
cancellation of its context on purpose: a shutdown is normally triggered
*because* the caller's context was cancelled, and honouring it would abandon the
gossip leave.

Budget it properly. The gossip leave costs the broadcast drain
(`INFRAPI_CLUSTER_BROADCAST_TIMEOUT_ON_SHUTDOWN`, `10s`) plus the leave itself
(`INFRAPI_CLUSTER_LEAVE_TIMEOUT`, `20s`), and each role's lifecycler takes its
turn before any of that. `INFRAPI_CLUSTER_STOP_TIMEOUT` below the sum of the
first two is refused at load time, because it would guarantee a truncated leave:
this replica would then be remembered as *dead* rather than gone, and every peer
would keep it in the ring until its heartbeat went stale. The budget is a
ceiling, so a shutdown with nothing left to say still returns at once.

!!! warning "A dying leader keeps its token"

    With `INFRAPI_CLUSTER_KEEP_INSTANCE_IN_RING_ON_SHUTDOWN=true`, the default,
    a stopping replica sits in `LEAVING` — which a leader role counts as
    eligible. **A dying leader therefore keeps the leader token until its
    heartbeat goes stale, up to `INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT`.**

    That is the trade: far fewer leader changes across a rolling restart, at the
    price of up to a minute of nothing running at the defaults. Shorten the
    heartbeat timeout, or set the key to `false` to make planned failover
    immediate at the cost of a leader change on every restart.

A cluster is single use: dskit services cannot be restarted. A `Start` that
fails means the process should exit, not rebuild the `Cluster` — the collectors
of the failed attempt are already registered. `Err` delivers the first
asynchronous failure of any underlying service, and a value on it means the same
thing.

## Running the examples

`examples/cluster` holds one directory per use case, each runnable on its own:

```bash
cd examples/cluster/leader     && go run .   # leader mode
cd examples/cluster/broadcast  && go run .   # broadcast mode
cd examples/cluster/roles      && go run .   # several roles, a ring each
cd examples/cluster/gated      && go run .   # Role.Run
cd examples/cluster/state      && go run .   # sharing state through the store
cd examples/cluster/pages      && go run .   # these status pages
cd examples/cluster/shutdown   && go run .   # Stop, and what queries say after
```

Each carries its own `app.env`, so one directory tells you exactly which keys
that case needs. They are a Go module of their own, so they run from their own
directory rather than with `go run ./examples/...`.

All seven use the in-memory store: no socket, no peer, done in under a second.
They show the API. The one that shows the behaviour is `node/`.

### Two real replicas

Clustering is the one thing here that cannot be shown in one process. Two
terminals, from `examples/cluster/node`:

```bash
INFRAPI_CONFIG_DOTENV_FILE=node1.env go run .
INFRAPI_CONFIG_DOTENV_FILE=node2.env go run .
```

`node2.env` differs from `node1.env` only in its identity, its two ports, and
the seed node it joins. Each replica prints what it believes once a second:

```text
node-1 | scheduler(leader): members=2 leader=node-1 -> ACTING  | collector(broadcast): members=2 -> ACTING
node-2 | scheduler(leader): members=2 leader=node-1 -> passive | collector(broadcast): members=2 -> ACTING
```

Interrupt the leader and watch the two modes part company. The broadcast role
drops it from its membership immediately; the leader role keeps counting it,
because it is `LEAVING` and a leader role treats that as eligible, until its
heartbeat goes stale and the survivor takes the token. The status pages are on
`http://127.0.0.1:8081/-/cluster` and `:8082`.

The dotenv files shorten the heartbeats so that an election and a failover
happen while you watch. A real deployment leaves them alone.

`examples/cluster/README.md` walks through every case in detail, including what
each line of output means.
