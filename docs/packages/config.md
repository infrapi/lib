# config

```go
import "github.com/infrapi/lib/pkg/config"
```

Reads a dotenv file once, then hands out typed and validated values.

## Loading

```go
vars, err := config.NewConfig()
```

`NewConfig()` reads the file named by `INFRAPI_CONFIG_DOTENV_FILE`, or `.env` when that
variable is unset, and returns an error if the file cannot be read.

!!! note "The process environment is not a fallback"
    Values are taken from the dotenv file only. An `INFRAPI_APP_NAME` exported in the
    shell is invisible to `GetString`; put it in the dotenv file, or point
    `INFRAPI_CONFIG_DOTENV_FILE` at a file that contains it.

The returned `Variables` exposes the raw map, the file path and the default rule set:

```go
type Variables struct {
	Values        map[string]string
	Path          string
	DefaultConfig *DefaultConfig
}
```

## Reading a single value

Every getter takes a `*VariableOpts`:

```go
type VariableOpts struct {
	Key       string // dotenv key
	Default   string // used when the key is absent or empty
	Validator string // go-playground/validator rule
	Sensible  bool   // mask the value in error messages
}
```

```go
port, err := vars.GetInt(&config.VariableOpts{
	Key:       "MY_APP_PORT",
	Default:   "8080",
	Validator: "number",
})
```

| Getter | Returns | Parsing |
|--------|---------|---------|
| `GetString` | `string` | raw value |
| `GetBool` | `bool` | `strconv.ParseBool` |
| `GetInt` | `int` | `strconv.Atoi` |
| `GetInt64` | `int64` | `strconv.ParseInt` base 10 |
| `GetSliceString` | `[]string` | `json.Unmarshal`, so the value must be a JSON array |
| `GetDuration` | `time.Duration` | `time.ParseDuration` |

Rules that apply to all of them:

- A missing or empty key with no `Default` is an error.
- A missing or empty key with a `Default` returns that default **without running the
  validator**. Defaults are trusted.
- `Validator` accepts any [go-playground/validator](https://github.com/go-playground/validator)
  rule string, applied to the raw string value.
- `Sensible: true` replaces the value with `[masked]` in validation errors. Use it for
  tokens and passwords.
- Every error message names the key and the dotenv path, so a bad deployment is easy to
  trace.

## Application configuration

`GetAppConfig()` resolves the whole `INFRAPI_APP_*` set in one call and returns a typed
struct:

```go
cfg, err := vars.GetAppConfig()
```

```go
type AppConfig struct {
	AppName                 string
	AppLogLevel             logrus.Level
	AppListenType           string
	AppListenAddress        string
	AppPlatform             string
	AppRegion               string
	AppFqdn                 string
	AppLocation             string
	AppUrl                  string
	AppTrustedProxies       []string
	AppCorsAllowOrigins     []string
	AppCorsAllowMethods     []string
	AppCorsAllowHeaders     []string
	AppCorsExposeHeaders    []string
	AppCorsAllowCredentials bool
	AppCorsMaxAge           int64
}
```

The first failing key aborts the call, so the error tells you exactly what to fix.
Individual getters (`GetAppName`, `GetAppLogLevel`, `GetAppCorsMaxAge`, ...) are exported
too when only one value is needed.

## Default variables

`NewDefaultConfig()` builds the rules below. A key with no default is mandatory.

| Key | Default | Validator |
|-----|---------|-----------|
| `INFRAPI_APP_NAME` | - | `required` |
| `INFRAPI_APP_LOG_LEVEL` | `info` | `oneof=trace debug info error` |
| `INFRAPI_APP_LISTEN_TYPE` | `tcp` | `oneof=tcp systemd` |
| `INFRAPI_APP_LISTEN_ADDRESS` | `127.0.0.1:8080` | `hostname_port` |
| `INFRAPI_APP_FQDN` | `os.Hostname()` | `fqdn` |
| `INFRAPI_APP_PLATFORM` | - | `oneof=sandbox preprod prod` |
| `INFRAPI_APP_REGION` | - | `oneof=eu-west-1 eu-west-2 au-southeast-1` |
| `INFRAPI_APP_LOCATION` | - | `oneof=dc1 dc2 dc3` |
| `INFRAPI_APP_URL` | - | `http_url` |
| `INFRAPI_APP_TRUSTED_PROXIES` | `[]` | - |
| `INFRAPI_APP_CORS_ALLOW_ORIGINS` | `["*"]` | - |
| `INFRAPI_APP_CORS_ALLOW_METHODS` | `["PUT", "PATCH", "HEAD"]` | - |
| `INFRAPI_APP_CORS_ALLOW_HEADERS` | `["Origin"]` | - |
| `INFRAPI_APP_CORS_EXPOSE_HEADERS` | `["Content-Length"]` | - |
| `INFRAPI_APP_CORS_ALLOW_CREDENTIALS` | `true` | - |
| `INFRAPI_APP_CORS_MAX_AGE` | `12` | - |

`INFRAPI_APP_CORS_MAX_AGE` is a number of hours. The `*_PROXIES`, `*_ORIGINS`,
`*_METHODS` and `*_HEADERS` values are JSON arrays.

## Overriding the rules

`DefaultConfig` is a plain struct of `*VariableOpts`, so a service can tighten or relax a
rule before reading anything:

```go
vars, err := config.NewConfig()
if err != nil {
	return err
}

vars.DefaultConfig.AppRegion.Validator = "oneof=emea apac"
vars.DefaultConfig.AppPlatform.Validator = "oneof=development staging production"

cfg, err := vars.GetAppConfig()
```

`examples/config` walks through defaults, custom validators and the error messages
produced by an invalid file.
