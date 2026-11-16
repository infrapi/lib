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

// testCheckPath is the check endpoint withDiscovery's document names: not
// Kayra's usual path, so a test passes only when the client read it from
// the document.
const testCheckPath = "/apis/authorization.infrapi.io/v1beta1/check"

// withDiscovery serves Kayra's discovery document at DiscoveryPath, naming
// testCheckPath, and hands every other request to next.
func withDiscovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != DiscoveryPath {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"domain":"infrapi.io","apis":{"authorization":"/apis/authorization.infrapi.io/v1beta1"},` +
			`"endpoints":{"userinfo":"/oauth2/userinfo","check":"` + testCheckPath + `","jwks":"/oauth2/discovery/keys","apis":"/apis"}}`))
	})
}

// authorizeStub serves a minimal stand-in for the authorization
// service: its discovery document, and its check endpoint, which checks
// the bearer token against want and, on match, answers with body (an
// {"allowed":...,"reason":...} object, as the real service does).
func authorizeStub(t *testing.T, want, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(withDiscovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, testCheckPath, r.URL.Path)

		if r.Header.Get("Authorization") != "Bearer "+want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})))
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
	srv := httptest.NewServer(withDiscovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})))
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

// A 4xx from the check is about the request: Kayra's problem is passed on,
// status, detail and solution, rather than hidden behind a 502.
func TestAuthorize_passesOnRequestProblems(t *testing.T) {
	for _, tc := range []struct {
		name, body, detail, solution string
		status                       int
	}{
		{"unknown namespace", `{"status":404,"title":"Not Found","detail":"namespace \"core\" not found","origin":"catalog.CheckNamespace"}`,
			`namespace "core" not found`, "", http.StatusNotFound},
		{"unknown resource", `{"status":404,"title":"Not Found","detail":"api \"compute.infrapi.io\" has no resource \"keys\"","solution":"list its resources with infrapi-cli apis"}`,
			`api "compute.infrapi.io" has no resource "keys"`, "list its resources with infrapi-cli apis", http.StatusNotFound},
		{"not subscribed", `{"status":403,"title":"Forbidden","detail":"namespace \"core\" is not subscribed to api \"compute.infrapi.io\""}`,
			`namespace "core" is not subscribed to api "compute.infrapi.io"`, "", http.StatusForbidden},
		{"no body", ``, "authorization service answered Method Not Allowed", "", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(withDiscovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})))
			defer srv.Close()

			client, err := NewClient(ClientOptions{BaseURL: srv.URL})
			require.NoError(t, err)

			_, _, err = client.Authorize(context.Background(), "token", AuthorizeRequest{})
			require.Error(t, err)
			e := infraerrors.ErrorCast(err)
			require.NotNil(t, e)
			assert.Equal(t, tc.status, e.Code)
			assert.Equal(t, tc.detail, e.Message)
			assert.Equal(t, tc.solution, e.Solution)
		})
	}
}

// RequireAccess answers the caller with the check's own problem.
func TestRequireAccess_passesOnRequestProblems(t *testing.T) {
	srv := httptest.NewServer(withDiscovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"title":"Not Found","detail":"namespace \"core\" not found"}`))
	})))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/apis/compute.infrapi.io/v1/namespaces/core/vm", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	testAccessEngine(t, client).ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), `namespace \"core\" not found`)
}

// A 5xx is the service failing: a retryable 502, with its detail.
func TestAuthorize_serviceFailureDetail(t *testing.T) {
	srv := httptest.NewServer(withDiscovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":503,"detail":"no openapi spec stored for api \"compute.infrapi.io\""}`))
	})))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	_, _, err = client.Authorize(context.Background(), "token", AuthorizeRequest{})
	e := infraerrors.ErrorCast(err)
	require.NotNil(t, e)
	assert.Equal(t, http.StatusBadGateway, e.Code)
	assert.Contains(t, e.Message, `Service Unavailable: no openapi spec stored for api "compute.infrapi.io"`)
	require.NotNil(t, e.Retryable)
	assert.True(t, *e.Retryable)
}
