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
- **Recovery** and **logging** middleware, from `gin.Default()`.
- **Request metrics**, recorded for every route.
- **Four endpoints**, with nothing to configure:

| Endpoint | Serves |
|----------|--------|
| `GET /-/metadata` | The application identity. |
| `GET /-/metrics` | Prometheus exposition format. |
| `GET /-/openapi.json` | OpenAPI 3.1, generated from the route table. |
| `GET /-/docs` | Swagger UI reading that specification. |

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
    `/-/openapi.json` keeps working: it is generated in process and depends on nothing
    external.

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
