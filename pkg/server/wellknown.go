package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/api"
	"github.com/infrapi/lib/pkg/config"
)

// APIIndexPath is where the engine lists its API groups, at its own root,
// not under a group's: where a caller who knows only the service's address
// starts.
const APIIndexPath = "/.well-known/api.json"

// APIIndex is the document served at APIIndexPath: the service, and each
// of its roots.
type APIIndex struct {
	// App is the service's name, AppName.
	App string `json:"app"`
	// Groups are the service's roots, the primary first.
	Groups []APIIndexGroup `json:"groups"`
}

// APIIndexGroup is one root: what it serves and where.
type APIIndexGroup struct {
	// Name is the group's short name, the first label of API: compute.
	// For a root outside the /apis/<group>/<version> layout, the segment
	// before its version.
	Name string `json:"name"`
	// API is the group's full name, compute.infrapi.io, as Kayra
	// registers it; empty for a root outside the /apis layout.
	API string `json:"api"`
	// Version is the version served, v1; empty when the root does not end
	// in one.
	Version string `json:"version"`
	// Root is the path the group is served under, /apis/compute.infrapi.io/v1.
	Root string `json:"root"`
	// URL is Root on AppUrl, or Root alone when AppUrl is not set.
	URL string `json:"url"`
	// Primary is the primary root's: routes under no root are its.
	Primary bool `json:"primary"`
	// Links are the group's own endpoints, as URLs.
	Links APIIndexLinks `json:"links"`
}

// APIIndexLinks are a group's own endpoints.
type APIIndexLinks struct {
	Docs     string `json:"docs"`
	OpenAPI  string `json:"openapi"`
	Metrics  string `json:"metrics"`
	Healthz  string `json:"healthz"`
	Readyz   string `json:"readyz"`
	Metadata string `json:"metadata"`
	// MCP is the group's MCP endpoint (MCPPath), when the service has
	// mounted one.
	MCP string `json:"mcp,omitempty"`
}

// apiIndex describes rs, built once: the roots do not change.
func apiIndex(cfg *config.AppConfig, rs roots) APIIndex {
	base := strings.TrimRight(cfg.AppUrl, "/")
	index := APIIndex{App: cfg.AppName, Groups: make([]APIIndexGroup, 0, len(rs.list))}
	for i, root := range rs.list {
		g := describeRoot(root)
		g.Root, g.URL, g.Primary = root, base+root, i == 0
		g.Links = APIIndexLinks{
			Docs:     g.URL + DocsPath,
			OpenAPI:  g.URL + OpenAPIPath,
			Metrics:  g.URL + MetricsPath,
			Healthz:  g.URL + HealthzPath,
			Readyz:   g.URL + ReadyzPath,
			Metadata: g.URL + MetadataPath,
		}
		index.Groups = append(index.Groups, g)
	}
	return index
}

// describeRoot reads a root's name, API and version: from the
// /apis/<group>/<version> layout of pkg/api, or, outside it, from its last
// segments, /grafana/v1.
func describeRoot(root string) APIIndexGroup {
	segs := strings.Split(strings.Trim(root, "/"), "/")
	if len(segs) == 3 && "/"+segs[0] == api.Prefix {
		if g := (api.Group{Name: segs[1], Version: segs[2]}); g.Validate() == nil {
			name, _, _ := strings.Cut(g.Name, ".")
			return APIIndexGroup{Name: name, API: g.Name, Version: g.Version}
		}
	}
	last := segs[len(segs)-1]
	if api.ValidateVersion(last) == nil && len(segs) > 1 {
		return APIIndexGroup{Name: segs[len(segs)-2], Version: last}
	}
	return APIIndexGroup{Name: last}
}

// apiIndexHandler serves the index, each group's MCP endpoint linked when
// app has the route: the service mounts it after NewServerGin, so it is
// looked for at each request.
func apiIndexHandler(app *gin.Engine, index APIIndex) gin.HandlerFunc {
	return func(c *gin.Context) {
		mounted := map[string]bool{}
		for _, r := range app.Routes() {
			if root, ok := strings.CutSuffix(r.Path, MCPPath); ok {
				mounted[root] = true
			}
		}
		out := index
		out.Groups = make([]APIIndexGroup, len(index.Groups))
		for i, g := range index.Groups {
			if mounted[g.Root] {
				g.Links.MCP = g.URL + MCPPath
			}
			out.Groups[i] = g
		}
		c.JSON(http.StatusOK, out)
	}
}
