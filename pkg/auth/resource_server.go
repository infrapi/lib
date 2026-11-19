package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/api"
	infraerrors "github.com/infrapi/lib/pkg/errors"
	"github.com/infrapi/lib/pkg/server"
)

// ResourceOf is a request path's resource (RFC 8707): ClientOptions.PublicURL
// and the path's root, /apis/<group>/<version>. "" without PublicURL, or for
// a path outside that layout.
func (a *Client) ResourceOf(path string) string {
	p, err := api.Parse(path)
	if a.publicURL == "" || err != nil {
		return ""
	}
	return a.publicURL + "/apis/" + p.API + "/" + p.Version
}

// covers tells whether a token issued for aud may be used on resource: aud
// is resource, or a URL resource is under (a gateway's, every app behind
// it). A token without an aud, not one of Kayra's authorization server,
// had its provider's claims checked by Kayra already.
func covers(aud []string, resource string) bool {
	if len(aud) == 0 || resource == "" {
		return true
	}
	for _, a := range aud {
		a = strings.TrimRight(a, "/")
		if resource == a || strings.HasPrefix(resource, a+"/") {
			return true
		}
	}
	return false
}

// checkAudience refuses, 401, a token of Kayra's authorization server
// issued for another resource than the request's.
func (a *Client) checkAudience(c *gin.Context, user *UserInfo, origin string) bool {
	resource := a.ResourceOf(c.Request.URL.Path)
	if covers(user.Audience, resource) {
		return true
	}
	a.unauthorized(c, true, infraerrors.New(http.StatusUnauthorized, origin,
		"this token was issued for %s, not for %s", strings.Join(user.Audience, ", "), resource).
		WithSolution("ask the authorization server for a token for %s (its resource parameter)", resource))
	return false
}

// unauthorized aborts with err, a 401, its WWW-Authenticate header naming
// the request's resource metadata (RFC 9728), and invalid_token when a
// token was sent but refused (RFC 6750).
func (a *Client) unauthorized(c *gin.Context, tokenSent bool, err error) {
	h := "Bearer"
	if resource := a.ResourceOf(c.Request.URL.Path); resource != "" {
		h += fmt.Sprintf(` resource_metadata="%s%s"`, resource, server.ProtectedResourcePath)
	}
	if tokenSent {
		h += `, error="invalid_token"`
	}
	c.Header("WWW-Authenticate", h)
	server.AbortError(c, err)
}

// AuthorizationServer is Kayra's authorization server's issuer, from its
// discovery document: what a protected resource's metadata names
// (server.WithProtectedResource). The service's URL when the document
// names none.
func (a *Client) AuthorizationServer(ctx context.Context) (string, error) {
	d, err := a.Discover(ctx)
	if err != nil {
		return "", err
	}
	if d.Issuer != "" {
		return d.Issuer, nil
	}
	return a.baseURL, nil
}
