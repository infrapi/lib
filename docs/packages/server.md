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
- **Recovery** and **logging** middleware.
- **`GET /-/metadata`**, answering with the application identity:

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
hands:

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
