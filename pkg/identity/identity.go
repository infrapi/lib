// Package identity is a service's own identity at Kayra: the API it serves,
// system:api:<group>, proven by a key of its own. The service signs its
// assertions with its private key (ES256), and serves the public half at
// <root>/-/jwks.json (server.WithIdentity), where Kayra's reconciler reads
// it when the API's registration says identity.jwks_from: spec.
// auth.ClientCredentials gets the API's tokens with it.
//
// Keys are EC P-256, in PEM: PKCS#8 ("PRIVATE KEY") or SEC 1 ("EC PRIVATE
// KEY"), as infrapi-cli app keygen and openssl write them.
package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// APIPrefix starts an API's subject at Kayra: system:api:compute.infrapi.io.
const APIPrefix = "system:api:"

// Subject is the API's subject at Kayra, its client id: system:api:<group>.
func Subject(group string) string { return APIPrefix + group }

// Key is a service's identity key, EC P-256.
type Key struct {
	private *ecdsa.PrivateKey
	kid     string
}

// GenerateKey makes a new key.
func GenerateKey() (*Key, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return newKey(k)
}

// ParseKey reads a PEM private key: PKCS#8 or SEC 1, EC P-256.
func ParseKey(data []byte) (*Key, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("identity: no PEM block")
	}
	var priv any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		priv, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		priv, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("identity: a %q PEM block: a private key, PRIVATE KEY or EC PRIVATE KEY", block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	k, ok := priv.(*ecdsa.PrivateKey)
	if !ok || k.Curve != elliptic.P256() {
		return nil, errors.New("identity: an EC P-256 key only")
	}
	return newKey(k)
}

// LoadKey reads the PEM key at path.
func LoadKey(path string) (*Key, error) {
	data, err := os.ReadFile(path) //#nosec G304 -- the key file the service's configuration names
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	k, err := ParseKey(data)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, path)
	}
	return k, nil
}

// LoadKeys reads each key of paths: the first signs, every one is
// published. To rotate, serve the new key after the old one, sync the
// registration, then serve the new one alone and sync again.
func LoadKeys(paths []string) ([]*Key, error) {
	keys := make([]*Key, 0, len(paths))
	for _, p := range paths {
		k, err := LoadKey(p)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func newKey(k *ecdsa.PrivateKey) (*Key, error) {
	key := &Key{private: k}
	jwk, err := key.publicJWK()
	if err != nil {
		return nil, err
	}
	// RFC 7638: the SHA-256 of the required members, in lexical order
	thumb, _ := json.Marshal(struct {
		Crv string `json:"crv"`
		Kty string `json:"kty"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}{jwk.Crv, jwk.Kty, jwk.X, jwk.Y})
	sum := sha256.Sum256(thumb)
	key.kid = b64(sum[:])
	return key, nil
}

// PEM is the private key, PKCS#8.
func (k *Key) PEM() ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k.private)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// KeyID is the key's id: its RFC 7638 thumbprint.
func (k *Key) KeyID() string { return k.kid }

// JWK is a public key as JSON Web Key (RFC 7517).
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Kid string `json:"kid,omitempty"`
	Use string `json:"use,omitempty"`
	Alg string `json:"alg,omitempty"`
}

// JWKS is a JWK set, what <root>/-/jwks.json serves.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// PublicJWKS is the public half of keys.
func PublicJWKS(keys ...*Key) JWKS {
	set := JWKS{Keys: []JWK{}}
	for _, k := range keys {
		jwk, _ := k.publicJWK()
		jwk.Kid, jwk.Use, jwk.Alg = k.kid, "sig", "ES256"
		set.Keys = append(set.Keys, jwk)
	}
	return set
}

func (k *Key) publicJWK() (JWK, error) {
	pub, err := k.private.PublicKey.Bytes() // 0x04 || X || Y
	if err != nil {
		return JWK{}, fmt.Errorf("identity: %w", err)
	}
	return JWK{Kty: "EC", Crv: "P-256", X: b64(pub[1:33]), Y: b64(pub[33:65])}, nil
}

// Sign is a JWT of claims signed by k, ES256, its kid in the header.
func (k *Key) Sign(claims map[string]any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": k.kid})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, k.private, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64(sig), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
