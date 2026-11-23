package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/identity"
)

// Each root's -/jwks.json, without a token: every key's public half.
func TestWithIdentity(t *testing.T) {
	cfg := testAppConfig(t)
	k1, err := identity.GenerateKey()
	require.NoError(t, err)
	k2, err := identity.GenerateKey()
	require.NoError(t, err)
	app, err := NewServerGin(cfg, []string{testRoot}, WithAuth(gateDeny), WithIdentity(k1, k2))
	require.NoError(t, err)
	w := get(t, app, testRoot+JWKSPath)
	require.Equal(t, http.StatusOK, w.Code)
	var set identity.JWKS
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &set))
	require.Len(t, set.Keys, 2)
	assert.Equal(t, k1.KeyID(), set.Keys[0].Kid)
	assert.Equal(t, k2.KeyID(), set.Keys[1].Kid)
	assert.NotContains(t, w.Body.String(), `"d"`)

	plain, err := NewServerGin(cfg, []string{testRoot})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, get(t, plain, testRoot+JWKSPath).Code)
}
