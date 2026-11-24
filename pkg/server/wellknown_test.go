package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func index(t *testing.T, w interface{ Bytes() []byte }) APIIndex {
	t.Helper()
	var idx APIIndex
	require.NoError(t, json.Unmarshal(w.Bytes(), &idx))
	return idx
}

// The index lists every root, the primary first, with its group's name,
// API, version and URLs; without a token, however the auth is set.
func TestAPIIndex(t *testing.T) {
	app := multiRootEngine(t, WithAuth(gateDeny))

	w := get(t, app, APIIndexPath)
	require.Equal(t, http.StatusOK, w.Code, "no token needed")
	idx := index(t, w.Body)
	assert.Equal(t, "test-app", idx.App)
	require.Len(t, idx.Groups, 2)

	assert.Equal(t, APIIndexGroup{
		Name: "system", API: "system.infrapi.io", Version: "v1", Root: systemRoot,
		URL: "http://test.example.com" + systemRoot, Primary: true,
		Links: APIIndexLinks{
			Docs:     "http://test.example.com" + systemRoot + "/-/docs",
			OpenAPI:  "http://test.example.com" + systemRoot + "/-/openapi.json",
			Metrics:  "http://test.example.com" + systemRoot + "/-/metrics",
			Healthz:  "http://test.example.com" + systemRoot + "/-/healthz",
			Readyz:   "http://test.example.com" + systemRoot + "/-/readyz",
			Metadata: "http://test.example.com" + systemRoot + "/-/metadata",
		},
	}, idx.Groups[0])
	assert.Equal(t, "catalog", idx.Groups[1].Name)
	assert.Equal(t, "catalog.infrapi.io", idx.Groups[1].API)
	assert.False(t, idx.Groups[1].Primary)
}

// Without AppUrl, the URLs are the paths; a root outside the /apis layout
// is described from its last segments.
func TestAPIIndex_outsideTheLayout(t *testing.T) {
	cfg := testAppConfig(t)
	cfg.AppUrl = ""
	app, err := NewServerGin(cfg, []string{"/grafana/v1", "/orgs"})
	require.NoError(t, err)

	idx := index(t, get(t, app, APIIndexPath).Body)
	require.Len(t, idx.Groups, 2)
	assert.Equal(t, "grafana", idx.Groups[0].Name)
	assert.Equal(t, "", idx.Groups[0].API)
	assert.Equal(t, "v1", idx.Groups[0].Version)
	assert.Equal(t, "/grafana/v1", idx.Groups[0].URL)
	assert.Equal(t, "/grafana/v1/-/docs", idx.Groups[0].Links.Docs)
	assert.Equal(t, APIIndexGroup{Name: "orgs"}, APIIndexGroup{Name: idx.Groups[1].Name, API: idx.Groups[1].API, Version: idx.Groups[1].Version})
}

// A group's MCP endpoint, once the service mounts it: linked in the index,
// and documented in its specification.
func TestAPIIndex_MCP(t *testing.T) {
	app := multiRootEngine(t)
	idx := index(t, get(t, app, APIIndexPath).Body)
	assert.Empty(t, idx.Groups[0].Links.MCP, "none mounted")

	app.Any(systemRoot+MCPPath, func(c *gin.Context) { c.Status(http.StatusOK) })
	idx = index(t, get(t, app, APIIndexPath).Body)
	assert.Equal(t, "http://test.example.com"+systemRoot+"/-/mcp", idx.Groups[0].Links.MCP)
	assert.Empty(t, idx.Groups[1].Links.MCP, "the other group has none")

	var spec map[string]any
	require.NoError(t, json.Unmarshal(get(t, app, systemRoot+OpenAPIPath).Body.Bytes(), &spec))
	post := spec["paths"].(map[string]any)[systemRoot+MCPPath].(map[string]any)["post"].(map[string]any)
	assert.Contains(t, post["summary"], "MCP tools")
}
