package server

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Tracing: what identifies a call and follows it through the service — its
// request id, and its OpenTelemetry span.

// RequestIDHeader carries a call's id: whatever stands in front of the
// service sets it — a gateway, a proxy, the caller itself — and the engine
// puts it in the request's context (CorrelationID), the access log line and
// every problem document. The engine generates none of its own. The access
// log reads it from the request, then from the response, for an id the
// service attached itself.
const RequestIDHeader = "X-Request-Id"

type correlationIDKey struct{}

// WithCorrelationID stores the id every response and log line of this call
// carries.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey{}, id)
}

// CorrelationID returns it, or empty.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDKey{}).(string)
	return id
}

// spans opens a span per request with the global tracer provider — the one
// pkg/tracing sets up; none, and the spans are not recorded — continuing the
// trace a traceparent header carries in. A span is named after the route.
// The engine's own endpoints, under /-/, open none: a scrape is not a call.
func spans(service string) gin.HandlerFunc {
	return otelgin.Middleware(service, otelgin.WithGinFilter(func(c *gin.Context) bool {
		return !strings.HasPrefix(c.Request.URL.Path, "/-/")
	}))
}

// correlate puts the request's id in its context and on its span, and the
// span's trace and span ids in the access log line: a log line leads to its
// trace, and a request id to both.
func correlate(c *gin.Context) {
	ctx := c.Request.Context()
	span := trace.SpanFromContext(ctx)
	if id := c.GetHeader(RequestIDHeader); id != "" {
		ctx = WithCorrelationID(ctx, id)
		span.SetAttributes(attribute.String("http.request.header.x-request-id", id))
		c.Request = c.Request.WithContext(ctx)
	}
	if sc := span.SpanContext(); sc.IsValid() {
		LogField(c, "trace_id", sc.TraceID().String())
		LogField(c, "span_id", sc.SpanID().String())
	}
	c.Next()
}
