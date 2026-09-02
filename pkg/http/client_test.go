package http

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	nhttp "net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// generateTestCert creates a self-signed CA certificate and returns PEM-encoded cert and key.
func generateTestCert(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generateTestCert: generate key: %s", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-ca"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("generateTestCert: create cert: %s", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return
}

var api_client_server *nhttp.Server

func setup_api_client_server(t *testing.T) {
	serverMux := nhttp.NewServeMux()

	serverMux.HandleFunc("/api/v1/puppet/ok", func(w nhttp.ResponseWriter, r *nhttp.Request) {
		if _, err := w.Write([]byte("It works!")); err != nil {
			t.Fatalf("client_test.go(WriteOk): %s", err)
		}
	})

	serverMux.HandleFunc("/api/v1/puppet/slow", func(w nhttp.ResponseWriter, r *nhttp.Request) {
		time.Sleep(9999 * time.Second)
		if _, err := w.Write([]byte("This will never return!!!!!")); err != nil {
			t.Fatalf("client_test.go(WriteSlow): %s", err)
		}
	})

	serverMux.HandleFunc("/api/v1/puppet/redirect", func(w nhttp.ResponseWriter, r *nhttp.Request) {
		nhttp.Redirect(w, r, "/api/v1/puppet/ok", nhttp.StatusPermanentRedirect)
	})

	// Handler for retry testing
	var retryCounter int32
	serverMux.HandleFunc("/api/v1/puppet/retry", func(w nhttp.ResponseWriter, r *nhttp.Request) {
		count := atomic.AddInt32(&retryCounter, 1)
		if count < 3 {
			w.WriteHeader(nhttp.StatusInternalServerError)
			if _, err := w.Write([]byte("Server error")); err != nil {
				t.Fatalf("client_test.go(WriteRetry): %s", err)
			}
			return
		}
		w.WriteHeader(nhttp.StatusOK)
		if _, err := w.Write([]byte("Success after retries")); err != nil {
			t.Fatalf("client_test.go(WriteRetry): %s", err)
		}
	})

	// Handler for 429 testing
	var rateLimitCounter int32
	serverMux.HandleFunc("/api/v1/puppet/ratelimit", func(w nhttp.ResponseWriter, r *nhttp.Request) {
		count := atomic.AddInt32(&rateLimitCounter, 1)
		if count < 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(nhttp.StatusTooManyRequests)
			if _, err := w.Write([]byte("Rate limited")); err != nil {
				t.Fatalf("client_test.go(WriteRateLimit): %s", err)
			}
			return
		}
		w.WriteHeader(nhttp.StatusOK)
		if _, err := w.Write([]byte("Success after rate limit")); err != nil {
			t.Fatalf("client_test.go(WriteRateLimit): %s", err)
		}
	})

	api_client_server = &nhttp.Server{
		Addr:    "127.0.0.1:8080",
		Handler: serverMux,
	}
	go api_client_server.ListenAndServe() //nolint:errcheck

	/* let the server start */
	time.Sleep(1 * time.Second)
}

func shutdown_api_client_server() {
	_ = api_client_server.Close()
}

func TestAPIClient_NewClient(t *testing.T) {
	setup_api_client_server(t)
	defer shutdown_api_client_server()

	_, err := NewClient(nil)
	if err == nil {
		t.Fatalf("client_test.go(NewClient): nil opts should returns error")
	}

	_, err = NewClient(&ClientOptions{
		Server:      "127.0.0.1",
		Application: "puppet",
	})
	if err != nil {
		t.Fatalf("client_test.go(NewClientMandatory): %s", err)
	}

	_, err = NewClient(&ClientOptions{
		Server: "127.0.0.1",
	})
	if err != nil {
		if err.Error() != "missing mandatory attribute Application" {
			t.Fatalf("client_test.go(NewClient): %s", err)
		}
	} else {
		t.Fatalf("client_test.go(NewClient): missing Application should returns error")
	}

	_, err = NewClient(&ClientOptions{
		Application: "puppet",
	})
	if err != nil {
		if err.Error() != "missing mandatory attribute Server" {
			t.Fatalf("client_test.go(NewClient): %s", err)
		}
	} else {
		t.Fatalf("client_test.go(NewClient): missing Server should returns error")
	}

	client, err := NewClient(&ClientOptions{
		Server:        "127.0.0.1",
		Token:         "empty",
		GlobalTimeout: 2,
		APIVersion:    "v1",
		Application:   "puppet",
	})
	if err != nil {
		t.Fatalf("client_test.go(NewClient): %s", err)
	}
	if client.Port != 443 {
		t.Fatalf("client_test.go(NewClient): expect port 443 but got %d", client.Port)
	}
	if client.Protocol != "https" {
		t.Fatalf("client_test.go(NewClient): expect protocol https but got %s", client.Protocol)
	}
}

