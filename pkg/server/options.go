package server

import (
	"fmt"
	"path"
	"strings"
)

// Option adjusts the engine NewServerGin builds.
type Option func(*options) error

type options struct {
	specs     []OpenAPISpec
	readiness []ReadinessCheck
}

// OpenAPISpec is an OpenAPI document a service maintains itself — a contract
// written by hand, with its schemas, rather than the route table the engine
// can describe on its own.
type OpenAPISpec struct {
	// Name labels the document in Swagger UI's selector, e.g. "orders v1".
	Name string
	// Path places it under OpenAPIPrefix, e.g. "orders/v1.yaml" is served at
	// /-/openapi/orders/v1.yaml. Its extension sets the content type: .json,
	// or .yaml / .yml.
	Path string
	// Content is the document itself.
	Content []byte
}

// WithOpenAPI serves the service's own OpenAPI documents under OpenAPIPrefix
// and points the documentation page at them, with a selector when there are
// several — one per major version, say. They replace the generated
// specification: OpenAPIPath is no longer served.
func WithOpenAPI(specs ...OpenAPISpec) Option {
	return func(o *options) error {
		names, paths := map[string]bool{}, map[string]bool{}
		for _, s := range specs {
			if s.Name == "" {
				return fmt.Errorf("server: OpenAPI document %q has no name", s.Path)
			}
			if s.Path == "" || strings.HasPrefix(s.Path, "/") || path.Clean(s.Path) != s.Path ||
				strings.HasPrefix(s.Path, "..") {
				return fmt.Errorf("server: OpenAPI document %q: path %q must be relative and clean, like orders/v1.yaml",
					s.Name, s.Path)
			}
			if specContentType(s.Path) == "" {
				return fmt.Errorf("server: OpenAPI document %q: path %q must end in .json, .yaml or .yml", s.Name, s.Path)
			}
			if len(s.Content) == 0 {
				return fmt.Errorf("server: OpenAPI document %q is empty", s.Name)
			}
			if names[s.Name] || paths[s.Path] {
				return fmt.Errorf("server: two OpenAPI documents share the name %q or the path %q", s.Name, s.Path)
			}
			names[s.Name], paths[s.Path] = true, true
		}
		o.specs = append(o.specs, specs...)
		return nil
	}
}

// specContentType is the media type of a document, from its extension.
func specContentType(p string) string {
	switch path.Ext(p) {
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	}
	return ""
}
