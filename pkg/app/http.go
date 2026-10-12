package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/gin-gonic/gin"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/server"
	ginmiddleware "github.com/oapi-codegen/gin-middleware"

	"github.com/infrapi/lib/pkg/app/idempotency"
	"github.com/infrapi/lib/pkg/app/internal/relay"
	"github.com/infrapi/lib/pkg/app/problem"
)

// infrapiContract documents the routes the library serves besides the
// service's: /-/docs shows it with the service's own.
//
//go:embed infrapi.yaml
var infrapiContract []byte

var infrapiSpec = server.OpenAPISpec{Name: "infrapi (replica routes)", Path: "infrapi.yaml", Content: infrapiContract}

// Health paths, next to the engine's own /-/metrics and /-/metadata.
const (
	HealthPath = "/-/healthz"
	ReadyPath  = "/-/readyz"
)

// Headers the relay sets: on a write's answer, the replica that performed it;
// on a relayed request, the replica that relayed it.
const (
	LeaderHeader    = relay.LeaderHeader
	ForwardedHeader = relay.ForwardedHeader
)

// readiness is what /-/readyz answers: 503 while joining the cluster or
// draining, 200 otherwise.
type readiness struct{ joined, draining atomic.Bool }

// newEngine is the whole HTTP side: the library's engine with the service's
// versions on it, behind the API's middlewares, and the operational pages. It
// returns each version's prefix, which the gateway routes on.
func newEngine(ctx context.Context, spec Spec, cfg *loaded, rt *Runtime, versions []Version, store idempotency.Store,
	db *sql.DB,
	ready *readiness) (*gin.Engine, []string, error) {
	if len(versions) == 0 {
		return nil, nil, errors.New("app: the service has no version")
	}
	specs := make([]server.OpenAPISpec, 0, len(versions))
	docs := make([]*openapi3.T, 0, len(versions))
	prefixes := make([]string, 0, len(versions))
	for _, v := range versions {
		if v.Routes == nil {
			return nil, nil, fmt.Errorf("app: version %q has no routes", v.OpenAPI.Name)
		}
		// What every contract shares is added: the contract is served,
		// validated and documented complete.
		content, err := complete(v.OpenAPI.Content)
		if err != nil {
			return nil, nil, fmt.Errorf("app: version %q: %w", v.OpenAPI.Name, err)
		}
		v.OpenAPI.Content = content
		doc, err := openapi3.NewLoader().LoadFromData(content)
		if err != nil {
			return nil, nil, fmt.Errorf("app: version %q: %w", v.OpenAPI.Name, err)
		}
		if err := doc.Validate(openapi3.NewLoader().Context); err != nil {
			return nil, nil, fmt.Errorf("app: version %q: %w", v.OpenAPI.Name, err)
		}
		prefix := v.Prefix
		if prefix == "" {
			if prefix = commonPrefix(doc); prefix == "/" {
				return nil, nil, fmt.Errorf("app: version %q: its paths share no prefix; set Version.Prefix", v.OpenAPI.Name)
			}
		}
		specs, docs, prefixes = append(specs, v.OpenAPI), append(docs, doc), append(prefixes, prefix)
	}

	gin.SetMode(gin.ReleaseMode)
	// The library's own routes are documented next to the service's.
	engine, err := server.NewServerGin(cfg.app, server.WithOpenAPI(append(slices.Clone(specs), infrapiSpec)...))
	if err != nil {
		return nil, nil, err
	}
	// Handlers that take a context.Context get gin's: without the fallback,
	// what the middlewares put in the request's context — the correlation id
	// — would not reach them.
	engine.ContextWithFallback = true
	engine.Use(correlate)
	engine.NoRoute(notFound)

	auth := authenticate(cfg.Auth.APIKeySHA256)
	// Each write runs the way cluster.writes says, by its operationId: here,
	// on a ring's leader, or here and on every member of a ring.
	writes, err := newDispatcher(cfg, rt.Cluster, docs)
	if err != nil {
		return nil, nil, err
	}

	var ids *idStore
	for i, v := range versions {
		// In order: what is unauthenticated or malformed never claims a key,
		// and only the leader validates and remembers.
		chain := []gin.HandlerFunc{paramFields, auth, writes.handle, validator(docs[i])}
		if store != nil {
			chain = append(chain, idempotency.Middleware(store, scopeOf(spec.IdempotencyScope), int64(cfg.Gateway.MaxBodyBytes)))
		}
		// Last before the handlers, so that what the idempotency store keeps
		// is the API's ids.
		if _, set, err := extension(docs[i].Extensions, IDsExtension); err != nil {
			return nil, nil, fmt.Errorf("app: version %q: %w", v.OpenAPI.Name, err)
		} else if set {
			if db == nil {
				return nil, nil, fmt.Errorf("app: version %q: %s keeps the ids in the database: set database.dsn",
					v.OpenAPI.Name, IDsExtension)
			}
			if ids == nil {
				if ids, err = newIDStore(ctx, db); err != nil {
					return nil, nil, err
				}
			}
			mw, err := idsMiddleware(ids, docs[i], writes.broadcasts, rt.Cluster.InstanceID)
			if err != nil {
				return nil, nil, fmt.Errorf("app: version %q: %w", v.OpenAPI.Name, err)
			}
			chain = append(chain, mw)
		}
		v.Routes(&router{group: engine.Group("", chain...), doc: docs[i]})
	}

	engine.GET(HealthPath, func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET(ReadyPath, func(c *gin.Context) {
		switch {
		case ready.draining.Load():
			c.String(http.StatusServiceUnavailable, "draining")
		case !ready.joined.Load():
			c.String(http.StatusServiceUnavailable, "joining the cluster")
		default:
			c.Status(http.StatusOK)
		}
	})
	engine.GET("/", indexHandler(cfg, rt.Cluster, specs))
	engine.GET(BroadcastStatusPath+"/:id", statusHandler(rt.Cluster))
	// The broadcast writes the other members publish are applied here,
	// through the engine, once the replica has joined.
	if len(writes.fanouts) > 0 {
		go newApplier(rt.Cluster, engine).run(ctx, ready.joined.Load)
	}
	// Read only: evicting a replica from the ring is not offered.
	if err := rt.Cluster.RegisterGin(engine, cluster.HandlerOptions{}); err != nil {
		return nil, nil, err
	}
	return engine, prefixes, nil
}

// commonPrefix is the leading path segments every path of doc shares, as
// "/orders/v1/", stopping at the first template.
func commonPrefix(doc *openapi3.T) string {
	paths := doc.Paths.InMatchingOrder()
	sort.Strings(paths)
	if len(paths) == 0 {
		return "/"
	}
	common := strings.Split(strings.Trim(paths[0], "/"), "/")
	for _, p := range paths[1:] {
		segs := strings.Split(strings.Trim(p, "/"), "/")
		n := 0
		for n < len(common) && n < len(segs) && common[n] == segs[n] {
			n++
		}
		common = common[:n]
	}
	out := "/"
	for _, s := range common {
		if strings.HasPrefix(s, "{") {
			break
		}
		out += s + "/"
	}
	return out
}

// validator checks requests against one version's contract.
func validator(doc *openapi3.T) gin.HandlerFunc {
	return ginmiddleware.OapiRequestValidatorWithOptions(doc, &ginmiddleware.Options{
		SilenceServersWarning: true,
		Options: openapi3filter.Options{
			// The key is checked before; a contract declares it for its
			// documentation's sake.
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
			MultiError:         false,
		},
		ErrorHandler: func(c *gin.Context, message string, status int) {
			if status == http.StatusNotFound {
				notFound(c)
				return
			}
			problem.Abort(c, problem.Invalid("%s", cleanValidation(message)))
		},
	})
}

// cleanValidation keeps what a caller can act on: kin-openapi's messages carry
// the whole schema after the reason, and the gin middleware prefixes them with
// the Go type of the error.
func cleanValidation(s string) string {
	if i := strings.Index(s, "\nSchema:"); i > 0 {
		s = s[:i]
	}
	for _, prefix := range []string{"error in openapi3filter.RequestError: ",
		"error in openapi3filter.SecurityRequirementsError: "} {
		s = strings.TrimPrefix(s, prefix)
	}
	return strings.TrimSpace(s)
}

// notFound answers what no route matched, in the API's one error shape.
func notFound(c *gin.Context) {
	problem.Abort(c, problem.NotFound("no such route: %s %s", c.Request.Method, c.Request.URL.Path))
}

// correlate gives every call an id, taken from X-Correlation-ID when the
// caller — or the gateway — set one, and puts it and the consumer the gateway
// authenticated into the access log line.
func correlate(c *gin.Context) {
	id := c.GetHeader("X-Correlation-ID")
	if id == "" {
		id = c.GetHeader("X-Request-ID")
	}
	if id == "" {
		// crypto/rand.Text cannot fail: no error to drop.
		id = rand.Text()
	}
	c.Header("X-Correlation-ID", id)
	c.Request = c.Request.WithContext(problem.WithCorrelationID(c.Request.Context(), id))
	server.LogField(c, "correlation_id", id)
	if consumer := c.GetHeader("X-Consumer-Username"); consumer != "" {
		server.LogField(c, "consumer", consumer)
	}
	c.Next()
}

// paramFields puts the route's path parameters into the access log line — the
// tenant, the resource — under their own names, but for those the engine
// writes itself.
func paramFields(c *gin.Context) {
	for _, p := range c.Params {
		server.LogField(c, p.Key, p.Value)
	}
	c.Next()
}

// authenticate checks the apikey header when the service is reached without
// the gateway — the same header the gateway checks, so a caller authenticates
// one way whichever of the two it reaches. Behind the gateway, which checks
// the key and strips it, no key is configured.
func authenticate(wantSHA256 string) gin.HandlerFunc {
	if wantSHA256 == "" {
		slog.Warn("no API key configured: the API authenticates nobody, and must only be reachable through the gateway")
		return func(c *gin.Context) { c.Next() }
	}
	want, err := hex.DecodeString(wantSHA256)
	if err == nil && len(want) != sha256.Size {
		err = errors.New("not a sha256")
	}
	return func(c *gin.Context) {
		// A broadcast write another member published was authenticated
		// there.
		if replaying(c.Request.Context()) {
			c.Next()
			return
		}
		key := c.GetHeader("apikey")
		sum := sha256.Sum256([]byte(key))
		if err != nil || key == "" || subtle.ConstantTimeCompare(sum[:], want) != 1 {
			problem.Abort(c, &problem.Error{Status: http.StatusUnauthorized, Code: "unauthenticated",
				Title: "Unauthenticated", Detail: "a valid key is required in the apikey header"})
			return
		}
		c.Next()
	}
}
