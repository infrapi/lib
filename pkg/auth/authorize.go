package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// AuthorizePath is the endpoint an authorization decision is made
// against — Genesis's rbac.RegisterAuthorizeRoute.
const AuthorizePath = "/api/v1/authorize"

// AuthorizeRequest mirrors Genesis's rbac.AuthorizeRequest: Namespace and
// Stack normally come straight from a request's own :namespace/:stack
// route params, Resource/Verb from ParseResource/VerbFor against the
// rest of the path and the HTTP method.
type AuthorizeRequest struct {
	Namespace string `json:"namespace"`
	Stack     string `json:"stack"`
	Resource  string `json:"resource"`
	Verb      string `json:"verb"`
}

// authorizeResponse mirrors Genesis's rbac.AuthorizeResponse.
type authorizeResponse struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

// Authorize asks the authentication/authorization service whether
// token's caller may req.Verb req.Resource on req.Stack within
// req.Namespace, returning the decision and (when denied) the reason the
// service gave. Unlike Verify, this is never cached: an authorization
// decision can change (an edited rolebinding or role) independently of
// the token's own remaining lifetime, so every call reaches the service.
func (a *Client) Authorize(ctx context.Context, token string, req AuthorizeRequest) (allowed bool, reason string, err error) {
	if token == "" {
		return false, "", infraerrors.New(http.StatusUnauthorized, "auth.Authorize", "empty bearer token")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return false, "", infraerrors.Wrap(err, http.StatusInternalServerError, "auth.Authorize", "")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+AuthorizePath, bytes.NewReader(body))
	if err != nil {
		return false, "", infraerrors.Wrap(err, http.StatusInternalServerError, "auth.Authorize", "")
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := a.http.Do(httpReq)
	if err != nil {
		return false, "", infraerrors.Unavailable("auth: contact authorization service: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, "", infraerrors.Wrap(err, http.StatusBadGateway, "auth.Authorize", "read authorization service response")
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return false, "", infraerrors.New(http.StatusUnauthorized, "auth.Authorize", "authorization service rejected the token")
	case resp.StatusCode != http.StatusOK:
		return false, "", infraerrors.Upstream(resp.StatusCode >= http.StatusInternalServerError,
			"auth: authorization service answered %s", http.StatusText(resp.StatusCode))
	}

	var out authorizeResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return false, "", infraerrors.Wrap(err, http.StatusBadGateway, "auth.Authorize", "decode authorization service response")
	}
	return out.Allowed, out.Reason, nil
}
