package server

import (
	"context"

	"github.com/gin-gonic/gin"
)

// Tracing: what identifies a call and follows it through the service.

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

// correlate puts the request's id in its context.
func correlate(c *gin.Context) {
	if id := c.GetHeader(RequestIDHeader); id != "" {
		c.Request = c.Request.WithContext(WithCorrelationID(c.Request.Context(), id))
	}
	c.Next()
}
