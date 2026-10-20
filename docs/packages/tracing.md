# tracing

```go
import "github.com/infrapi/lib/pkg/tracing"
```

Sends a service's traces over OTLP. It sets OpenTelemetry's tracer provider and
propagators up from the `INFRAPI_TRACING_*` keys, once, at start; the
[`server`](server.md) engine opens a span per request with it.

```go
vars, err := config.NewConfig()
appCfg, err := vars.GetAppConfig()
tracingCfg, err := vars.GetTracingConfig()

shutdown, err := tracing.Setup(ctx, appCfg, tracingCfg)
if err != nil {
	return err
}
defer shutdown(context.Background()) // flushes the spans still buffered
```

```dotenv
INFRAPI_TRACING_ENABLED="true"
INFRAPI_TRACING_ENDPOINT="http://collector:4318"
INFRAPI_TRACING_PROTOCOL="http"            # or grpc, on the collector's gRPC port (4317)
INFRAPI_TRACING_SAMPLE_RATIO="1"
```

| Key | Default | |
|-----|---------|---|
| `INFRAPI_TRACING_ENABLED` | `false` | Off, `Setup` sets nothing: the spans are not recorded and cost next to nothing. |
| `INFRAPI_TRACING_ENDPOINT` | - | The OTLP receiver, as a URL; required when enabled. `http://` sends in clear, `https://` over TLS. Over HTTP, an address without a path posts to `/v1/traces`. |
| `INFRAPI_TRACING_PROTOCOL` | `http` | `http` (OTLP/HTTP, protobuf) or `grpc`. |
| `INFRAPI_TRACING_SAMPLE_RATIO` | `1` | The share of new traces kept, `0` to `1`. A trace the caller already sampled is followed whatever the ratio. |
| `INFRAPI_TRACING_HEADERS` | `[]` | Sent with every export, as a JSON list of `Name=value`: a tenant, a token. Masked in errors. |
| `INFRAPI_TRACING_CA_CERT_PATH` | - | A PEM file of the CA the collector's certificate is signed by, trusted besides the system's: a private CA. |
| `INFRAPI_TRACING_CLIENT_CERT_PATH` | - | The PEM certificate the service presents to the collector: mTLS. With its key. |
| `INFRAPI_TRACING_CLIENT_KEY_PATH` | - | Its PEM private key. |

## TLS

The endpoint's scheme decides: `http://` sends in clear, `https://` over TLS 1.2 or
later, checking the collector's certificate.

- **A public or system-installed CA**: nothing to set, the system's CAs are trusted.
- **A private CA**: `INFRAPI_TRACING_CA_CERT_PATH`, trusted besides the system's.
- **mTLS**: `INFRAPI_TRACING_CLIENT_CERT_PATH` and `INFRAPI_TRACING_CLIENT_KEY_PATH`,
  both or neither.

The three keys are for an `https://` endpoint: with `http://` they are refused at start
rather than ignored, and so is a file that cannot be read or holds no certificate.
There is no way to skip the certificate check. The same applies over OTLP/HTTP and
gRPC.

## What a span carries

- **The service**, on every span: `service.name` (`INFRAPI_APP_NAME`, the attribute
  OpenTelemetry requires for a backend to name the service), `service.platform`,
  `service.region`, `service.location` and `service.fqdn`.
- **The trace it belongs to**: the W3C `traceparent` and `baggage` headers carry a
  trace into the service and out of it, through any client that propagates them. A
  call that arrives with one continues its caller's trace.
- **The request** (from [`server`](server.md)): one span per call, named after its
  route — `GET /widgets/:id` — with the method, the status and the request's
  `X-Request-Id`. The engine's own endpoints, under `/-/`, open none.

## Logs and traces

With `INFRAPI_APP_LOG_FORMAT=json`, the access log line of a traced call carries
its `trace_id` and `span_id`: a log line leads to its trace, and a request id to
both.

A service opens spans of its own with OpenTelemetry's API, from the request's
context, under the request's span:

```go
ctx, span := otel.Tracer("bookmarks").Start(r.Context(), "store.Create")
defer span.End()
```
