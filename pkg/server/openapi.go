package server

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/config"
)

const (
	// OpenAPIPath is where the engine serves the specification it generates
	// from its route table: of every route, or, beside the documents supplied
	// with WithOpenAPI, of its operational endpoints only.
	OpenAPIPath = "/-/openapi.json"

	// OperationsName labels the operational endpoints' specification in the
	// docs page's selector, beside the documents supplied with WithOpenAPI.
	OperationsName = "operations"

	// DocsPath is where the engine serves the Swagger UI reading that
	// specification, or the ones supplied with WithOpenAPI.
	DocsPath = "/-/docs"

	// OpenAPIPrefix is where the documents supplied with WithOpenAPI are
	// served, each at its own path below it.
	OpenAPIPrefix = "/-/openapi/"

	// swaggerUI is the pinned Swagger UI release the docs page loads.
	swaggerUI = "5.32.14"
)

// openAPI is the subset of the OpenAPI 3.1 document that can be told from a gin
// route table: paths, methods and path parameters. Bodies, query parameters and
// schemas are not in the table, so they are not invented here.
type openAPI struct {
	OpenAPI string                          `json:"openapi"`
	Info    openAPIInfo                     `json:"info"`
	Servers []openAPIServer                 `json:"servers,omitempty"`
	Paths   map[string]map[string]operation `json:"paths"`
}

type openAPIInfo struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

type openAPIServer struct {
	URL string `json:"url"`
}

type operation struct {
	Summary    string              `json:"summary,omitempty"`
	Parameters []parameter         `json:"parameters,omitempty"`
	Responses  map[string]response `json:"responses"`
}

type parameter struct {
	Name     string            `json:"name"`
	In       string            `json:"in"`
	Required bool              `json:"required"`
	Schema   map[string]string `json:"schema"`
}

type response struct {
	Description string               `json:"description"`
	Content     map[string]mediaType `json:"content,omitempty"`
}

type mediaType struct {
	Schema map[string]any `json:"schema,omitempty"`
}

// defaultVersion stands in when the binary carries no version information at
// all. A module build reports one, down to "(devel)" for an untagged build.
const defaultVersion = "0.0.0"

// buildVersion is the version of the binary embedding the library, the closest
// thing to an API version a generated document can claim.
func buildVersion() string {
	info, _ := debug.ReadBuildInfo()
	return moduleVersion(info)
}

func moduleVersion(info *debug.BuildInfo) string {
	if info == nil || info.Main.Version == "" {
		return defaultVersion
	}
	return info.Main.Version
}

// openAPIPath rewrites a gin route into an OpenAPI path template and returns the
// parameters it carries: /users/:id becomes /users/{id}, /files/*path becomes
// /files/{path}.
func openAPIPath(route string) (string, []parameter) {
	var params []parameter

	segments := strings.Split(route, "/")
	for i, segment := range segments {
		if len(segment) < 2 || (segment[0] != ':' && segment[0] != '*') {
			continue
		}

		name := segment[1:]
		segments[i] = "{" + name + "}"
		params = append(params, parameter{
			Name:     name,
			In:       "path",
			Required: true,
			Schema:   map[string]string{"type": "string"},
		})
	}

	return strings.Join(segments, "/"), params
}

