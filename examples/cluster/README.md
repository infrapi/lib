# cluster examples

One directory per use case, each runnable on its own:

```bash
cd examples/cluster/leader && go run .
```

Every directory carries its own `app.env`, so reading one folder tells you
exactly which configuration keys that case needs. They share a single Go module
and a small `internal/demo` helper that does nothing but load the dotenv and
start the cluster — everything specific to a case stays in its `main.go`.

| Directory | What it shows | Needs |
|-----------|---------------|-------|
| [`leader/`](leader) | Leader mode: of every replica that joined, exactly one acts | nothing |
| [`broadcast/`](broadcast) | Broadcast mode: every healthy replica acts | nothing |
| [`roles/`](roles) | Several roles in one process, a ring each | nothing |
| [`gated/`](gated) | `Role.Run`: a periodic action gated on the predicate | nothing |
| [`state/`](state) | Sharing application state through the store | nothing |
| [`state-proto/`](state-proto) | **A value of your own: protobuf plus a CRDT merge** | an ephemeral TCP port |
| [`pages/`](pages) | The status pages on the server package engine | a free TCP port |
| [`shutdown/`](shutdown) | What `Stop` does, and what queries say afterwards | nothing |
| [`node/`](node) | **A real two-node cluster over gossip** | `127.0.0.1-2`, TCP 7946, 8080 |
| [`zones/`](zones) | **Two data centres, with bridges between them** | `127.0.0.1-4`, TCP 7946, 8080 |

The first seven use the in-memory key-value store: no socket, no peer, finish in
under a second. They show the API. `node/` and `zones/` are the ones that show
the behaviour, because a single replica is trivially its own leader and tells
you nothing about an election.

## Start here: leader and broadcast

The package gives a service one predicate, `Role.ShouldRun()`, and the
coordination mode decides what it means.

```bash
cd leader && go run .
```

```text
role       scheduler, mode leader, ring key "scheduler"
should this replica act?  true
leader                    replica-1 (127.0.0.1:7946)
```

A role is declared in Go, because the mode conditions the application code:

```go
cluster.Options{Roles: []cluster.RoleSpec{
    {Name: "scheduler", Mode: cluster.ModeLeader},
}}
```

while the dotenv lists which of the declared roles *this* replica joins, the way
Mimir's `-target` flag does:

```bash
INFRAPI_CLUSTER_ROLES=["scheduler"]
```

`broadcast/` is the same shape with the other mode, and shows that asking a
broadcast role for its leader is an error rather than an invented answer.

| | `ModeLeader` | `ModeBroadcast` |
|---|---|---|
| `ShouldRun` is true on | the owner of the leader token | every healthy member |
| Ring states accepted | `ACTIVE`, `LEAVING` | `ACTIVE` |
| `Leader`, `IsLeader` | the elected replica | `ErrWrongMode` |

Including `LEAVING` is what keeps a rolling restart from changing leader at
every step. Excluding it from broadcast is what makes a draining replica stop
working. `node/` shows both, in the same second.

## roles: one cluster, several rings

```bash
cd roles && go run .
```

```text
joined roles, in declaration order: [scheduler collector]

scheduler  mode leader    ring key "scheduler"  acting true  leader replica-1
collector  mode broadcast ring key "collector"  acting true  leader n/a in this mode

compactor  cluster: role not joined by this instance: "compactor"
```

The roles share one key-value layer and, with the gossip store, one mesh. They
are separated by the key their ring is stored under. That is the Mimir layout,
and it means several different services can join one mesh, each registering in
the ring of its own role and ignoring the others'.

Two mismatches, deliberately treated differently:

- a role **declared but not selected** is fine, and `Cluster.Role` reports
  `ErrRoleNotJoined` — a binary legitimately knows more roles than a deployment
  runs;
- a role **selected but not declared** fails `cluster.New` outright. It is
  always a typo or a bad rollout, and accepting it would mean a scheduler that
  never schedules with nothing in the logs.

## gated: running something, once, somewhere

```bash
cd gated && go run .
```

`Role.Run` re-checks the predicate on every tick, never overlaps two runs, and
cancels the action's context the moment this replica stops being eligible.

It is not a distributed lock, and the example says so at length because it
matters: the ring has no quorum and no lease, so during a partition two replicas
can each believe they are the leader for up to
`INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT`, and the cancellation is cooperative. **The
action must be idempotent, or fenced by whatever it writes to** — a
compare-and-swap, a conditional write, a unique constraint.

