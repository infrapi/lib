package server

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

func gateAllow(c *gin.Context) { c.Next() }

func gateDeny(c *gin.Context) {
	AbortError(c, infraerrors.New(http.StatusUnauthorized, "test.gateDeny", "no"))
}

// TestWithAuth_gatesOnlyRoutesAddedAfter mirrors the contracts behaviour:
// the middleware applies to routes the service adds, never to the engine's
// own /-/ endpoints registered before it.
func TestWithAuth_gatesOnlyRoutesAddedAfter(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t), WithAuth(gateDeny))
	require.NoError(t, err)

	app.GET("/widgets", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := get(t, app, "/-/healthz")
	assert.Equal(t, http.StatusOK, w.Code)

	w = get(t, app, "/widgets")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestWithAuth_allows(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t), WithAuth(gateAllow))
	require.NoError(t, err)

	app.GET("/widgets", func(c *gin.Context) { c.Status(http.StatusOK) })

	assert.Equal(t, http.StatusOK, get(t, app, "/widgets").Code)
}

func TestWithAuth_refusesNilMiddleware(t *testing.T) {
	_, err := NewServerGin(testAppConfig(t), WithAuth(nil))
	assert.Error(t, err)
}

func TestWithAuth_none(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t))
	require.NoError(t, err)
	app.GET("/widgets", func(c *gin.Context) { c.Status(http.StatusOK) })
	assert.Equal(t, http.StatusOK, get(t, app, "/widgets").Code)
}
