package tracing

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/infrapi/lib/pkg/config"
)

// pki is a private CA, a server certificate for 127.0.0.1 and a client
// certificate it signed, as PEM files in a temporary directory.
type pki struct {
	dir                          string
	caPEM, serverCert, serverKey string
	clientCert, clientKey        string
	pool                         *x509.CertPool
	serverTLS                    tls.Certificate
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	p := &pki{dir: t.TempDir(), pool: x509.NewCertPool()}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	p.pool.AddCert(ca)
	p.caPEM = p.write(t, "ca.pem", "CERTIFICATE", caDER)

	leaf := func(name string, serial int64, usage x509.ExtKeyUsage) (string, string, tls.Certificate) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		require.NoError(t, err)
		keyDER, err := x509.MarshalECPrivateKey(key)
		require.NoError(t, err)
		certPath := p.write(t, name+".pem", "CERTIFICATE", der)
		keyPath := p.write(t, name+"-key.pem", "EC PRIVATE KEY", keyDER)
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		require.NoError(t, err)
		return certPath, keyPath, pair
	}
	p.serverCert, p.serverKey, p.serverTLS = leaf("server", 2, x509.ExtKeyUsageServerAuth)
	p.clientCert, p.clientKey, _ = leaf("client", 3, x509.ExtKeyUsageClientAuth)
	return p
}

func (p *pki) write(t *testing.T, name, kind string, der []byte) string {
	t.Helper()
	path := filepath.Join(p.dir, name)
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600))
	return path
}

// collector is an OTLP/HTTP receiver over TLS, signed by the private CA, that
// requires a client certificate the same CA signed.
func (p *pki) collector(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var posts atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/v1/traces" {
			posts.Add(1)
		}
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{p.serverTLS}, ClientCAs: p.pool,
		ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &posts
}

// export sends one span through Setup with cfg and waits for the flush.
func export(t *testing.T, cfg *config.TracingConfig) {
	t.Helper()
	restore(t)
	shutdown, err := Setup(context.Background(), app, cfg)
	require.NoError(t, err)
	_, span := otel.Tracer("test").Start(context.Background(), "work")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Logf("shutdown: %v", err) // an export the collector refused
	}
}

// A private CA's collector, with mTLS: the export goes through with the CA and
// the client certificate, and not without either.
func TestSetup_privateCAAndMTLS(t *testing.T) {
	p := newPKI(t)
	srv, posts := p.collector(t)
	base := config.TracingConfig{Enabled: true, Endpoint: srv.URL, Protocol: "http", SampleRatio: 1}

	ok := base
	ok.CACertPath, ok.ClientCertPath, ok.ClientKeyPath = p.caPEM, p.clientCert, p.clientKey
	export(t, &ok)
	assert.Equal(t, int32(1), posts.Load(), "the private CA trusted, the client certificate presented")

	noClient := base
	noClient.CACertPath = p.caPEM
	export(t, &noClient)
	assert.Equal(t, int32(1), posts.Load(), "no client certificate: the collector refuses")

	noCA := base
	noCA.ClientCertPath, noCA.ClientKeyPath = p.clientCert, p.clientKey
	export(t, &noCA)
	assert.Equal(t, int32(1), posts.Load(), "the system's CAs alone do not know the private CA")
}

// gRPC takes the same TLS; it connects lazily, so Setup does not wait.
func TestSetup_grpcTLS(t *testing.T) {
	p := newPKI(t)
	restore(t)
	shutdown, err := Setup(context.Background(), app, &config.TracingConfig{Enabled: true, Protocol: "grpc",
		Endpoint: "https://127.0.0.1:1", CACertPath: p.caPEM, ClientCertPath: p.clientCert, ClientKeyPath: p.clientKey})
	require.NoError(t, err)
	assert.NoError(t, shutdown(context.Background()))
}

// A file that cannot be read or holds no certificate stops the service at
// start, rather than every export failing later.
func TestTLSConfig_errors(t *testing.T) {
	p := newPKI(t)
	notPEM := filepath.Join(p.dir, "not.pem")
	require.NoError(t, os.WriteFile(notPEM, []byte("hello"), 0o600))
	for name, c := range map[string]struct {
		cfg  config.TracingConfig
		want string
	}{
		"a missing CA":        {config.TracingConfig{CACertPath: filepath.Join(p.dir, "nope.pem")}, "CA certificate"},
		"a CA that is no PEM": {config.TracingConfig{CACertPath: notPEM}, "no PEM certificate"},
		"a key of another":    {config.TracingConfig{ClientCertPath: p.clientCert, ClientKeyPath: p.serverKey}, "client certificate"},
	} {
		_, err := tlsConfig(&c.cfg)
		assert.ErrorContains(t, err, c.want, name)
	}
	tc, err := tlsConfig(&config.TracingConfig{})
	require.NoError(t, err)
	assert.Nil(t, tc, "nothing set: the exporter's own TLS, the system's CAs")
}
