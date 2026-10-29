package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
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
	ordersV2 := OpenAPISpec{Name: "orders v2", Path: "orders/v2.json",
		Content: []byte(`{"openapi":"3.0.3","info":{"title":"Orders","version":"2"},"paths":{}}`)}
	app, err := NewServerGin(testAppConfig(t), WithOpenAPI(ordersV1, ordersV2))
	require.NoError(t, err)

	w := get(t, app, "/-/openapi/orders/v1.yaml")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/yaml", w.Header().Get("Content-Type"))
	assert.Equal(t, string(ordersV1.Content), w.Body.String())

	w = get(t, app, "/-/openapi/orders/v2.json")
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	page, err := io.ReadAll(get(t, app, DocsPath).Body)
	require.NoError(t, err)
	body := string(page)
	assert.Contains(t, body, `urls: [{"name":"orders v1","url":"/-/openapi/orders/v1.yaml"},`+
		`{"name":"orders v2","url":"/-/openapi/orders/v2.json"},`+
		`{"name":"operations","url":"/-/openapi.json"}]`)
	assert.Contains(t, body, "swagger-ui-standalone-preset.js")
	assert.Contains(t, body, `layout: "StandaloneLayout"`)
	assert.NotContains(t, body, `url: "`+OpenAPIPath+`"`)
}

// Beside the service's documents, the generated specification describes
// the operational endpoints only, those the service adds under /-/ too: the
// service's documents are the only description of its API.
func TestWithOpenAPI_operations(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t), WithOpenAPI(ordersV1), WithOperations(Operation{
		Method: http.MethodGet, Path: "/-/workers/:id", Summary: "A worker",
		Responses: map[string]Response{"200": {Description: "The worker", ContentType: "application/json",
			Schema: map[string]any{"type": "object"}}},
	}))
	require.NoError(t, err)
	app.GET("/orders/:id", func(c *gin.Context) {})
	app.GET("/-/workers", func(c *gin.Context) {})
	app.GET("/-/workers/:id", func(c *gin.Context) {})

	w := get(t, app, OpenAPIPath)
	require.Equal(t, http.StatusOK, w.Code)
	var spec openAPI
	require.NoError(t, json.NewDecoder(w.Body).Decode(&spec))
	assert.Contains(t, spec.Info.Title, OperationsName)
	assert.Contains(t, spec.Paths, HealthzPath)
	assert.Contains(t, spec.Paths, ReadyzPath)
	assert.Contains(t, spec.Paths, MetricsPath)
	assert.Contains(t, spec.Paths, "/-/metadata")
	assert.Contains(t, spec.Paths, "/-/workers")
	assert.NotContains(t, spec.Paths, "/orders/{id}")
	assert.NotContains(t, spec.Paths, "/-/openapi/orders/v1.yaml")

	// The engine's endpoints are documented, and those the service
	// documents; the others are listed only.
	readyz := spec.Paths[ReadyzPath]["get"]
	assert.NotEmpty(t, readyz.Summary)
	assert.Contains(t, readyz.Responses["503"].Content, "application/problem+json")
	assert.Contains(t, spec.Paths[MetricsPath]["get"].Responses["200"].Content, "text/plain")
	worker := spec.Paths["/-/workers/{id}"]["get"]
	assert.Equal(t, "A worker", worker.Summary)
	assert.Len(t, worker.Parameters, 1)
	assert.Equal(t, map[string]any{"type": "object"}, worker.Responses["200"].Content["application/json"].Schema)
	assert.Contains(t, spec.Paths["/-/workers"]["get"].Responses, "default")
}

func TestWithOperations_refuses(t *testing.T) {
	ok := map[string]Response{"200": {Description: "ok"}}
	for name, op := range map[string]Operation{
		"no method":     {Path: "/-/x", Responses: ok},
		"not under /-/": {Method: http.MethodGet, Path: "/x", Responses: ok},
		"no response":   {Method: http.MethodGet, Path: "/-/x"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewServerGin(testAppConfig(t), WithOperations(op))
			assert.Error(t, err)
		})
	}
}

func TestWithOpenAPI_refusesTheOperationsName(t *testing.T) {
	spec := ordersV1
	spec.Name = OperationsName
	_, err := NewServerGin(testAppConfig(t), WithOpenAPI(spec))
	assert.ErrorContains(t, err, "operational endpoints")
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
