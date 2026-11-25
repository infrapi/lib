package server

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/events"
	"github.com/infrapi/lib/pkg/identity"
)

// Option adjusts the engine NewServerGin builds.
type Option func(*options) error

type options struct {
	specs      []OpenAPISpec
	operations []Operation
	readiness  []ReadinessCheck
	auth       gin.HandlerFunc
	// authorizationServer is WithProtectedResource's
	authorizationServer func(ctx context.Context) (string, error)
	// resource is WithResource's: "" for AppUrl and the root
	resource string
	// identity is WithIdentity's keys
	identity []*identity.Key
	// events is WithEvents' recorder, and its hooks
	events     *events.Recorder
	eventHooks []EventHooks
}

// OpenAPISpec is an OpenAPI document a service maintains itself — a contract
// written by hand, with its schemas, rather than the route table the engine
// can describe on its own.
type OpenAPISpec struct {
	// Name labels the document in Swagger UI's selector, e.g. "orders v1".
	Name string
	// Path places it under OpenAPIPrefix, e.g. "orders/v1.yaml" is served at
	// <root>/-/openapi/orders/v1.yaml. Its extension sets the content type: .json,
	// or .yaml / .yml.
	Path string
	// Content is the document itself.
	Content []byte
	// Root is the engine's root the document describes the API of, served
	// and offered under it: the primary root when empty.
	Root string
}

// WithOpenAPI serves the service's own OpenAPI documents under OpenAPIPrefix
// and points the documentation page at them, in a selector — one per major
// version, say. Beside them, the generated specification at OpenAPIPath keeps
// to the operational endpoints, those under <root>/-/, and comes last in the
// selector as OperationsName: the API has one description, not two.
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
			if s.Name == OperationsName || OpenAPIPrefix+s.Path == OperationsPath {
				return fmt.Errorf("server: OpenAPI document %q at %q: the operational endpoints' name or path", s.Name, s.Path)
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

// WithAuth adds mw as global middleware in front of every route but the
// operational ones - typically [pkg/auth.Client.Middleware]. Like
// WithOpenAPI's contract validation, it never reaches a route under a root's
// -/, the engine's (metadata, health, metrics, OpenAPI, docs) or the
// service's, nor /.well-known/api.json, told by the route pattern: they
// answer without a token. To ask for one on an operational route of its own,
// the service puts the middleware on that route.
func WithAuth(mw gin.HandlerFunc) Option {
	return func(o *options) error {
		if mw == nil {
			return fmt.Errorf("server: WithAuth middleware is nil")
		}
		o.auth = mw
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

// WithProtectedResource serves each root's protected resource metadata (RFC
// 9728), for an OAuth client, an MCP client say, to find the authorization
// server that issues its tokens: at <root>/-/oauth-protected-resource, which
// a 401's WWW-Authenticate header names (auth.ClientOptions.PublicURL), and
// at /.well-known/oauth-protected-resource<root>. Its resource is AppUrl and
// the root; authorizationServer names the issuer, Kayra's
// (auth.Client.AuthorizationServer), read at each request. Without
// authentication, like every operational endpoint.
func WithProtectedResource(authorizationServer func(ctx context.Context) (string, error)) Option {
	return func(o *options) error {
		if authorizationServer == nil {
			return fmt.Errorf("server: WithProtectedResource needs an authorization server")
		}
		o.authorizationServer = authorizationServer
		return nil
	}
}

// WithResource sets the resource (RFC 8707) WithProtectedResource's metadata
// names, which a client asks its tokens for: a URL every root is under,
// rather than AppUrl and the root. The gateway's, so that a token is good for
// every API behind it: a service that passes its caller's token on to other
// APIs (an MCP gateway, ulgen) names it, and its callers get a token those
// APIs accept too. The token is still checked against the request's root
// (auth.ClientOptions.PublicURL), which such a token covers.
func WithResource(url string) Option {
	return func(o *options) error {
		o.resource = strings.TrimRight(url, "/")
		return nil
	}
}

// WithIdentity serves the public half of keys, the service's identity
// keys, at each root's -/jwks.json: Kayra's reconciler reads them there when
// the API's registration says identity.jwks_from: spec, and trusts what the
// first one signs (auth.ClientCredentials). Every key is published; list
// the new key after the old one while rotating. Without authentication,
// like every operational endpoint. No keys, no route.
func WithIdentity(keys ...*identity.Key) Option {
	return func(o *options) error {
		o.identity = append(o.identity, keys...)
		return nil
	}
}
