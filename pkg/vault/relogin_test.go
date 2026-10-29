package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// appRoleStub answers AppRole logins with a new token each, of a 20 minute
// lease, and KV v2 reads of secret/creds to a token it still accepts.
type appRoleStub struct {
	mu     sync.Mutex
	logins int
	reads  int
	valid  map[string]bool
}

func newAppRoleStub(t *testing.T) (*appRoleStub, *httptest.Server) {
	t.Helper()
	s := &appRoleStub{valid: map[string]bool{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *appRoleStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	var reply any
	switch r.URL.Path {
	case "/v1/auth/approle/login":
		var body struct {
			RoleID   string `json:"role_id"`
			SecretID string `json:"secret_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RoleID != "role" || body.SecretID != "s3cret" {
			w.WriteHeader(http.StatusBadRequest)
			reply = map[string]any{"errors": []string{"invalid role or secret ID"}}
			break
		}
		s.logins++
		token := fmt.Sprintf("s.token%d", s.logins)
		s.valid[token] = true
		// Vault answers with "data": null beside "auth".
		reply = map[string]any{"data": nil, "auth": map[string]any{"client_token": token, "lease_duration": 1200}}
	case "/v1/secret/data/creds":
		s.reads++
		if !s.valid[r.Header.Get("X-Vault-Token")] {
			w.WriteHeader(http.StatusForbidden)
			reply = map[string]any{"errors": []string{"permission denied"}}
			break
		}
		reply = map[string]any{"data": map[string]any{"data": map[string]any{"user": "svc"}, "metadata": map[string]any{}}}
	default:
		w.WriteHeader(http.StatusNotFound)
		reply = map[string]any{"errors": []string{}}
	}
	if err := json.NewEncoder(w).Encode(reply); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *appRoleStub) revokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.valid)
}

func appRoleClient(t *testing.T, addr string) *Client {
	t.Helper()
	client, err := NewClient(ClientOptions{Address: addr, RetryCount: -1})
	require.NoError(t, err)
	require.NoError(t, client.Login(context.Background(), LoginOptions{AppRole: &AppRoleLogin{RoleID: "role", SecretID: "s3cret"}}))
	return client
}

func TestAppRole_logsInAgainWhenRefused(t *testing.T) {
	stub, srv := newAppRoleStub(t)
	client := appRoleClient(t, srv.URL)

	stub.revokeAll()
	data, err := client.GetSecret(context.Background(), "secret", "creds")
	require.NoError(t, err)
	assert.Equal(t, "svc", data["user"])
	assert.Equal(t, 2, stub.logins)
	assert.Equal(t, 2, stub.reads, "refused once, then read with the new token")
}

func TestAppRole_logsInAgainBeforeExpiry(t *testing.T) {
	stub, srv := newAppRoleStub(t)
	client := appRoleClient(t, srv.URL)
	clock := time.Now()
	client.now = func() time.Time { return clock }

	_, err := client.GetSecret(context.Background(), "secret", "creds")
	require.NoError(t, err)
	assert.Equal(t, 1, stub.logins, "the token serves while it is valid")

	clock = clock.Add(19 * time.Minute)
	_, err = client.GetSecret(context.Background(), "secret", "creds")
	require.NoError(t, err)
	assert.Equal(t, 2, stub.logins, "logged in again before the 20 minute lease ends")
	assert.Equal(t, 2, stub.reads, "without a refused call")
}

// Calls refused at once log in again once: the others use its token.
func TestAppRole_logsInAgainOnce(t *testing.T) {
	stub, srv := newAppRoleStub(t)
	client := appRoleClient(t, srv.URL)
	_, err := client.GetSecret(context.Background(), "secret", "creds")
	require.NoError(t, err)

	stub.revokeAll()
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Go(func() {
			_, err := client.GetSecret(context.Background(), "secret", "creds")
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	assert.Equal(t, 2, stub.logins)
}

func TestAppRole_secretIDFile(t *testing.T) {
	stub, srv := newAppRoleStub(t)
	file := filepath.Join(t.TempDir(), "secret-id")
	require.NoError(t, os.WriteFile(file, []byte("s3cret\n"), 0o600))

	client := testClient(t, srv.URL)
	require.NoError(t, client.Login(context.Background(), LoginOptions{AppRole: &AppRoleLogin{RoleID: "role", SecretIDFile: file}}))
	assert.Equal(t, 1, stub.logins)
}

func TestAppRole_refused(t *testing.T) {
	_, srv := newAppRoleStub(t)
	client := testClient(t, srv.URL)

	err := client.Login(context.Background(), LoginOptions{AppRole: &AppRoleLogin{RoleID: "role", SecretID: "wrong"}})
	var e *infraerrors.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, http.StatusBadGateway, e.Code)
	assert.Equal(t, "upstream_auth", e.Slug)
}

// A token given to Login is not the client's to replace: Vault refusing it
// is the caller's error.
func TestToken_notRenewed(t *testing.T) {
	stub, srv := newAppRoleStub(t)
	client := testClient(t, srv.URL)
	require.NoError(t, client.Login(context.Background(), LoginOptions{Token: "s.out-of-band"}))

	_, err := client.GetSecret(context.Background(), "secret", "creds")
	var e *infraerrors.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "upstream_auth", e.Slug)
	assert.Equal(t, 0, stub.logins)
	assert.Equal(t, 1, stub.reads)
}

func TestErrors(t *testing.T) {
	_, srv := newAppRoleStub(t)
	client := appRoleClient(t, srv.URL)

	_, err := client.GetSecret(context.Background(), "secret", "missing")
	var e *infraerrors.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, http.StatusNotFound, e.Code)
	assert.Contains(t, e.Error(), `vault: get secret "missing": not found`)

	srv.Close()
	_, err = client.GetSecret(context.Background(), "secret", "creds")
	require.ErrorAs(t, err, &e)
	assert.Equal(t, http.StatusServiceUnavailable, e.Code)
	require.NotNil(t, e.Retryable)
	assert.True(t, *e.Retryable)
}
