package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ordersV1 = OpenAPISpec{Name: "orders v1", Path: "orders/v1.yaml",
	Content: []byte("openapi: 3.0.3\ninfo: {title: Orders, version: \"1\"}\npaths: {}\n")}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// The service's own documents are served as they are, and the documentation
// page offers them in a selector instead of the generated specification.
func TestWithOpenAPI_servesTheServicesDocuments(t *testing.T) {
	ordersV2 := OpenAPISpec{Name: "orders v2", Path: "orders/v2.json", Content: []byte(`{"openapi":"3.1.0"}`)}
	app, err := NewServerGin(testAppConfig(t), WithOpenAPI(ordersV1, ordersV2))
	require.NoError(t, err)

	w := get(t, app, "/-/openapi/orders/v1.yaml")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/yaml", w.Header().Get("Content-Type"))
	assert.Equal(t, string(ordersV1.Content), w.Body.String())

	w = get(t, app, "/-/openapi/orders/v2.json")
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	page, _ := io.ReadAll(get(t, app, DocsPath).Body)
	body := string(page)
	assert.Contains(t, body, `urls: [{"name":"orders v1","url":"/-/openapi/orders/v1.yaml"},`+
		`{"name":"orders v2","url":"/-/openapi/orders/v2.json"}]`)
	assert.Contains(t, body, "swagger-ui-standalone-preset.js")
	assert.Contains(t, body, `layout: "StandaloneLayout"`)
	assert.NotContains(t, body, `url: "`+OpenAPIPath+`"`)

	// The service's documents are the only description of its API: the
	// generated one is gone.
	assert.Equal(t, http.StatusNotFound, get(t, app, OpenAPIPath).Code)
}

// Without documents, nothing changes: the page reads the generated one.
func TestWithOpenAPI_none(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t), WithOpenAPI())
	require.NoError(t, err)
	body := get(t, app, DocsPath).Body.String()
	assert.Contains(t, body, `url: "`+OpenAPIPath+`"`)
	assert.Equal(t, http.StatusOK, get(t, app, OpenAPIPath).Code)
	assert.NotContains(t, body, "standalone-preset")
}

// A document name reaches a script: it cannot close it.
func TestWithOpenAPI_escapesNames(t *testing.T) {
	spec := ordersV1
	spec.Name = "</script><script>alert(1)</script>"
	app, err := NewServerGin(testAppConfig(t), WithOpenAPI(spec))
	require.NoError(t, err)
	assert.NotContains(t, get(t, app, DocsPath).Body.String(), "<script>alert(1)</script>")
}

func TestWithOpenAPI_refusesBadDocuments(t *testing.T) {
	for name, spec := range map[string]OpenAPISpec{
		"no name":       {Path: "a.yaml", Content: []byte("x")},
		"absolute path": {Name: "a", Path: "/a.yaml", Content: []byte("x")},
		"escaping path": {Name: "a", Path: "../a.yaml", Content: []byte("x")},
		"unclean path":  {Name: "a", Path: "a//b.yaml", Content: []byte("x")},
		"no known type": {Name: "a", Path: "a.txt", Content: []byte("x")},
		"empty":         {Name: "a", Path: "a.yaml"},
	} {
		_, err := NewServerGin(testAppConfig(t), WithOpenAPI(spec))
		assert.Error(t, err, name)
	}

	_, err := NewServerGin(testAppConfig(t), WithOpenAPI(ordersV1, ordersV1))
	assert.ErrorContains(t, err, "share")
	assert.True(t, strings.Contains(err.Error(), "orders v1"))
}
