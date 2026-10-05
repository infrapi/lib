package idempotency

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

// handler counts how often it really runs, and answers with the body it got.
func handler(runs *atomic.Int32, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runs.Add(1)
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(b)
	})
}

// chain puts the middleware in front of h the way the server does: a gin
// route, with h a plain handler behind it.
func chain(store Store, h http.Handler) http.Handler {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Any("/tenants/:tenant/*path", Middleware(store, func(c *gin.Context) string { return c.Param("tenant") }),
		gin.WrapH(h))
	return e
}

func send(t *testing.T, h http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		r.Header.Set(Header, key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const path = "/tenants/acme/folders"

// The retried activity: same key, same body — the first answer, and nothing run again.
func TestAReplayRunsNothing(t *testing.T) {
	var runs atomic.Int32
	h := chain(NewMemory(), handler(&runs, http.StatusCreated))

	first := send(t, h, http.MethodPost, path, "wf-1", `{"name":"a"}`)
	again := send(t, h, http.MethodPost, path, "wf-1", `{"name":"a"}`)

	if runs.Load() != 1 {
		t.Errorf("the handler ran %d times, want 1", runs.Load())
	}
	if again.Code != first.Code || again.Body.String() != first.Body.String() {
		t.Errorf("replay = %d %q, want %d %q", again.Code, again.Body, first.Code, first.Body)
	}
	if again.Header().Get(ReplayedHeader) != "true" {
		t.Error("the replay is not marked as one")
	}
	if first.Header().Get(ReplayedHeader) != "" {
		t.Error("the first answer is marked as a replay")
	}
}

// A key reused for another request is refused rather than answered with the
// response to a question that was not asked.
func TestAReusedKeyIsRefused(t *testing.T) {
	var runs atomic.Int32
	h := chain(NewMemory(), handler(&runs, http.StatusCreated))

	send(t, h, http.MethodPost, path, "wf-1", `{"name":"a"}`)
	w := send(t, h, http.MethodPost, path, "wf-1", `{"name":"b"}`)

	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "idempotency_key_reused") {
		t.Errorf("reused key = %d %s", w.Code, w.Body)
	}
	if runs.Load() != 1 {
		t.Errorf("the handler ran %d times, want 1", runs.Load())
	}
}

// A failure that may pass is forgotten, so the retry it invites actually runs.
func TestAServerErrorIsNotRemembered(t *testing.T) {
	var runs atomic.Int32
	h := chain(NewMemory(), handler(&runs, http.StatusBadGateway))

	send(t, h, http.MethodPost, path, "wf-1", `{"name":"a"}`)
	send(t, h, http.MethodPost, path, "wf-1", `{"name":"a"}`)
	if runs.Load() != 2 {
		t.Errorf("the handler ran %d times, want 2 — a 5xx must not be replayed", runs.Load())
	}
}

// Keys belong to a tenant: two tenants may pick the same one.
func TestKeysAreScopedToTheTenant(t *testing.T) {
	var runs atomic.Int32
	h := chain(NewMemory(), handler(&runs, http.StatusCreated))

	send(t, h, http.MethodPost, "/tenants/acme/folders", "k", `{"name":"a"}`)
	send(t, h, http.MethodPost, "/tenants/other/folders", "k", `{"name":"a"}`)
	if runs.Load() != 2 {
		t.Errorf("the handler ran %d times, want 2", runs.Load())
	}
}

// Without a key, nothing is remembered.
func TestNoKeyPassesThrough(t *testing.T) {
	var runs atomic.Int32
	h := chain(NewMemory(), handler(&runs, http.StatusCreated))
	send(t, h, http.MethodPost, path, "", `{"name":"a"}`)
	send(t, h, http.MethodPost, path, "", `{"name":"a"}`)
	if runs.Load() != 2 {
		t.Errorf("the handler ran %d times, want 2", runs.Load())
	}
}