## state: sharing data, not caching it

```bash
cd state && go run .
```

The store the rings use also carries application state, through a codec declared
up front:

```go
stateCodec := codec.NewProtoCodec("fleet-state", func() proto.Message { return &FleetState{} })

cluster.Options{Codecs: []codec.Codec{stateCodec}}
...
client, err := c.KVClient("fleet-state")
```

Codecs are a construction-time option rather than a setter because the gossip
store only learns them when the replica joins.

This example joins **no role at all**, which `cluster.New` accepts: a service may
want the shared store and no coordination.

One rule that is not optional: the store is a membership store, not a cache.
Every key costs a broadcast worker and a slot in the state each replica gossips,
on every push/pull. Keys must be few and long-lived. A key per request, per job
or per challenge is how this becomes an out-of-memory incident.

## state-proto: a value of your own

```bash
cd state-proto && go run .
```

`state/` shares data using a type dskit already provides. This one brings its
own, which is what a real service does, and that means meeting the requirement
the gossip store imposes: the value must implement `memberlist.Mergeable`.

| File | What it is |
|------|------------|
| `fleet.proto` | The wire format. Reference only — copy it into your own repository and generate from it there. |
| `fleet.go` | The type, hand-written with protobuf struct tags so the example needs no protoc, and wire compatible with generated code. |
| `fleet_test.go` | **The part worth copying.** |

The rule the type implements is last write wins per entry, decided by a
timestamp the owner sets. Each replica only ever writes its own entry, which is
what makes merging two copies well defined with nobody coordinating.

### Why the tests are the point

dskit requires `Merge` to be idempotent, commutative and associative — a CRDT.
That is not a formality. Gossip delivers updates out of order, more than once,
to every replica, and nothing reconciles afterwards: a merge that lacks those
properties leaves replicas in different states **for good**, and nothing in
dskit or in this library will tell you.

`fleet_test.go` checks each property directly, so a change to the merge rule
that breaks one fails the build rather than a production cluster:

```go
TestFleetState_Merge_is_idempotent      // re-applying is not news
TestFleetState_Merge_is_commutative     // arrival order cannot matter
TestFleetState_Merge_is_associative     // grouping cannot matter
```

### Tombstones, and the one place commutativity is broken on purpose

Deleting an entry does not remove it. The compare-and-swap returns a state
without it, `Merge` is called with `localCAS` true, and the entry becomes a
tombstone — gossiped as gone, because a replica that has not heard about the
deletion yet would otherwise hand the entry back and resurrect it.

That `localCAS` branch is deliberately **not** commutative, which is why it only
ever runs on a local write and never on a value arriving from a peer. The
tombstone is stamped with the current time rather than the entry's old
timestamp, or a message already in flight would outrank it and bring the entry
back.

Tombstones are hidden from the application: the store calls `RemoveTombstones`
with a zero limit on every read. They are expired for good once
`INFRAPI_CLUSTER_OBSOLETE_ENTRIES_TIMEOUT` has passed.

### Looking at the key as the store holds it

The replica stays up after the walkthrough, serving its own status pages and
republishing every five seconds. The gossip page lists the key under the
configured prefix:

```text
Key               Codec        Version  Size
collectors/fleet  fleet-state  6        53 B
```

The size is the encoded payload — snappy over protobuf, 53 bytes for two
entries. The version is **node-local**: it counts the updates this replica
applied, its own writes included, which is why it climbs here with nobody else
in the cluster. Two replicas disagreeing on it is normal and says nothing about
whether they agree on the value.

It climbs because each republication carries a fresh timestamp, so each one is a
genuine change. That is not avoidable by writing the same thing: dskit calls
`Merge` with whatever the compare-and-swap function returned, and **if the merge
reports no change the whole CAS is treated as a failure** — slept on for a
second, retried up to ten times, then returned as an error. So read and compare
first, and call `CAS` only when there is something to write; calling it to find
out costs ten seconds and an error.
Three views of the same value:

| Link | Shows |
|------|-------|
| `?viewKey=collectors%2Ffleet&format=json-pretty` | the decoded value, through this type's own json tags |
| `?viewKey=collectors%2Ffleet&format=struct` | the same, as the Go value the codec produced |
| `?downloadKey=collectors%2Ffleet` | the bytes on the wire |

