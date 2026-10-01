# gateway

```go
import infragateway "github.com/infrapi/lib/pkg/gateway"
```

A client for the [Apache APISIX](https://apisix.apache.org/) Admin API: routes, services,
upstreams and consumers as explicit CRUD methods on `Client`, on top of [`http`](http.md),
which supplies the TLS/mTLS, retry and timeout behaviour. Admin API failures come back as
an [`errors.Error`](errors.md), so a service already handling that type elsewhere handles
these too.

## Creating a client

```go
client, err := infragateway.NewClient(&infragateway.ClientOptions{
	Server: "apisix-admin.example.com",
	APIKey: apiKey,
})
```

`Server` and `APIKey` are mandatory; everything else has a default:

| Option | Type | Default |
|--------|------|---------|
| `Protocol` | `string` | `http` |
| `Port` | `int` | `9180` |
| `UserAgent` | `string` | `infrapi-lib-gateway` |
| `GlobalTimeout` | `int` (seconds) | `30` |
| `RetryCount` | `int` | `3` |
| `RetryWaitTime` | `time.Duration` | `1s` |
| `RetryMaxWaitTime` | `time.Duration` | `30s` |

`APIKey` is sent as the `X-API-KEY` header on every request, the authentication scheme the
Admin API expects.

`Update(opts)` rebuilds an existing client in place with a new set of options, which is
what `NewClient` calls internally.

`client.HTTP` is the [`*http.Client`](http.md) `NewClient` built. Every collection method
sends its request through `client.HTTP.NewRequest`, built for exactly this - a URL that
does not fit `http.Client.Do`'s own `/api/{version}/{application}/{path}` shape - so this
package never imports resty directly; `client.HTTP`'s TLS, retry and timeout configuration
still applies.

## Collections

Four Admin API collections are explicit methods on `Client`, named `<Op><Type>` -
`NewRoute`, `GetUpstream`, `ListServices`, `UpdateConsumer`, `DeleteRoute`, and so on:

```go
upstream, err := client.NewUpstream(ctx, "1", infragateway.Upstream{
	Type:  "roundrobin",
	Nodes: []infragateway.Node{{Host: "127.0.0.1", Port: 1980, Weight: 1}},
})

route, err := client.NewRoute(ctx, "1", infragateway.Route{
	URI:      "/hello",
	Upstream: upstream, // *Upstream, plugs straight in - no unwrapping
})

id, created, err := client.CreateRoute(ctx, value) // APISIX generates the id
route, err = client.GetRoute(ctx, "1")
routes, err := client.ListRoutes(ctx)
route, err = client.UpdateRoute(ctx, "1", map[string]any{"desc": "says hello"}) // partial
err = client.DeleteRoute(ctx, "1")
```

Routes, services and upstreams follow this pattern. `Update<Type>` sends a partial document
- a `map[string]any` is the common case - and APISIX merges it into the stored value;
fields left out keep their current value. `New<Type>` replaces the value wholesale, so use
it when the caller owns the full document.

`Consumer`s are keyed by `Username` rather than a generated id, so their shape is slightly
different: `NewConsumer(ctx, consumer)` takes the id from `consumer.Username` instead of a
separate argument, there is no `CreateConsumer`, and `GetConsumer`/`UpdateConsumer`/
`DeleteConsumer` take `username` where the others take `id`.

Every operation returns the plain domain struct - `*Upstream`, `*Route`, `*Service`,
`*Consumer` - so the result of one call plugs directly into another, as `Upstream` does
above. `CreateUpstream`/`CreateRoute`/`CreateService` additionally return the
APISIX-generated id, since there is nowhere else to get it once you let APISIX choose.

## Errors

Every error this package returns is an [`*errors.Error`](errors.md), the same RFC 9457
application error the rest of this library uses:

```go
_, err := client.GetRoute(ctx, "1")
var apiErr *infraerrors.Error
if infraerrors.As(err, &apiErr) {
	fmt.Println(apiErr.Code, apiErr.Message)
}

// or, when only the status code matters:
code := infraerrors.ErrorCode(err)
```

`infraerrors.As` is `errors.As`, re-exported so this never needs the standard library
`errors` package either - see [`errors`](errors.md).

`Code` is the Admin API's HTTP status for a response it sent - `404` from a `Get` on a
deleted route, for instance, `Message` its `error_msg` when it sent one. `Origin` names the
call that broke, in the form `gateway.{collection}.{Op}`, e.g. `gateway.routes.Get`.

A transport failure - connection refused, timeout - and a response body that fails to
decode are wrapped the same way, as a `502 Bad Gateway` `*errors.Error` carrying the
underlying error as its cause, reachable with `errors.Unwrap` or `errors.Is`.

## Retry

Retries are on by default and trigger on a transport error, `429 Too Many Requests`, or
any `5xx`, with exponential backoff between `RetryWaitTime` and `RetryMaxWaitTime` - the
same policy [`http`](http.md) applies, since that is what executes every request.

## TLS and mTLS

The client always negotiates TLS 1.2 or later. Certificates can be passed as PEM bytes or
as filesystem paths; when both are given, the in-memory content wins.

```go
client, err := infragateway.NewClient(&infragateway.ClientOptions{
	Server:   "apisix-admin.example.com",
	APIKey:   apiKey,
	Protocol: "https",

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

`client.HTTP.RestyClient` is the underlying `*resty.Client`. Anything neither wrapper
exposes - middlewares, proxies, custom transports - is configurable there.

`examples/gateway` runs all of this against a local stand-in for the Admin API, including
the error paths above.

## Testing against a real APISIX

`pkg/gateway`'s own tests run against a local stand-in for the Admin API, same as the
example - no real APISIX required. `TestIntegration_RealAdminAPI` is the exception: it
drives a real, already-running Admin API through an upstream, a service, a route and a
consumer, and is the test to run while building or verifying your own integration. It is
skipped unless `APISIX_ADMIN_SERVER` and `APISIX_ADMIN_APIKEY` are set:

```bash
APISIX_ADMIN_SERVER=127.0.0.1 APISIX_ADMIN_APIKEY=<your-admin-key> \
	go test ./pkg/gateway/... -run TestIntegration_RealAdminAPI -v
```

`APISIX_ADMIN_PROTOCOL` and `APISIX_ADMIN_PORT` override the `http`/`9180` defaults. The
test cleans up everything it creates, whether it passes or fails.
