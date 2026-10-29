package server

import (
	"fmt"
	"net/http"
	"strings"
)

// Operation documents a route under /-/ in the specification of the
// operational endpoints, which otherwise knows of it only its method and
// path, from the route table.
type Operation struct {
	// Method and Path are the route's, as registered: GET, /-/workers/:id.
	Method string
	Path   string
	// Summary says what the route is for, in a line.
	Summary string
	// Responses by status code, e.g. "200".
	Responses map[string]Response
}

// Response is one answer of an Operation.
type Response struct {
	Description string
	// ContentType of the body, if any: application/json, text/plain, ...
	ContentType string
	// Schema of the body, a JSON Schema as OpenAPI 3.1 takes it, e.g.
	// {"type": "object", "properties": {...}}.
	Schema map[string]any
}

// WithOperations documents routes the service adds under /-/, beside the
// engine's own, which are documented already. A route left out is still
// listed, undocumented.
func WithOperations(ops ...Operation) Option {
	return func(o *options) error {
		for _, op := range ops {
			if op.Method == "" || !strings.HasPrefix(op.Path, "/-/") {
				return fmt.Errorf("server: operation %s %q: a method and a path under /-/ are required", op.Method, op.Path)
			}
			if len(op.Responses) == 0 {
				return fmt.Errorf("server: operation %s %s documents no response", op.Method, op.Path)
			}
		}
		o.operations = append(o.operations, ops...)
		return nil
	}
}

// problemSchema is the application/problem+json document WriteError answers
// with.
var problemSchema = map[string]any{
	"type":     "object",
	"required": []string{"title", "status", "code", "retryable"},
	"properties": map[string]any{
		"title":          map[string]any{"type": "string"},
		"status":         map[string]any{"type": "integer"},
		"detail":         map[string]any{"type": "string"},
		"code":           map[string]any{"type": "string"},
		"retryable":      map[string]any{"type": "boolean"},
		"correlation_id": map[string]any{"type": "string"},
	},
}

var statusSchema = map[string]any{
	"type":       "object",
	"properties": map[string]any{"status": map[string]any{"type": "string", "const": "ok"}},
}

func stringProperties(names ...string) map[string]any {
	props := map[string]any{}
	for _, n := range names {
		props[n] = map[string]any{"type": "string"}
	}
	return props
}

// engineOperations documents the engine's own operational endpoints.
var engineOperations = []Operation{
	{
		Method: http.MethodGet, Path: HealthzPath, Summary: "Whether the process serves, for liveness checks",
		Responses: map[string]Response{
			"200": {Description: "The process serves", ContentType: "application/json", Schema: statusSchema},
		},
	},
	{
		Method: http.MethodGet, Path: ReadyzPath, Summary: "Whether the service is ready for traffic, per its readiness checks",
		Responses: map[string]Response{
			"200": {Description: "Ready", ContentType: "application/json", Schema: statusSchema},
			"503": {Description: "Not ready: starting, stopping, or a readiness check failed",
				ContentType: "application/problem+json", Schema: problemSchema},
		},
	},
	{
		Method: http.MethodGet, Path: MetricsPath, Summary: "Prometheus metrics",
		Responses: map[string]Response{
			"200": {Description: "The Prometheus text exposition format", ContentType: "text/plain"},
		},
	},
	{
		Method: http.MethodGet, Path: "/-/metadata", Summary: "Where this instance runs",
		Responses: map[string]Response{
			"200": {Description: "The service's name and location", ContentType: "application/json", Schema: map[string]any{
				"type":       "object",
				"properties": stringProperties("appName", "appPlatform", "appRegion", "appLocation", "appFqdn", "appUrl"),
			}},
		},
	},
	{
		Method: http.MethodGet, Path: OpenAPIPath, Summary: "This specification",
		Responses: map[string]Response{
			"200": {Description: "OpenAPI 3.1", ContentType: "application/json"},
		},
	},
	{
		Method: http.MethodGet, Path: DocsPath, Summary: "Swagger UI on the service's specifications",
		Responses: map[string]Response{
			"200": {Description: "An HTML page", ContentType: "text/html"},
		},
	},
}
