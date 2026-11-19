package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each root's protected resource metadata, without a token, at its -/ and
// at the well-known URI; 503 while the authorization server is unknown.
func TestWithProtectedResource(t *testing.T) {
	cfg := testAppConfig(t)
	issuer := "https://kayra.example.com"
	var fail error
	app, err := NewServerGin(cfg, []string{testRoot}, WithAuth(gateDeny),
		WithProtectedResource(func(context.Context) (string, error) { return issuer, fail }))
	require.NoError(t, err)
	for _, path := range []string{testRoot + ProtectedResourcePath, WellKnownProtectedResource + testRoot} {
		w := get(t, app, path)
		require.Equal(t, http.StatusOK, w.Code, path)
		var pr ProtectedResource
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pr))
		assert.Equal(t, strings.TrimRight(cfg.AppUrl, "/")+testRoot, pr.Resource, path)
		assert.Equal(t, []string{issuer}, pr.AuthorizationServers, path)
		assert.Equal(t, []string{"header"}, pr.BearerMethodsSupported, path)
	}
	fail = errors.New("kayra is down")
	assert.Equal(t, http.StatusServiceUnavailable, get(t, app, testRoot+ProtectedResourcePath).Code)

	_, err = NewServerGin(cfg, []string{testRoot}, WithProtectedResource(nil))
	assert.Error(t, err)
	plain, err := NewServerGin(cfg, []string{testRoot})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, get(t, plain, testRoot+ProtectedResourcePath).Code)
}
