package server

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// ProtectedResourcePath is a root's protected resource metadata (RFC 9728),
// with WithProtectedResource.
const ProtectedResourcePath = "/-/oauth-protected-resource"

// WellKnownProtectedResource prefixes a root's metadata at the engine's root:
// RFC 9728's well-known URI, /.well-known/oauth-protected-resource<root>.
const WellKnownProtectedResource = "/.well-known/oauth-protected-resource"

// ProtectedResource is a root's protected resource metadata.
type ProtectedResource struct {
	// Resource is the root's URL, AppUrl and the root: the resource
	// (RFC 8707) a client asks a token for.
	Resource string `json:"resource"`
	// AuthorizationServers are the issuers of its tokens: Kayra's.
	AuthorizationServers []string `json:"authorization_servers"`
	// BearerMethodsSupported: the Authorization header only.
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

// protectedResourceHandler answers resource's metadata, the authorization
// server read at each request: 503 while it cannot be.
func protectedResourceHandler(resource string, authorizationServer func(context.Context) (string, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		issuer, err := authorizationServer(c.Request.Context())
		if err != nil {
			AbortError(c, infraerrors.Unavailable("the authorization server: %v", err))
			return
		}
		c.JSON(http.StatusOK, ProtectedResource{
			Resource: resource, AuthorizationServers: []string{issuer}, BearerMethodsSupported: []string{"header"},
		})
	}
}
