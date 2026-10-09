package app

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The backend's ids become the API's: an object's, each object's of a list,
// and nothing else.
func TestSwapIDs(t *testing.T) {
	to := func(backend string) (string, error) { return "public-" + backend, nil }
	for in, want := range map[string]string{
		`{"id":"bm-1","title":"x"}`:           `{"id":"public-bm-1","title":"x"}`,
		`[{"id":"bm-1"},{"id":"bm-2","n":3}]`: `[{"id":"public-bm-1"},{"id":"public-bm-2","n":3}]`,
		`{"title":"no id"}`:                   `{"title":"no id"}`,
		`{"id":42}`:                           `{"id":42}`,
		``:                                    ``,
		`not json`:                            `not json`,
	} {
		out, err := swapIDs([]byte(in), to)
		require.NoError(t, err, in)
		if in == "" || in == "not json" {
			assert.Equal(t, in, string(out))
			continue
		}
		assert.JSONEq(t, want, string(out), in)
	}
	_, err := swapIDs([]byte(`{"id":"bm-1"}`), func(string) (string, error) { return "", errors.New("db down") })
	assert.ErrorContains(t, err, "db down")
}

// The handler reads the backend's id from gin's parameters and from the URL.
func TestSetPathParam(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/bookmarks/v1/tenants/acme/bookmarks/2d1f6c1e-uuid", nil)
	c.Params = gin.Params{{Key: "tenant", Value: "acme"}, {Key: "id", Value: "2d1f6c1e-uuid"}}
	setPathParam(c, 6, "id", "bm-1")
	assert.Equal(t, "bm-1", c.Param("id"))
	assert.Equal(t, "/bookmarks/v1/tenants/acme/bookmarks/bm-1", c.Request.URL.Path)
}
