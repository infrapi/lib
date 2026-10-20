# server

```go
import "github.com/infrapi/lib/pkg/server"
```

One constructor that turns an `*config.AppConfig` into a ready `*gin.Engine`.

```go
engine, err := server.NewServerGin(cfg)
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
- **Gin in release mode** — no debug line per route at start — unless
  `INFRAPI_APP_LOG_LEVEL` is `debug` or `trace`, or the mode was chosen already
  (`GIN_MODE`, or `gin.SetMode` before `NewServerGin`, as a test's `gin.TestMode`).
- **Four endpoints**, with nothing to configure:

| Endpoint | Serves |
|----------|--------|
| `GET /-/metadata` | The application identity. |
| `GET /-/metrics` | Prometheus exposition format. |
| `GET /-/openapi.json` | OpenAPI 3.1, generated from the route table - unless the service passes its own documents with `WithOpenAPI`. |
| `GET /-/docs` | Swagger UI reading that specification, or the documents passed with `WithOpenAPI`. |

The paths are exported as `server.MetricsPath`, `server.OpenAPIPath` and
`server.DocsPath`.

`/-/metadata` answers with:

```json
{
  "appName": "my-service",
  "appPlatform": "sandbox",
  "appRegion": "eu-west-1",
  "appLocation": "dc1",
  "appFqdn": "my-service.example.com",
  "appUrl": "http://my-service.example.com:8080"
}
```

Nothing else is registered. The engine is a normal `*gin.Engine`: add groups, routes and
middleware on top of it.

```go
v1 := engine.Group("/api/v1")
v1.GET("/widgets", listWidgets)
```


## Your own OpenAPI documents

The generated specification knows paths and parameters, not bodies or schemas. A
service whose contract is written by hand passes it with `WithOpenAPI`, and `/-/docs`
documents that instead:

```go
//go:embed api/orders/v1.yaml
var ordersV1 []byte

engine, err := server.NewServerGin(cfg, server.WithOpenAPI(
	server.OpenAPISpec{Name: "orders v1", Path: "orders/v1.yaml", Content: ordersV1},
))
```

- Each document is served as it is under `server.OpenAPIPrefix`, here
  `/-/openapi/orders/v1.yaml`, as `application/yaml` (`.yaml`, `.yml`) or
  `application/json` (`.json`).
- With several documents — one file per major version, say — Swagger UI offers them in
  a selector.
- They replace the generated specification: `/-/openapi.json` is no longer served, so
  the API has one description, not two.
- `NewServerGin` refuses a document without a name or content, a path that is absolute,
  not clean or not `.json`/`.yaml`/`.yml`, two documents sharing a name or a path, and a
  document that is not valid OpenAPI 3.0.

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
- A request no document describes — `/-/metrics`, a route the service adds on its own —
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

## Metrics

Two collectors are recorded for every request, carrying the service FQDN in a `name`
label:

```text
http_requests_total{method="GET",name="my-service.example.com",path="/api/v1/widgets/:id",status="200"} 1
http_request_duration_seconds_bucket{method="GET",name="my-service.example.com",path="/api/v1/widgets/:id",le="0.005"} 1
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

!!! note "What the route table cannot tell"
    Gin knows methods, paths and path parameters. It does not know request bodies, query
    parameters, response schemas or status codes, so the document does not invent them:
    every operation carries a single `default` response marked as undocumented. It is an
    accurate map of the surface, not a contract.

!!! warning "The docs page loads Swagger UI from a CDN"
    `/-/docs` is a small HTML page pulling a pinned Swagger UI release from
    `cdn.jsdelivr.net`. A browser without internet access renders an empty page, while
    the documents themselves - `/-/openapi.json`, or those under `/-/openapi/` - keep
    working: they are served in process and depend on nothing external.

## Configuration in memory

`AppConfig` is a plain struct, so settings can be adjusted before the engine is built -
useful for tests and for values that are not worth a dotenv key:

```go
cfg.AppCorsAllowOrigins = []string{"https://app.example.com"}
cfg.AppTrustedProxies = []string{"10.0.0.0/8"}

engine, err := server.NewServerGin(cfg)
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

`examples/server` runs this end to end, including a live request against `/-/metadata`.
