// Package tracing sends a service's traces over OTLP: it sets OpenTelemetry's
// tracer provider and propagators up from the INFRAPI_TRACING_* keys, once,
// at start. pkg/server opens a span per request with whatever provider is set
// — none, and the spans cost next to nothing.
//
//	shutdown, err := tracing.Setup(ctx, appCfg, tracingCfg)
//	if err != nil { … }
//	defer shutdown(context.Background()) // flushes what is left
package tracing

import (
	"context"
	"fmt"
	"net/url"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/infrapi/lib/pkg/config"
)

// Shutdown flushes the spans still buffered and stops the export. Call it
// once, when the service stops.
type Shutdown func(context.Context) error

// Setup sets the global tracer provider and propagators from cfg. Disabled, it
// sets nothing and returns a Shutdown that does nothing.
//
// The spans are exported in batches, to cfg.Endpoint over OTLP/HTTP or gRPC,
// sampled at cfg.SampleRatio — a trace the caller already sampled is followed
// whatever the ratio. The W3C traceparent and baggage headers carry a trace
// in and out of the service. Every span says which service, platform, region
// and location it comes from.
func Setup(ctx context.Context, app *config.AppConfig, cfg *config.TracingConfig) (Shutdown, error) {
	if cfg == nil || !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := newExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("tracing: %w", err)
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", app.AppName),
		attribute.String("deployment.environment.name", app.AppPlatform),
		attribute.String("cloud.region", app.AppRegion),
		attribute.String("host.name", app.AppFqdn),
		attribute.String("infrapi.location", app.AppLocation),
	))
	if err != nil {
		return nil, fmt.Errorf("tracing: resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	return provider.Shutdown, nil
}

// newExporter is the OTLP client for cfg.Protocol. The endpoint's scheme
// decides TLS: http:// sends in clear, https:// over TLS. Over HTTP, an
// endpoint without a path posts to /v1/traces.
func newExporter(ctx context.Context, cfg *config.TracingConfig) (*otlptrace.Exporter, error) {
	switch cfg.Protocol {
	case "grpc":
		return otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(cfg.Endpoint),
			otlptracegrpc.WithHeaders(cfg.Headers))
	case "http", "":
		u, err := url.Parse(cfg.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", cfg.Endpoint, err)
		}
		// The collector's address alone means its traces path, as
		// OTEL_EXPORTER_OTLP_ENDPOINT does; a path given is kept.
		if u.Path == "" || u.Path == "/" {
			u.Path = "/v1/traces"
		}
		return otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(u.String()),
			otlptracehttp.WithHeaders(cfg.Headers))
	}
	return nil, fmt.Errorf("protocol %q is not http or grpc", cfg.Protocol)
}
