package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokens is Kayra, its userinfo per token: "okta" has no aud (another
// provider's), the others are its authorization server's.
func tokensStub(t *testing.T) *httptest.Server {
	t.Helper()
	users := map[string]string{
		"okta":    `{"sub":"jdoe"}`,
		"gateway": `{"sub":"jdoe","aud":"https://gw.example.com","client_id":"infrapi-cli"}`,
		"compute": `{"sub":"jdoe","aud":["https://gw.example.com/apis/compute.infrapi.io/v1"],"client_id":"https://claude.ai/client.json"}`,
		"vault":   `{"sub":"jdoe","aud":"https://gw.example.com/apis/vault.infrapi.io/v1","client_id":"x"}`,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case DiscoveryPath:
			_, _ = w.Write([]byte(`{"domain":"infrapi.io","issuer":"https://kayra.example.com","endpoints":{"userinfo":"/oauth2/userinfo","check":"/check"}}`))
		case "/check":
			_, _ = w.Write([]byte(`{"allowed":true}`))
		default:
			body, ok := users[r.Header.Get("Authorization")[len("Bearer "):]]
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"status":401,"title":"Unauthorized","detail":"unknown token"}`))
				return
			}
			_, _ = w.Write([]byte(body))
		}
	}))
}

// A 401 names the request's resource metadata; a token of Kayra's
// authorization server must be for the request's resource, or a URL it is
// under; another provider's token, without an aud, is not checked here.
func TestResourceServer(t *testing.T) {
	svc := tokensStub(t)
	defer svc.Close()
	client, err := NewClient(ClientOptions{BaseURL: svc.URL, PublicURL: "https://gw.example.com/", CacheTTL: -1})
	require.NoError(t, err)
	for name, mw := range map[string]gin.HandlerFunc{"Middleware": client.Middleware(), "RequireAccess": client.RequireAccess()} {
		app := gin.New()
		app.GET("/apis/compute.infrapi.io/v1/namespaces/core/vms", mw, func(c *gin.Context) { c.Status(http.StatusOK) })
		call := func(token string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/apis/compute.infrapi.io/v1/namespaces/core/vms", nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			app.ServeHTTP(w, req)
			return w
		}
		meta := `Bearer resource_metadata="https://gw.example.com/apis/compute.infrapi.io/v1/-/oauth-protected-resource"`
		w := call("")
		assert.Equal(t, http.StatusUnauthorized, w.Code, name)
		assert.Equal(t, meta, w.Header().Get("WWW-Authenticate"), name)
		w = call("unknown")
		assert.Equal(t, http.StatusUnauthorized, w.Code, name)
		assert.Equal(t, meta+`, error="invalid_token"`, w.Header().Get("WWW-Authenticate"), name)
		for token, want := range map[string]int{"okta": 200, "gateway": 200, "compute": 200, "vault": 401} {
			assert.Equal(t, want, call(token).Code, name+": "+token)
		}
		w = call("vault")
		assert.Contains(t, w.Body.String(), "issued for https://gw.example.com/apis/vault.infrapi.io/v1, not for https://gw.example.com/apis/compute.infrapi.io/v1", name)
		assert.Contains(t, w.Header().Get("WWW-Authenticate"), `error="invalid_token"`, name)
	}

	// without PublicURL: no resource, nothing checked, a bare challenge
	plain, err := NewClient(ClientOptions{BaseURL: svc.URL, CacheTTL: -1})
	require.NoError(t, err)
	app := gin.New()
	app.GET("/apis/compute.infrapi.io/v1/namespaces/core/vms", plain.Middleware(), func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/apis/compute.infrapi.io/v1/namespaces/core/vms", nil)
	req.Header.Set("Authorization", "Bearer vault")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	issuer, err := client.AuthorizationServer(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://kayra.example.com", issuer)
}

func TestUserInfoAudience(t *testing.T) {
	var u UserInfo
	require.NoError(t, json.Unmarshal([]byte(`{"sub":"x","aud":["a","b"],"client_id":"c","email":"x@example.com"}`), &u))
	assert.Equal(t, []string{"a", "b"}, u.Audience)
	assert.Equal(t, "c", u.ClientID)
	assert.Equal(t, map[string]string{"email": "x@example.com"}, u.Attributes)
}
