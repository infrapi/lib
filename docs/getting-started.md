# Getting started

## 1. Add the dependency

```bash
go get github.com/infrapi/lib
```

## 2. Write a dotenv file

Configuration comes from a dotenv file, not from the process environment. The only
environment variable the library reads directly is `INFRAPI_CONFIG_DOTENV_FILE`, which
points at that file (default: `.env` in the working directory).

```bash title=".env"
INFRAPI_APP_NAME="my-service"
INFRAPI_APP_PLATFORM="sandbox"
INFRAPI_APP_REGION="eu-west-1"
INFRAPI_APP_LOCATION="dc1"
INFRAPI_APP_URL="http://my-service.example.com:8080"
INFRAPI_APP_LISTEN_ADDRESS="127.0.0.1:8080"
```

`INFRAPI_APP_NAME`, `INFRAPI_APP_PLATFORM`, `INFRAPI_APP_REGION`, `INFRAPI_APP_LOCATION`
and `INFRAPI_APP_URL` have no default value, so `GetAppConfig()` fails without them. Everything else falls back to the defaults listed in the
[config reference](packages/config.md#default-variables).

## 3. Load the configuration

```go
vars, err := config.NewConfig()
if err != nil {
	log.Fatal(err) // dotenv file missing or unreadable
}

cfg, err := vars.GetAppConfig()
if err != nil {
	log.Fatal(err) // a key is missing or fails its validator
}
```

## 4. Serve

```go
engine, err := server.NewServerGin(cfg)
if err != nil {
	log.Fatal(err)
}

widgets, err := server.APIGroup(engine, api.Group{Name: "widgets", Domain: "example.com", Version: "v1"})
if err != nil {
	log.Fatal(err)
}
widgets.GET("/widget", func(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"namespace": server.Namespace(c), "widgets": []string{"foo", "bar"}})
})

if err := engine.Run(cfg.AppListenAddress); err != nil {
	log.Fatal(err)
}
```

The engine already answers `GET /-/metadata` with the application identity, and the
route above is `GET /apis/widgets.example.com/v1/namespaces/:namespace/widget`.

## 5. Call another service

```go
client, err := infraclient.NewClient(&infraclient.ClientOptions{
	Server:    "widgets.example.com",
	API:       api.Group{Name: "widgets", Domain: "example.com", Version: "v1"},
	Namespace: "demo",
	Token:     os.Getenv("INFRAPI_TOKEN"),
})
if err != nil {
	log.Fatal(err)
}

resp, err := client.Get("widget", nil)
```

That request hits
`https://widgets.example.com:443/apis/widgets.example.com/v1/namespaces/demo/widget`. See
the [client reference](packages/client.md) for TLS, retry and hedging options, and
[api](packages/api.md) for the path layout.

## Runnable examples

Each package has a self-contained program under `examples/`:

```bash
go run ./examples/api
go run ./examples/config
go run ./examples/errors
go run ./examples/client
go run ./examples/listener
go run ./examples/server
```

`examples/config`, `examples/listener` and `examples/server` expect to be run from
the repository root: they point `INFRAPI_CONFIG_DOTENV_FILE` at dotenv files
shipped next to the source.
