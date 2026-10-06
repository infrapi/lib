// Package problem is the one shape every error of this API takes:
// application/problem+json, with a stable code and an explicit retryable flag.
//
// Explicit, because the caller is a workflow engine that retries activities, and
// the status code alone is the wrong thing to decide on. A 409 for "already
// exists" will never resolve itself; a 409 for "another call with this key is
// still running" will in a second. A 500 from the system behind the API is
// usually worth retrying, except when that system answers 500 to a request it
// will refuse for ever — which Grafana 13 does for every organisation delete.
// Only the code that made the call knows which is which, so it says so here.
package problem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Error is a refusal or a failure the caller is told about. Handlers return it
// as an ordinary error; Write renders it.
type Error struct {
	Status    int
	Code      string
	Title     string
	Detail    string
	Retryable bool

	// Current is what exists already, on already_exists: the caller decides
	// whether it is theirs, and needs to see it to decide.
	Current any
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code
}

func newf(status int, code, title string, retryable bool, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Title: title, Retryable: retryable,
		Detail: fmt.Sprintf(format, args...)}
}

// Invalid is a request that can never succeed as it is.
func Invalid(format string, args ...any) *Error {
	return newf(http.StatusBadRequest, "invalid_request", "Invalid request", false, format, args...)
}

// NotFound is a resource that does not exist.
func NotFound(format string, args ...any) *Error {
	return newf(http.StatusNotFound, "not_found", "Not found", false, format, args...)
}

// AlreadyExists carries what is there, so the caller can tell its own earlier
// attempt from somebody else's object.
func AlreadyExists(current any, format string, args ...any) *Error {
	e := newf(http.StatusConflict, "already_exists", "Already exists", false, format, args...)
	e.Current = current
	return e
}

// NotEmpty refuses a delete that would take other things with it.
func NotEmpty(format string, args ...any) *Error {
	return newf(http.StatusConflict, "folder_not_empty", "Not empty", false, format, args...)
}

// Protected refuses to change what something other than this API owns — the
// tenant's own team, which onboarding created and every private resource
// depends on.
func Protected(format string, args ...any) *Error {
	return newf(http.StatusConflict, "protected", "Protected", false, format, args...)
}

// NotOnboarded is a tenant whose prerequisites do not exist on the platform.
func NotOnboarded(format string, args ...any) *Error {
	return newf(http.StatusUnprocessableEntity, "tenant_not_onboarded", "Tenant not onboarded", false, format, args...)
}

// KeyReused is an idempotency key sent again with a different request.
func KeyReused(format string, args ...any) *Error {
	return newf(http.StatusUnprocessableEntity, "idempotency_key_reused", "Idempotency key reused", false, format, args...)
}

// InProgress is an idempotency key whose first call has not finished. Worth
// retrying: it will have, soon.
func InProgress(format string, args ...any) *Error {
	return newf(http.StatusConflict, "in_progress", "In progress", true, format, args...)
}

// UpstreamAuth is the system behind the API refusing *our* credentials. Not the
// caller's fault, and not something a retry fixes: the configuration does.
func UpstreamAuth(format string, args ...any) *Error {
	return newf(http.StatusBadGateway, "upstream_auth", "Upstream refused our credentials", false, format, args...)
}

// Upstream is the system behind the API failing; retryable says whether that
// failure is the kind that passes.
func Upstream(retryable bool, format string, args ...any) *Error {
	return newf(http.StatusBadGateway, "upstream_error", "Upstream error", retryable, format, args...)
}

// Unavailable is the system behind the API not answering at all.
func Unavailable(format string, args ...any) *Error {
	return newf(http.StatusServiceUnavailable, "upstream_unavailable", "Upstream unavailable", true, format, args...)
}

type ctxKey struct{}

// WithCorrelationID stores the id every response and log line of this call
// carries.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// CorrelationID returns it, or empty.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

type body struct {
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Detail        string `json:"detail,omitempty"`
	Code          string `json:"code"`
	Retryable     bool   `json:"retryable"`
	CorrelationID string `json:"correlation_id,omitempty"`
	Current       any    `json:"current,omitempty"`
}

// Write renders err. Anything that is not an *Error is a bug on this side: it
// is logged in full and reported without its text, which may carry what the
// caller has no business seeing.
func Write(w http.ResponseWriter, r *http.Request, err error) {
	var p *Error
	if !errors.As(err, &p) {
		slog.ErrorContext(r.Context(), "unexpected error", "err", err, "path", r.URL.Path,
			"correlation_id", CorrelationID(r.Context()))
		p = &Error{Status: http.StatusInternalServerError, Code: "internal_error",
			Title: "Internal error", Detail: "an unexpected error; the log carries it under this correlation id"}
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(body{
		Title: p.Title, Status: p.Status, Detail: p.Detail, Code: p.Code, Retryable: p.Retryable,
		CorrelationID: CorrelationID(r.Context()), Current: p.Current,
	})
}

// Abort renders err and stops the gin chain: what a middleware does when it
// refuses a request.
func Abort(c *gin.Context, err error) {
	Write(c.Writer, c.Request, err)
	c.Abort()
}
