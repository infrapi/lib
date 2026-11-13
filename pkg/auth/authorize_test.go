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

// authorizeStub serves a minimal stand-in for the authorization
// service's POST AuthorizePath: it checks the bearer token against want
// and, on match, answers with body (an {"allowed":...,"reason":...}
// object, as the real service does).
func authorizeStub(t *testing.T, want, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, AuthorizePath, r.URL.Path)

		if r.Header.Get("Authorization") != "Bearer "+want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestAuthorize_allowed(t *testing.T) {
	srv := authorizeStub(t, "good-token", `{"allowed":true}`)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	allowed, reason, err := client.Authorize(context.Background(), "good-token", AuthorizeRequest{
		Namespace: "core", Api: "compute.infrapi.io", Resource: "vm", Verb: "list",
	})
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.Empty(t, reason)
}

func TestAuthorize_denied(t *testing.T) {
	srv := authorizeStub(t, "good-token", `{"allowed":false,"reason":"your role does not grant this action"}`)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	allowed, reason, err := client.Authorize(context.Background(), "good-token", AuthorizeRequest{
		Namespace: "core", Api: "compute.infrapi.io", Resource: "vm", Verb: "delete",
	})
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Equal(t, "your role does not grant this action", reason)
}

func TestAuthorize_rejectsWrongToken(t *testing.T) {
	srv := authorizeStub(t, "good-token", `{}`)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	_, _, err = client.Authorize(context.Background(), "bad-token", AuthorizeRequest{})
	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, infraerrors.ErrorCode(err))
}

func TestAuthorize_rejectsEmptyToken(t *testing.T) {
	client, err := NewClient(ClientOptions{BaseURL: "http://unused.invalid"})
	require.NoError(t, err)

	_, _, err = client.Authorize(context.Background(), "", AuthorizeRequest{})
	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, infraerrors.ErrorCode(err))
}

func TestAuthorize_upstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	_, _, err = client.Authorize(context.Background(), "token", AuthorizeRequest{})
	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, infraerrors.ErrorCode(err))
}

func TestAuthorize_unreachable(t *testing.T) {
	client, err := NewClient(ClientOptions{BaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)

	_, _, err = client.Authorize(context.Background(), "token", AuthorizeRequest{})
	require.Error(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, infraerrors.ErrorCode(err))
}

func TestAuthorizePathFor(t *testing.T) {
	assert.Equal(t, AuthorizePath, AuthorizePathFor(""))
	assert.Equal(t, AuthorizePath, AuthorizePathFor(DefaultAPIDomain))
	assert.Equal(t, "/apis/authorization.example.com/v1/check", AuthorizePathFor("example.com"))
}

// A client for another API domain asks that domain's check endpoint, from
// Authorize and from RequireAccess alike.
func TestAuthorize_apiDomain(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allowed":true}`))
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL, APIDomain: "example.com"})
	require.NoError(t, err)

	allowed, _, err := client.Authorize(context.Background(), "good-token", AuthorizeRequest{
		Namespace: "core", Api: "compute.example.com", Resource: "vm", Verb: "list",
	})
	require.NoError(t, err)
	assert.True(t, allowed)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/apis/compute.example.com/v1/namespaces/core/vm", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	testAccessEngine(t, client).ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	assert.Equal(t, []string{"/apis/authorization.example.com/v1/check", "/apis/authorization.example.com/v1/check"}, paths)
}

func TestNewClient_rejectsInvalidAPIDomain(t *testing.T) {
	for _, domain := range []string{"not a domain", "Example.COM", "-bad.io"} {
		_, err := NewClient(ClientOptions{BaseURL: "http://unused.invalid", APIDomain: domain})
		assert.Error(t, err, domain)
	}
}
