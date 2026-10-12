# app

```go
import "github.com/infrapi/lib/pkg/app"
```

Runs an InfraPI service in one call. The service brings what is its own — its
OpenAPI contracts and their handlers — and `app` wires everything an API shares,
from one configuration file and the environment.

!!! note "Its own Go module"

    `app` builds on [`cluster`](cluster.md), so it is a separate module too: a service
    that only needs `config` or `server` does not inherit dskit.

    ```bash
    go get github.com/infrapi/lib/pkg/app
    ```

## A service

Contract first: the OpenAPI file is written by hand, and everything else follows
from it. Nothing generates the contract from the code.

```text
api/orders/v1.yaml        the contract: paths, schemas, examples
oapi-codegen.yaml         what to generate from it
generate.go               //go:generate go tool oapi-codegen -config oapi-codegen.yaml api/orders/v1.yaml
internal/ordersv1/gen.go  generated: models, router, the interface to implement
handlers.go               that interface, one method per operation
main.go                   app.Main
config.yaml               the configuration
```

```go
//go:embed api/orders/v1.yaml
var ordersV1 []byte

type Config struct {
	MaxQuantity int `yaml:"max_quantity"`
}

func main() {
	var cfg Config
	app.Main(app.Spec{
		Name:    "orders-api",
		Service: &cfg,
		API: func(ctx context.Context, rt *app.Runtime) ([]app.Version, error) {
			return []app.Version{{
				OpenAPI: server.OpenAPISpec{Name: "orders v1", Path: "orders/v1.yaml", Content: ordersV1},
				Routes:  func(r app.Router) { r.Mount(handlerV1(newOrders(cfg.MaxQuantity))) },
			}}, nil
		},
	})
}
```

The handlers implement the generated `StrictServerInterface`, typed request in,
typed response out:

```go
func (o *orders) GetOrder(ctx context.Context, req api.GetOrderRequestObject) (api.GetOrderResponseObject, error) {
	order, err := o.store.Get(ctx, req.Tenant, req.Id)
	if err != nil {
		return nil, err // a *problem.Error as it says; anything else a 500 that hides its text
	}
	return api.GetOrder200JSONResponse(order), nil
}
```

To change the API, edit the contract and run `go generate`: the compiler lists
what the handlers must now do, and the contract and the code cannot drift apart.
A version 2 is a second contract and a second generated package, next to the
first.

`examples/app` is this service, complete — a full CRUD, its test and the
generated code: copy it to start a new one.

