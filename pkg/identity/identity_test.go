package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKey(t *testing.T) {
	k, err := GenerateKey()
	require.NoError(t, err)
	data, err := k.PEM()
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "identity.pem")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	again, err := LoadKey(path)
	require.NoError(t, err)
	assert.Equal(t, k.KeyID(), again.KeyID(), "the kid is the key's thumbprint")

	// SEC 1, as openssl ecparam writes it
	der, err := x509.MarshalECPrivateKey(k.private)
	require.NoError(t, err)
	sec1, err := ParseKey(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	require.NoError(t, err)
	assert.Equal(t, k.KeyID(), sec1.KeyID())

	// RFC 7638: the thumbprint of {"crv","kty","x","y"}
	jwks := PublicJWKS(k)
	require.Len(t, jwks.Keys, 1)
	j := jwks.Keys[0]
	assert.Equal(t, []string{"EC", "P-256", "sig", "ES256", k.KeyID()}, []string{j.Kty, j.Crv, j.Use, j.Alg, j.Kid})
	sum := sha256.Sum256([]byte(`{"crv":"P-256","kty":"EC","x":"` + j.X + `","y":"` + j.Y + `"}`))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), k.KeyID())

	// refused: no PEM, a public key, RSA, another curve
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsaDER, _ := x509.MarshalPKCS8PrivateKey(rsaKey)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p384DER, _ := x509.MarshalPKCS8PrivateKey(p384)
	pubDER, _ := x509.MarshalPKIXPublicKey(&k.private.PublicKey)
	for name, bad := range map[string][]byte{
		"no PEM": []byte("nope"),
		"public": pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}),
		"RSA":    pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rsaDER}),
		"P-384":  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p384DER}),
	} {
		_, err := ParseKey(bad)
		assert.Error(t, err, name)
	}
	_, err = LoadKeys([]string{path, filepath.Join(t.TempDir(), "missing.pem")})
	assert.Error(t, err)
}

// Sign: an ES256 JWT, its kid the key's, verifiable with its public key.
func TestSign(t *testing.T) {
	k, err := GenerateKey()
	require.NoError(t, err)
	jwt, err := k.Sign(map[string]any{"sub": Subject("compute.infrapi.io")})
	require.NoError(t, err)
	parts := strings.Split(jwt, ".")
	require.Len(t, parts, 3)
	var header map[string]string
	h, _ := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, json.Unmarshal(h, &header))
	assert.Equal(t, map[string]string{"alg": "ES256", "typ": "JWT", "kid": k.KeyID()}, header)
	p, _ := base64.RawURLEncoding.DecodeString(parts[1])
	assert.JSONEq(t, `{"sub":"system:api:compute.infrapi.io"}`, string(p))
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	require.Len(t, sig, 64)
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	assert.True(t, ecdsa.Verify(&k.private.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])))
}
