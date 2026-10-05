package server

import (
	"bytes"
	"html/template"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/config"
)

const (
	// OpenAPIPath is where the engine serves the generated specification, when
	// the service supplies no document of its own with WithOpenAPI.
	OpenAPIPath = "/-/openapi.json"

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
	Description string `json:"description"`
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
// request, so routes the service adds after NewServerGin are included.
func openAPISpec(app *gin.Engine, cfg *config.AppConfig) *openAPI {
	spec := &openAPI{
		OpenAPI: "3.1.0",
		Info: openAPIInfo{
			Title:       cfg.AppName,
			Version:     buildVersion(),
			Description: "Generated from the route table of " + cfg.AppFqdn + ".",
		},
		Paths: map[string]map[string]operation{},
	}

	if cfg.AppUrl != "" {
		spec.Servers = []openAPIServer{{URL: cfg.AppUrl}}
	}

	for _, route := range app.Routes() {
		path, params := openAPIPath(route.Path)

		if _, ok := spec.Paths[path]; !ok {
			spec.Paths[path] = map[string]operation{}
		}

		spec.Paths[path][strings.ToLower(route.Method)] = operation{
			Parameters: params,
			Responses: map[string]response{
				"default": {Description: "Undocumented: this specification is generated from the route table."},
			},
		}
	}

	return spec
}

// docsPage renders the Swagger UI page once: on the generated specification,
// or on the documents the service supplied, offered in a selector.
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
func newDocsPage(cfg *config.AppConfig, specs []OpenAPISpec) []byte {
	var urls []docsURL
	for _, s := range specs {
		urls = append(urls, docsURL{Name: s.Name, URL: OpenAPIPrefix + s.Path})
	}

	var page bytes.Buffer
	// the template is a constant and its data has every key it names, so
	// execution has nothing left to fail on
	_ = docsPage.Execute(&page, map[string]any{
		"Title":     cfg.AppName,
		"SpecPath":  OpenAPIPath,
		"SwaggerUI": swaggerUI,
		"URLs":      urls,
	})

	return page.Bytes()
}

// openAPIHandlers registers the specifications and the documentation page.
func openAPIHandlers(app *gin.Engine, cfg *config.AppConfig, specs []OpenAPISpec) {
	page := newDocsPage(cfg, specs)

	// The generated specification only stands in for a contract the service
	// does not have: next to one, it would be a second, poorer description of
	// the same API.
	if len(specs) == 0 {
		app.GET(OpenAPIPath, func(c *gin.Context) {
			c.JSON(http.StatusOK, openAPISpec(app, cfg))
		})
	}

	for _, s := range specs {
		content, contentType := s.Content, specContentType(s.Path)
		app.GET(OpenAPIPrefix+s.Path, func(c *gin.Context) {
			c.Data(http.StatusOK, contentType, content)
		})
	}

	app.GET(DocsPath, func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", page)
	})
}