func TestAPIClient_CustomUserAgent(t *testing.T) {
	customAgent := "custom-user-agent/1.0"
	client, err := NewClient(&ClientOptions{
		Server:      "127.0.0.1",
		Application: "puppet",
		UserAgent:   customAgent,
		Protocol:    "http",
		Port:        8080,
	})
	if err != nil {
		t.Fatalf("client_test.go(NewClient): %s", err)
	}

	// Verify the user agent is set correctly
	if client.RestyClient.Header().Get("User-Agent") != customAgent {
		t.Fatalf("client_test.go(CustomUserAgent): expected '%s' but got '%s'", customAgent, client.RestyClient.Header().Get("User-Agent"))
	}
}

func TestAPIClient_DefaultUserAgent(t *testing.T) {
	client, err := NewClient(&ClientOptions{
		Server:      "127.0.0.1",
		Application: "puppet",
		Protocol:    "http",
		Port:        8080,
	})
	if err != nil {
		t.Fatalf("client_test.go(NewClient): %s", err)
	}

	expectedAgent := "infrapi-lib-http-puppet"
	if client.RestyClient.Header().Get("User-Agent") != expectedAgent {
		t.Fatalf("client_test.go(DefaultUserAgent): expected '%s' but got '%s'", expectedAgent, client.RestyClient.Header().Get("User-Agent"))
	}
}

func TestAPIClient_methods(t *testing.T) {
	setup_api_client_server(t)
	defer shutdown_api_client_server()

	client, err := NewClient(&ClientOptions{
		Server:        "127.0.0.1",
		Token:         "empty",
		GlobalTimeout: 2,
		APIVersion:    "v1",
		Application:   "puppet",
		Protocol:      "http",
		Port:          8080,
		RetryCount:    0, // Disable retries for basic tests
	})
	if err != nil {
		t.Fatalf("client_test.go(NewClient): %s", err)
	}

	res, err := client.Do("GET", "redirect", nil)
	if err != nil {
		t.Fatalf("client_test.go(redirect): %s", err)
	}
	if string(res.Body) != "It works!" {
		t.Fatalf("client_test.go(redirect): Got back '%#v' but expected 'It works!'\n", res)
	}

	/* Verify timeout works */
	_, err = client.Do("GET", "slow", nil)
	if err == nil {
		t.Fatalf("client_test.go(slow): Timeout did not trigger on slow request")
	}

	// head
	res, err = client.Head("ok", nil)
	if err != nil {
		t.Fatalf("client_test.go(head): %s", err)
	}
	if string(res.Body) != "" {
		t.Fatalf("client_test.go(head): Got back '%#v' but expected empty body\n", res)
	}

	res, err = client.Do("GET", "ok", nil)
	checkOk(t, res, err, "GET")

	res, err = client.Get("ok", &RequestOptions{Server: "127.0.0.1"})
	checkOk(t, res, err, "GET")

	res, err = client.Post("ok", &RequestOptions{Data: []byte("hello")})
	checkOk(t, res, err, "POST")

	res, err = client.Delete("ok", &RequestOptions{Headers: map[string]string{"hello": "world"}})
	checkOk(t, res, err, "DELETE")

	res, err = client.Patch("ok", nil)
	checkOk(t, res, err, "PATCH")

	res, err = client.Put("ok", nil)
	checkOk(t, res, err, "PUT")

	res, err = client.Get("fail", nil)
	if err == nil {
		t.Fatalf("client_test.go(getFail): should return error")
	}
	if res.Code != 404 {
		t.Fatalf("client_test.go(getFail): expect 404 but got %d", res.Code)
	}

	_, err = client.Get("fail", &RequestOptions{Server: "bad.local"})
	if err == nil {
		t.Fatalf("client_test.go(getFail): should return error")
	}
}

