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

const testCertPEM = "-----BEGIN CERTIFICATE-----\nMIIB...fake...\n-----END CERTIFICATE-----\n"

func pkiStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/pki/cert/12:34:56" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":["no certificate"]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"certificate": testCertPEM},
		})
	}))
}

func TestGetCertificate(t *testing.T) {
	srv := pkiStub(t)
	defer srv.Close()
	client := testClient(t, srv.URL)

	cert, err := client.GetCertificate(context.Background(), "pki", "12:34:56")
	require.NoError(t, err)
	assert.Equal(t, testCertPEM, string(cert))
}

func TestGetCertificate_notFound(t *testing.T) {
	srv := pkiStub(t)
	defer srv.Close()
	client := testClient(t, srv.URL)

	_, err := client.GetCertificate(context.Background(), "pki", "missing")
	assert.Error(t, err)
}
