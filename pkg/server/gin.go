// Package server builds the gin engine shared by InfraPI services.
package server

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/infrapi/lib/pkg/config"
)

// NewServerGin creates a gin.Engine pre-configured from the supplied AppConfig:
// CORS, recovery, logging, request metrics, and the endpoints every InfraPI
// service exposes without asking for them, under each of its roots.
//
// roots are where the service's APIs live, one per API group it serves:
// group.Root(), /apis/compute.infrapi.io/v1 (see pkg/api). The first is the
// primary root. Each root gets its own endpoints, about the routes under it,
// so a gateway routing /apis/<group>/<version>/* reaches them along with
// that group's API:
//
//	GET <root>/-/metadata      the application identity
//	GET <root>/-/healthz       200 while the process serves
//	GET <root>/-/readyz        200 when ready for traffic, 503 when a WithReadiness check fails
//	GET <root>/-/metrics       Prometheus exposition format, the root's requests
//	GET <root>/-/openapi.json  OpenAPI 3.1, generated from the root's routes
//	GET <root>/-/docs          Swagger UI reading that specification
//
// A route under no root (an OAuth2 endpoint, a discovery document) counts as
// the primary root's: in its metrics and its specification. MetadataPath,
// HealthzPath and the other paths are relative to a root.
//
// At its own root, the engine lists its roots, each with its group's name,
// API, version, URL and endpoints:
//
//	GET /.well-known/api.json  APIIndex
//
// These endpoints, the index included, and every route the service adds
// under a root's -/, answer without authentication and are never validated
// against a contract: WithAuth and WithOpenAPI leave them out by route
// pattern, in whatever order the routes are registered. A service that wants
// a token on one of its own operational routes puts the middleware on it:
//
//	engine.Group(root).GET("/-/workers", client.Middleware(), workers)
//
// Access logs and recovered panics go to gin.DefaultWriter, as gin's text
// lines unless cfg.AppLogFormat is "json", which writes one JSON object per
// line instead.
//
// Every request carries its X-Request-Id (RequestIDHeader) in its context,
// for the problem documents, and has an OpenTelemetry span with the tracer
// provider pkg/tracing sets up. Gin runs in release mode unless the
// service logs at debug or trace.
//
// Options adjust it: WithOpenAPI documents the service with its own OpenAPI
// documents rather than the generated specification, and validates every
// request against them before any handler runs; WithReadiness adds the checks
// <root>/-/readyz runs; WithAuth puts an authentication middleware - typically
// pkg/auth.Client.Middleware - in front of every other route.
func NewServerGin(cfg *config.AppConfig, roots []string, opts ...Option) (*gin.Engine, error) {
	rs, err := newRoots(roots)
	if err != nil {
		return nil, err
	}
	var o options
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return nil, err
		}
	}
	specs, err := rs.specs(o.specs)
	if err != nil {
		return nil, err
	}

	ginMode(cfg.AppLogLevel)
	app := gin.New()
	app.Use(logMiddlewares(cfg.AppLogFormat, gin.DefaultWriter)...)
	app.Use(spans(cfg.AppName, rs), correlate)

	if err := app.SetTrustedProxies(cfg.AppTrustedProxies); err != nil {
		return nil, err
	}

	corsConfig := cors.DefaultConfig()
	corsConfig.AllowOrigins = cfg.AppCorsAllowOrigins
	corsConfig.AllowMethods = cfg.AppCorsAllowMethods
	corsConfig.AllowHeaders = cfg.AppCorsAllowHeaders
	corsConfig.ExposeHeaders = cfg.AppCorsExposeHeaders
	corsConfig.AllowCredentials = cfg.AppCorsAllowCredentials
	corsConfig.MaxAge = time.Duration(cfg.AppCorsMaxAge) * time.Hour

	app.Use(cors.New(corsConfig))

	m := newRootMetrics(cfg.AppFqdn, rs)
	app.Use(m.middleware())

	// The engine's own endpoints, per root. open is their route patterns:
	// what the contracts and the authentication below let through.
	open := map[string]bool{}
	for _, root := range rs.list {
		ops := app.Group(root)
		ops.GET(MetadataPath, func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"appName":     cfg.AppName,
				"appPlatform": cfg.AppPlatform,
				"appRegion":   cfg.AppRegion,
				"appLocation": cfg.AppLocation,
				"appFqdn":     cfg.AppFqdn,
				"appUrl":      cfg.AppUrl,
			})
		})
		ops.GET(MetricsPath, m.handler(root))
		healthHandlers(ops, o.readiness)
		if err := openAPIHandlers(app, ops, rs, root, cfg, specs[root], o.operations); err != nil {
			return nil, err
		}
		for _, p := range []string{MetadataPath, MetricsPath, HealthzPath, ReadyzPath, OpenAPIPath, DocsPath} {
			open[root+p] = true
		}
		if o.authorizationServer != nil {
			h := protectedResourceHandler(strings.TrimRight(cfg.AppUrl, "/")+root, o.authorizationServer)
			ops.GET(ProtectedResourcePath, h)
			app.GET(WellKnownProtectedResource+root, h)
			open[root+ProtectedResourcePath] = true
			open[WellKnownProtectedResource+root] = true
		}
		for _, s := range specs[root] {
			open[root+OpenAPIPrefix+s.Path] = true
		}
		if len(specs[root]) > 0 {
			open[root+OperationsPath] = true
		}
	}

	// The index of the roots, at the engine's own root.
	app.GET(APIIndexPath, apiIndexHandler(apiIndex(cfg, rs)))
	open[APIIndexPath] = true

	// The contracts supplied with WithOpenAPI are enforced, not only
	// documented, on every route but the operational ones.
	if len(o.specs) > 0 {
		contracts, err := newContractRouter(o.specs)
		if err != nil {
			return nil, err
		}
		app.Use(unless(open, rs, contracts.validate))
	}

	// Same for the authentication: never on an operational route, the
	// engine's or one the service adds under a root's -/, told by its
	// route pattern. A service that wants a token on one of its own puts
	// the middleware on that route.
	if o.auth != nil {
		app.Use(unless(open, rs, o.auth))
	}

	return app, nil
}

