// Package auth authenticates InfraPI requests against an upstream
// authentication service: it forwards the caller's bearer token to that
// service's POST /oauth2/userinfo endpoint - the same check it runs on
// every request it protects itself - and resolves it to a [UserInfo].
// [Client.Middleware] turns that into a gin middleware that authenticates
// every route it wraps.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// UserinfoPath is the endpoint a token is verified against.
const UserinfoPath = "/oauth2/userinfo"

// DefaultTimeout bounds a Verify call when ClientOptions leaves Timeout
// unset.
const DefaultTimeout = 5 * time.Second

// ClientOptions configures a Client. BaseURL is mandatory.
type ClientOptions struct {
	// BaseURL of the authentication service, e.g. "https://auth.example.com".
	BaseURL string

	// Timeout per request (default: DefaultTimeout). Ignored when
	// HTTPClient is set.
	Timeout time.Duration

	// HTTPClient overrides the client Timeout would otherwise build, for
	// TLS, proxy or transport settings Timeout alone cannot express.
	HTTPClient *http.Client
}

// Client calls an authentication service's POST /oauth2/userinfo endpoint
// to resolve a bearer token to the identity it names.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a Client calling opts.BaseURL.
func NewClient(opts ClientOptions) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("auth: NewClient requires BaseURL")
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}

	return &Client{baseURL: strings.TrimRight(opts.BaseURL, "/"), http: httpClient}, nil
}

// Verify forwards token to the authentication service's userinfo endpoint
// and returns the identity it resolves to.
//
// A token the service itself refuses - missing, malformed, expired, signed
// by an unknown provider - comes back as a 401 *errors.Error; the service
// answering with anything else, or not answering at all, comes back as
// infraerrors.Upstream or infraerrors.Unavailable respectively, so a
// caller can tell "this caller is not who they claim" from "the auth
// service is having a bad day".
func (a *Client) Verify(ctx context.Context, token string) (*UserInfo, error) {
	if token == "" {
		return nil, infraerrors.New(http.StatusUnauthorized, "auth.Verify", "empty bearer token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+UserinfoPath, nil)
	if err != nil {
		return nil, infraerrors.Wrap(err, http.StatusInternalServerError, "auth.Verify", "")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, infraerrors.Unavailable("auth: contact authentication service: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, infraerrors.Wrap(err, http.StatusBadGateway, "auth.Verify", "read authentication service response")
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, infraerrors.New(http.StatusUnauthorized, "auth.Verify", "authentication service rejected the token")
	case resp.StatusCode != http.StatusOK:
		return nil, infraerrors.Upstream(resp.StatusCode >= http.StatusInternalServerError,
			"auth: authentication service answered %s", http.StatusText(resp.StatusCode))
	}

	var info UserInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, infraerrors.Wrap(err, http.StatusBadGateway, "auth.Verify", "decode authentication service response")
	}
	return &info, nil
}
