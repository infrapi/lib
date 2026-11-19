---
title: server
description: One constructor that turns an AppConfig into a ready gin.Engine with metrics, tracing, OpenAPI docs and error rendering.
weight: 80
---

# server

```go
import "github.com/infrapi/lib/pkg/server"
```

One constructor that turns an `*config.AppConfig` into a ready `*gin.Engine`, serving
its own endpoints under the root of the service's API group:

```go
var group = api.Group{Name: "compute.infrapi.io", Version: "v1"}

// health, metrics, docs...: /apis/compute.infrapi.io/v1/-/
engine, err := server.NewServerGin(cfg, []string{group.Root()})
if err != nil {
	return err
}

if err := engine.Run(cfg.AppListenAddress); err != nil {
	return err
}
```

## What it wires

- **Trusted proxies** from `AppTrustedProxies`. An invalid entry is returned as an error
  instead of being ignored, so a bad `INFRAPI_APP_TRUSTED_PROXIES` fails at startup.
- **CORS** from the `AppCors*` fields. `AppCorsMaxAge` is read as hours.
- **Recovery** and **access logging**, written to `gin.DefaultWriter`. Their format
  follows `AppLogFormat` (`INFRAPI_APP_LOG_FORMAT`): gin's text lines by default, or
  with `json` one JSON object per line, as below.
- **Request metrics**, recorded for every route.
- **The call's id**: the request's `X-Request-Id` (`server.RequestIDHeader`), set by
  what stands in front of the service, is put in the request's context, for the
  access log line and every problem document.
- **A span per call**, named after its route, with OpenTelemetry's global tracer
  provider — the one [`tracing`](tracing.md) sets up; none, and the spans are not
  recorded. It continues the trace a `traceparent` header carries in, carries the
  request id, and puts its `trace_id` and `span_id` in the access log line. The
  engine's own endpoints, under `<root>/-/`, open none.
