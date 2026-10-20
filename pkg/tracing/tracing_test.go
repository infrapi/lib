package tracing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/infrapi/lib/pkg/config"
)

var app = &config.AppConfig{AppName: "hello-api", AppPlatform: "sandbox", AppRegion: "eu-west-1",
	AppFqdn: "hello.example.com", AppLocation: "dc1"}

// restore puts the global provider and propagators back after a test.
func restore(t *testing.T) {
	tp, prop := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(prop)
	})
}

// Disabled, nothing is set.
func TestSetup_disabled(t *testing.T) {
	before := otel.GetTracerProvider()
	shutdown, err := Setup(context.Background(), app, &config.TracingConfig{})
	require.NoError(t, err)
	assert.Equal(t, before, otel.GetTracerProvider())
	assert.NoError(t, shutdown(context.Background()))
}

// Enabled over OTLP/HTTP: the spans reach the collector, with the headers
// configured, and the W3C propagators are set.
func TestSetup_http(t *testing.T) {
	restore(t)
	var (
		mu     sync.Mutex
		posts  int
		tenant string
	)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		if r.URL.Path == "/v1/traces" {
			posts++
			tenant = r.Header.Get("X-Scope-OrgID")
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	shutdown, err := Setup(context.Background(), app, &config.TracingConfig{Enabled: true,
		Endpoint: collector.URL, Protocol: "http", SampleRatio: 1, Headers: map[string]string{"X-Scope-OrgID": "core"}})
	require.NoError(t, err)

	_, span := otel.Tracer("test").Start(context.Background(), "work")
	assert.True(t, span.SpanContext().IsSampled())
	span.End()
	require.NoError(t, shutdown(context.Background()), "the shutdown flushes the batch")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, posts)
	assert.Equal(t, "core", tenant)

	carrier := propagation.MapCarrier{}
	ctx := trace.ContextWithSpanContext(context.Background(), span.SpanContext())
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	assert.NotEmpty(t, carrier.Get("traceparent"), "traceparent goes out")
}

// gRPC connects lazily: Setup does not wait for the collector.
func TestSetup_grpc(t *testing.T) {
	restore(t)
	shutdown, err := Setup(context.Background(), app, &config.TracingConfig{Enabled: true,
		Endpoint: "http://127.0.0.1:1", Protocol: "grpc", SampleRatio: 0})
	require.NoError(t, err)
	assert.NoError(t, shutdown(context.Background()))
}

func TestSetup_unknownProtocol(t *testing.T) {
	restore(t)
	_, err := Setup(context.Background(), app, &config.TracingConfig{Enabled: true, Endpoint: "http://x", Protocol: "udp"})
	assert.ErrorContains(t, err, `protocol "udp"`)
}
