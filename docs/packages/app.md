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

```go
//go:embed api/orders/v1.yaml
var ordersV1 []byte

type Config struct {
	MaxQuantity int `yaml:"max_quantity"`
}

func main() {
	var cfg Config
	app.Main(app.Spec{
		Name:         "orders-api",
		Service:      &cfg,
		LeaderWrites: true,
		API: func(ctx context.Context, rt *app.Runtime) ([]app.Version, error) {
			h := newOrders(cfg)
			return []app.Version{{
				OpenAPI: server.OpenAPISpec{Name: "orders v1", Path: "orders/v1.yaml", Content: ordersV1},
				Routes: func(r gin.IRouter) {
					r.POST("/orders/v1/tenants/:tenant/orders", h.place)
					r.GET("/orders/v1/tenants/:tenant/orders/:id", h.read)
				},
			}}, nil
		},
	})
}
```

`examples/app` is this service, complete: copy it to start a new one.

| `Spec` field | |
|---|---|
| `Name` | Default of `INFRAPI_APP_NAME`; also names the ring's key prefix and gossip label, and the APISIX objects. |
| `Service` | Pointer to the service's own configuration, decoded from the file's `service` section, unknown keys refused. |
| `LeaderWrites` | The leader of the writer ring alone performs writes; the other replicas relay theirs to it. |
| `IdempotencyScope` | Whose an `Idempotency-Key` is. Default: the consumer the gateway authenticated. |
| `API` | Builds the service once the configuration is loaded; returns its versions. |

A `Version` is one contract and its routes. Its `Prefix` — `/orders/v1/` — is
derived from the contract's paths unless set. Handlers written with
`oapi-codegen`'s gin server register in `Routes` like any others.

## What it wires

On every version's routes, in this order:

1. the route's path parameters into the access log line (`tenant`, `id`…);
2. the API key (`apikey` header), when one is configured;
3. the relay of writes to the leader, with `LeaderWrites` and a cluster;
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
| `cluster.roles` | `["writer"]` | the one ring |
| `cluster.instance_port` | the API's port | the leader's address is where writes are relayed |
| `cluster.keep_instance_in_ring_on_shutdown` | `false` | a stopping replica hands leadership over at once |
| `cluster.kv_prefix`, `cluster.gossip_cluster_label` | from `Spec.Name` | apart from other services' rings |

## The writer ring

With `cluster.enabled` and `LeaderWrites`, the replicas elect a leader and only
it performs writes. A write that reaches another replica is relayed to it
unchanged (`X-Infrapi-Forwarded-By`); every write's answer names the replica that
performed it (`X-Infrapi-Leader`). Reads are served where they arrive. No leader
yet, or leadership moving mid-relay: `503`, `retryable: true`.

This is not a lock: during a partition each side can elect its own leader. Writes
that converge on their body end where one leader would; others must be fenced in
what they write to. See [`cluster`](cluster.md).

`infrapi_app_relayed_requests_total{method,route}` counts the writes a replica
relayed.

## The gateway

With a `gateway` section, the leader keeps an APISIX in line with the ring through
its Admin API ([`gateway`](gateway.md)), every `interval`:

| object | id | holds |
|---|---|---|
| upstream | `<name>-read` | every replica, health-checked on `/-/readyz` |
| upstream | `<name>-write` | the leader alone |
| service | `<name>-api` | `key-auth` (header `apikey`), `limit-count` per consumer (429), `client-control` (413), `request-id` (`X-Correlation-ID`) |
| routes | `<name>-<prefix>-read`, `-write` | per version: `GET`/`HEAD` to read, `POST`/`PUT`/`PATCH`/`DELETE` to write |
| route | `<name>-docs` | `/-/docs` and `/-/openapi/*`, no key |

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