// openAPISpec describes the routes currently registered on app. It is built per
// request, so routes the service adds after NewServerGin are included. With
// operations, it describes only those under /-/, the engine's own and any the
// service adds there, but the documents served under OpenAPIPrefix: the API
// is in those documents. A route in documented, by method and path, carries
// its summary and responses; the others an undocumented default.
func openAPISpec(app *gin.Engine, cfg *config.AppConfig, operations bool, documented map[string]Operation) *openAPI {
	spec := &openAPI{
		OpenAPI: "3.1.0",
		Info: openAPIInfo{
			Title:       cfg.AppName,
			Version:     buildVersion(),
			Description: "Generated from the route table of " + cfg.AppFqdn + ".",
		},
		Paths: map[string]map[string]operation{},
	}
	if operations {
		spec.Info.Title = cfg.AppName + " " + OperationsName
		spec.Info.Description = "The operational endpoints of " + cfg.AppFqdn + ", generated from its route table."
	}

	if cfg.AppUrl != "" {
		spec.Servers = []openAPIServer{{URL: cfg.AppUrl}}
	}

	for _, route := range app.Routes() {
		if operations && (!strings.HasPrefix(route.Path, "/-/") || strings.HasPrefix(route.Path, OpenAPIPrefix)) {
			continue
		}
		path, params := openAPIPath(route.Path)

		if _, ok := spec.Paths[path]; !ok {
			spec.Paths[path] = map[string]operation{}
		}

		op := operation{
			Parameters: params,
			Responses: map[string]response{
				"default": {Description: "Undocumented: this specification is generated from the route table."},
			},
		}
		if doc, ok := documented[route.Method+" "+route.Path]; ok {
			op.Summary, op.Responses = doc.Summary, map[string]response{}
			for status, r := range doc.Responses {
				res := response{Description: r.Description}
				if r.ContentType != "" {
					res.Content = map[string]mediaType{r.ContentType: {Schema: r.Schema}}
				}
				op.Responses[status] = res
			}
		}
		spec.Paths[path][strings.ToLower(route.Method)] = op
	}

	return spec
}

// docsPage renders the Swagger UI page once: on the generated specification,
// or on the documents the service supplied and the operational endpoints',
// offered in a selector.
var docsPage = template.Must(template.New("docs").Parse(
	`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} API</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@{{.SwaggerUI}}/swagger-ui.css">
</head>
<body>
<div id="swagger-ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@{{.SwaggerUI}}/swagger-ui-bundle.js" crossorigin></script>
{{- if .URLs}}
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@{{.SwaggerUI}}/swagger-ui-standalone-preset.js" crossorigin></script>
{{- end}}
<script>
window.onload = () => {
{{- if .URLs}}
  SwaggerUIBundle({urls: {{.URLs}}, dom_id: "#swagger-ui", deepLinking: true,
    presets: [SwaggerUIBundle.presets.apis, SwaggerUIStandalonePreset], layout: "StandaloneLayout"});
{{- else}}
  SwaggerUIBundle({url: {{.SpecPath}}, dom_id: "#swagger-ui", deepLinking: true});
{{- end}}
};
</script>
</body>
</html>
`))

// docsURL is one entry of Swagger UI's selector.
type docsURL struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// newDocsPage renders the documentation page served at DocsPath.
func newDocsPage(cfg *config.AppConfig, specs []OpenAPISpec) ([]byte, error) {
	var urls []docsURL
	for _, s := range specs {
		urls = append(urls, docsURL{Name: s.Name, URL: OpenAPIPrefix + s.Path})
	}
	if len(urls) > 0 {
		urls = append(urls, docsURL{Name: OperationsName, URL: OpenAPIPath})
	}

	var page bytes.Buffer
	if err := docsPage.Execute(&page, map[string]any{
		"Title":     cfg.AppName,
		"SpecPath":  OpenAPIPath,
		"SwaggerUI": swaggerUI,
		"URLs":      urls,
	}); err != nil {
		return nil, fmt.Errorf("server: render the docs page: %w", err)
	}
	return page.Bytes(), nil
}

// openAPIHandlers registers the specifications and the documentation page.
func openAPIHandlers(app *gin.Engine, cfg *config.AppConfig, specs []OpenAPISpec, ops []Operation) error {
	page, err := newDocsPage(cfg, specs)
	if err != nil {
		return err
	}

	// Beside the service's documents, the generated specification keeps to
	// the operational endpoints: describing the API too, it would be a
	// second, poorer description of it.
	operations := len(specs) > 0
	documented := map[string]Operation{}
	for _, op := range append(slices.Clone(engineOperations), ops...) {
		documented[op.Method+" "+op.Path] = op
	}
	app.GET(OpenAPIPath, func(c *gin.Context) {
		c.JSON(http.StatusOK, openAPISpec(app, cfg, operations, documented))
	})

	for _, s := range specs {
		content, contentType := s.Content, specContentType(s.Path)
		app.GET(OpenAPIPrefix+s.Path, func(c *gin.Context) {
			c.Data(http.StatusOK, contentType, content)
		})
	}

	app.GET(DocsPath, func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", page)
	})
	return nil
}
