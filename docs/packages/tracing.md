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
INFRAPI_TRACING_HEADERS=["X-Scope-OrgID=core"]
```

| Key | Default | |
|-----|---------|---|
| `INFRAPI_TRACING_ENABLED` | `false` | Off, `Setup` sets nothing: the spans are not recorded and cost next to nothing. |
| `INFRAPI_TRACING_ENDPOINT` | - | The OTLP receiver, as a URL; required when enabled. `http://` sends in clear, `https://` over TLS. Over HTTP, an address without a path posts to `/v1/traces`. |
| `INFRAPI_TRACING_PROTOCOL` | `http` | `http` (OTLP/HTTP, protobuf) or `grpc`. |
| `INFRAPI_TRACING_SAMPLE_RATIO` | `1` | The share of new traces kept, `0` to `1`. A trace the caller already sampled is followed whatever the ratio. |
| `INFRAPI_TRACING_HEADERS` | `[]` | Sent with every export, as a JSON list of `Name=value`: a tenant, a token. Masked in errors. |

## What a span carries

- **The service**: `service.name` (`INFRAPI_APP_NAME`), `deployment.environment.name`
  (platform), `cloud.region`, `host.name` (FQDN) and `infrapi.location`, on every span.
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
