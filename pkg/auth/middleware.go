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
