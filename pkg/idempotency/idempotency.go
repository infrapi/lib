// Package idempotency makes a write safe to repeat.
//
// The caller is a workflow engine that retries activities: a create whose
// response was lost is sent again, and must not create a second folder. The
// same Idempotency-Key with the same request replays the first response and
// does nothing; the same key with a different request is refused, because
// replaying an answer to a question that was not asked is worse than an error.
//
// Only answers that will not change are remembered. A 5xx is forgotten, so the
// retry it invites actually runs again.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	infraerrors "github.com/infrapi/lib/pkg/errors"
	"github.com/infrapi/lib/pkg/server"
)

// Header is what callers send.
const Header = "Idempotency-Key"

// ReplayedHeader marks a response that is a replay.
const ReplayedHeader = "Idempotent-Replayed"

// Outcome is what Begin decided.
type Outcome int

const (
	// Proceed: the key is new, or its earlier attempt was abandoned; run.
	Proceed Outcome = iota
	// Replay: the same request already completed; send what it answered.
	Replay
	// Mismatch: the key was used for a different request.
	Mismatch
	// Busy: the first call with this key has not finished.
	Busy
)

// Record is a remembered response.
type Record struct {
	Code        int
	ContentType string
	Body        []byte
}

// Store keeps keys. Scoped to the tenant: two tenants may use the same key.
type Store interface {
	Begin(ctx context.Context, scope, key, hash string) (Outcome, *Record, error)
	Finish(ctx context.Context, scope, key string, rec Record) error
	Abandon(ctx context.Context, scope, key string) error
}

// Scope says whose a key is: the same key under two scopes names two
// different requests. Nil is DefaultScope.
type Scope func(*gin.Context) string

// DefaultScope keys a request to the caller the gateway authenticated
// (X-Consumer-Username): two callers may pick the same key. A service whose
// keys belong to something narrower — a tenant — passes its own Scope.
func DefaultScope(c *gin.Context) string { return c.GetHeader("X-Consumer-Username") }

// DefaultMaxBody is the largest body Middleware reads when given none: the
// gateway's own default.
const DefaultMaxBody = 1 << 20

// Middleware applies idempotency to writes that carry a key. Reads, and writes
// without one, pass through untouched. The key and whether the answer was a
// replay go into the request's access log line.
//
// The body is read whole to be hashed, up to maxBody bytes (DefaultMaxBody
// when not positive). A larger one is refused with 413 rather than cut: a
// truncated body would reach the handler as if it were the request.
func Middleware(store Store, scopeOf Scope, maxBody int64) gin.HandlerFunc {
	if scopeOf == nil {
		scopeOf = DefaultScope
	}
	if maxBody <= 0 {
		maxBody = DefaultMaxBody
	}
	return func(c *gin.Context) {
		r := c.Request
		key := r.Header.Get(Header)
		if key == "" || r.Method == http.MethodGet || r.Method == http.MethodHead {
			c.Next()
			return
		}
		server.LogField(c, "idempotency_key", key)

		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil {
			server.AbortError(c, infraerrors.Invalid("the body could not be read: %v", err))
			return
		}
		if int64(len(body)) > maxBody {
			server.AbortError(c, infraerrors.TooLarge("the body is over %d bytes", maxBody))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		scope := scopeOf(c)
		hash := hashOf(r.Method, r.URL.Path, r.URL.RawQuery, body)

		outcome, rec, err := store.Begin(r.Context(), scope, key, hash)
		if err != nil {
			server.AbortError(c, err)
			return
		}
		switch outcome {
		case Replay:
			server.LogField(c, "replayed", true)
			c.Header(ReplayedHeader, "true")
			c.Data(rec.Code, rec.ContentType, rec.Body)
			c.Abort()
			return
		case Mismatch:
			server.AbortError(c, keyReused(
				"Idempotency-Key %q was already used for a different request; use a new key for a new request", key))
			return
		case Busy:
			server.AbortError(c, inProgress(
				"a request with Idempotency-Key %q is still running; retry once it has finished", key))
			return
		}

		rw := &recorder{ResponseWriter: c.Writer}
		c.Writer = rw
		c.Next()
		c.Writer = rw.ResponseWriter

		// Background: the request's own context may be cancelled the moment
		// the response is written, and the record must survive that.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		if code := rw.Status(); code >= 500 {
			err = store.Abandon(ctx, scope, key)
		} else {
			err = store.Finish(ctx, scope, key, Record{
				Code: code, ContentType: rw.Header().Get("Content-Type"), Body: rw.buf.Bytes(),
			})
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "idempotency record not saved: a retry will run again",
				"key", key, "scope", scope, "err", err, "correlation_id", server.CorrelationID(r.Context()))
		}
	}
}

// keyReused is an idempotency key sent again with a different request.
func keyReused(format string, args ...any) *infraerrors.Error {
	return infraerrors.New(http.StatusUnprocessableEntity, "idempotency.Middleware", format, args...).
		WithTitle("Idempotency key reused").WithSlug("idempotency_key_reused").WithRetryable(false)
}

// inProgress is an idempotency key whose first call has not finished. Worth
// retrying: it will have, soon.
func inProgress(format string, args ...any) *infraerrors.Error {
	return infraerrors.New(http.StatusConflict, "idempotency.Middleware", format, args...).
		WithTitle("In progress").WithSlug("in_progress").WithRetryable(true)
}

// hashOf identifies the request a key was used for: what it did, to what, and
// with which body.
func hashOf(method, path, query string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method + " " + path + "?" + query + "\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// recorder keeps a copy of the body while passing it through; the status is
// the gin writer's own.
type recorder struct {
	gin.ResponseWriter
	buf bytes.Buffer
}

func (r *recorder) Write(b []byte) (int, error) {
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

func (r *recorder) WriteString(s string) (int, error) {
	r.buf.WriteString(s)
	return r.ResponseWriter.WriteString(s)
}
