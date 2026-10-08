package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/app/idempotency"
)

// A write's identity: random without an Idempotency-Key, derived from it with
// one, so that a retry creates the same thing again.
func TestWriteIdentity(t *testing.T) {
	req := func(key string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/orders/v1/tenants/acme/orders", nil)
		if key != "" {
			r.Header.Set(idempotency.Header, key)
		}
		return r
	}
	a, b := newWriteIdentity(req("")), newWriteIdentity(req(""))
	assert.NotEqual(t, a.id, b.id)
	assert.Regexp(t, `^[a-z2-7]{26}$`, a.id, "lower-case, usable in an id")
	assert.WithinDuration(t, time.Now(), a.at, time.Second)

	k1, k2 := newWriteIdentity(req("wf-1")), newWriteIdentity(req("wf-1"))
	assert.Equal(t, k1.id, k2.id, "the same key, the same identity")
	assert.NotEqual(t, k1.id, newWriteIdentity(req("wf-2")).id)
}

// Handlers read the identity from their context: on every write, none on a read.
func TestWriteIDInHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg, err := loadConfig(Spec{Name: "orders-api"}, writeFile(t, required))
	require.NoError(t, err)
	cl, err := newCluster(cfg) // disabled: identity only
	require.NoError(t, err)
	f := newFanout(cl, "", "http", 0)
	var id string
	var at time.Time
	e := gin.New()
	e.Any("/x", func(c *gin.Context) { f.handle(c, "") }, func(c *gin.Context) {
		id, at = WriteID(c.Request.Context()), WriteTime(c.Request.Context())
		c.Status(http.StatusNoContent)
	})

	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/x", nil))
	assert.NotEmpty(t, id)
	assert.False(t, at.IsZero())

	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	assert.Empty(t, id, "a read is not a write")
	assert.True(t, at.IsZero())
	assert.Empty(t, WriteID(context.Background()))
}
