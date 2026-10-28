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

	// CacheTTL caches a successful Verify result in memory for this long,
	// keyed by a hash of the token, so repeated calls with the same token
	// skip the HTTP round trip. Zero (default) means DefaultCacheTTL
	// (1h); a negative value disables caching entirely, so every call
	// reaches the authentication service and a revoked token or a
	// changed group membership is reflected immediately.
	CacheTTL time.Duration
}

// Client calls an authentication service's POST /oauth2/userinfo endpoint
// to resolve a bearer token to the identity it names.
type Client struct {
	baseURL string
	http    *http.Client
	cache   *cache // nil when ClientOptions.CacheTTL is negative
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

	c := &Client{baseURL: strings.TrimRight(opts.BaseURL, "/"), http: httpClient}
	switch {
	case opts.CacheTTL == 0:
		c.cache = newCache(DefaultCacheTTL)
	case opts.CacheTTL > 0:
		c.cache = newCache(opts.CacheTTL)
	}
	return c, nil
}

// Verify forwards token to the authentication service's userinfo endpoint
// and returns the identity it resolves to, serving a cached result
// instead when a prior Verify for the same token has not expired -
// ClientOptions.CacheTTL (1h by default) controls that, and a negative
// value turns caching off.
//
// A token the service itself refuses - missing, malformed, expired, signed
// by an unknown provider - comes back as a 401 *errors.Error; the service
// answering with anything else, or not answering at all, comes back as
// infraerrors.Upstream or infraerrors.Unavailable respectively, so a
// caller can tell "this caller is not who they claim" from "the auth
// service is having a bad day". Neither is cached: only a successful
// resolution is worth saving a round trip on.
func (a *Client) Verify(ctx context.Context, token string) (*UserInfo, error) {
	if token == "" {
		return nil, infraerrors.New(http.StatusUnauthorized, "auth.Verify", "empty bearer token")
	}

	if a.cache != nil {
		if info, ok := a.cache.get(token); ok {
			return info, nil
		}
	}

	info, err := a.verify(ctx, token)
	if err != nil {
		return nil, err
	}

	if a.cache != nil {
		a.cache.set(token, info)
	}
	return info, nil
}

// verify is the HTTP call Verify makes on a cache miss.
func (a *Client) verify(ctx context.Context, token string) (*UserInfo, error) {
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
	defer func() { _ = resp.Body.Close() }()

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
