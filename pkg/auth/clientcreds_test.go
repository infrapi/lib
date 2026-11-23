package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/identity"
)

// fakeAS is Kayra's authorization server: its metadata, with a public
// issuer the service cannot reach, and its token endpoint, checking the
// assertion against the API's key (or the platform's token).
type fakeAS struct {
	srv      *httptest.Server
	key      *identity.Key
	jobToken string
	issued   atomic.Int32
	down     atomic.Bool
	mu       sync.Mutex
	form     map[string][]string
}

func newFakeAS(t *testing.T, key *identity.Key) *fakeAS {
	f := &fakeAS{key: key}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const issuer = "https://kayra.example.com"
		switch r.URL.Path {
		case AuthorizationServerMetadataPath:
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "token_endpoint": issuer + "/oauth2/token"})
		case "/oauth2/token":
			if f.down.Load() {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_ = r.ParseForm()
			f.mu.Lock()
			f.form = r.PostForm
			f.mu.Unlock()
			if ok, why := f.valid(r.PostForm.Get("client_assertion"), issuer+"/oauth2/token"); !ok {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client", "error_description": why})
				return
			}
			n := f.issued.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token-" + string(rune('0'+n)), "token_type": "Bearer", "expires_in": 100})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAS) valid(assertion, aud string) (bool, string) {
	if f.key == nil {
		return assertion == f.jobToken, "not the job's token"
	}
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return false, "no JWT"
	}
	jwk := identity.PublicJWKS(f.key).Keys[0]
	x, _ := base64.RawURLEncoding.DecodeString(jwk.X)
	y, _ := base64.RawURLEncoding.DecodeString(jwk.Y)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
	if err != nil {
		return false, "key"
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return false, "signature"
	}
	var c map[string]any
	p, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(p, &c)
	if c["aud"] != aud || c["iss"] != "system:api:compute.infrapi.io" || c["sub"] != c["iss"] || c["jti"] == "" {
		return false, "claims"
	}
	return true, ""
}

func TestClientCredentials(t *testing.T) {
	key, err := identity.GenerateKey()
	require.NoError(t, err)
	as := newFakeAS(t, key)
	kayra, err := NewClient(ClientOptions{BaseURL: as.srv.URL})
	require.NoError(t, err)
	now := time.Now()
	clock := func() time.Time { return now }
	src, err := NewClientCredentials(ClientCredentialsOptions{Kayra: kayra, ClientID: identity.Subject("compute.infrapi.io"), Key: key,
		Resources: []string{"https://gateway.example.com/apis/network.infrapi.io/v1"}, Now: clock})
	require.NoError(t, err)
	ctx := context.Background()

	tok, err := src.Token(ctx)
	require.NoError(t, err)
	assert.Equal(t, "token-1", tok)
	as.mu.Lock()
	assert.Equal(t, []string{"https://gateway.example.com/apis/network.infrapi.io/v1"}, as.form["resource"])
	assert.Equal(t, []string{"client_credentials"}, as.form["grant_type"])
	assert.Equal(t, []string{JWTBearer}, as.form["client_assertion_type"])
	as.mu.Unlock()

	// kept until 80% of its life
	now = now.Add(79 * time.Second)
	tok, _ = src.Token(ctx)
	assert.Equal(t, "token-1", tok)
	now = now.Add(2 * time.Second)
	tok, _ = src.Token(ctx)
	assert.Equal(t, "token-2", tok)

	// Kayra down: the kept one until it expires
	as.down.Store(true)
	now = now.Add(90 * time.Second)
	tok, err = src.Token(ctx)
	require.NoError(t, err)
	assert.Equal(t, "token-2", tok)
	now = now.Add(20 * time.Second)
	_, err = src.Token(ctx)
	assert.Error(t, err)

	// another key: Kayra's refusal, its reason
	other, _ := identity.GenerateKey()
	as.down.Store(false)
	bad, _ := NewClientCredentials(ClientCredentialsOptions{Kayra: kayra, ClientID: identity.Subject("compute.infrapi.io"), Key: other})
	_, err = bad.Token(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_client: signature")

	// concurrent callers share the token
	fresh, _ := NewClientCredentials(ClientCredentialsOptions{Kayra: kayra, ClientID: identity.Subject("compute.infrapi.io"), Key: key})
	before := as.issued.Load()
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { _, _ = fresh.Token(ctx) })
	}
	wg.Wait()
	assert.Equal(t, before+1, as.issued.Load())
}

// TokenFile: the platform's token, read again for every token asked.
func TestClientCredentialsTokenFile(t *testing.T) {
	as := newFakeAS(t, nil)
	as.jobToken = "k8s-token-1"
	kayra, _ := NewClient(ClientOptions{BaseURL: as.srv.URL})
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("k8s-token-1\n"), 0o600))
	now := time.Now()
	src, err := NewClientCredentials(ClientCredentialsOptions{Kayra: kayra, ClientID: identity.Subject("compute.infrapi.io"), TokenFile: path, Now: func() time.Time { return now }})
	require.NoError(t, err)
	_, err = src.Token(context.Background())
	require.NoError(t, err)

	// rotated by the kubelet
	as.jobToken = "k8s-token-2"
	require.NoError(t, os.WriteFile(path, []byte("k8s-token-2"), 0o600))
	now = now.Add(time.Hour)
	_, err = src.Token(context.Background())
	require.NoError(t, err)

	for _, opts := range []ClientCredentialsOptions{
		{ClientID: "x", TokenFile: path},
		{Kayra: kayra, TokenFile: path},
		{Kayra: kayra, ClientID: "x"},
		{Kayra: kayra, ClientID: "x", TokenFile: path, Key: &identity.Key{}},
	} {
		_, err := NewClientCredentials(opts)
		assert.Error(t, err)
	}
}
