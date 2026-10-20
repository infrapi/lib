package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The request's id reaches the problem documents.
func TestCorrelate(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t))
	require.NoError(t, err)
	app.GET("/fail", func(c *gin.Context) { AbortError(c, assert.AnError) })
	_, body := call(t, app, http.MethodGet, "/fail", "", map[string]string{RequestIDHeader: "req-9"})
	assert.Equal(t, "req-9", body["correlation_id"])
	_, body = call(t, app, http.MethodGet, "/fail", "", nil)
	assert.NotContains(t, body, "correlation_id", "no id in, none out: the engine makes none up")
}

// With a tracer provider set, every call has a span named after its route,
// continuing the trace a traceparent carries in, with the request id on it;
// the access log line leads to it. The engine's own endpoints have none.
func TestSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp, prop := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(tp); otel.SetTextMapPropagator(prop) })

	var logs bytes.Buffer
	previous := gin.DefaultWriter
	gin.DefaultWriter = &logs
	t.Cleanup(func() { gin.DefaultWriter = previous })

	cfg := testAppConfig(t)
	cfg.AppLogFormat = LogFormatJSON
	app, err := NewServerGin(cfg)
	require.NoError(t, err)
	app.GET("/widgets/:id", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/widgets/42", nil)
	req.Header.Set(RequestIDHeader, "req-7")
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	app.ServeHTTP(httptest.NewRecorder(), req)
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, MetricsPath, nil))

	ended := recorder.Ended()
	require.Len(t, ended, 1, "one span, none for /-/metrics")
	span := ended[0]
	assert.Equal(t, "GET /widgets/:id", span.Name())
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", span.SpanContext().TraceID().String(), "the caller's trace")
	attrs := map[string]string{}
	for _, a := range span.Attributes() {
		attrs[string(a.Key)] = a.Value.String()
	}
	assert.Equal(t, "req-7", attrs["http.request.header.x-request-id"])

	var line map[string]any
	require.NoError(t, json.Unmarshal(bytes.SplitN(logs.Bytes(), []byte("\n"), 2)[0], &line), logs.String())
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", line["trace_id"])
	assert.Equal(t, span.SpanContext().SpanID().String(), line["span_id"])
	assert.Equal(t, "req-7", line["request_id"])
}