func TestAPIClient_Retry(t *testing.T) {
	setup_api_client_server(t)
	defer shutdown_api_client_server()

	client, err := NewClient(&ClientOptions{
		Server:           "127.0.0.1",
		Application:      "puppet",
		Protocol:         "http",
		Port:             8080,
		RetryCount:       3,
		RetryWaitTime:    100 * time.Millisecond,
		RetryMaxWaitTime: 1 * time.Second,
	})
	if err != nil {
		t.Fatalf("client_test.go(NewClient): %s", err)
	}

	res, err := client.Get("retry", nil)
	if err != nil {
		t.Fatalf("client_test.go(retry): %s", err)
	}
	if string(res.Body) != "Success after retries" {
		t.Fatalf("client_test.go(retry): Got back '%s' but expected 'Success after retries'", string(res.Body))
	}
}

func TestAPIClient_RateLimit(t *testing.T) {
	setup_api_client_server(t)
	defer shutdown_api_client_server()

	client, err := NewClient(&ClientOptions{
		Server:           "127.0.0.1",
		Application:      "puppet",
		Protocol:         "http",
		Port:             8080,
		RetryCount:       3,
		RetryWaitTime:    100 * time.Millisecond,
		RetryMaxWaitTime: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("client_test.go(NewClient): %s", err)
	}

	res, err := client.Get("ratelimit", nil)
	if err != nil {
		t.Fatalf("client_test.go(ratelimit): %s", err)
	}
	if string(res.Body) != "Success after rate limit" {
		t.Fatalf("client_test.go(ratelimit): Got back '%s' but expected 'Success after rate limit'", string(res.Body))
	}
}

func checkOk(t *testing.T, res *Response, err error, verb string) {
	if err != nil {
		t.Fatalf("client_test.go(%s): %s", verb, err)
	}
	if string(res.Body) != "It works!" {
		t.Fatalf("client_test.go(%s): Got back '%#v' but expected 'It works!'\n", verb, res)
	}
}

func TestAPIClient_CustomHeaders(t *testing.T) {
	client, err := NewClient(&ClientOptions{
		Server:      "127.0.0.1",
		Application: "puppet",
		Protocol:    "http",
		Port:        8080,
		Headers:     map[string]string{"X-Custom": "infrapi"},
	})
	if err != nil {
		t.Fatalf("client_test.go(CustomHeaders): %s", err)
	}
	if client.RestyClient.Header().Get("X-Custom") != "infrapi" {
		t.Fatalf("client_test.go(CustomHeaders): custom header not set")
	}
}

func TestAPIClient_UnsupportedMethod(t *testing.T) {
	setup_api_client_server(t)
	defer shutdown_api_client_server()

	client, err := NewClient(&ClientOptions{
		Server:      "127.0.0.1",
		Application: "puppet",
		Protocol:    "http",
		Port:        8080,
		RetryCount:  0,
	})
	if err != nil {
		t.Fatalf("client_test.go(UnsupportedMethod): %s", err)
	}
	_, err = client.Do("CONNECT", "ok", nil)
	if err == nil {
		t.Fatalf("client_test.go(UnsupportedMethod): expected error for unsupported method")
	}
}

func TestAPIClient_CACertContent(t *testing.T) {
	certPEM, _ := generateTestCert(t)
	_, err := NewClient(&ClientOptions{
		Server:        "127.0.0.1",
		Application:   "puppet",
		CACertContent: certPEM,
	})
	if err != nil {
		t.Fatalf("client_test.go(CACertContent): %s", err)
	}
}

func TestAPIClient_CACertPath(t *testing.T) {
	certPEM, _ := generateTestCert(t)
	f, err := os.CreateTemp(t.TempDir(), "ca-*.pem")
	if err != nil {
		t.Fatalf("client_test.go(CACertPath): create temp file: %s", err)
	}
	if _, err = f.Write(certPEM); err != nil {
		t.Fatalf("client_test.go(CACertPath): write cert: %s", err)
	}
	if err = f.Close(); err != nil {
		t.Fatalf("client_test.go(CACertPath): close temp file: %s", err)
	}

	_, err = NewClient(&ClientOptions{
		Server:      "127.0.0.1",
		Application: "puppet",
		CACertPath:  f.Name(),
	})
	if err != nil {
		t.Fatalf("client_test.go(CACertPath): %s", err)
	}
}

func TestAPIClient_CACertPathError(t *testing.T) {
	_, err := NewClient(&ClientOptions{
		Server:      "127.0.0.1",
		Application: "puppet",
		CACertPath:  "/nonexistent/ca.pem",
	})
	if err == nil {
		t.Fatalf("client_test.go(CACertPathError): expected error for bad CA path")
	}
}

func TestAPIClient_mTLSContent(t *testing.T) {
	certPEM, keyPEM := generateTestCert(t)
	_, err := NewClient(&ClientOptions{
		Server:            "127.0.0.1",
		Application:       "puppet",
		ClientCertContent: certPEM,
		ClientKeyContent:  keyPEM,
	})
	if err != nil {
		t.Fatalf("client_test.go(mTLSContent): %s", err)
	}
}

func TestAPIClient_mTLSContentError(t *testing.T) {
	_, err := NewClient(&ClientOptions{
		Server:            "127.0.0.1",
		Application:       "puppet",
		ClientCertContent: []byte("not-a-cert"),
		ClientKeyContent:  []byte("not-a-key"),
	})
	if err == nil {
		t.Fatalf("client_test.go(mTLSContentError): expected error for invalid cert content")
	}
}

func TestAPIClient_mTLSPath(t *testing.T) {
	certPEM, keyPEM := generateTestCert(t)
	dir := t.TempDir()

	certFile, err := os.CreateTemp(dir, "cert-*.pem")
	if err != nil {
		t.Fatalf("client_test.go(mTLSPath): %s", err)
	}
	certFile.Write(certPEM) //nolint:errcheck
	if err = certFile.Close(); err != nil {
		t.Fatalf("client_test.go(mTLSPath): %s", err)
	}

	keyFile, err := os.CreateTemp(dir, "key-*.pem")
	if err != nil {
		t.Fatalf("client_test.go(mTLSPath): %s", err)
	}
	keyFile.Write(keyPEM) //nolint:errcheck
	if err = keyFile.Close(); err != nil {
		t.Fatalf("client_test.go(mTLSPath): %s", err)
	}

	_, err = NewClient(&ClientOptions{
		Server:         "127.0.0.1",
		Application:    "puppet",
		ClientCertPath: certFile.Name(),
		ClientKeyPath:  keyFile.Name(),
	})
	if err != nil {
		t.Fatalf("client_test.go(mTLSPath): %s", err)
	}
}

func TestAPIClient_mTLSPathError(t *testing.T) {
	_, err := NewClient(&ClientOptions{
		Server:         "127.0.0.1",
		Application:    "puppet",
		ClientCertPath: "/nonexistent/cert.pem",
		ClientKeyPath:  "/nonexistent/key.pem",
	})
	if err == nil {
		t.Fatalf("client_test.go(mTLSPathError): expected error for bad cert path")
	}
}

func TestAPIClient_HedgingNonReadOnly(t *testing.T) {
	_, err := NewClient(&ClientOptions{
		Server:      "127.0.0.1",
		Application: "puppet",
		Protocol:    "http",
		Port:        8080,
		Hedging: &HedgingOptions{
			Enabled:            true,
			Delay:              10 * time.Millisecond,
			UpTo:               2,
			NonReadOnlyAllowed: true,
		},
	})
	if err != nil {
		t.Fatalf("client_test.go(HedgingNonReadOnly): %s", err)
	}
}

func TestAPIClient_Hedging(t *testing.T) {
	setup_api_client_server(t)
	defer shutdown_api_client_server()

	client, err := NewClient(&ClientOptions{
		Server:      "127.0.0.1",
		Application: "puppet",
		Protocol:    "http",
		Port:        8080,
		RetryCount:  0,
		Hedging: &HedgingOptions{
			Enabled: true,
			Delay:   10 * time.Millisecond,
			UpTo:    3,
		},
	})
	if err != nil {
		t.Fatalf("client_test.go(NewClient): %s", err)
	}

	res, err := client.Get("ok", nil)
	if err != nil {
		t.Fatalf("client_test.go(hedging): %s", err)
	}
	if string(res.Body) != "It works!" {
		t.Fatalf("client_test.go(hedging): Got back '%s' but expected 'It works!'", string(res.Body))
	}
}

// Benchmark tests
func BenchmarkClient_Get(b *testing.B) {
	serverMux := nhttp.NewServeMux()
	serverMux.HandleFunc("/api/v1/puppet/benchmark", func(w nhttp.ResponseWriter, r *nhttp.Request) {
		_, _ = fmt.Fprint(w, "benchmark")
	})

	server := &nhttp.Server{
		Addr:    "127.0.0.1:8081",
		Handler: serverMux,
	}
	go server.ListenAndServe() //nolint:errcheck
	defer server.Close()       //nolint:errcheck

	time.Sleep(100 * time.Millisecond)

	client, _ := NewClient(&ClientOptions{
		Server:      "127.0.0.1",
		Application: "puppet",
		Protocol:    "http",
		Port:        8081,
		RetryCount:  0,
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = client.Get("benchmark", nil)
	}
}
