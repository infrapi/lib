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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/credentials"

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
// in and out of the service. Every span carries the service's identity:
// service.name, .platform, .region, .location and .fqdn.
func Setup(ctx context.Context, app *config.AppConfig, cfg *config.TracingConfig) (Shutdown, error) {
	if cfg == nil || !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := newExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("tracing: %w", err)
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(attributes(app)...))
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

// attributes describe the service on every span, under service.*:
// service.name, which OpenTelemetry requires for a backend to name the
// service, then its platform, region, location and FQDN.
func attributes(app *config.AppConfig) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("service.name", app.AppName),
		attribute.String("service.platform", app.AppPlatform),
		attribute.String("service.region", app.AppRegion),
		attribute.String("service.location", app.AppLocation),
		attribute.String("service.fqdn", app.AppFqdn),
	}
}

// newExporter is the OTLP client for cfg.Protocol. The endpoint's scheme
// decides TLS: http:// sends in clear, https:// over TLS, trusting the
// system's CAs and cfg.CACertPath's, and presenting cfg's client certificate
// when there is one. Over HTTP, an endpoint without a path posts to
// /v1/traces.
func newExporter(ctx context.Context, cfg *config.TracingConfig) (*otlptrace.Exporter, error) {
	tlsCfg, err := tlsConfig(cfg)
	if err != nil {
		return nil, err
	}
	switch cfg.Protocol {
	case "grpc":
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(cfg.Endpoint), otlptracegrpc.WithHeaders(cfg.Headers)}
		if tlsCfg != nil {
			opts = append(opts, otlptracegrpc.WithTLSCredentials(credentials.NewTLS(tlsCfg)))
		}
		return otlptracegrpc.New(ctx, opts...)
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
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(u.String()), otlptracehttp.WithHeaders(cfg.Headers)}
		if tlsCfg != nil {
			opts = append(opts, otlptracehttp.WithTLSClientConfig(tlsCfg))
		}
		return otlptracehttp.New(ctx, opts...)
	}
	return nil, fmt.Errorf("protocol %q is not http or grpc", cfg.Protocol)
}

// tlsConfig is the TLS of an export to a private CA's collector, or with a
// client certificate (mTLS); nil when neither is set, and the exporter uses
// the system's CAs. A file that cannot be read or holds no certificate stops
// the service at start.
func tlsConfig(cfg *config.TracingConfig) (*tls.Config, error) {
	if cfg.CACertPath == "" && cfg.ClientCertPath == "" {
		return nil, nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CACertPath != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool() // no system pool on this platform: the CA alone
		}
		pem, err := os.ReadFile(cfg.CACertPath)
		if err != nil {
			return nil, fmt.Errorf("CA certificate: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA certificate %s: no PEM certificate in it", cfg.CACertPath)
		}
		tc.RootCAs = pool
	}
	if cfg.ClientCertPath != "" {
		cert, err := tls.LoadX509KeyPair(cfg.ClientCertPath, cfg.ClientKeyPath)
		if err != nil {
			return nil, fmt.Errorf("client certificate: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	return tc, nil
}
