package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

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

// errorBody is the RFC 9457 problem document a service answers with. Its
// own type, rather than infraerrors.Error marshalled directly: the
// correlation id comes from the request's context, not the error, and
// Retryable is always present here, even when the error left it unset.
type errorBody struct {
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Detail        string `json:"detail,omitempty"`
	Code          string `json:"code"`
	Retryable     bool   `json:"retryable"`
	CorrelationID string `json:"correlation_id,omitempty"`
	Current       any    `json:"current,omitempty"`
}

// WriteError renders err as application/problem+json. Anything that is not
// an *infraerrors.Error is a bug on this side: it is logged in full and
// reported without its text, which may carry what the caller has no
// business seeing.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var e *infraerrors.Error
	if !infraerrors.As(err, &e) {
		slog.ErrorContext(r.Context(), "unexpected error", "err", err, "path", r.URL.Path,
			"correlation_id", CorrelationID(r.Context()))
		e = &infraerrors.Error{Code: http.StatusInternalServerError, Slug: "internal_error",
			Title: "Internal error", Message: "an unexpected error; the log carries it under this correlation id"}
	}

	// The title sent to clients: the one set on the error, or the status
	// phrase, which is what RFC 9457's "about:blank" asks for.
	title := e.Title
	if title == "" {
		title = http.StatusText(e.Code)
	}
	retryable := e.Retryable != nil && *e.Retryable
	w.Header().Set("Content-Type", infraerrors.MediaType)
	w.WriteHeader(e.Code)
	if jsonErr := json.NewEncoder(w).Encode(errorBody{
		Title: title, Status: e.Code, Detail: e.Message, Code: e.Slug, Retryable: retryable,
		CorrelationID: CorrelationID(r.Context()), Current: e.Current,
	}); jsonErr != nil {
		// The status is sent: all that is left is to say so in the log.
		slog.WarnContext(r.Context(), "writing a problem document", "err", jsonErr, "code", e.Slug,
			"correlation_id", CorrelationID(r.Context()))
	}
}

// AbortError renders err and stops the gin chain: what a middleware does
// when it refuses a request.
func AbortError(c *gin.Context, err error) {
	WriteError(c.Writer, c.Request, err)
	c.Abort()
}
