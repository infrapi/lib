package server

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
