---
title: client
description: Resty-based client of the APIs server exposes, with bearer token, TLS/mTLS, retry and optional hedging.
weight: 30
---

# client

```go
import infraclient "github.com/infrapi/lib/pkg/client"
```

The client of the APIs InfraPI services expose with [`server`](server.md): a
[resty](https://resty.dev) client with InfraPI defaults - bearer token, JSON headers,
TLS 1.2 floor, retry on transient failures and optional hedging - that builds the paths of
[`api`](api.md).

## Creating a client

```go
compute := api.Group{Name: "compute", Domain: "infrapi.io", Version: "v1"}

client, err := infraclient.NewClient(&infraclient.ClientOptions{
	Server:    "compute.example.com",
	API:       compute,
	Namespace: "core",
	Token:     token,
})
```

`Server` and `API` are mandatory, and `API` must pass `api.Group.Validate`; everything
else has a default:

| Option | Type | Default |
|--------|------|---------|
| `Protocol` | `string` | `https` |
| `Port` | `int` | `443` |
| `Namespace` | `string` | none: each request must then name one |
| `UserAgent` | `string` | `infrapi-lib-client-{name}.{domain}` |
| `Headers` | `map[string]string` | `Content-Type` and `Accept` set to `application/json` |
| `GlobalTimeout` | `int` (seconds) | `60` |
| `RetryCount` | `int` | `3` |
| `RetryWaitTime` | `time.Duration` | `1s` |
| `RetryMaxWaitTime` | `time.Duration` | `30s` |

`Token`, when set, is sent as a bearer token on every request.

`Update(opts)` rebuilds an existing client in place with a new set of options, which is
what `NewClient` calls internally.

## Request URL

Requests name a resource, and the client builds:

```text
{Protocol}://{Server}:{Port}/apis/{name}.{domain}/{version}/namespaces/{namespace}/{resource}
```

So on the client above, `client.Get("vm/123", nil)` requests
`https://compute.example.com:443/apis/compute.infrapi.io/v1/namespaces/core/vm/123`,
and `client.Post("vm/123/reboot", nil)` its `reboot` action. Each segment of the
resource is escaped. A resource with an empty, `.` or `..` segment, or a request without
a namespace, fails before anything is sent.

## Sending requests

```go
resp, err := client.Post("vm", &infraclient.RequestOptions{
	Data:      body,                                  // raw request body
	Headers:   map[string]string{"X-Request-Id": id}, // merged into the request
	Server:    "compute-eu.example.com",              // per-request host override
	Namespace: "edge",                                // per-request namespace override
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
resp, err := client.Get("vm", &infraclient.RequestOptions{
	MinHttpCode: 200,
	MaxHttpCode: 299,
})
```

The error message is the HTTP status text; inspect `resp.Body` for the server payload.

## Retry

Retries are on by default and trigger on a transport error, `429 Too Many Requests`, or
any `5xx`, with exponential backoff between `RetryWaitTime` and `RetryMaxWaitTime`.

```go
client, err := infraclient.NewClient(&infraclient.ClientOptions{
	Server:           "compute.example.com",
	API:              compute,
	RetryCount:       5,
	RetryWaitTime:    200 * time.Millisecond,
	RetryMaxWaitTime: 5 * time.Second,
})
```

## Hedging

Hedging fires staggered concurrent requests and keeps the first success, trading extra
load for a shorter tail latency.

```go
client, err := infraclient.NewClient(&infraclient.ClientOptions{
	Server: "compute.example.com",
	API:    compute,
	Hedging: &infraclient.HedgingOptions{
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

> [!WARNING]
> Hedging duplicates requests. It is limited to `GET`, `HEAD`, `OPTIONS` and `TRACE`
> unless `NonReadOnlyAllowed` is set, and resty disables retry while hedging is on.

## TLS and mTLS

The client always negotiates TLS 1.2 or later. Certificates can be passed as PEM bytes or
as filesystem paths; when both are given, the in-memory content wins.

```go
client, err := infraclient.NewClient(&infraclient.ClientOptions{
	Server: "compute.example.com",
	API:    compute,

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

## Routes outside the layout

`Do` always builds a namespaced path. A route of the same service outside it - a
cluster-wide endpoint such as `auth`'s `/apis/authorization.infrapi.io/v1/check`, or
one under `/-/` - goes through `NewRequest`, which keeps this client's TLS, retry, header
and timeout configuration without exposing resty to the caller:

```go
resp, err := client.NewRequest(ctx).Get("https://compute.example.com/-/metadata")
```

`NewRequest(ctx)` returns a `*Request` with `Get`, `Post`, `Put`, `Patch` and `Delete`,
each taking the full URL and returning the same `*Response` as `Do`. Unlike `Do`, a
`Request` applies no success-range check: any response that comes back, 2xx or not, is
returned without an error, and the caller decides what counts as a failure.

`StatusText(code)` and the `StatusBadGateway` constant are `net/http`'s own, re-exported
so a caller deciding what counts as a failure - and building an
[`errors.Error`](errors.md) to report it - never needs to import `net/http` itself either:

```go
if resp.Code < 200 || resp.Code >= 300 {
	return infraerrors.New(resp.Code, "myapp.Call", "%s", infraclient.StatusText(resp.Code))
}
```

## Escape hatch

`client.RestyClient` is the underlying `*resty.Client`. Anything neither wrapper exposes -
middlewares, cookies, proxies, custom transports - is configurable there.

`examples/client` runs all of this against a local server, including retry and hedging.