// unless runs mw on every request but the operational ones: whose route
// pattern is in open, or under a root's -/. The pattern is gin's, matched
// before any middleware runs: a request cannot make a route of its own look
// like an operational one.
func unless(open map[string]bool, rs roots, mw gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if p := c.FullPath(); open[p] || rs.operational(p) {
			c.Next()
			return
		}
		mw(c)
	}
}

// ginMode switches gin to release mode — no debug line per route at start,
// no warning — unless the service logs at debug or trace, or the mode was
// chosen already: GIN_MODE, or gin.SetMode before NewServerGin (a test's
// gin.TestMode).
func ginMode(level log.Level) {
	if os.Getenv(gin.EnvGinMode) != "" || gin.Mode() != gin.DebugMode || level >= log.DebugLevel {
		return
	}
	gin.SetMode(gin.ReleaseMode)
}

// roots are the service's roots, the primary first.
type roots struct {
	list []string
}

// newRoots refuses a root the engine's own endpoints could not go under: an
// absolute, clean path, not / itself, without parameters, and no root inside
// another.
func newRoots(list []string) (roots, error) {
	if len(list) == 0 {
		return roots{}, fmt.Errorf("server: no root: give one per API group, like /apis/compute.infrapi.io/v1")
	}
	for i, root := range list {
		if !strings.HasPrefix(root, "/") || root == "/" || path.Clean(root) != root ||
			strings.ContainsAny(root, ":*{}") || slices.Contains(strings.Split(root, "/"), "-") {
			return roots{}, fmt.Errorf("server: root %q must be an absolute, clean path without parameters, like /apis/compute.infrapi.io/v1", root)
		}
		for _, other := range list[:i] {
			if root == other || strings.HasPrefix(root, other+"/") || strings.HasPrefix(other, root+"/") {
				return roots{}, fmt.Errorf("server: roots %q and %q overlap", other, root)
			}
		}
	}
	return roots{list: slices.Clone(list)}, nil
}

// owner is the root a path belongs to: the one it is under, the primary
// when it is under none.
func (rs roots) owner(p string) string {
	for _, root := range rs.list {
		if p == root || strings.HasPrefix(p, root+"/") {
			return root
		}
	}
	return rs.list[0]
}

// operational tells whether a path is under a root's -/.
func (rs roots) operational(p string) bool {
	for _, root := range rs.list {
		if strings.HasPrefix(p, root+"/-/") {
			return true
		}
	}
	return false
}

// specs sorts the documents supplied with WithOpenAPI by root: a document
// without one is the primary's.
func (rs roots) specs(specs []OpenAPISpec) (map[string][]OpenAPISpec, error) {
	out := map[string][]OpenAPISpec{}
	for _, s := range specs {
		root := s.Root
		if root == "" {
			root = rs.list[0]
		}
		if !slices.Contains(rs.list, root) {
			return nil, fmt.Errorf("server: OpenAPI document %q: root %q is none of the engine's %v", s.Name, s.Root, rs.list)
		}
		out[root] = append(out[root], s)
	}
	return out, nil
}
