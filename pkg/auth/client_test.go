package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// authStub serves a minimal stand-in for the authentication service's
// POST /oauth2/userinfo: it checks the bearer token against want and, on
// match, answers with body (a sub/provider/authorization/groups object
// with directory attributes flattened in, as the real service does).
func authStub(t *testing.T, want, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, UserinfoPath, r.URL.Path)

		if r.Header.Get("Authorization") != "Bearer "+want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestNewClient_requiresBaseURL(t *testing.T) {
	_, err := NewClient(ClientOptions{})
	assert.Error(t, err)
}

func TestVerify_resolvesUserInfo(t *testing.T) {
	srv := authStub(t, "good-token",
		`{"sub":"jdoe","provider":"okta","authorization":"idm","groups":["sre","sandbox-apps"],"email":"jdoe@example.com"}`)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	info, err := client.Verify(context.Background(), "good-token")
	require.NoError(t, err)
	assert.Equal(t, "jdoe", info.Subject)
	assert.Equal(t, "okta", info.Provider)
	assert.Equal(t, "idm", info.Authorization)
	assert.Equal(t, []string{"sre", "sandbox-apps"}, info.Groups)
	assert.Equal(t, map[string]string{"email": "jdoe@example.com"}, info.Attributes)
}

func TestVerify_rejectsWrongToken(t *testing.T) {
	srv := authStub(t, "good-token", `{}`)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	_, err = client.Verify(context.Background(), "bad-token")
	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, infraerrors.ErrorCode(err))
}

func TestVerify_rejectsEmptyToken(t *testing.T) {
	client, err := NewClient(ClientOptions{BaseURL: "http://unused.invalid"})
	require.NoError(t, err)

	_, err = client.Verify(context.Background(), "")
	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, infraerrors.ErrorCode(err))
}

func TestVerify_upstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	_, err = client.Verify(context.Background(), "token")
	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, infraerrors.ErrorCode(err))
}

func TestVerify_unreachable(t *testing.T) {
	client, err := NewClient(ClientOptions{BaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)

	_, err = client.Verify(context.Background(), "token")
	require.Error(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, infraerrors.ErrorCode(err))
}
