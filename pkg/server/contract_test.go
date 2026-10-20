package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var bookmarksV1 = OpenAPISpec{Name: "bookmarks v1", Path: "bookmarks/v1.yaml", Content: []byte(`openapi: 3.0.3
info: {title: Bookmarks, version: "1"}
paths:
  /bookmarks/v1/tenants/{tenant}/bookmarks:
    parameters:
      - {name: tenant, in: path, required: true, schema: {type: string, pattern: "^[a-z0-9-]{1,40}$"}}
    get:
      parameters:
        - {name: limit, in: query, schema: {type: integer, minimum: 1}}
      responses: {"200": {description: ok}}
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              additionalProperties: false
              required: [url]
              properties: {url: {type: string}}
      responses: {"201": {description: created}}
security: [{apikey: []}]
components:
  securitySchemes:
    apikey: {type: apiKey, in: header, name: apikey}
`)}

func call(t *testing.T, h http.Handler, method, path, body string, header map[string]string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	if strings.Contains(w.Header().Get("Content-Type"), "json") && w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
	}
	return w.Code, out
}

// A contract supplied with WithOpenAPI is enforced: a request off it is a 400
// problem document before any handler runs; a request it does not describe
// goes through.
func TestWithOpenAPI_validatesRequests(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t), WithOpenAPI(bookmarksV1))
	require.NoError(t, err)
	reached := 0
	handler := func(c *gin.Context) { reached++; c.Status(http.StatusNoContent) }
	app.GET("/bookmarks/v1/tenants/:tenant/bookmarks", handler)
	app.POST("/bookmarks/v1/tenants/:tenant/bookmarks", handler)
	app.GET("/own", handler)

	base := "/bookmarks/v1/tenants/acme/bookmarks"
	for name, c := range map[string]struct {
		method, path, body string
		code               int
	}{
		"a valid read":             {http.MethodGet, base + "?limit=5", "", http.StatusNoContent},
		"a valid write":            {http.MethodPost, base, `{"url":"https://x.io"}`, http.StatusNoContent},
		"a field off the contract": {http.MethodPost, base, `{"url":"https://x.io","colour":"red"}`, http.StatusBadRequest},
		"a missing field":          {http.MethodPost, base, `{}`, http.StatusBadRequest},
		"a path off the pattern":   {http.MethodGet, "/bookmarks/v1/tenants/ACME/bookmarks", "", http.StatusBadRequest},
		"a query off the schema":   {http.MethodGet, base + "?limit=0", "", http.StatusBadRequest},
		"a route of the service's": {http.MethodGet, "/own", "", http.StatusNoContent},
	} {
		code, body := call(t, app, c.method, c.path, c.body, map[string]string{RequestIDHeader: "req-1"})
		assert.Equal(t, c.code, code, name)
		if c.code == http.StatusBadRequest {
			assert.Equal(t, "invalid_request", body["code"], name)
			assert.Equal(t, "req-1", body["correlation_id"], name)
			assert.NotContains(t, body["detail"], "Schema:", name+": the reason, not the schema")
		}
	}
	assert.Equal(t, 3, reached, "the handlers saw only what the contract allows, and the service's own route")
}

// A document that is not valid OpenAPI stops the service at start: it would
// be served and documented, and not enforced.
func TestWithOpenAPI_refusesAnInvalidContract(t *testing.T) {
	_, err := NewServerGin(testAppConfig(t), WithOpenAPI(OpenAPISpec{Name: "broken", Path: "broken.yaml",
		Content: []byte("openapi: 3.0.3\ninfo: {}\npaths: {}\n")}))
	assert.ErrorContains(t, err, `OpenAPI document "broken"`)
}
