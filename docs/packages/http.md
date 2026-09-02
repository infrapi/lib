# http

```go
import infrahttp "github.com/infrapi/lib/pkg/http"
```

A [resty](https://resty.dev) client with InfraPI defaults: bearer token, JSON headers,
TLS 1.2 floor, retry on transient failures and optional hedging.

## Creating a client

```go
client, err := infrahttp.NewClient(&infrahttp.ClientOptions{
	Server:      "api.example.com",
	Application: "myapp",
	Token:       token,
})
```

`Server` and `Application` are mandatory; everything else has a default:

| Option | Type | Default |
|--------|------|---------|
| `Protocol` | `string` | `https` |
| `Port` | `int` | `443` |
| `APIVersion` | `string` | `v1` |
| `UserAgent` | `string` | `infrapi-lib-http-{application}` |
| `Headers` | `map[string]string` | `Content-Type` and `Accept` set to `application/json` |
| `GlobalTimeout` | `int` (seconds) | `60` |
| `RetryCount` | `int` | `3` |
| `RetryWaitTime` | `time.Duration` | `1s` |
| `RetryMaxWaitTime` | `time.Duration` | `30s` |

`Token`, when set, is sent as a bearer token on every request.

`Update(opts)` rebuilds an existing client in place with a new set of options, which is
what `NewClient` calls internally.

## Request URL

Paths are relative. The client builds:

```text
{Protocol}://{Server}:{Port}/api/{APIVersion}/{Application}/{path}
```

So `client.Get("status", nil)` on the client above requests
`https://api.example.com:443/api/v1/myapp/status`.

## Sending requests

```go
resp, err := client.Post("items", &infrahttp.RequestOptions{
	Data:    body,                                   // raw request body
	Headers: map[string]string{"X-Request-Id": id},  // merged into the request
	Server:  "api-eu.example.com",                   // per-request host override
})
```

`Get`, `Post`, `Put`, `Patch`, `Delete` and `Head` all delegate to
`Do(method, path, opts)`, and `opts` may be `nil`.

```go
type Response struct {
	Code int
	Body []byte
}
```

`Response` is always returned, even alongside an error, so the status code and body of a
failed call stay readable. When the transport itself fails, `Code` is `500`.

## Success range

A response is an error when its status code is below `MinHttpCode` or above
`MaxHttpCode`. The defaults are `200` and `400`, and both bounds are inclusive, so a
`400 Bad Request` counts as success unless the range is narrowed:

```go
resp, err := client.Get("status", &infrahttp.RequestOptions{
	MinHttpCode: 200,
	MaxHttpCode: 299,
})
```

The error message is the HTTP status text; inspect `resp.Body` for the server payload.

## Retry

Retries are on by default and trigger on a transport error, `429 Too Many Requests`, or
any `5xx`, with exponential backoff between `RetryWaitTime` and `RetryMaxWaitTime`.

```go
client, err := infrahttp.NewClient(&infrahttp.ClientOptions{
	Server:           "api.example.com",
	Application:      "myapp",
	RetryCount:       5,
	RetryWaitTime:    200 * time.Millisecond,
	RetryMaxWaitTime: 5 * time.Second,
})
```

## Hedging

Hedging fires staggered concurrent requests and keeps the first success, trading extra
load for a shorter tail latency.

```go
client, err := infrahttp.NewClient(&infrahttp.ClientOptions{
	Server:      "api.example.com",
	Application: "myapp",
	Hedging: &infrahttp.HedgingOptions{
		Enabled:      true,
		Delay:        150 * time.Millisecond,
		UpTo:         2,
		MaxPerSecond: 5,
	},
})
```

| Field | Meaning |
|-------|---------|
| `Enabled` | Turns hedging on. |
| `Delay` | Wait before each additional request. `0` sends them immediately. |
| `UpTo` | Maximum concurrent hedged requests. Anything below `2` leaves hedging inert. |
| `MaxPerSecond` | Rate cap across hedged requests, fractional allowed. `0` is unlimited. |
| `NonReadOnlyAllowed` | Also hedge `POST`, `PUT`, `PATCH` and `DELETE`. |

!!! warning
    Hedging duplicates requests. It is limited to `GET`, `HEAD`, `OPTIONS` and `TRACE`
    unless `NonReadOnlyAllowed` is set, and resty disables retry while hedging is on.

## TLS and mTLS

The client always negotiates TLS 1.2 or later. Certificates can be passed as PEM bytes or
as filesystem paths; when both are given, the in-memory content wins.

```go
client, err := infrahttp.NewClient(&infrahttp.ClientOptions{
	Server:      "api.example.com",
	Application: "myapp",

	CACertPath: "/etc/ssl/certs/internal-ca.pem",

	ClientCertPath: "/etc/ssl/certs/myapp.pem",
	ClientKeyPath:  "/etc/ssl/private/myapp.key",
})
```

| Option | Purpose |
|--------|---------|
| `CACertContent` / `CACertPath` | Trust an internal CA for the server certificate. |
| `ClientCertContent` / `ClientKeyContent` | mTLS keypair from memory. |
| `ClientCertPath` / `ClientKeyPath` | mTLS keypair from disk. |

An mTLS keypair needs both halves: cert and key must come from the same source.

## Escape hatch

`client.RestyClient` is the underlying `*resty.Client`. Anything the wrapper does not
expose - middlewares, cookies, proxies, custom transports - is configurable there.

`examples/http` runs all of this against a local server, including retry and hedging.
