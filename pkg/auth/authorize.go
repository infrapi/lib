package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// AuthorizeRequest mirrors Kayra's rbac.AuthorizeRequest: Namespace and
// Api normally come straight from a request's own :namespace/:api route
// params, Resource/Verb from ParseResource/VerbFor against the rest of
// the path and the HTTP method. Api is the calling app's own full API
// group name (e.g. "compute.infrapi.io"), matching how it's actually
// reachable (/apis/compute.infrapi.io/v1/...) — never the bare app
// name.
type AuthorizeRequest struct {
	Namespace string `json:"namespace"`
	Api       string `json:"api"`
	Resource  string `json:"resource"`
	Verb      string `json:"verb"`
	// Name is the object the request names, when it names one.
	Name string `json:"name,omitempty"`
	// RequestID, SourceIP and UserAgent are the request's as the service
	// received it: Kayra's audit records them, not the service's own.
	RequestID string `json:"request_id,omitempty"`
	SourceIP  string `json:"source_ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
}

// Decision is Kayra's answer to a check: Allowed, Reason when it is not,
// and User, the caller's subject.
type Decision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
	User    string `json:"user"`
}

// Authorize asks the authentication/authorization service whether
// token's caller may req.Verb req.Resource on req.Api within
// req.Namespace, returning the decision and (when denied) the reason the
// service gave. Unlike Verify, this is never cached: an authorization
// decision can change (an edited rolebinding or role) independently of
// the token's own remaining lifetime, so every call reaches the service.
func (a *Client) Authorize(ctx context.Context, token string, req AuthorizeRequest) (allowed bool, reason string, err error) {
	d, err := a.Decide(ctx, token, req)
	return d.Allowed, d.Reason, err
}

// Decide is Authorize's whole answer, the caller's subject included.
func (a *Client) Decide(ctx context.Context, token string, req AuthorizeRequest) (Decision, error) {
	allowed, reason, user, err := a.decide(ctx, token, req)
	return Decision{Allowed: allowed, Reason: reason, User: user}, err
}

func (a *Client) decide(ctx context.Context, token string, req AuthorizeRequest) (allowed bool, reason, user string, err error) {
	if token == "" {
		return false, "", "", infraerrors.New(http.StatusUnauthorized, "auth.Authorize", "empty bearer token")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return false, "", "", infraerrors.Wrap(err, http.StatusInternalServerError, "auth.Authorize", "")
	}

	doc, err := a.Discover(ctx)
	if err != nil {
		return false, "", "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+doc.Endpoints.Check, bytes.NewReader(body))
	if err != nil {
		return false, "", "", infraerrors.Wrap(err, http.StatusInternalServerError, "auth.Authorize", "")
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := a.http.Do(httpReq)
	if err != nil {
		return false, "", "", infraerrors.Unavailable("auth: contact authorization service: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, "", "", infraerrors.Wrap(err, http.StatusBadGateway, "auth.Authorize", "read authorization service response")
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return false, "", "", infraerrors.New(http.StatusUnauthorized, "auth.Authorize", "authorization service rejected the token")
	case resp.StatusCode != http.StatusOK:
		return false, "", "", serviceError(resp.StatusCode, respBody)
	}

	var out Decision
	if err := json.Unmarshal(respBody, &out); err != nil {
		return false, "", "", infraerrors.Wrap(err, http.StatusBadGateway, "auth.Authorize", "decode authorization service response")
	}
	return out.Allowed, out.Reason, out.User, nil
}

// problem is the part of an RFC 9457 problem the authorization service
// answers with that a caller can act on.
type problem struct {
	Detail   string `json:"detail"`
	Solution string `json:"solution"`
}

// serviceError is the error for a check the service answered with status,
// other than 200 and 401. A 4xx is about the request itself — Kayra's
// unknown namespace or registration (404), resource missing from the API's
// spec (404), verb the resource lacks (405), namespace not subscribed
// (403): it is passed on with its status, detail and solution, for the
// caller to fix its request. Anything else is the service failing: a 502,
// retryable for a 5xx, with the service's detail when it gave one.
func serviceError(status int, body []byte) *infraerrors.Error {
	var p problem
	_ = json.Unmarshal(body, &p) // not a problem: no detail to pass on

	if status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		detail := p.Detail
		if detail == "" {
			detail = "authorization service answered " + http.StatusText(status)
		}
		err := infraerrors.New(status, "auth.Authorize", "%s", detail)
		if p.Solution != "" {
			err = err.WithSolution("%s", p.Solution)
		}
		return err
	}

	err := infraerrors.Upstream(status >= http.StatusInternalServerError,
		"auth: authorization service answered %s", http.StatusText(status))
	if p.Detail != "" {
		err = infraerrors.Upstream(status >= http.StatusInternalServerError,
			"auth: authorization service answered %s: %s", http.StatusText(status), p.Detail)
	}
	return err
}
