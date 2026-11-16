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

const (
	catalogRoot = "/apis/catalog.infrapi.io/v1"
	systemRoot  = "/apis/system.infrapi.io/v1"
)

// multiRootEngine serves two groups, as Kayra does, with routes under each
// and one under neither.
func multiRootEngine(t *testing.T, opts ...Option) *gin.Engine {
	t.Helper()
	app, err := NewServerGin(testAppConfig(t), []string{systemRoot, catalogRoot}, opts...)
	require.NoError(t, err)
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	app.GET(systemRoot+"/namespaces", ok)
	app.GET(catalogRoot+"/apiregistrations", ok)
	app.GET("/oauth2/userinfo", ok)
	return app
}

func paths(t *testing.T, app *gin.Engine, root string) []string {
	t.Helper()
	w := get(t, app, root+OpenAPIPath)
	require.Equal(t, http.StatusOK, w.Code)
	var spec openAPI
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &spec))
	var out []string
	for p := range spec.Paths {
		out = append(out, p)
	}
	return out
}

// Each group has its own endpoints, about its own routes; a route under no
// group is the primary's.
func TestRoots_perGroup(t *testing.T) {
	app := multiRootEngine(t)

	system, catalog := paths(t, app, systemRoot), paths(t, app, catalogRoot)
	assert.Contains(t, system, systemRoot+"/namespaces")
	assert.Contains(t, system, systemRoot+"/-/healthz")
	assert.Contains(t, system, "/oauth2/userinfo", "under no root: the primary's")
	assert.NotContains(t, system, catalogRoot+"/apiregistrations")
	assert.NotContains(t, system, catalogRoot+"/-/healthz")

	assert.Contains(t, catalog, catalogRoot+"/apiregistrations")
	assert.Contains(t, catalog, catalogRoot+"/-/healthz")
	assert.NotContains(t, catalog, systemRoot+"/namespaces")
	assert.NotContains(t, catalog, "/oauth2/userinfo")

	page := get(t, app, catalogRoot+DocsPath).Body.String()
	assert.Contains(t, page, `url: "`+catalogRoot+OpenAPIPath+`"`)

	for _, p := range []string{MetadataPath, HealthzPath, ReadyzPath} {
		assert.Equal(t, http.StatusOK, get(t, app, catalogRoot+p).Code, p)
	}
	assert.Equal(t, http.StatusNotFound, get(t, app, HealthzPath).Code, "nothing under a bare /-/")
}

// A group's metrics count its routes' requests only.
func TestRoots_metricsPerGroup(t *testing.T) {
	app := multiRootEngine(t)
	get(t, app, catalogRoot+"/apiregistrations")
	get(t, app, "/oauth2/userinfo")

	catalog := get(t, app, catalogRoot+MetricsPath).Body.String()
	system := get(t, app, systemRoot+MetricsPath).Body.String()
	assert.Contains(t, catalog, `path="`+catalogRoot+`/apiregistrations"`)
	assert.NotContains(t, system, `path="`+catalogRoot+`/apiregistrations"`)
	assert.Contains(t, system, `path="/oauth2/userinfo"`, "under no root: the primary's")
	assert.NotContains(t, catalog, `path="/oauth2/userinfo"`)
}

// A document supplied with WithOpenAPI is served and offered under its root
// only, the primary's when it names none; a root the engine does not have
// is refused.
func TestRoots_specs(t *testing.T) {
	doc := []byte(`{"openapi":"3.0.3","info":{"title":"c","version":"1"},"paths":{}}`)
	app := multiRootEngine(t, WithOpenAPI(
		OpenAPISpec{Name: "catalog v1", Path: "catalog/v1.json", Content: doc, Root: catalogRoot},
		OpenAPISpec{Name: "system v1", Path: "system/v1.json", Content: doc},
	))
	assert.Equal(t, http.StatusOK, get(t, app, catalogRoot+OpenAPIPrefix+"catalog/v1.json").Code)
	assert.Equal(t, http.StatusNotFound, get(t, app, systemRoot+OpenAPIPrefix+"catalog/v1.json").Code)
	assert.Equal(t, http.StatusOK, get(t, app, systemRoot+OpenAPIPrefix+"system/v1.json").Code)
	assert.Contains(t, get(t, app, catalogRoot+DocsPath).Body.String(), "catalog v1")
	assert.NotContains(t, get(t, app, catalogRoot+DocsPath).Body.String(), "system v1")

	_, err := NewServerGin(testAppConfig(t), []string{systemRoot}, WithOpenAPI(
		OpenAPISpec{Name: "x", Path: "x.json", Content: doc, Root: catalogRoot}))
	assert.ErrorContains(t, err, "none of the engine's")
}

// Nothing under a root's -/ asks for a token, the engine's endpoints, the
// documents of WithOpenAPI and the service's own operational routes
// included, however late they are added; the rest does. A service puts the
// middleware on an operational route that needs a token.
func TestRoots_engineEndpointsAreOpen(t *testing.T) {
	doc := []byte(`{"openapi":"3.0.3","info":{"title":"c","version":"1"},"paths":{}}`)
	app := multiRootEngine(t, WithAuth(gateDeny), WithOpenAPI(
		OpenAPISpec{Name: "catalog v1", Path: "catalog/v1.json", Content: doc, Root: catalogRoot}))
	// the service's own, after NewServerGin: open under -/, unless it says so
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	app.GET(catalogRoot+"/-/workers", ok)
	app.GET(catalogRoot+"/-/secrets", gateDeny, ok)

	for _, root := range []string{systemRoot, catalogRoot} {
		for _, p := range []string{MetadataPath, HealthzPath, ReadyzPath, MetricsPath, OpenAPIPath, DocsPath} {
			assert.Equal(t, http.StatusOK, get(t, app, root+p).Code, root+p)
		}
	}
	assert.Equal(t, http.StatusOK, get(t, app, catalogRoot+OpenAPIPrefix+"catalog/v1.json").Code)
	assert.Equal(t, http.StatusOK, get(t, app, catalogRoot+"/-/workers").Code, "the service's own, open")
	assert.Equal(t, http.StatusOK, get(t, app, APIIndexPath).Code)

	for _, p := range []string{catalogRoot + "/-/secrets", catalogRoot + "/apiregistrations", "/oauth2/userinfo"} {
		assert.Equal(t, http.StatusUnauthorized, get(t, app, p).Code, p)
	}
	// a path made to look like an open one is matched as what it is
	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest(http.MethodGet, catalogRoot+"/apiregistrations?x="+strings.Repeat("/-/healthz", 2), nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
