# cluster examples

One directory per use case, each runnable on its own:

```bash
cd examples/cluster/roles && go run .
```

Every directory carries its own `app.env`, so reading one folder tells you
exactly which configuration keys that case needs. They share a single Go module
and a small `internal/demo` helper that does nothing but load the dotenv and
start the cluster — everything specific to a case stays in its `main.go`.

| Directory | What it shows | Needs |
|-----------|---------------|-------|
| [`roles/`](roles) | Several roles in one process, a ring each | nothing |
| [`state-proto/`](state-proto) | **A value of your own: protobuf plus a CRDT merge** | an ephemeral TCP port |
| [`kvapi/`](kvapi) | **A CRUD API over the store, gossip or etcd** | `127.0.0.1-2`, TCP 8085; etcd needs podman |
| [`node/`](node) | **A real two-node cluster over gossip** | `127.0.0.1-2`, TCP 7946, 8080 |
| [`zones/`](zones) | **Two data centres: a leader overall, a leader per DC, maintenance flags** | `127.0.0.1-4`, TCP 7946, 8080 |

`roles/` uses the in-memory key-value store: no socket, no peer, finishes in
under a second. It shows the API. `node/` and `zones/` are the ones that show
the behaviour, because a single replica is trivially its own leader and tells
you nothing about an election.

## The two modes

The package gives a service one predicate, `Role.ShouldRun()`, and the
coordination mode decides what it means.

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

| | `ModeLeader` | `ModeBroadcast` |
|---|---|---|
| `ShouldRun` is true on | the leader: the member that may lead with the smallest token | every healthy member |
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

## state-proto: a value of your own

```bash
cd state-proto && go run .
```

The store the rings use also carries application state, through a codec
declared up front in `cluster.Options.Codecs` — a construction-time option,
because the gossip store only learns its codecs when the replica joins. A
service may want the shared store and no role at all, which `cluster.New`
accepts.

The value is the service's own type, and that means meeting the requirement the
gossip store imposes: it must implement `memberlist.Mergeable`.

One rule that is not optional: the store is a membership store, not a cache.
Every key costs a broadcast worker and a slot in the state each replica gossips,
on every push/pull. Keys must be few and long-lived. A key per request, per job
or per challenge is how this becomes an out-of-memory incident.

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
with a zero limit on every read. They keep propagating for
`INFRAPI_CLUSTER_TOMBSTONE_TIMEOUT` so that a replica which was down still
learns of the deletion, and only then may be dropped — which makes that timeout
the longest a replica can be absent and come back converged.

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

## kvapi: a CRUD API, and the backend swapped under it

```bash
cd kvapi

INFRAPI_CONFIG_DOTENV_FILE=memberlist-1.env  go run .
INFRAPI_CONFIG_DOTENV_FILE=memberlist-2.env  go run .       # second terminal

podman compose up -d                                        # etcd on :2379
INFRAPI_CONFIG_DOTENV_FILE=etcd-1.env        go run .
INFRAPI_CONFIG_DOTENV_FILE=etcd-2.env        go run .       # second terminal
```

Four routes over one key-value key, and the program never learns which store it
is on:

```bash
curl -X PUT    :8085/api/v1/items/one -d '{"value":"hello"}'
curl           :8085/api/v1/items
curl           :8085/api/v1/items/one
curl -X DELETE :8085/api/v1/items/one
```

The four dotenv files differ in exactly one block. That is the whole exercise:
the code above is the same for both backends.

| Dotenv | Store | How the replicas share |
|--------|-------|------------------------|
| `memberlist-1.env`, `memberlist-2.env` | gossip between the replicas | they talk to each other, no external store |
| `etcd-1.env`, `etcd-2.env` | etcd on `:2379` | they never talk to each other, both talk to etcd |

There is deliberately **no in-memory variant** here. That store is a map inside
one process: a second replica gets its own, nothing is shared, and an API
demonstrating a cluster over it would demonstrate nothing. It earns its place in
tests and in the single-process examples above, not here.

Verified with two gossip replicas: a write on `node-1` and a write on `node-2`
both show up on both, and a delete issued on one disappears from the other.

```text
127.0.0.1:8085  node-1 memberlist [('from-one','node-1'), ('from-two','node-2')]
127.0.0.2:8085  node-2 memberlist [('from-one','node-1'), ('from-two','node-2')]
```

