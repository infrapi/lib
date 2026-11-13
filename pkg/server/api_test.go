package server

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/api"
)

func TestAPIGroup(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t))
	require.NoError(t, err)

	vms, err := APIGroup(app, api.Group{Name: "compute", Domain: "infrapi.io", Version: "v1"})
	require.NoError(t, err)
	vms.GET("/vm/:id", func(c *gin.Context) {
		c.String(http.StatusOK, "%s/%s", Namespace(c), c.Param("id"))
	})

	w := get(t, app, "/apis/compute.infrapi.io/v1/namespaces/core/vm/123")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "core/123", w.Body.String())

	w = get(t, app, "/apis/compute.infrapi.io/v1/namespaces/Core/vm/123")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "not a lower case DNS label")
}

func TestAPIGroup_invalid(t *testing.T) {
	_, err := APIGroup(gin.New(), api.Group{Name: "compute", Domain: "infrapi.io"})
	assert.Error(t, err)
}
