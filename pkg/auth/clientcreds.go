package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/infrapi/lib/pkg/errors"
	"github.com/infrapi/lib/pkg/identity"
)

// AuthorizationServerMetadataPath is where Kayra's authorization server
// serves its metadata (RFC 8414).
const AuthorizationServerMetadataPath = "/.well-known/oauth-authorization-server"

// JWTBearer is the client assertion type of RFC 7523: the client
// authenticates with a JWT.
const JWTBearer = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" //#nosec G101 -- a URN naming an assertion type, no credential

// ClientCredentialsOptions configures ClientCredentials. Kayra, ClientID
// and one of Key or TokenFile are required.
type ClientCredentialsOptions struct {
	// Kayra is the Client of the Kayra that issues the tokens: their
	// endpoint is its authorization server's, from its metadata.
	Kayra *Client
	// ClientID is the identity the tokens are of: an API's,
	// identity.Subject(group), system:api:compute.infrapi.io.
	ClientID string
	// Key signs the assertions (private_key_jwt): the service's identity
	// key, its public half served at -/jwks.json (server.WithIdentity).
	Key *identity.Key
	// TokenFile is a platform's identity token instead, a projected
	// Kubernetes service account token say, read again for every token
	// asked: one of the API's federated credentials must match it.
	TokenFile string
	// Resources are what the tokens are for (RFC 8707): the URL of each
	// API it calls, https://gateway.example.com/apis/network.infrapi.io/v1,
	// one of Kayra's oauth2.resources or under one. Kayra's own when empty.
	Resources []string
	// Now is the clock, time.Now when nil: for tests.
	Now func() time.Time
}

// ClientCredentials gets the tokens of one of Kayra's own identities, an
// API's, from Kayra's token endpoint (client_credentials, RFC 6749 4.4),
// authenticated by a JWT (RFC 7523): one Key signs, or the platform's
// token in TokenFile. Token keeps the token and asks a new one once 80% of
// its life is gone. Safe for concurrent use.
type ClientCredentials struct {
	opts ClientCredentialsOptions
	now  func() time.Time

	mu      sync.Mutex
	token   string
	renew   time.Time
	expires time.Time
}

// NewClientCredentials returns the token source opts configures.
func NewClientCredentials(opts ClientCredentialsOptions) (*ClientCredentials, error) {
	switch {
	case opts.Kayra == nil:
		return nil, errors.New("auth: NewClientCredentials requires Kayra")
	case opts.ClientID == "":
		return nil, errors.New("auth: NewClientCredentials requires ClientID")
	case (opts.Key == nil) == (opts.TokenFile == ""):
		return nil, errors.New("auth: NewClientCredentials requires one of Key or TokenFile")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &ClientCredentials{opts: opts, now: now}, nil
}

// Token is a valid token, asked of Kayra when none is kept or the kept one
// is past 80% of its life. While Kayra cannot answer, the kept one is
// returned until it expires.
func (s *ClientCredentials) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Before(s.renew) {
		return s.token, nil
	}
	token, life, err := s.fetch(ctx)
	if err != nil {
		if s.token != "" && now.Before(s.expires) {
			return s.token, nil
		}
		return "", err
	}
	s.token, s.expires, s.renew = token, now.Add(life), now.Add(life*8/10)
	return token, nil
}

// metadata is the part of Kayra's authorization server metadata read here.
type metadata struct {
	Issuer        string `json:"issuer"`
	TokenEndpoint string `json:"token_endpoint"`
}

func (s *ClientCredentials) fetch(ctx context.Context) (string, time.Duration, error) {
	kayra := s.opts.Kayra
	var m metadata
	if err := kayra.getJSON(ctx, kayra.baseURL+AuthorizationServerMetadataPath, &m); err != nil {
		return "", 0, err
	}
	if m.Issuer == "" || m.TokenEndpoint == "" {
		return "", 0, infraerrors.Upstream(false, "Kayra's authorization server metadata names no issuer or token endpoint: is its oauth2 on?")
	}
	assertion, err := s.assertion(m)
	if err != nil {
		return "", 0, err
	}
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {s.opts.ClientID},
		"client_assertion_type": {JWTBearer},
		"client_assertion":      {assertion},
	}
	for _, r := range s.opts.Resources {
		form.Add("resource", r)
	}
	// the token endpoint through the URL the service reaches Kayra by: the
	// issuer's may be a public one it cannot
	endpoint := kayra.baseURL + "/oauth2/token"
	if path := strings.TrimPrefix(m.TokenEndpoint, strings.TrimRight(m.Issuer, "/")); strings.HasPrefix(path, "/") {
		endpoint = kayra.baseURL + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := kayra.http.Do(req)
	if err != nil {
		return "", 0, infraerrors.Unavailable("Kayra's token endpoint %s: %v", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken      string `json:"access_token"`
		ExpiresIn        int    `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		if out.Error != "" {
			return "", 0, infraerrors.Upstream(false, "Kayra refused a token of %s: %s: %s", s.opts.ClientID, out.Error, out.ErrorDescription)
		}
		return "", 0, infraerrors.Upstream(resp.StatusCode >= 500, "Kayra's token endpoint answered %d", resp.StatusCode)
	}
	life := time.Duration(out.ExpiresIn) * time.Second
	if life <= 0 {
		life = time.Minute
	}
	return out.AccessToken, life, nil
}

// assertion is the JWT the client authenticates with: signed by its Key,
// for Kayra's token endpoint, a minute long, used once; or the platform's
// token, read from TokenFile.
func (s *ClientCredentials) assertion(m metadata) (string, error) {
	if s.opts.TokenFile != "" {
		data, err := os.ReadFile(s.opts.TokenFile)
		if err != nil {
			return "", fmt.Errorf("auth: the identity token: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	now := s.now()
	return s.opts.Key.Sign(map[string]any{
		"iss": s.opts.ClientID, "sub": s.opts.ClientID, "aud": m.TokenEndpoint,
		"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "jti": rand.Text(),
	})
}

// getJSON reads url's JSON document into out.
func (a *Client) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return infraerrors.Unavailable("%s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return infraerrors.Upstream(resp.StatusCode >= 500, "%s answered %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return infraerrors.Upstream(false, "%s: %v", url, err)
	}
	return nil
}
