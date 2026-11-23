package auth

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/api"
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
			a.unauthorized(c, false, infraerrors.New(http.StatusUnauthorized, "auth.Middleware", "%s", err))
			return
		}

		user, err := a.Verify(c.Request.Context(), token)
		if err != nil {
			if infraerrors.ErrorCode(err) == http.StatusUnauthorized {
				a.unauthorized(c, true, err)
			} else {
				server.AbortError(c, err)
			}
			return
		}
		if !a.checkAudience(c, user, "auth.Middleware") {
			return
		}

		c.Set(contextKey, user)
		c.Next()
	}
}

// RequireAccess authorizes every request it wraps against the
// authorization service's check endpoint (POST, the path its discovery
// document names, see Client.Discover). The request path must follow
// pkg/api's layout,
// /apis/{group}/{version}/namespaces/{namespace}/{resource}, which
// is what server.APIGroup mounts, or /apis/{group}/{version}/{resource}
// for a cluster-scoped resource, its namespace "": Kayra decides it on
// its cluster RBAC. The namespace and the API come from it,
// Resource and Verb from the rest of the path and the HTTP method (see
// ParseResource and VerbFor). Anything else is answered 404. The path is
// read from the request rather than from route parameters, so it works on
// any route pattern. It reads the bearer token straight from the request
// itself, so it does not depend on Middleware having run first - each
// guards a different concern (identity vs access) and reaches the service
// independently. Authorize is never cached, so an edited role or
// rolebinding takes effect on the very next request regardless of how long
// Middleware's own Verify result has left to live.
func (a *Client) RequireAccess() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := extractBearer(c.GetHeader("Authorization"))
		if err != nil {
			a.unauthorized(c, false, infraerrors.New(http.StatusUnauthorized, "auth.RequireAccess", "%s", err))
			return
		}
		// the token's audience, when the service names its resource:
		// Verify's result is cached, Middleware's included
		if a.publicURL != "" {
			user, err := a.Verify(c.Request.Context(), token)
			if err != nil {
				if infraerrors.ErrorCode(err) == http.StatusUnauthorized {
					a.unauthorized(c, true, err)
				} else {
					server.AbortError(c, err)
				}
				return
			}
			if !a.checkAudience(c, user, "auth.RequireAccess") {
				return
			}
		}

		p, err := api.Parse(c.Request.URL.Path)
		if err != nil {
			server.AbortError(c, infraerrors.New(http.StatusNotFound, "auth.RequireAccess", "%s", err))
			return
		}
		// api.Parse refused an empty resource already: ParseResource cannot fail.
		resource, hasID, _ := ParseResource(p.Resource)
		verb, err := VerbFor(c.Request.Method, hasID)
		if err != nil {
			server.AbortError(c, infraerrors.New(http.StatusMethodNotAllowed, "auth.RequireAccess", "%s", err))
			return
		}

		d, err := a.Decide(c.Request.Context(), token, AuthorizeRequest{
			Namespace: p.Namespace,
			Api:       p.API,
			Resource:  resource,
			Verb:      verb,
			Name:      ObjectName(p.Resource),
			RequestID: c.GetHeader(server.RequestIDHeader),
			SourceIP:  c.ClientIP(),
			UserAgent: c.Request.UserAgent(),
		})
		if err != nil {
			server.AbortError(c, err)
			return
		}
		allowed, reason := d.Allowed, d.Reason
		if !allowed {
			detail := reason
			if detail == "" {
				detail = "not authorized"
			}
			server.AbortError(c, infraerrors.New(http.StatusForbidden, "auth.RequireAccess", "%s", detail))
			return
		}

		// the check passed: its user, for the request's event (server.WithEvents)
		c.Set(server.CheckedUserKey, d.User)
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

// CheckedUser is the caller's subject, as Kayra's check answered it in
// RequireAccess: "" before it, when it refused, or with a Kayra that does
// not say.
func CheckedUser(c *gin.Context) string {
	return c.GetString(server.CheckedUserKey)
}

// ObjectName is the object a resource path names: web-1 for vms/web-1
// and vms/web-1/reboot, "" for vms.
func ObjectName(resource string) string {
	segs := strings.Split(strings.Trim(resource, "/"), "/")
	switch {
	case len(segs) < 2:
		return ""
	case len(segs)%2 == 0:
		return segs[len(segs)-1]
	}
	return segs[len(segs)-2]
}