### With gossip, this API has no durable storage

Worth being blunt about, because the example makes it look like a database. The
gossip store keeps everything in the replicas' memory and writes nothing to
disk. Restart one replica and it pulls the collection back from a live peer;
**stop them all at once and the collection is gone.**

So an API like this one is honest only when the data is derived or disposable. If
the items matter, either run it on `etcd-*.env`, where etcd persists them to its
volume, or keep a real source of truth elsewhere and treat the gossip store as a
shared cache each replica repopulates on startup. That second shape is often the
right one: a database holds the records, the cluster holds the fast shared view
of them.

The two etcd dotenv files are in this directory for exactly that reason — it is
the same four routes either way, which is what makes the choice a deployment
decision rather than a rewrite.

### Three things the API had to get right

**One key, not one per item.** The whole collection lives under
`collectors/items`. One key per item is perfectly possible — `collectors/items/one`,
`collectors/items/two` — and it is the better layout for some shapes of data.
It is the wrong one here, for two reasons.

The first is cost. In the gossip store every key carries a broadcast worker and
a slot in the state each replica propagates on every push/pull. A bounded set of
keys is fine; an unbounded one, or one with short-lived keys, is how this becomes
an out-of-memory incident.

The second is how a watcher learns about a deletion, and it needs care.

| | One key, map inside | One key per item |
|---|---|---|
| Gossip cost | one worker, one state slot | one of each **per item** |
| Writes to different items | contend on the same key, and retry | never contend |
| Listing | decode the map | `List("items/")` |
| Watching one item | impossible, you get the whole collection | `WatchKey` on it |
| Discovering a new item | `WatchKey` fires, the map grew | `WatchPrefix` fires |
| Per-item size on the gossip page | no | yes |
| `Delete` on one item | no, it is a `CAS` on the map | yes |

### Deleting, and the one thing to get right

`WatchPrefix` is the odd one out: dskit skips its callback when the value comes
back nil, which is exactly what a key whose whole value was deleted gives. So a
replica watching `items/` by prefix is told about every creation and every
update, and **never** about a deletion.

`WatchKey` has no such check — it calls back with a nil value, so a per-key
watcher does learn. But you can only watch a key you already know about, and the
only way to discover new ones is the prefix watch.

The way out is not to delete at all: **make the deletion an update**, carrying a
`deleted` flag in the value. The value is then never nil, so `WatchPrefix`
notifies it like any other change, every replica converges on "this is gone",
and reclaiming the space is a local decision each node makes later.

That is what `items.go` does, and it is why the one-key layout works here too:
a deletion is a tombstone inside the map rather than an entry removed from it.
It is also what makes the per-key layout viable — with a soft delete there is no
notification gap left, and the choice comes back to cost against contention.

So the rule is in two parts. **Soft-delete either way**, because a hard delete is
either unnotified or, on gossip, re-addable by a lagging peer. And then
**dedicated keys when the cardinality is bounded and known** — one per role, one
per data centre, one per tenant from a fixed list — against **one key holding a
map when items are many, dynamic or short-lived**, which is what a CRUD API has.

**The value is a CRDT**, because the gossip store merges concurrent copies with
nobody arbitrating. `items.go` implements last-write-wins per item and
`items_test.go` checks idempotence, commutativity and associativity directly.
Two replicas writing the same id at once resolve by timestamp and one write is
lost — the price of this merge rule, not a flaw in it. ../state-proto has the
long explanation.

**A delete is a tombstone**, not a removal, or a replica that has not heard yet
would gossip the item back. The API never shows them: the store calls
`RemoveTombstones` with a zero limit on every read. The deletion is real, but it
becomes permanent in stages — hidden from the API at once, still gossiped for
`INFRAPI_CLUSTER_TOMBSTONE_TIMEOUT` (`5m`) so an absent replica still learns of
it, and only then droppable. That timeout is therefore also the longest a
replica can be away and come back converged; the package page has the full
lifecycle.

### Deleting something that is already gone

`DELETE` on a missing item answers `404`, and getting there was not obvious: a
compare-and-swap that changes nothing can come back as success or as an error
depending on the store. The handler tracks whether there was anything to delete
and uses that rather than the error, which is the only answer that holds for all
three backends.

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