The instructive part is the deleted entry. The application never sees it — every
`Get` above showed one owner — but the raw store still carries it:

```json
"replica-2": { "updated_at_ms": 1791185925845, "deleted": true }
```

That is the tombstone doing its job, and the gap between what the store holds
and what the application reads is `RemoveTombstones` being called with a zero
limit on the way out.

### If you use etcd instead

`Mergeable` is a gossip-store requirement. The etcd and in-memory clients only
encode and decode with the codec and rely on a plain compare-and-swap, so a
bare protobuf message is enough there — at the cost of code that will not move
to the gossip store later.

## pages: the status pages

```bash
cd pages && go run .
```

```text
/-/cluster                                               200  html        7672 bytes
/-/cluster  (Accept: application/json)                   200  json         361 bytes
/-/cluster/ring/scheduler                                200  html        7761 bytes
/-/cluster/ring/scheduler  (Accept: application/json)    200  json         329 bytes
/-/cluster/ring/nope                                     404  json          63 bytes
/-/cluster/memberlist                                    404  json          42 bytes
```

Everything lives behind `/-/cluster`, next to the `/-/metadata` and `/-/metrics`
the server package already serves. Each page renders HTML by default and returns
its own model as JSON when the request asks for it.

- `/-/cluster` — the index: identity, store, and a card per role with its mode,
  members, leader and verdict.
- `/-/cluster/ring/{role}` — that role's ring: every registered replica, with the
  ones the mode excludes marked as such. That row is usually why someone opened
  the page.
- `/-/cluster/memberlist` — dskit's own gossip page. It is 404 here because this
  example uses the in-memory store.

The stylesheet is inlined rather than served as a file, so relocating the pages
with `HandlerOptions.PathPrefix` cannot break their styling, and they render in
an airgapped network. To look at them in a browser, replace the shutdown at the
end of `pages/main.go` with the two-line block the comment there gives.

## shutdown: the four answers

```bash
cd shutdown && go run .
```

A cluster is single use: dskit services cannot be restarted, so a failed or
finished cluster means the process should exit rather than rebuild it. After
`Stop`, every query reports `ErrStopped` rather than an empty ring, so
application code can tell a torn-down cluster from one that simply has no leader
right now. The four states worth distinguishing are `ErrDisabled`,
`ErrNotStarted`, `ErrStopped` and `ErrRoleNotJoined`.

## node: the one that actually shows a cluster

Two terminals, from `examples/cluster/node`:

```bash
INFRAPI_CONFIG_DOTENV_FILE=node1.env go run .
INFRAPI_CONFIG_DOTENV_FILE=node2.env go run .
```

The two replicas differ by **address**, not by port: `node-1` is `127.0.0.1` and
`node-2` is `127.0.0.2`, both serving on `:8080` and gossiping on `:7946`. That
is the shape of a real deployment, where replicas differ by host. The whole of
`127.0.0.0/8` is local on Linux; on macOS, `127.0.0.2` needs
`sudo ifconfig lo0 alias 127.0.0.2` first.

Two addresses per replica, and they are not the same thing:

| Key | What it is |
|-----|------------|
| `INFRAPI_CLUSTER_GOSSIP_BIND_ADDRS` / `_PORT` | where the replica gossips with its peers |
| `INFRAPI_CLUSTER_INSTANCE_ADDR` / `_PORT` | what it advertises **in its rings**, which is where peers reach its API |

The second has to match `INFRAPI_APP_LISTEN_ADDRESS`, or the ring advertises an
address nothing listens on — and the leader link on the status page points at
nowhere. Conflating the two, or letting them drift apart, is the usual way a
ring address becomes useless.

Each replica prints what it believes once a second:

```text
node-1 | scheduler(leader): members=2 leader=node-1 -> ACTING  | collector(broadcast): members=2 -> ACTING
node-2 | scheduler(leader): members=2 leader=node-1 -> passive | collector(broadcast): members=2 -> ACTING
```

Now interrupt the leader with `Ctrl+C` and watch the two modes part company:

```text
node-2 | scheduler(leader): members=2 leader=node-1 -> passive | collector(broadcast): members=1 -> ACTING
```

