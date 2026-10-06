package app

import (
	"context"
	"encoding/json"

	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/app/problem"
)

// Router is where a version registers its handlers, behind the API's
// middlewares. Paths are the contract's, with {name} for a parameter:
// "/orders/v1/tenants/{tenant}/orders/{id}".
type Router interface {
	// Handle serves method and path with h.
	Handle(method, path string, h HandlerFunc)
	GET(path string, h HandlerFunc)
	POST(path string, h HandlerFunc)
	PUT(path string, h HandlerFunc)
	PATCH(path string, h HandlerFunc)
	DELETE(path string, h HandlerFunc)

	// Mount serves every operation of the version's contract with h: the
	// net/http handler a generator such as oapi-codegen builds from that same
	// contract. Each operation is still a route of its own here — measured,
	// logged and validated as one — and h does the dispatching.
	Mount(h http.Handler)
}

// HandlerFunc handles one call.
type HandlerFunc func(*Context)

// Context is one call: its request, and how to answer it.
type Context struct{ g *gin.Context }

// Context is the request's context: cancelled when the caller goes away, and
// carrying the call's correlation id.
func (c *Context) Context() context.Context { return c.g.Request.Context() }

// Request is the request itself.
func (c *Context) Request() *http.Request { return c.g.Request }

// Param is a path parameter of the route, by its name in the contract.
func (c *Context) Param(name string) string { return c.g.Param(name) }

// Query is a query parameter, empty when absent.
func (c *Context) Query(name string) string { return c.g.Query(name) }

// Header is a request header, empty when absent.
func (c *Context) Header(name string) string { return c.g.GetHeader(name) }

// SetHeader sets a response header; before the answer is written.
func (c *Context) SetHeader(name, value string) { c.g.Header(name, value) }

// Bind decodes the JSON body into v. The contract has already validated it;
// what fails here is a body that does not fit v, an invalid_request.
func (c *Context) Bind(v any) error {
	if err := json.NewDecoder(c.g.Request.Body).Decode(v); err != nil {
		return problem.Invalid("the body does not decode: %v", err)
	}
	return nil
}

// JSON answers status with v as JSON.
func (c *Context) JSON(status int, v any) { c.g.JSON(status, v) }

// NoContent answers 204.
func (c *Context) NoContent() { c.g.Status(http.StatusNoContent) }

// Error answers with err as a problem document, and stops the call: a
// *problem.Error as it says, anything else as a 500 that hides its text.
func (c *Context) Error(err error) { problem.Abort(c.g, err) }

// router is the Router of one version: a gin route group, behind the
// version's middlewares, and its contract for Mount.
type router struct {
	group gin.IRouter
	doc   *openapi3.T
}

func (r *router) Handle(method, path string, h HandlerFunc) {
	r.group.Handle(method, ginPath(path), func(g *gin.Context) { h(&Context{g: g}) })
}

func (r *router) GET(path string, h HandlerFunc)    { r.Handle(http.MethodGet, path, h) }
func (r *router) POST(path string, h HandlerFunc)   { r.Handle(http.MethodPost, path, h) }
func (r *router) PUT(path string, h HandlerFunc)    { r.Handle(http.MethodPut, path, h) }
func (r *router) PATCH(path string, h HandlerFunc)  { r.Handle(http.MethodPatch, path, h) }
func (r *router) DELETE(path string, h HandlerFunc) { r.Handle(http.MethodDelete, path, h) }

func (r *router) Mount(h http.Handler) {
	serve := func(g *gin.Context) {
		// The path values are the route's; h's own mux sets them again when
		// it dispatches, but a plain handler finds them here too.
		for _, p := range g.Params {
			g.Request.SetPathValue(p.Key, p.Value)
		}
		h.ServeHTTP(g.Writer, g.Request)
	}
	for _, path := range r.doc.Paths.InMatchingOrder() {
		for method := range r.doc.Paths.Value(path).Operations() {
			r.group.Handle(method, ginPath(path), serve)
		}
	}
}

// ginPath writes a contract path in gin's syntax: {id} becomes :id.
func ginPath(path string) string {
	segments := strings.Split(path, "/")
	for i, s := range segments {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segments[i] = ":" + s[1:len(s)-1]
		}
	}
	return strings.Join(segments, "/")
}

// scopeOf adapts the service's idempotency scope, written against Context, to
// the middleware's.
func scopeOf(scope func(*Context) string) func(*gin.Context) string {
	if scope == nil {
		return nil
	}
	return func(g *gin.Context) string { return scope(&Context{g: g}) }
}
