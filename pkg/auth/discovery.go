package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// DiscoveryPath is where Kayra serves its discovery document, without
// authentication.
const DiscoveryPath = "/.well-known/kayra.json"

// Discovery is Kayra's discovery document: its API domain, the paths of
// its built-in groups and of the endpoints its clients call. Paths are
// relative to the service's URL.
type Discovery struct {
	// Domain is Kayra's app.api_domain: its built-in groups are
	// <name>.<Domain>. Other APIs' groups are their own full names.
	Domain string `json:"domain"`
	// APIs maps each built-in group's name (system, catalog,
	// authorization) to its path, /apis/<name>.<Domain>/v1.
	APIs map[string]string `json:"apis"`
	// Endpoints are the paths clients call.
	Endpoints DiscoveryEndpoints `json:"endpoints"`
}

// DiscoveryEndpoints are the paths of the endpoints a client calls.
type DiscoveryEndpoints struct {
	// Userinfo resolves a bearer token to its identity.
	Userinfo string `json:"userinfo"`
	// Check decides whether a caller may act: what Authorize and
	// RequireAccess ask.
	Check string `json:"check"`
	// JWKS is the public keys of the service's self-signed tokens.
	JWKS string `json:"jwks"`
	// APIs is the discovery of every API group, with its resources.
	APIs string `json:"apis"`
}

// Discover returns the service's discovery document, read from
// DiscoveryPath on the first call and kept for the Client's lifetime. A
// failed read is not kept: the next call tries again, so a service
// started before Kayra answers once Kayra does. The error is a 503,
// retryable.
func (a *Client) Discover(ctx context.Context) (*Discovery, error) {
	a.discoverMu.Lock()
	defer a.discoverMu.Unlock()
	if a.discovered != nil {
		return a.discovered, nil
	}

	url := a.baseURL + DiscoveryPath
	fail := func(format string, args ...any) error {
		return infraerrors.Unavailable("auth: discover the authorization service at %s: "+format, append([]any{url}, args...)...).
			WithSolution("check the service's URL is Kayra's, and that it serves %s", DiscoveryPath)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fail("%v", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fail("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fail("read: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fail("answered %s", http.StatusText(resp.StatusCode))
	}

	var doc Discovery
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fail("decode: %v", err)
	}
	if !strings.HasPrefix(doc.Endpoints.Check, "/") {
		return nil, fail("no check endpoint path, got %q", doc.Endpoints.Check)
	}
	a.discovered = &doc
	return a.discovered, nil
}