The broadcast role dropped the departing replica immediately. The leader role
still counts it and still calls it the leader, because it is `LEAVING` and a
leader role treats that as eligible. A few seconds later its heartbeat goes
stale and the survivor takes the token:

```text
node-2 | scheduler(leader): members=1 leader=node-2 -> ACTING | collector(broadcast): members=1 -> ACTING
```

**That delay is the deliberate trade**, and it is bounded by
`INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT` — `5s` in these dotenv files, `1m` by
default. Far fewer leader changes across a rolling restart, at the price of a
pause before a planned failover. Set
`INFRAPI_CLUSTER_KEEP_INSTANCE_IN_RING_ON_SHUTDOWN="false"` to make the handover
immediate, at the cost of a leader change on every restart.

The status pages are worth having open while you do this:

- <http://127.0.0.1:8081/-/cluster> and <http://127.0.0.1:8082/-/cluster>
- `http://127.0.0.1:8081/-/cluster/ring/scheduler` — where the departing replica
  shows as `LEAVING` and `excluded`
- `http://127.0.0.1:8081/-/cluster/memberlist` — dskit's gossip view, which works
  here because this is the only example using the gossip store

The heartbeats in these files are shortened so an election and a failover happen
while you watch. A real deployment leaves them alone.

## zones: two data centres, and the bridges between them

Four terminals, from `examples/cluster/zones`:

```bash
INFRAPI_CONFIG_DOTENV_FILE=dc1-bridge.env go run .
INFRAPI_CONFIG_DOTENV_FILE=dc1-member.env go run .
INFRAPI_CONFIG_DOTENV_FILE=dc2-bridge.env go run .
INFRAPI_CONFIG_DOTENV_FILE=dc2-member.env go run .
```

Two zones, one cluster, and gossip that stays inside a zone. A **member**
gossips only with peers in its own zone; a **bridge** also reaches the bridges
of other zones, so the two bridges are the only path state takes between dc1 and
dc2. Each replica prints what it believes, and the map is the zones it can
currently see:

```text
dc1-bridge  | dc1  bridge  | members=4 map[dc1:2 dc2:2] leader=dc2-member  -> passive
dc1-member  | dc1  member  | members=4 map[dc1:2 dc2:2] leader=dc2-member  -> passive
dc2-bridge  | dc2  bridge  | members=4 map[dc1:2 dc2:2] leader=dc2-member  -> passive
dc2-member  | dc2  member  | members=4 map[dc1:2 dc2:2] leader=dc2-member  -> ACTING
```

Four replicas that agree are one cluster. Four that disagree are two.

### Now kill the dc2 bridge

```text
dc1-bridge  | dc1  bridge  | members=3 map[dc1:2 dc2:1] leader=dc2-member  -> passive
dc1-member  | dc1  member  | members=3 map[dc1:2 dc2:1] leader=dc2-member  -> passive
dc2-member  | dc2  member  | members=3 map[dc1:2 dc2:1] leader=dc2-member  -> ACTING
```

They still agree. dskit noticed the zone had no bridge left and abandoned
zone-aware routing, logging

```text
memberlist zone-aware routing is skipped because a zone has no alive bridge  zone=dc2
```

and counting it in
`infrapi_cluster_memberlist_client_zone_aware_routing_select_nodes_skipped_total`
on `/-/metrics`.

So losing a zone's bridges costs the **traffic saving**, not the cluster. That
is worth knowing before designing around it, in both directions: it will not
split-brain on you, and it will not isolate a zone for you either. If zones
must genuinely not know about each other, give each one its own
`INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL` instead — separate meshes by
construction, with a leader each.

Run at least two bridges per zone anyway, and alert on that counter: a cluster
quietly gossiping full-mesh is doing the thing you enabled this to avoid.

### The addresses

Four replicas on one machine, told apart by address rather than by port, so all
four serve on `:8080` and gossip on `:7946` — the shape of a real deployment.
`127.0.0.0/8` is local on Linux; on macOS each alias beyond `127.0.0.1` needs
`sudo ifconfig lo0 alias <address>` first.

`dc2-member` seeds across zones on purpose, off `dc1-bridge`, so that it can
join even with no bridge running in dc2. That is what makes the experiment above
possible from a cold start.

## See also

The package documentation is at
<https://infrapi.github.io/lib/packages/cluster/>, and covers the configuration
keys, the gossip-label-versus-ring-key distinction, the backends, and the
operational hazards in full.