The leader also runs the scheduler's periodic job, through `Role.Run`, every
three seconds:

```text
node-2 | scheduler job #2 runs here
```

`Role.Run` re-checks the predicate on every tick, never overlaps two runs, and
cancels the job's context the moment this replica stops leading: start `node-1`
alone and its job runs, start `node-2` and, if it takes the token, the job moves
there and stops on `node-1`.

It is not a distributed lock. The ring has no quorum and no lease, so during a
partition two replicas can each believe they are the leader for up to
`INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT`, and the cancellation is cooperative. **The
job must be idempotent, or fenced by whatever it writes to** — a
compare-and-swap, a conditional write, a unique constraint.

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

The status pages are worth having open while you do this. Each renders HTML,
or its own model as JSON when the request sends `Accept: application/json`:

- <http://127.0.0.1:8081/-/cluster> and <http://127.0.0.1:8082/-/cluster>
- `http://127.0.0.1:8081/-/cluster/ring/scheduler` — where the departing replica
  shows as `LEAVING` and `excluded`
- `http://127.0.0.1:8081/-/cluster/memberlist` — dskit's gossip view, 404 unless
  the store is memberlist

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
dc2.

Two leader roles, side by side:

- `scheduler` spans every zone: **one leader for the whole deployment**;
- `scheduler-dc1` / `scheduler-dc2`: a role is a ring, so a role named after the
  zone elects **one leader per data centre**. Both are declared in the code; each
  dotenv joins its own (`INFRAPI_CLUSTER_ROLES=["scheduler","scheduler-dc1"]`).

Each replica prints what it believes about both, and the map is the zones it
sees in that ring:

```text
dc1-bridge | dc1  bridge  | scheduler     map[dc1:2 dc2:2] leader=dc1-bridge -> ACTING  | scheduler-dc1 map[dc1:2] leader=dc1-member -> passive
dc1-member | dc1  member  | scheduler     map[dc1:2 dc2:2] leader=dc1-bridge -> passive | scheduler-dc1 map[dc1:2] leader=dc1-member -> ACTING
dc2-bridge | dc2  bridge  | scheduler     map[dc1:2 dc2:2] leader=dc1-bridge -> passive | scheduler-dc2 map[dc2:2] leader=dc2-bridge -> ACTING
dc2-member | dc2  member  | scheduler     map[dc1:2 dc2:2] leader=dc1-bridge -> passive | scheduler-dc2 map[dc2:2] leader=dc2-bridge -> passive
```

Four replicas that agree are one cluster. Four that disagree are two.

### Put a replica in maintenance

Every replica carries a leader check, a maintenance flag of its own:

```bash
INFRAPI_CLUSTER_LEADER_CHECKS=["!file:///tmp/dc1-bridge.maintenance"]
```

`!` negates: the replica may lead as long as the file does **not** exist. Each
replica runs its checks every second, publishes the answer in the ring after two
identical results, and the leader is elected among the replicas that may lead.

```bash
touch /tmp/dc1-bridge.maintenance
```

```text
dc1-bridge | scheduler leader=dc2-member -> passive (may not lead: !file:///tmp/dc1-bridge.maintenance: holds) | scheduler-dc1 leader=dc1-member -> passive (may not lead: …)
dc2-member | scheduler leader=dc2-member -> ACTING  | scheduler-dc2 leader=dc2-bridge -> passive
```

The global leadership left `dc1-bridge` within two seconds; it stays in the
cluster and keeps serving, it just leads nothing. Now take the whole of dc2 out:

```bash
touch /tmp/dc2-bridge.maintenance /tmp/dc2-member.maintenance
```

```text
dc2-member | scheduler leader=dc1-member -> passive (may not lead: …) | scheduler-dc2 leader=none -> no leader (may not lead: …)
```

The global role found a replica that may lead, in dc1. The dc2 role did not, and
has **no leader**: nothing acts there until a replica may lead again — never a
fallback to one that may not. `rm` the files and the replicas may lead again —
and the leadership **moves back**: the leader is the eligible replica with the
smallest token, so the one that owned it before takes it again. Expect two
changes of leader per maintenance, not one.

Any check works the same way — `tcp://db:5432`, `http://…/healthz`,
`exec:/usr/local/bin/is-primary` — and all of them must pass. The package page
has the full list.

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