| `Spec` field | |
|---|---|
| `Name` | Default of `INFRAPI_APP_NAME`; also names the ring's key prefix and gossip label, and the APISIX objects. |
| `Service` | Pointer to the service's own configuration, decoded from the file's `service` section, unknown keys refused. |
| `IdempotencyScope` | `func(*app.Context) string`: whose an `Idempotency-Key` is, e.g. `c.Param("tenant")`. Default: the consumer the gateway authenticated. |
| `API` | Builds the service once the configuration is loaded; returns its versions. The cluster is not joined yet. |
| `Roles` | The service's own rings, `[]cluster.RoleSpec`: see [The service's roles](#the-services-roles). |
| `Background` | `func(ctx, *app.Runtime) error`, run once the replica serves and has joined the cluster; its context ends when the shutdown starts, which waits for it. An error stops the service. |

A `Version` is one contract and its routes. Its `Prefix` — `/orders/v1/` — is
derived from the contract's paths unless set.

### What every contract gets

A contract need not say what every contract shares: `app` adds it when it loads
the contract, unless the contract has it already, and serves, validates and
documents the result.

- the `Problem` schema, and a `default` answer on every operation that is a
  problem document — the errors of [Errors](#errors);
- the `apikey` header, as a security scheme every operation requires, unless the
  contract says its own `security`.

The author's order and comments are kept. With [`contract`](contract.md), a
service does not write its contract at all: it describes its resource in a few
lines.

## Handlers

Handlers generated from the contract plug in whole: `Mount` takes the
`http.Handler` that `oapi-codegen` builds for net/http (`std-http-server`, with
`strict-server` if you like) and serves every operation of the contract with
it. Each operation stays a route of its own for the middlewares, the metrics
and the logs.

```go
Routes: func(r app.Router) {
	r.Mount(ordersv1.HandlerWithOptions(ordersv1.NewStrictHandlerWithOptions(handlers, nil, opts),
		ordersv1.StdHTTPServerOptions{ErrorHandlerFunc: badRequest}))
},
```

For a route or two, handlers can also be written by hand. A service still sees
no web framework: it writes against `app.Router` and `app.Context`, and paths
are the contract's, `{name}` for a parameter. Nothing then checks that they
match the contract, until a request does.

```go
Routes: func(r app.Router) {
	r.GET("/orders/v1/tenants/{tenant}/orders/{id}", func(c *app.Context) {
		order, err := store.Get(c.Context(), c.Param("tenant"), c.Param("id"))
		if err != nil {
			c.Error(err) // a *problem.Error as it says; anything else a 500 that hides its text
			return
		}
		c.JSON(http.StatusOK, order)
	})
},
```

| `app.Context` | |
|---|---|
| `Context()` | the request's context: cancelled when the caller leaves, carries the correlation id |
| `Request()` | the `*http.Request` |
| `Param`, `Query`, `Header` | a path parameter by its contract name, a query parameter, a request header |
| `Bind(&v)` | decodes the JSON body; the contract already validated it |
| `JSON(status, v)`, `NoContent()`, `SetHeader` | the answer |
| `Error(err)` | a problem document, and the call stops |

## What it wires

On every version's routes, in this order:

1. the route's path parameters into the access log line (`tenant`, `id`…);
2. the API key (`apikey` header), when one is configured;
3. the relay of writes to the leader, with `cluster.leader_writes`;
4. validation against that version's contract;
5. `Idempotency-Key`, when a database is configured.

Around them, the engine of [`server`](server.md) with the contracts passed to
`WithOpenAPI`, the socket of [`listener`](listener.md), and:

| path | |
|---|---|
| `/` | landing page: who answered, who leads, links |
| `/-/docs`, `/-/openapi/…` | the contracts, one file per version |
| `/-/metrics`, `/-/metadata` | the engine's |
| `/-/healthz` | 200 while the process runs |
| `/-/readyz` | 503 while joining the cluster or draining |
| `/-/cluster`, `/-/cluster/memberlist`, `/-/cluster/ring/writer` | the ring, read only |

Every error is `application/problem+json` with a `code` and an explicit
`retryable` ([`pkg/app/problem`](#errors)). Every log line is one object with
`time`, `level` and `msg`, whoever wrote it; an API call's line also carries
`correlation_id`, `consumer`, its path parameters, and on a write
`idempotency_key`, `replayed`, `relayed` and `leader`.

On SIGINT or SIGTERM a replica turns `/-/readyz` to 503, leaves the ring — the
others elect a new leader at once — waits `shutdown.drain_delay`, then stops
serving.

## Configuration

One YAML file, named by `-config` or `INFRAPI_CONFIG_FILE`, optional. Its `app` and
`cluster` sections are the library's `INFRAPI_APP_*` and `INFRAPI_CLUSTER_*` keys,
lowercase and without the prefix: the same names, defaults and validation as a
dotenv file. A key the library does not know is refused.

```yaml
app:
  platform: sandbox
  region: eu-west-1
  location: dc1
  url: https://orders.example.com
cluster:
  enabled: true
  join_members: [orders-1:7946, orders-2:7946]
  leader_writes: true    # writes performed by the leader alone; default false
  leader_checks: ["!file:///etc/infrapi/maintenance", "tcp://db:5432"]
auth:
  apikey_sha256: …       # or INFRAPI_AUTH_APIKEY_SHA256
database:
  dsn: postgres://…      # or INFRAPI_DATABASE_DSN
gateway:
  admin_url: http://apisix:9180
  admin_key: …           # or INFRAPI_GATEWAY_ADMIN_KEY
  interval: 15s
  rate_limit: {count: 600, window_seconds: 60}
  max_body_bytes: 1048576
shutdown:
  drain_delay: 3s
broadcast:
  replay_window: 1h      # how long a replica that was down can catch up a broadcast write
  max_body_bytes: 65536  # a broadcast write's body, which the gossip carries
service:
  max_quantity: 10       # the service's own
```

The environment wins over the file: every `INFRAPI_APP_*` and `INFRAPI_CLUSTER_*`
variable, and the three secrets above, which are better kept out of it.

`app` changes a few of the library's defaults:

| key | default here | why |
|---|---|---|
| `app.name` | `Spec.Name` | |
| `app.listen_address` | `0.0.0.0:8080` | a service listens on every interface |
| `app.log_format` | `json` | |
| `cluster.roles` | every role in `Spec.Roles` | which of the service's roles this replica joins; `writer` is added when a leader is needed |
| `cluster.instance_port` | the API's port | the leader's address is where writes are relayed |
| `cluster.instance_addr`, `cluster.gossip_bind_addrs` | the IP of `app.listen_address`, unless it is a wildcard (`0.0.0.0`): then the library's (the interfaces; every address) | peers reach the API where it listens, and two replicas on one host do not share `:7946` |
| `cluster.keep_instance_in_ring_on_shutdown` | `false` | a stopping replica hands leadership over at once |
| `cluster.kv_prefix`, `cluster.gossip_cluster_label` | from `Spec.Name` | apart from other services' rings |

## The writer ring: `leader_writes`

A service behind several replicas, possibly in several data centres, often wants
**one writer**: a backend that does not take concurrent writes well, an order to
keep between calls, a rate limit that counts per caller. `cluster.leader_writes`
gives it that without a line of code: the replicas elect a leader, and every
write, wherever it arrives, is performed by that leader.

It is the operator's choice, not the code's: a backend that takes concurrent
writes needs no single writer, and the same service can run either way. `Spec`
says nothing about it.

```yaml
cluster:
  enabled: true
  join_members: [orders-1.dc1:7946, orders-1.dc2:7946]
  leader_writes: true                                   # default false
  leader_checks: ["!file:///etc/orders/maintenance"]    # optional: who may lead
```

### What it does

1. **A ring, `writer`, and its leader.** Every replica joins the library's own
   leader-mode role. The leader is the healthy replica
   [that may lead](cluster.md#choosing-who-may-lead) with the
   [smallest token](cluster.md#how-the-leader-is-chosen); every replica computes
   the same answer from the ring it gossips.
2. **Writes go to the leader.** `POST`, `PUT`, `PATCH`, `DELETE` that reach a
   replica which is not the leader are relayed to it, as they came: same method,
   path, headers and body, plus `X-Infrapi-Forwarded-By: <relaying replica>` and
   the call's `X-Correlation-ID`, so one id follows the call in both replicas'
   logs. The leader's answer comes back unchanged.
3. **Reads stay where they arrive.** `GET`, `HEAD`, `OPTIONS` are served by the
   replica that received them: they change nothing, and the backend is the same.
4. **Every write's answer names who performed it**: `X-Infrapi-Leader: <id>`.
5. **APISIX follows.** With the [gateway](#the-gateway), the write upstream holds the
   leader alone, so writes rarely need the hop; the read upstream holds every
   replica.

```text
 client ──POST──► APISIX ──► replica dc2 ──relay──► leader dc1 ──► backend
                                  │                      │
 client ◄── 201, X-Infrapi-Leader: dc1-a ◄───────────────┘
 client ──GET───► APISIX ──► any replica ─────────────────────────► backend
```

### In the chain

The relay sits after authentication and before the contract's validation and
the idempotency store, so that:

- a caller that is not authenticated is refused by the replica it reached, and
  is never relayed;
- only the leader validates the request and claims its `Idempotency-Key`: a key
  lives in one place, whatever replica the retry reaches.

### When there is no leader to give the write to

Every refusal is a problem document with `retryable: true`: the caller, or its
workflow engine, tries again.

| Status, `code` | When |
|---|---|
| `503 no_leader` | no replica may lead right now: the cluster is starting, or every replica fails its leader checks |
| `503 not_leader` | the write was relayed, and the replica it reached is no longer the leader: leadership moved during the hop. It is not relayed a second time, so two replicas that disagree cannot bounce it |
| `503 leader_unreachable` | the leader did not answer the relay. It may or may not have performed the write: send it with an `Idempotency-Key`, and the retry is safe |

### Failover

When the leader stops, fails its checks, or disappears, the next replica that may
lead takes over:

- a replica that **stops** leaves the ring at once, and hands over immediately
  (`keep_instance_in_ring_on_shutdown` is false under `app`);
- a replica that **fails its checks** is skipped after `leader_check_threshold ×
  leader_check_interval`, plus a gossip round;
- a replica that **dies** is skipped once its heartbeat is stale,
  `heartbeat_timeout`.

Writes in that window answer `503`, retryable. When the old leader may lead
again, it takes the leadership back if its token is the smallest: two changes,
not one.

### What it is not

Not a lock. The ring has no quorum and no lease: during a partition, each side can
elect its own leader and both write, until the partition heals. Writes that
converge on their body — a create that checks what exists, a replace that sets
what it was given, a delete of nothing that succeeds — end where one leader
would. Others must be fenced in what they write to: a compare-and-swap, a
conditional write, a unique constraint.

### Without it

Every replica performs the writes it receives, and APISIX's write upstream holds
every replica like the read one. With the gateway, a leader is still elected: it
alone keeps APISIX's configuration in line with the ring.

**No role, no ring, no leader.** The writer ring is joined only when something
needs a leader: `cluster.leader_writes`, or the gateway (`gateway.admin_url`).
With neither, and no [role of the service's](#the-services-roles), the replicas
gossip and show their status pages, and that is all; `cluster.leader_checks` is
then refused at start, since there is nobody to choose.

### Watching it

- `/` says which replica answered, whether it leads, and to whom it relays.
- `/-/cluster/ring/writer`: every replica, its state, whether it may lead and why
  not.
- `infrapi_app_relayed_requests_total{method,route}`: the writes a replica relayed.
- `infrapi_cluster_leader_eligible{name="writer"}`: 1 when this replica may lead.
- Access log fields: `relayed` and `leader` on every write.

## Which ring runs each write

`leader_writes` sends every write to one leader. The contract can say more, **per
operation**: which ring a write runs on. The ring's mode, declared in
`Spec.Roles`, says how:

| The write's ring | It runs |
|---|---|
| a leader role | on that role's leader; relayed there from any other replica |
| an all-mode role | here, then on every member of that role, through the gossip |
| `local` | on the replica that received it |

Two extensions, written in the contract next to what they govern:

```yaml
openapi: 3.0.3
info: {title: Bookmarks, version: "1"}
x-infrapi-writes: everybody          # every write not marked otherwise

paths:
  /bookmarks/v1/tenants/{tenant}/bookmarks/{id}:
    delete:
      operationId: deleteBookmark
      x-infrapi-ring: cache          # this one: on the leader of cache
    put:
      operationId: replaceBookmark
      x-infrapi-ring: local          # this one: where it arrives
```

```go
Roles: []cluster.RoleSpec{
	{Name: "everybody", Mode: cluster.ModeAll},
	{Name: "cache", Mode: cluster.ModeLeader},
},
```

```yaml
cluster:
  roles: [everybody, cache]   # the configuration only says which rings this replica joins
```

- A write's ring is its `x-infrapi-ring`, else the contract's `x-infrapi-writes`,
  else the writer ring's leader with `cluster.leader_writes`, else `local`.
- **A read** runs where it arrives, unless its own `x-infrapi-ring` names a
  leader role: it is then relayed to that leader, like a write — for what only
  the leader holds. `x-infrapi-writes` never applies to reads, and a broadcast
  ring on a read is refused: a read has one answer.
- A ring must be one of `Spec.Roles`, and one this replica joins: its writes
  would otherwise have nowhere to go. An all-mode ring on a read, or
  `x-infrapi-writes` together with `cluster.leader_writes`, is refused at start.
- Without a cluster, every write is local.
- Every write gets its `BroadcastID` and `BroadcastTime`, whatever its ring.
- The extensions are the contract's own: they show in `/-/docs`, and OpenAPI
  tools ignore them.
- With the gateway, the write upstream holds the writer's leader alone with
  `leader_writes`, every replica otherwise; the relay takes a leader write to
  its leader wherever it lands.

## Writes on every replica

A write given to a **broadcast** ring is performed **by every replica**, each on
what it keeps for itself — a local disk, a local cache, a process on its host.
The replica that receives it runs it, then **publishes it in the gossip**: every
other member of the ring applies it, through the same handler.

```text
POST …/bookmarks ──► node-1: handler runs, 201
                       └─ publishes the write in the cluster's store (gossip)
                            ├─► node-2: sees it, runs the same request, same handler ─► done
                            └─► node-3: sees it, runs the same request, same handler ─► done
                     ◄── 201 to the caller, once every replica has applied it (strict)
```

No replica calls another: the write travels in the cluster's own gossip, the
way the ring does, and each replica records in it how the write went there.

### The handlers must give the same answer everywhere

Each replica runs the handler on its own. What a handler would otherwise draw on
its own — a random id, the current time — must come from the write instead, or
every replica creates something different:

```go
b.ID = "bm-" + app.BroadcastID(ctx)[:10]   // not rand.Text()
b.CreatedAt = app.BroadcastTime(ctx)        // not time.Now()
```

- `app.BroadcastID(ctx)` is the write's **broadcast id**, the same on every replica: 26
  lower-case letters and digits. With an `Idempotency-Key`, it is derived from
  the key and the path, so a retry creates the same thing again instead of a
  second one.
- `app.BroadcastUUID(ctx)` is the broadcast id as a UUID (version 4): the id to
  give what a write creates when ids are UUIDs — unpredictable, and the same on
  every replica.
- `app.BroadcastTime(ctx)` is when the first replica received the write.
- Both are set on **every** write, whatever its ring: a handler written this way
  runs unchanged on any ring. Both are empty on a read.

### What is published, and how it is applied

- Only a write that **succeeded** on the first replica (status below 400) is
  published: a refused or failed one never reaches the others.
- What is published is the request as the caller sent it — method, path, query,
  `Content-Type`, body — with its broadcast id, its time and its
  `X-Correlation-ID`; its other headers stay where it came in. It goes to the
  members the ring holds at that moment, a replica that is down too.
- Each replica applies the writes meant for it **in process**, oldest first, one
  at a time: the request goes through the engine as if it had come in there — the
  same validation, the same handler, the same ids (`x-infrapi-ids`) — without
  the API key, checked where it came in, and without the `Idempotency-Key`: the
  broadcast id does that job. Nothing outside the process can pass for such a
  write.
- The body of a broadcast write is at most **64 KiB** (`broadcast.max_body_bytes`):
  it goes to every replica through the gossip. Beyond, `413`.

### A replica that was down catches up

A replica that is down when a write is made — crashed, or stopped and kept in the
ring (`keep_instance_in_ring_on_shutdown: true`) — applies it **when it is back**:
the gossip gives it the writes it has not applied, as it joins.

- Within the **replay window**, one hour by default: the request is kept that
  long. Past it, the request is dropped, and a replica that had not applied it
  has `missed` it.
- A replica that **left** the ring when it stopped
  (`keep_instance_in_ring_on_shutdown: false`, the default under `app`) was not
  one of the ring's when the write was made: it does not get it. Nor does a
  replica that joins for the first time: it starts from what it has.

```yaml
broadcast:
  replay_window: 1h        # at most 24h, the time the outcomes are kept
  max_body_bytes: 65536
```

### What the caller is answered: `x-infrapi-broadcast`

At the root of the contract, or on one operation:

```yaml
x-infrapi-broadcast: strict        # the default
paths:
  /bookmarks/v1/tenants/{tenant}/bookmarks:
    post:
      operationId: createBookmark
      x-infrapi-broadcast: async   # this one answers at once
```

| Value | The caller waits | When a replica did not take the write |
|---|---|---|
| `strict` | for every replica that is up, a minute at most | `502 broadcast_incomplete`, `retryable: true`, naming the replicas that do not have it yet |
| `tolerant` | for every replica that is up, a minute at most | the first replica's answer; the failure is in the logs, the metrics and the status |
| `async` | for nobody: answered once the write is published | the first replica's answer; the failure is in the logs, the metrics and the status |

- `strict` holds the first replica's answer until the others have applied the
  write, and sends it only when all did: a success means every replica has it.
  A replica that is down counts as not having it — it will, when back.
- After a `502`, **retry with the same `Idempotency-Key`**: the broadcast id
  comes from the key, so the write is the same one, and the replicas that
  **failed** it are asked again — those alone. The others have it already.
- The write is published before the caller is answered, whatever the value: a
  replica that stops right after answering does not take the write with it.
- On a write that is not broadcast, or on a read, `x-infrapi-broadcast` is
  refused at start.

### Did every replica take it: the broadcast id and its status

Every write is answered with `X-Infrapi-Broadcast-ID`. Any replica tells how a
broadcast write went, replica by replica:

```bash
curl http://any-replica:8080/-/broadcast/im3vmkqm3xncm2i4t4zw2e53wd
```
```json
{"broadcast_id":"2wz5qmemf3emfd6heriefwhnsq","status":"partial","by":"node1","method":"POST",
 "path":"/bookmarks/v1/tenants/demo/bookmarks",
 "replicas":{"node1":{"state":"done","status":201},
             "node2":{"state":"failed","status":503,"error":"answered 503: …"}}}
```

- `status` sums the write up: `pending` while a replica has not applied it — one
  that is down, until it is back or the replay window closes —, `complete` when
  every replica took it, `partial` when one did not, `error` when it could not be
  published at all (no ring, no members; `error` says why) and only the first
  replica has it.
- Each replica's `state` is `pending` until it applied the write, then `done` or
  `failed`, with the status its handler answered; or `missed`, when the replay
  window closed before it applied it. `attempt` counts the retries that asked it
  again.
- The route is documented in `/-/docs`, under **infrapi (replica routes)**, with
  `/-/healthz` and `/-/readyz`: the library serves that contract next to the
  service's.
- The writes and their outcomes live in **one key** of the cluster's store,
  `broadcast-status`, which every replica updates as it applies a write, and
  reads locally: no call to answer.
- Kept **24 hours**, and at most the **10 000** most recent writes; their
  requests, for the replay window only, and at most **8 MiB** of them, the
  oldest dropped first. Everything old is dropped from the value itself, so
  there is nothing to delete; the caps bound the key, which every full gossip
  exchange sends whole, whatever the write rate.
- `404` for a write that is not broadcast, unknown, or older than that.

### In the logs

One write, followed across replicas by its `broadcast_id` and `correlation_id`:

```json
// node-2, which received the call: its access line
{"msg":"request","method":"POST","status":201,"broadcast_to":"node-1","broadcast_id":"ukolazloc2zg7umkpikknl3ezv","correlation_id":"OGN7YXV7…"}
// node-1, which applied it: one line for the write, then its access line
{"msg":"broadcast write: applied","component":"broadcast","from":"node-2","method":"POST","path":"/bookmarks/v1/tenants/acme/bookmarks","status":201,"duration":"1.7ms","broadcast_id":"ukolazloc2zg7umkpikknl3ezv","correlation_id":"OGN7YXV7…"}
{"msg":"request","method":"POST","status":201,"broadcast_from":"node-2","broadcast_id":"ukolazloc2zg7umkpikknl3ezv","correlation_id":"OGN7YXV7…"}
```

- `broadcast write: applied` (info) or `broadcast write: not applied here`
  (error, with `err`), on the replica that applied it, with the status and how
  long it took; `broadcast write: a replica is registered but not healthy`
  (warning) on the one that received it, for each replica that is down.
- On the access line: `broadcast_to` and `broadcast_failed`, the replicas that
  took the write and those that did not (`strict` and `tolerant`), or
  `broadcast=async`; `broadcast_from` on a replica that applied a write. In the
  text format they follow gin's line as `key=value`:

  ```text
  [GIN] 2026/10/08 - 16:38:25 | 201 | 650µs | 127.0.0.1 | POST "/bookmarks/v1/tenants/demo/bookmarks" broadcast_from=node1 correlation_id=TPQYR6P2… tenant=demo broadcast_id=go3ouhwv…
  ```
- These lines carry `component=broadcast`, as the cluster's carry
  `component=cluster`.
- `infrapi_app_broadcast_writes_total{method,route,outcome}` counts the writes
  each replica applied for another, `ok` or `failed`.

### What it does not do

- **No catching up past the window**, nor for a replica that left the ring or
  joins later. What every replica must eventually hold belongs in a shared
  store, or is rebuilt in `Background` as a replica starts.
- **No undo.** A replica that fails a write leaves the others with it: `strict`
  says so to the caller, who retries; `tolerant` and `async` log it, count it
  and keep it in the status.
- **No order between replicas.** Each replica applies the writes oldest first;
  two writes to the same item, received at the same time by two replicas, may be
  applied in either order.
- **The bodies travel in the gossip.** Between hosts, encrypt it
  (`cluster.gossip_tls_enabled` and its certificates, in [cluster](cluster.md))
  when they hold what must not be read on the network.

When every replica must see the same data, a store they share — a database, a
shared volume, the backend the service fronts — is simpler than keeping copies
in step.

## The API's own ids: `x-infrapi-ids`

A service in front of another system — Grafana, a cloud, a storage — need not
show that system's ids: the library gives the API its own, **UUIDs**, and
pairs them with the backend's, which the handlers alone see.

With the [contract](contract.md) format, `id: infrapi` is all it takes: the
contract it writes marks every id. By hand, three extensions:

```yaml
x-infrapi-ids: shop                  # at the root: the library keeps the ids
paths:
  /shop/v1/products/{id}:
    parameters:
      - {name: id, in: path, required: true, schema: {type: string, x-infrapi-id: products}}
components:
  schemas:
    Product:
      properties:
        id:        {type: string, x-infrapi-id: products}    # a product's own id
        parent_id: {type: string, x-infrapi-ref: products}   # a reference to one
        team_ids:  {type: array, items: {type: string}, x-infrapi-ref: teams}   # each item
```
```yaml
database:
  dsn: postgres://…          # where the pairs are kept, with the idempotency keys
```

A contract of one resource may mark nothing but the root: its value is then
the resource, and its `{id}` and the `id` of its objects are that resource's.

- **In**: the UUID in the URL becomes the backend's id before the handler runs,
  and so does every marked id in the body — at any depth, in lists, in lists of
  objects — and in the query (a filter such as `?parent_id=`). An `{id}` the API
  does not know is a `404`; an unknown reference, a `400` naming the field; one
  that is not a UUID, a `400`.
- **Out**: every marked id of the answer becomes its UUID: an object's, each
  item's of a list or of `{items: [...]}`, a reference's, and the `current` of a
  problem — the item as it is, on a conflict. A creation gets one, the same on
  every replica that runs it (`app.BroadcastUUID`); an item met for the first
  time — made outside the API, or a reference to it — gets one too.
- **A delete** that succeeds forgets the pair. When the contract answers `204`
  and never `404` — gone or already gone — a delete of an id the API does not
  know is a `204`: a retried delete does not fail.
- **The handlers** read and answer the backend's ids, as if there were no
  library: the contract's file says `type: string`, and what is served says
  `format: uuid`, references included.
- **What it does not turn**: ids in free text — a problem's `detail` — and in
  values the contract does not mark, such as a link to the backend's own page.
  Name objects by name in messages.
- **The pairs** are in one table, `infrapi_ids`: resource, scope (the path's other
  parameters, as the tenant), replica, the API's id, the backend's. When the
  writes are broadcast, every replica has its own backend, and its own pairs:
  the same UUID, each replica's backend id.
- The idempotency store keeps the answers with the API's ids.


Work of the service's own — a purge on one replica, a cache reload on every one
— runs on rings it declares, the [`cluster`](cluster.md) package's roles:

```go
app.Main(app.Spec{
	Name: "orders-api",
	Roles: []cluster.RoleSpec{
		{Name: "purger", Mode: cluster.ModeLeader},   // one replica acts
		{Name: "cache", Mode: cluster.ModeAll}, // every replica acts
	},
	Background: func(ctx context.Context, rt *app.Runtime) error {
		purger, err := rt.Cluster.Role("purger")
		if err != nil {
			return err // this replica does not run it: return nil to accept that
		}
		return purger.Run(ctx, time.Hour, purge) // on the leader only, moves with it
	},
	API: ...,
})
```

```yaml
cluster:
  enabled: true
  roles: [purger]   # this replica's share; every role of the service by default
```

- `cluster.roles` chooses among `Spec.Roles`; a name the service does not declare
  is refused at start, and so is `writer`, the library's own.
- `roles: []` joins none of them: `rt.Cluster.Role` then answers
  `cluster.ErrRoleNotJoined`.
- `cluster.leader_checks` apply to every leader-mode role a replica joins,
  `writer` included; `cluster.role_leader_checks` adds checks for one role:

  ```yaml
  cluster:
    leader_checks: ["!file:///etc/orders/maintenance"]   # every leader role
    role_leader_checks:                                   # and, for one role
      writer: ["http://backend:3000/api/health"]
      purger: ["exec:/usr/local/bin/is-active-dc"]
  ```

  Leave `leader_checks` out for no common rule. A role named there must be a
  leader-mode role this replica joins.
- The roles are reached from `Background`, not from `API`: the replica joins
  the cluster only once it serves.

## The gateway

With a `gateway` section, the leader keeps an APISIX in line with the ring through
its Admin API ([`gateway`](gateway.md)), every `interval`:

| object | id, by default | holds, by default |
|---|---|---|
| upstream | `<name>-read` | every replica, health-checked on `/-/readyz` |
| upstream | `<name>-write` | the leader alone |
| service | `<name>-api` | `key-auth` (header `apikey`), `limit-count` per consumer (429), `client-control` (413), `request-id` (`X-Correlation-ID`) |
| routes | `<name>-<version>-read`, `-write` | per version: `GET`/`HEAD`/`OPTIONS` to read, `POST`/`PUT`/`PATCH`/`DELETE` to write |
| route | `<name>-docs` | `/-/docs` and `/-/openapi/*`, no key |

All of it is the `gateway` section's to change; what it leaves out keeps the
default. `{name}` in an id is `gateway.name`.

```yaml
gateway:
  admin_url: http://apisix:9180
  admin_key: …                       # or INFRAPI_GATEWAY_ADMIN_KEY
  interval: 15s
  name: orders-api                   # default Spec.Name
  labels: {managed-by: orders-api}   # on every object

  read:  {upstream: "{name}-read",  methods: [GET, HEAD, OPTIONS]}
  write: {upstream: "{name}-write", methods: [POST, PUT, PATCH, DELETE]}
  service: "{name}-api"
  route_id: "{name}-{version}-{kind}"   # {version}: orders-v1; {kind}: read or write

  upstream:
    type: roundrobin
    timeout: {connect: 2s, send: 30s, read: 120s}
    keepalive_pool: {size: 320, idle_timeout: 60s, requests: 1000}   # default APISIX's
    health_check:                    # enabled: false drops it
      path: /-/readyz
      timeout: 1s
      healthy:   {interval: 2s, successes: 1, http_statuses: [200, 302]}
      unhealthy: {interval: 1s, http_failures: 2, tcp_failures: 2, timeouts: 3,
                  http_statuses: [429, 404, 500, 501, 502, 503, 504, 505]}

  rate_limit: {count: 600, window_seconds: 60}   # tunes the default limit-count
  max_body_bytes: 1048576                         # tunes the default client-control
  plugins:                           # by name, over the defaults
    request-id: null                 # null removes a default
    limit-count: {count: 100, time_window: 10, key_type: var, key: consumer_name,
                  rejected_code: 429, policy: redis, redis_host: redis}   # replaces it whole
    cors: {allow_origins: "https://console.example.com"}                  # adds one

  docs: {enabled: true, route: "{name}-docs", uris: [/-/docs, /-/openapi/*]}
```

Health check intervals are whole seconds, which is what APISIX counts in. The
service refuses to start on what would route a call wrong: the same upstream
for reads and writes, a method in both lists, a `route_id` without `{version}`
and `{kind}`, an empty id or method list.

An object is written only when it differs from what APISIX holds. Consumers —
who may call, with which key — are an operator's.

## Errors

`pkg/app/problem` is the one shape of every error, for the middlewares and for
the service's handlers alike:

```go
problem.Abort(c, problem.NotFound("no order %q", id))
problem.Abort(c, problem.AlreadyExists(current, "order %q exists", id))
problem.Abort(c, problem.Upstream(true, "the stock service failed: %v", err))
```

```json
{"title":"Not found","status":404,"detail":"no order \"acme-9\"","code":"not_found","retryable":false,"correlation_id":"…"}
```

`retryable` is explicit because the caller is often a workflow engine retrying
activities, and the status code alone is the wrong thing to decide on. An error
that is not a `*problem.Error` is a bug: logged in full, answered as a 500 without
its text.

## Idempotency

`pkg/app/idempotency`, applied to every write that carries an `Idempotency-Key`:
the same key with the same request replays the first answer
(`Idempotent-Replayed: true`) and runs nothing; with another request it is refused
(`idempotency_key_reused`); while the first is still running, `in_progress`,
retryable. A 5xx is not remembered. Records go to Postgres (`database.dsn`), in a
table the service creates on start and prunes hourly.

The body is hashed whole, so it is read up to `gateway.max_body_bytes` (1 MiB by
default, the same limit APISIX applies); a larger one is refused with `413
too_large`, never cut.
