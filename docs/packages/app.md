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
| `API` | Builds the service once the configuration is loaded; returns its versions. |

A `Version` is one contract and its routes. Its `Prefix` — `/orders/v1/` — is
derived from the contract's paths unless set.

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

Whether a service's writes go through one replica is the operator's choice, not
the code's: a backend that takes concurrent writes needs no single writer, and
the same service can run either way. `Spec` says nothing about it.

With `cluster.enabled` and `cluster.leader_writes`, the replicas elect a leader
and only it performs writes. A write that reaches another replica is relayed to it
unchanged (`X-Infrapi-Forwarded-By`); every write's answer names the replica that
performed it (`X-Infrapi-Leader`). Reads are served where they arrive. No leader
yet, or leadership moving mid-relay: `503`, `retryable: true`.

Who may lead is the [leader checks'](cluster.md#choosing-who-may-lead) to say:
`cluster.leader_checks` — `tcp://`, `http(s)://`, `exec:`, `file://`, `!`. A
replica in maintenance (`!file://…`) hands the writes, and APISIX's write
upstream, to another.

Without `leader_writes`, every replica performs the writes it receives, and
APISIX's write upstream holds every replica like the read one. A cluster is
still useful then: its leader alone keeps APISIX in line with the ring.

This is not a lock: during a partition each side can elect its own leader. Writes
that converge on their body end where one leader would; others must be fenced in
what they write to. See [`cluster`](cluster.md).

`infrapi_app_relayed_requests_total{method,route}` counts the writes a replica
relayed.

## The gateway

With a `gateway` section, the leader keeps an APISIX in line with the ring through
its Admin API ([`gateway`](gateway.md)), every `interval`:

| object | id, by default | holds, by default |
|---|---|---|
| upstream | `<name>-read` | every replica, health-checked on `/-/readyz` |
| upstream | `<name>-write` | the leader alone |
| service | `<name>-api` | `key-auth` (header `apikey`), `limit-count` per consumer (429), `client-control` (413), `request-id` (`X-Correlation-ID`) |
| routes | `<name>-<version>-read`, `-write` | per version: `GET`/`HEAD` to read, `POST`/`PUT`/`PATCH`/`DELETE` to write |
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

  read:  {upstream: "{name}-read",  methods: [GET, HEAD]}
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
