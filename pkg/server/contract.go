package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/gin-gonic/gin"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// contractRouter finds, for a request, the operation of the contracts it
// belongs to.
type contractRouter struct {
	routers []routers.Router
}

// newContractRouter loads every contract supplied with WithOpenAPI. A contract
// that does not load or is not valid OpenAPI stops the service at start: it
// would otherwise be served, documented and not enforced.
func newContractRouter(specs []OpenAPISpec) (*contractRouter, error) {
	cr := &contractRouter{}
	for _, s := range specs {
		doc, err := openapi3.NewLoader().LoadFromData(s.Content)
		if err != nil {
			return nil, fmt.Errorf("server: OpenAPI document %q: %w", s.Name, err)
		}
		if err := doc.Validate(context.Background()); err != nil {
			return nil, fmt.Errorf("server: OpenAPI document %q: %w", s.Name, err)
		}
		// The routes match whatever host the request came in on: the
		// contract's servers, if any, describe the gateway, not this engine.
		doc.Servers = openapi3.Servers{{URL: "/"}}
		r, err := gorillamux.NewRouter(doc)
		if err != nil {
			return nil, fmt.Errorf("server: OpenAPI document %q: %w", s.Name, err)
		}
		cr.routers = append(cr.routers, r)
	}
	return cr, nil
}

// validate checks each request against the operation of the contract it
// belongs to — its path and query parameters, its headers, its body — before
// any handler runs, and answers 400 with the reason when it does not hold. A
// request no contract describes — /-/metrics, a route the service adds on its
// own — goes through untouched.
func (cr *contractRouter) validate(c *gin.Context) {
	for _, r := range cr.routers {
		route, params, err := r.FindRoute(c.Request)
		if err != nil {
			continue // not this contract's, or not this method: gin answers
		}
		err = openapi3filter.ValidateRequest(c.Request.Context(), &openapi3filter.RequestValidationInput{
			Request: c.Request, PathParams: params, Route: route,
			Options: &openapi3filter.Options{
				// Authentication is enforced in front of the service: a
				// contract declares its scheme for its documentation's sake.
				AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
			},
		})
		if err != nil {
			AbortError(c, infraerrors.Invalid("%s", reason(err)))
			return
		}
		break
	}
	c.Next()
}

// reason keeps what a caller can act on: kin-openapi appends the whole schema
// to its messages, and the type of the error before them.
func reason(err error) string {
	var re *openapi3filter.RequestError
	msg := err.Error()
	if errors.As(err, &re) {
		msg = re.Error()
	}
	if i := strings.Index(msg, "\nSchema:"); i > 0 {
		msg = msg[:i]
	}
	return strings.TrimSpace(msg)
}
