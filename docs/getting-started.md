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

engine.GET("/api/v1/widgets", func(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"widgets": []string{"foo", "bar"}})
})

if err := engine.Run(cfg.AppListenAddress); err != nil {
	log.Fatal(err)
}
```

The engine already answers `GET /-/metadata` with the application identity.

## 5. Call another service

```go
client, err := infrahttp.NewClient(&infrahttp.ClientOptions{
	Server:      "api.example.com",
	Application: "myapp",
	Token:       os.Getenv("INFRAPI_TOKEN"),
})
if err != nil {
	log.Fatal(err)
}

resp, err := client.Get("status", nil)
```

That request hits `https://api.example.com:443/api/v1/myapp/status`. See the
[http reference](packages/http.md) for TLS, retry and hedging options.

## Runnable examples

Each package has a self-contained program under `examples/`:

```bash
go run ./examples/config
go run ./examples/http
go run ./examples/server
```

`examples/config` and `examples/server` expect to be run from the repository root:
they point `INFRAPI_CONFIG_DOTENV_FILE` at dotenv files shipped next to the source.
