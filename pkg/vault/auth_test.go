package vault

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogin_requiresAppRoleOrToken(t *testing.T) {
	client, err := NewClient(ClientOptions{Address: "http://vault.example.com"})
	require.NoError(t, err)

	err = client.Login(context.Background(), LoginOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires AppRole or Token")
}

func TestLogin_appRoleRequiresRoleAndSecret(t *testing.T) {
	client, err := NewClient(ClientOptions{Address: "http://vault.example.com"})
	require.NoError(t, err)

	err = client.Login(context.Background(), LoginOptions{AppRole: &AppRoleLogin{RoleID: "role"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RoleID and SecretID")
}

func TestLogin_token(t *testing.T) {
	client, err := NewClient(ClientOptions{Address: "http://vault.example.com"})
	require.NoError(t, err)

	assert.NoError(t, client.Login(context.Background(), LoginOptions{Token: "s.abc123"}))
}

func TestLogin_appRole(t *testing.T) {
	var requestedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		var body struct {
			RoleId   string `json:"role_id"`
			SecretId string `json:"secret_id"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "my-role", body.RoleId)
		assert.Equal(t, "my-secret", body.SecretId)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": nil,
			"auth": map[string]any{"client_token": "s.minted"},
		})
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	err := client.Login(context.Background(), LoginOptions{
		AppRole: &AppRoleLogin{RoleID: "my-role", SecretID: "my-secret"},
	})
	require.NoError(t, err)
	assert.Equal(t, "/v1/auth/approle/login", requestedPath)
}

// A response with no "data" element - like sys/health, or a malformed
// stub - makes vault-client-go fall back to parsing the whole body as
// Data, leaving Auth nil. Login must report that, not dereference it.
func TestLogin_appRoleNoAuthBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	err := client.Login(context.Background(), LoginOptions{AppRole: &AppRoleLogin{RoleID: "r", SecretID: "s"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no auth block")
}

func TestLogin_appRoleCustomMountPath(t *testing.T) {
	var requestedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "auth": map[string]any{"client_token": "s.minted"}})
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	err := client.Login(context.Background(), LoginOptions{
		AppRole: &AppRoleLogin{RoleID: "r", SecretID: "s", MountPath: "approle-eu"},
	})
	require.NoError(t, err)
	assert.Equal(t, "/v1/auth/approle-eu/login", requestedPath)
}