- **Gin in release mode** — no debug line per route at start — unless
  `INFRAPI_APP_LOG_LEVEL` is `debug` or `trace`, or the mode was chosen already
  (`GIN_MODE`, or `gin.SetMode` before `NewServerGin`, as a test's `gin.TestMode`).
- **Six endpoints per root**, with nothing to configure, under `<root>/-/`, open to
  anyone: no token, no contract validation (see [Authentication](#authentication)).
  The roots are the second argument, one per API group the service serves
  (`group.Root()`, `/apis/compute.infrapi.io/v1`): a gateway that routes
  `/apis/<group>/<version>/*` to the service reaches them with that group's API, and
  Kayra reads `<root>/-/openapi.json` there. Nothing is served under a bare `/-/`.
  `NewServerGin` refuses no root, a root that is not an absolute, clean path, is `/`,
  carries parameters or a `-` segment, and two roots one inside the other.

A service serving several groups, as Kayra does, passes them all; each gets its own
endpoints, about its own routes. The first is the primary root: a route under no root
(`/oauth2/userinfo`, a discovery document) counts as its.

```go
engine, err := server.NewServerGin(cfg, []string{
	"/apis/system.infrapi.io/v1", // primary
	"/apis/catalog.infrapi.io/v1",
})
// /apis/catalog.infrapi.io/v1/-/openapi.json: the catalog's routes only
// /apis/catalog.infrapi.io/v1/-/metrics:      the catalog's requests only
```

| Per root | |
|---|---|
| `metadata`, `healthz`, `readyz` | The same for every root: the process's identity and health. |
| `metrics` | The requests of the root's routes. The process's own collectors (Go runtime, `promauto`) appear under every root. |
| `openapi.json`, `docs` | The root's routes, and the `WithOpenAPI` documents given that `Root`. |

| Endpoint | Serves |
|----------|--------|
| `GET <root>/-/metadata` | The application identity. |
| `GET <root>/-/healthz` | `200` while the process serves: liveness. |
| `GET <root>/-/readyz` | `200` when ready for traffic, `503` when a `WithReadiness` check fails: readiness. |
| `GET <root>/-/metrics` | Prometheus exposition format, the root's requests. |
| `GET <root>/-/openapi.json` | OpenAPI 3.1, generated from the root's routes - of its endpoints under `<root>/-/` only when the service passes its own documents with `WithOpenAPI`. |
| `GET <root>/-/docs` | Swagger UI reading that specification, or the documents passed with `WithOpenAPI`. |

At its own root, not under a group's, the engine lists its groups, for a caller who
knows only its address, without a token:

```bash
curl -s http://compute.example.com/.well-known/api.json
```

```json
{
  "app": "kayra",
  "groups": [
    {"name": "system", "api": "system.infrapi.io", "version": "v1",
     "root": "/apis/system.infrapi.io/v1", "url": "https://kayra.example.com/apis/system.infrapi.io/v1",
     "primary": true,
     "links": {"docs": "https://kayra.example.com/apis/system.infrapi.io/v1/-/docs",
               "openapi": "…/-/openapi.json", "metrics": "…/-/metrics",
               "healthz": "…/-/healthz", "readyz": "…/-/readyz", "metadata": "…/-/metadata"}},
    {"name": "catalog", "api": "catalog.infrapi.io", "version": "v1", "…": "…"}
  ]
}
```

`url` is the root on `AppUrl`, the root alone when it is not set. A root outside the
`/apis/<group>/<version>` layout, such as tofu-api's `/grafana/v1`, has an empty `api`:
its `name` and `version` are its last segments. The path is `server.APIIndexPath`, the
document `server.APIIndex`.

The paths are exported relative to the root, as `server.MetadataPath`,
`server.MetricsPath`, `server.HealthzPath`, `server.ReadyzPath`, `server.OpenAPIPath`
and `server.DocsPath`: `group.Root() + server.HealthzPath` is
`/apis/compute.infrapi.io/v1/-/healthz`.

`<root>/-/metadata` answers with:

```json
{
  "appName": "my-service",
  "appPlatform": "sandbox",
  "appRegion": "eu-west-1",
  "appLocation": "dc1",
  "appFqdn": "my-service.infrapi.io",
  "appUrl": "http://my-service.example.com:8080"
}
```

Nothing else is registered. The engine is a normal `*gin.Engine`: add groups, routes and
middleware on top of it.

## API routes

Every InfraPI service exposes its resources under [`api`](api.md)'s layout,
`/apis/{group}/{version}/namespaces/{namespace}/{resource}`, the paths
[`client`](client.md) builds and [`auth`](auth.md)'s `RequireAccess` authorizes.
`APIGroup` mounts it:

```go
vms, err := server.APIGroup(engine, api.Group{Name: "compute.infrapi.io", Version: "v1"})
if err != nil {
	log.Fatal(err) // an invalid group, at start
}
vms.GET("/vm", listVMs)               // /apis/compute.infrapi.io/v1/namespaces/:namespace/vm
vms.GET("/vm/:id", getVM)             // .../vm/:id
vms.POST("/vm/:id/reboot", rebootVM)  // .../vm/:id/reboot
```

Routes are relative to the namespace, and after it segments alternate a resource or
action name and an instance id. `server.Namespace(c)` reads the namespace in a handler
(the route parameter is `server.NamespaceParam`). A namespace that is not a lower case
DNS label is answered `404` before any handler of the group runs.


## Your own OpenAPI documents

The generated specification knows paths and parameters, not bodies or schemas. A
service whose contract is written by hand passes it with `WithOpenAPI`, and `<root>/-/docs`
documents that instead:

```go
//go:embed api/orders/v1.yaml
var ordersV1 []byte

engine, err := server.NewServerGin(cfg, []string{group.Root()}, server.WithOpenAPI(
	server.OpenAPISpec{Name: "orders v1", Path: "orders/v1.yaml", Content: ordersV1},
))
```

- Each document is served as it is under `server.OpenAPIPrefix`, here
  `<root>/-/openapi/orders/v1.yaml`, as `application/yaml` (`.yaml`, `.yml`) or
  `application/json` (`.json`).
- A document is its `Root`'s, served and offered under that root only; without one, the
  primary root's. A `Root` that is none of the engine's is refused.
- Swagger UI offers them in a selector — one file per major version, say — followed by
  `operations`: `<root>/-/openapi/operations.json` (`server.OperationsPath`), which
  describes only the endpoints under `<root>/-/`, the engine's own and those the service
  adds there. The API has one description, not two.
- `<root>/-/openapi.json` is the whole API: the root's documents merged into one (the
  first one's `info`, every one's `paths` and `components`), with the operational
  endpoints' paths. It is what Kayra's reconciler reads to learn the API's resources.
- `NewServerGin` refuses a document without a name or content, named `operations` or at
  `operations.json`, a
  path that is absolute, not clean or not `.json`/`.yaml`/`.yml`, two documents sharing a
  name or a path, and a document that is not valid OpenAPI 3.0.

### Enforced, not only documented

Every request a document describes is validated against it before any handler runs:
its path and query parameters, its headers, its body. One that does not hold is
answered `400 invalid_request`, as a problem document whose `detail` says why:

```json
{"title":"Invalid request","status":400,"code":"invalid_request","retryable":false,
 "detail":"request body has an error: doesn't match schema #/components/schemas/OrderSpec: property \"colour\" is unsupported",
 "correlation_id":"5f0c…"}
```

- Handlers receive only what the contract allows: no hand-written checks for what the
  contract already says.
- A request no document describes — `<root>/-/metrics`, a route the service adds on its own —
  goes through untouched.
- Authentication is not checked here: a security scheme in the contract is
  documentation, and what stands in front of the service enforces it.
- The validation runs on the routes the service adds after `NewServerGin`, which is
  where they go.



With `INFRAPI_APP_LOG_FORMAT=json`, every request is one line:

```json
{"time":"2026-10-05T10:12:03.481Z","level":"info","msg":"request","method":"GET","path":"/users/42","route":"/users/:id","status":200,"latency_ms":1.204,"client_ip":"10.0.0.7","size":118,"user_agent":"curl/8.11.1","request_id":"5f0c…"}
```

- `time`, `level` and `msg` are the names logrus's `JSONFormatter` uses, so a
  service that logs through logrus with that formatter writes lines a collector
  parses the same way. `level` is `warning` for a 4xx and `error` for a 5xx.
- `path` is the URL path without its query string; `route` is the route pattern,
  empty when no route matched.
- `request_id` is `X-Request-Id`, from the request or else from the response.
- `errors` carries what handlers attached with `c.Error`, when they did.

A recovered panic is one line too, its stack in a field, followed by the line of
the request it ended with a 500:

```json
{"time":"…","level":"error","msg":"panic recovered","method":"GET","path":"/boom","panic":"kaboom","stack":"/src/handler.go:42 (0x4f2a1c)\n\t(*Handler).Get: return h.store.Get(id)\n…"}
```

The recovery is gin's, with JSON lines:

- The stack has gin's text format and starts where the panic was raised: one
  `file:line (0xpc)` line and one `<tab>function: source line` line per frame.
  The source line is `???` when the file is not there, as in an image that ships
  only the binary; gin drops the function line in that case, this keeps it.
- A client that closed the connection (`EPIPE`, `ECONNRESET`,
  `http.ErrAbortHandler`) is not a panic: one `warning` line,
  `"msg":"client connection broken"` with the `error`, no stack, and nothing
  written back to a connection that is gone.
- gin's text recovery dumps the request in debug mode, every header but
  `Authorization` — an `apikey` header included. The JSON lines never do.

### Fields of your own

`server.LogField` adds a member to the line of the current request — what the
service knows and the engine cannot:

```go
// in a gin handler
server.LogField(c, "tenant", tenant)

// in a net/http handler mounted with gin.WrapH, or its middleware
server.LogField(r.Context(), "resource", map[string]string{"kind": "folder", "id": id})
```

```json
{"time":"…","level":"info","msg":"request","method":"POST",…,"resource":{"id":"f1","kind":"folder"},"tenant":"acme"}
```

Your fields come after the engine's, in key order. The value is marshalled to
JSON (one that cannot be is written as its `%v` text); setting a key again
replaces it. The engine's own names (`time`, `level`, `msg`, `method`, `path`,
`route`, `status`, `latency_ms`, `client_ip`, `size`, `user_agent`, `request_id`,
`errors`, `error`, `panic`, `stack`) are refused, so nothing can hide what the engine saw.

With text logs, the fields follow gin's line as `key=value`, in key order; a
value with a space, a quote or an `=` is quoted:

```text
[GIN] 2026/10/08 - 16:38:25 | 201 |    2.52ms |  127.0.0.1 | POST     "/folders" tenant=acme broadcast_id=go3ouhwv…
```

The engine leaves the global logrus logger alone: a service that wants its own
lines in JSON sets `logrus.SetFormatter(&logrus.JSONFormatter{})` itself.

## Answering with an error

`server.WriteError` renders an [`*errors.Error`](errors.md) as
`application/problem+json`; `server.AbortError` does the same and stops the
gin chain, which is what a middleware does when it refuses a request:

```go
func getUser(c *gin.Context) {
	user, err := store.Get(c.Param("id"))
	if err != nil {
		server.AbortError(c, err) // *errors.Error as it says, anything else a 500
		return
	}
	c.JSON(http.StatusOK, user)
}
```

Anything that is not an `*errors.Error` is treated as a bug on this side: it is
logged in full, with the request's correlation id, and answered as an
`internal_error` 500 that does not repeat its text - a database DSN or a stack
trace has no business reaching the caller.

The engine puts the request's `X-Request-Id` in its context
(`server.CorrelationID`), and `WriteError` puts it in the payload's
`correlation_id`: every error answer carries the id the access log line does.
Setting that header — and returning it to the caller — is the job of what
stands in front of the service, a gateway or a proxy: the engine generates
none, and a call that reaches it without one has none.

## Authentication

`WithAuth` puts a `gin.HandlerFunc` in front of every route but the operational ones -
typically [`auth.Client.Middleware`](auth.md), which
authenticates each request against an upstream authentication service:

```go
client, err := auth.NewClient(auth.ClientOptions{BaseURL: "https://auth.example.com"})
engine, err := server.NewServerGin(cfg, []string{group.Root()}, server.WithAuth(client.Middleware()))
```

Same as `WithOpenAPI`'s contract validation, it never reaches an operational route:
anything under a root's `-/`, the engine's (metadata, health, metrics, OpenAPI, the
`WithOpenAPI` documents, docs) and the service's own (`/-/workers` below), and
`/.well-known/api.json`. They answer without a token. They are told by their route
pattern, not by the order routes are registered in, so a request cannot pass for one
of them.

An operational route of the service's that needs a token says so itself, with the
middleware on the route:

```go
ops := engine.Group(group.Root())
ops.GET("/-/workers", listWorkers)                      // open
ops.GET("/-/secrets", client.Middleware(), listSecrets) // a token
```

## OAuth clients

`WithProtectedResource` serves each root's protected resource metadata
(RFC 9728), for an OAuth client, an MCP client say, to find the
authorization server that issues its tokens, Kayra's. Without a token, at
`<root>/-/oauth-protected-resource`, which a 401's `WWW-Authenticate`
header names ([`auth.ClientOptions.PublicURL`](auth.md#a-resource-for-oauth-clients)),
and at `/.well-known/oauth-protected-resource<root>`:

```go
client, err := auth.NewClient(auth.ClientOptions{BaseURL: kayraURL, PublicURL: cfg.AppUrl})
engine, err := server.NewServerGin(cfg, roots,
	server.WithAuth(client.Middleware()), server.WithProtectedResource(client.AuthorizationServer))
```

```json
{"resource": "https://gateway.example.com/apis/compute.infrapi.io/v1",
 "authorization_servers": ["https://kayra.example.com"],
 "bearer_methods_supported": ["header"]}
```

The resource is `AppUrl` and the root. The authorization server is read at
each request: a 503 while Kayra cannot be reached.

## Health

A gateway or a load balancer checks `<root>/-/readyz` to know whether to send a replica
traffic, and `<root>/-/healthz` to know it is alive at all. Both answer `{"status":"ok"}`.

`<root>/-/readyz` runs the checks the service passes with `WithReadiness`, in order, each
bounded by `server.ReadinessTimeout` (2s); the first that fails answers `503` as a
retryable problem document saying why:

```json
{"title":"Upstream unavailable","status":503,"code":"upstream_unavailable","retryable":true,
 "detail":"not ready: draining","correlation_id":"5f0c…"}
```

A service makes one fail as soon as its shutdown starts, so that the gateway takes it
out of rotation before it stops serving, and checks there what it cannot serve
without:

```go
var draining atomic.Bool

engine, err := server.NewServerGin(cfg, []string{group.Root()}, server.WithReadiness(
	func(context.Context) error {
		if draining.Load() {
			return errors.New("draining")
		}
		return nil
	},
	func(ctx context.Context) error { return backend.Ping(ctx) },
))

// on SIGTERM: draining.Store(true), wait for the gateway to notice, then srv.Shutdown
```

Without checks, `<root>/-/readyz` answers `200` like `<root>/-/healthz`.

## Metrics

Two collectors are recorded for every request, carrying the service FQDN in a `name`
label:

```text
http_requests_total{method="GET",name="my-service.infrapi.io",path="/apis/widgets.infrapi.io/v1/namespaces/:namespace/widget/:id",status="200"} 1
http_request_duration_seconds_bucket{method="GET",name="my-service.infrapi.io",path="/apis/widgets.infrapi.io/v1/namespaces/:namespace/widget/:id",le="0.005"} 1
```

`path` is the route pattern, not the URL, so `/widgets/1` and `/widgets/2` share one time
series. A request that matched no route is labelled `unmatched`, which keeps a scan or a
404 flood from creating a series per URL.

The endpoint also gathers the default registry, so anything the service registers itself,
with `promauto` or otherwise, is exposed alongside the Go runtime collectors:

```go
var widgets = promauto.NewCounter(prometheus.CounterOpts{
	Name: "widgets_created_total",
	Help: "Widgets created.",
})
```

## OpenAPI

The specification is generated per request from `engine.Routes()`, so routes added after
`NewServerGin` are in it. Gin path parameters become OpenAPI ones: `/widgets/:id` is
served as `/widgets/{id}` with a required string path parameter, and `/files/*filepath`
the same way.

`info.title` is `AppName`, `info.version` is the version of the binary as reported by
`runtime/debug`, and `servers` holds `AppUrl`.

> [!NOTE]
> **What the route table cannot tell**
>
> Gin knows methods, paths and path parameters. It does not know request bodies, query
> parameters, response schemas or status codes, so the document does not invent them:
> every operation carries a single `default` response marked as undocumented. It is an
> accurate map of the surface, not a contract.

The engine's own endpoints - `<root>/-/healthz`, `<root>/-/readyz`, `<root>/-/metrics`, `<root>/-/metadata`,
`<root>/-/openapi.json`, `<root>/-/docs` - are documented: a summary, their status codes, content
types and schemas. A service adds its own under `<root>/-/`, on the root's group, and
documents them with `WithOperations`, by their method and their path relative to the
root:

```go
engine.Group(group.Root()).GET("/-/workers", listWorkers)

server.WithOperations(server.Operation{
	Method: http.MethodGet, Path: "/-/workers", Summary: "The workers and their state",
	Responses: map[string]server.Response{
		"200": {Description: "The workers", ContentType: "application/json",
			Schema: map[string]any{"type": "array", "items": map[string]any{"type": "object"}}},
	},
})
```

`NewServerGin` refuses an operation without a method, with a path outside `/-/`, or
without a response.

> [!WARNING]
> **The docs page loads Swagger UI from a CDN**
>
> `<root>/-/docs` is a small HTML page pulling a pinned Swagger UI release from
> `cdn.jsdelivr.net`. A browser without internet access renders an empty page, while
> the documents themselves - `<root>/-/openapi.json`, or those under `<root>/-/openapi/` - keep
> working: they are served in process and depend on nothing external.

## Configuration in memory

`AppConfig` is a plain struct, so settings can be adjusted before the engine is built -
useful for tests and for values that are not worth a dotenv key:

```go
cfg.AppCorsAllowOrigins = []string{"https://app.example.com"}
cfg.AppTrustedProxies = []string{"10.0.0.0/8"}

engine, err := server.NewServerGin(cfg, []string{group.Root()})
```

## Graceful shutdown

`NewServerGin` returns the handler, not a running server, so the lifecycle stays in your
hands. Pair it with [`listener`](listener.md) when the socket comes from the
configuration or from systemd:

```go
srv := &http.Server{
	Addr:              cfg.AppListenAddress,
	Handler:           engine,
	ReadHeaderTimeout: 5 * time.Second,
}

go func() {
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}()

quit := make(chan os.Signal, 1)
signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
<-quit

ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
if err := srv.Shutdown(ctx); err != nil {
	log.Fatal(err)
}
```

`examples/server` runs this end to end, including a live request against `<root>/-/metadata`.
