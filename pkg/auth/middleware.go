package auth

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	infraerrors "github.com/infrapi/lib/pkg/errors"
	"github.com/infrapi/lib/pkg/server"
)

// contextKey is the gin context key the resolved UserInfo is stored under.
const contextKey = "infrapi.auth.user"

// GetUser reads the UserInfo a prior Middleware call resolved for this
// request.
func GetUser(c *gin.Context) (*UserInfo, bool) {
	v, ok := c.Get(contextKey)
	if !ok {
		return nil, false
	}
	u, ok := v.(*UserInfo)
	return u, ok
}

// Middleware authenticates every request it wraps against the
// authentication service: the Authorization header's bearer token is
// forwarded to a.Verify, and on success the resolved UserInfo is stored
// for GetUser. A missing or empty bearer header, or a token the service
// refuses, aborts the request with a problem-detail response via
// server.AbortError - no handler downstream runs.
func (a *Client) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := extractBearer(c.GetHeader("Authorization"))
		if err != nil {
			server.AbortError(c, infraerrors.New(http.StatusUnauthorized, "auth.Middleware", "%s", err))
			return
		}

		user, err := a.Verify(c.Request.Context(), token)
		if err != nil {
			server.AbortError(c, err)
			return
		}

		c.Set(contextKey, user)
		c.Next()
	}
}

// RequireAccess authorizes every request it wraps against the
// authorization service's POST /api/v1/authorize, for routes mounted at
// a path shaped ".../namespaces/:namespace/stacks/:stack/*resource"
// (gin's "*resource" wildcard catch-all, so :namespace, :stack and the
// rest of the path are all available via c.Param without any other
// per-route configuration — see ParseResource/VerbFor for how the
// wildcard and the HTTP method become Resource/Verb). It reads the
// bearer token straight from the request itself, so it does not depend
// on Middleware having run first — each guards a different concern
// (identity vs access) and reaches the service independently; Authorize
// is never cached, so an edited role or rolebinding takes effect on the
// very next request regardless of how long Middleware's own Verify
// result has left to live.
func (a *Client) RequireAccess() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := extractBearer(c.GetHeader("Authorization"))
		if err != nil {
			server.AbortError(c, infraerrors.New(http.StatusUnauthorized, "auth.RequireAccess", "%s", err))
			return
		}

		resource, hasID, err := ParseResource(c.Param("resource"))
		if err != nil {
			server.AbortError(c, infraerrors.New(http.StatusNotFound, "auth.RequireAccess", "%s", err))
			return
		}
		verb, err := VerbFor(c.Request.Method, hasID)
		if err != nil {
			server.AbortError(c, infraerrors.New(http.StatusMethodNotAllowed, "auth.RequireAccess", "%s", err))
			return
		}

		allowed, reason, err := a.Authorize(c.Request.Context(), token, AuthorizeRequest{
			Namespace: c.Param("namespace"),
			Stack:     c.Param("stack"),
			Resource:  resource,
			Verb:      verb,
		})
		if err != nil {
			server.AbortError(c, err)
			return
		}
		if !allowed {
			detail := reason
			if detail == "" {
				detail = "not authorized"
			}
			server.AbortError(c, infraerrors.New(http.StatusForbidden, "auth.RequireAccess", "%s", detail))
			return
		}

		c.Next()
	}
}

// extractBearer pulls the token out of an "Authorization: Bearer <token>"
// header.
func extractBearer(header string) (string, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", fmt.Errorf("missing bearer token")
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", fmt.Errorf("empty bearer token")
	}
	return token, nil
}
