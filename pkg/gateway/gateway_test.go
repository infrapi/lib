package gateway

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// fakeAdmin is a minimal in-memory stand-in for the APISIX Admin API: enough
// of its request/response shape for the client and Resource to be exercised
// against a real HTTP server, per this repository's testing conventions.
type fakeAdmin struct {
	mu      sync.Mutex
	apiKey  string
	store   map[string]map[string]json.RawMessage // collection -> id -> value
	counter int64
}

func newFakeAdmin(apiKey string) *fakeAdmin {
	return &fakeAdmin{
		apiKey: apiKey,
		store: map[string]map[string]json.RawMessage{
			"routes":    {},
			"services":  {},
			"upstreams": {},
			"consumers": {},
		},
	}
}

func (f *fakeAdmin) handler() http.Handler {
	mux := http.NewServeMux()
	for _, collection := range []string{"routes", "services", "upstreams", "consumers"} {
		mux.HandleFunc("/apisix/admin/"+collection, f.collectionHandler(collection))
		mux.HandleFunc("/apisix/admin/"+collection+"/", f.itemHandler(collection))
	}
	return mux
}

func (f *fakeAdmin) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-API-KEY") != f.apiKey {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(errorEnvelope{Message: "wrong api key"})
		return false
	}
	return true
}

func (f *fakeAdmin) collectionHandler(collection string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !f.checkAuth(w, r) {
			return
		}

		f.mu.Lock()
		defer f.mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			items := make([]json.RawMessage, 0)
			for id, value := range f.store[collection] {
				idx := atomic.AddInt64(&f.counter, 1)
				raw, _ := json.Marshal(map[string]any{
					"key":           "/apisix/" + collection + "/" + id,
					"value":         json.RawMessage(value),
					"createdIndex":  idx,
					"modifiedIndex": idx,
				})
				items = append(items, raw)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"list":[%s],"total":%d}`, joinRaw(items), len(items))
		case http.MethodPost:
			body, _ := readAll(r)
			id := fmt.Sprintf("auto-%d", atomic.AddInt64(&f.counter, 1))
			f.store[collection][id] = body
			w.Header().Set("Content-Type", "application/json")
			writeItem(w, collection, id, body, atomic.AddInt64(&f.counter, 1))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (f *fakeAdmin) itemHandler(collection string) http.HandlerFunc {
	prefix := "/apisix/admin/" + collection + "/"
	return func(w http.ResponseWriter, r *http.Request) {
		if !f.checkAuth(w, r) {
			return
		}

		id := strings.TrimPrefix(r.URL.Path, prefix)

		f.mu.Lock()
		defer f.mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			value, ok := f.store[collection][id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(errorEnvelope{Message: "key not found"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			writeItem(w, collection, id, value, atomic.AddInt64(&f.counter, 1))
		case http.MethodPut:
			body, _ := readAll(r)
			f.store[collection][id] = body
			w.Header().Set("Content-Type", "application/json")
			writeItem(w, collection, id, body, atomic.AddInt64(&f.counter, 1))
		case http.MethodPatch:
			existing, ok := f.store[collection][id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(errorEnvelope{Message: "key not found"})
				return
			}
			patch, _ := readAll(r)
			merged := mergeJSON(existing, patch)
			f.store[collection][id] = merged
			w.Header().Set("Content-Type", "application/json")
			writeItem(w, collection, id, merged, atomic.AddInt64(&f.counter, 1))
		case http.MethodDelete:
			if _, ok := f.store[collection][id]; !ok {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(errorEnvelope{Message: "key not found"})
				return
			}
			delete(f.store[collection], id)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"key":"/apisix/%s/%s","deleted":"1"}`, collection, id)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func writeItem(w http.ResponseWriter, collection, id string, value json.RawMessage, idx int64) {
	_, _ = fmt.Fprintf(w, `{"key":"/apisix/%s/%s","value":%s,"createdIndex":%d,"modifiedIndex":%d}`,
		collection, id, value, idx, idx)
}

func readAll(r *http.Request) (json.RawMessage, error) {
	buf := make([]byte, 0, 1024)
	chunk := make([]byte, 1024)
	for {
		n, err := r.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}

func mergeJSON(existing, patch json.RawMessage) json.RawMessage {
	var e, p map[string]any
	_ = json.Unmarshal(existing, &e)
	_ = json.Unmarshal(patch, &p)
	if e == nil {
		e = map[string]any{}
	}
	for k, v := range p {
		e[k] = v
	}
	merged, _ := json.Marshal(e)
	return merged
}

func joinRaw(items []json.RawMessage) string {
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = string(item)
	}
	return strings.Join(parts, ",")
}

func startFakeAdmin(t *testing.T, apiKey string) (addr string, admin *fakeAdmin) {
	t.Helper()
	admin = newFakeAdmin(apiKey)
	srv := &http.Server{Handler: admin.handler()}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("startFakeAdmin: listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	return ln.Addr().String(), admin
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("splitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("splitHostPort: %v", err)
	}
	return host, port
}

func pemEncode(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}

func TestClient_NewClient(t *testing.T) {
	_, err := NewClient(nil)
	if err == nil {
		t.Fatalf("NewClient(nil): expected error")
	}

	_, err = NewClient(&ClientOptions{APIKey: "k"})
	if err == nil || err.Error() != "missing mandatory attribute Server" {
		t.Fatalf("NewClient: expected missing Server error, got %v", err)
	}

	_, err = NewClient(&ClientOptions{Server: "127.0.0.1"})
	if err == nil || err.Error() != "missing mandatory attribute APIKey" {
		t.Fatalf("NewClient: expected missing APIKey error, got %v", err)
	}

	client, err := NewClient(&ClientOptions{Server: "127.0.0.1", APIKey: "k"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.HTTP.Port != DefaultPort {
		t.Fatalf("NewClient: expected default port %d, got %d", DefaultPort, client.HTTP.Port)
	}
	if client.HTTP.Protocol != DefaultProtocol {
		t.Fatalf("NewClient: expected default protocol %q, got %q", DefaultProtocol, client.HTTP.Protocol)
	}
	if client.HTTP.RestyClient.Header().Get("User-Agent") != DefaultUserAgent {
		t.Fatalf("NewClient: expected default user agent %q, got %q", DefaultUserAgent, client.HTTP.RestyClient.Header().Get("User-Agent"))
	}
	if client.HTTP.RestyClient.Header().Get("X-API-KEY") != "k" {
		t.Fatalf("NewClient: expected X-API-KEY header to be set")
	}
}

func TestClient_CustomOptions(t *testing.T) {
	client, err := NewClient(&ClientOptions{
		Server:        "127.0.0.1",
		Port:          9999,
		Protocol:      "https",
		APIKey:        "k",
		UserAgent:     "custom/1.0",
		GlobalTimeout: 5,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.HTTP.Port != 9999 {
		t.Fatalf("expected port 9999, got %d", client.HTTP.Port)
	}
	if client.HTTP.Protocol != "https" {
		t.Fatalf("expected protocol https, got %s", client.HTTP.Protocol)
	}
	if client.HTTP.RestyClient.Header().Get("User-Agent") != "custom/1.0" {
		t.Fatalf("expected custom user agent")
	}
	if client.baseURL() != "https://127.0.0.1:9999/apisix/admin" {
		t.Fatalf("unexpected baseURL: %s", client.baseURL())
	}
}

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
	return pemEncode("CERTIFICATE", certDER), pemEncode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
}

func TestClient_TLS(t *testing.T) {
	certPEM, keyPEM := generateTestCert(t)

	if _, err := NewClient(&ClientOptions{Server: "127.0.0.1", APIKey: "k", CACertContent: certPEM}); err != nil {
		t.Fatalf("CACertContent: %v", err)
	}

	if _, err := NewClient(&ClientOptions{Server: "127.0.0.1", APIKey: "k", ClientCertContent: certPEM, ClientKeyContent: keyPEM}); err != nil {
		t.Fatalf("mTLS content: %v", err)
	}

	if _, err := NewClient(&ClientOptions{Server: "127.0.0.1", APIKey: "k", CACertPath: "/nonexistent/ca.pem"}); err == nil {
		t.Fatalf("CACertPath: expected error for missing file")
	}

	if _, err := NewClient(&ClientOptions{
		Server: "127.0.0.1", APIKey: "k",
		ClientCertPath: "/nonexistent/cert.pem", ClientKeyPath: "/nonexistent/key.pem",
	}); err == nil {
		t.Fatalf("mTLS path: expected error for missing files")
	}

	dir := t.TempDir()
	caPath := dir + "/ca.pem"
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatalf("write ca file: %v", err)
	}
	if _, err := NewClient(&ClientOptions{Server: "127.0.0.1", APIKey: "k", CACertPath: caPath}); err != nil {
		t.Fatalf("CACertPath: %v", err)
	}

	certPath, keyPath := dir+"/cert.pem", dir+"/key.pem"
	_ = os.WriteFile(certPath, certPEM, 0o600)
	_ = os.WriteFile(keyPath, keyPEM, 0o600)
	if _, err := NewClient(&ClientOptions{Server: "127.0.0.1", APIKey: "k", ClientCertPath: certPath, ClientKeyPath: keyPath}); err != nil {
		t.Fatalf("mTLS path: %v", err)
	}

	if _, err := NewClient(&ClientOptions{Server: "127.0.0.1", APIKey: "k", ClientCertContent: []byte("bad"), ClientKeyContent: []byte("bad")}); err == nil {
		t.Fatalf("mTLS content: expected error for invalid cert content")
	}
}

func newTestClient(t *testing.T, addr, apiKey string) *Client {
	t.Helper()
	host, port := splitHostPort(t, addr)
	client, err := NewClient(&ClientOptions{
		Server:           host,
		Port:             port,
		Protocol:         "http",
		APIKey:           apiKey,
		RetryWaitTime:    5 * time.Millisecond,
		RetryMaxWaitTime: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("newTestClient: %v", err)
	}
	return client
}

func TestClient_Routes_CRUD(t *testing.T) {
	addr, _ := startFakeAdmin(t, "secret")
	client := newTestClient(t, addr, "secret")
	ctx := context.Background()

	created, err := client.NewRoute(ctx, "1", Route{
		URI:    "/hello",
		Status: 1,
		Upstream: &Upstream{
			Type:  "roundrobin",
			Nodes: []Node{{Host: "127.0.0.1", Port: 1980, Weight: 1}},
		},
	})
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}
	if created.URI != "/hello" {
		t.Fatalf("NewRoute: expected URI /hello, got %q", created.URI)
	}

	got, err := client.GetRoute(ctx, "1")
	if err != nil {
		t.Fatalf("GetRoute: %v", err)
	}
	if got.Upstream.Nodes[0].Host != "127.0.0.1" {
		t.Fatalf("GetRoute: unexpected upstream node %+v", got.Upstream.Nodes)
	}

	list, err := client.ListRoutes(ctx)
	if err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListRoutes: expected 1 item, got %d", len(list))
	}

	updated, err := client.UpdateRoute(ctx, "1", map[string]any{"desc": "updated"})
	if err != nil {
		t.Fatalf("UpdateRoute: %v", err)
	}
	if updated.Desc != "updated" || updated.URI != "/hello" {
		t.Fatalf("UpdateRoute: expected merged route, got %+v", updated)
	}

	if err := client.DeleteRoute(ctx, "1"); err != nil {
		t.Fatalf("DeleteRoute: %v", err)
	}

	if _, err := client.GetRoute(ctx, "1"); err == nil {
		t.Fatalf("GetRoute after Delete: expected error")
	} else {
		var apiErr *infraerrors.Error
		if !infraerrors.As(err, &apiErr) {
			t.Fatalf("GetRoute after Delete: expected *infraerrors.Error, got %T: %v", err, err)
		}
		if apiErr.Code != http.StatusNotFound {
			t.Fatalf("GetRoute after Delete: expected 404, got %d", apiErr.Code)
		}
		if apiErr.Origin != "gateway.routes.Get" {
			t.Fatalf("GetRoute after Delete: expected origin gateway.routes.Get, got %q", apiErr.Origin)
		}
		if apiErr.Error() == "" {
			t.Fatalf("Error.Error(): expected non-empty message")
		}
	}
}

func TestClient_CreateService_AutoID(t *testing.T) {
	addr, _ := startFakeAdmin(t, "secret")
	client := newTestClient(t, addr, "secret")
	ctx := context.Background()

	id, created, err := client.CreateService(ctx, Service{Name: "svc", Desc: "a service"})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	if created.Name != "svc" {
		t.Fatalf("CreateService: expected name svc, got %q", created.Name)
	}
	if id == "" {
		t.Fatalf("CreateService: expected a generated id")
	}
	if strings.Contains(id, "/") {
		t.Fatalf("CreateService: expected a bare id, got %q", id)
	}

	// The returned id must be usable directly against Get/Update/Delete,
	// which build their own URL from collection and id.
	fetched, err := client.GetService(ctx, id)
	if err != nil {
		t.Fatalf("GetService(%q): %v", id, err)
	}
	if fetched.Name != "svc" {
		t.Fatalf("GetService(%q): expected name svc, got %q", id, fetched.Name)
	}
}

func TestClient_Upstreams_Consumers(t *testing.T) {
	addr, _ := startFakeAdmin(t, "secret")
	client := newTestClient(t, addr, "secret")
	ctx := context.Background()

	up, err := client.NewUpstream(ctx, "1", Upstream{
		Type:  "roundrobin",
		Nodes: []Node{{Host: "10.0.0.1", Port: 80, Weight: 1}},
	})
	if err != nil {
		t.Fatalf("NewUpstream: %v", err)
	}
	if up.Type != "roundrobin" {
		t.Fatalf("unexpected upstream: %+v", up)
	}

	consumer, err := client.NewConsumer(ctx, Consumer{
		Username: "alice",
		Plugins:  map[string]any{"key-auth": map[string]any{"key": "s3cr3t"}},
	})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	if consumer.Username != "alice" {
		t.Fatalf("unexpected consumer: %+v", consumer)
	}
}

func TestClient_NotFound(t *testing.T) {
	addr, _ := startFakeAdmin(t, "secret")
	client := newTestClient(t, addr, "secret")
	ctx := context.Background()

	if _, err := client.UpdateRoute(ctx, "missing", map[string]any{"desc": "x"}); err == nil {
		t.Fatalf("UpdateRoute: expected error for missing id")
	}
	if err := client.DeleteRoute(ctx, "missing"); err == nil {
		t.Fatalf("DeleteRoute: expected error for missing id")
	}
}

func TestClient_WrongAPIKey(t *testing.T) {
	addr, _ := startFakeAdmin(t, "secret")
	client := newTestClient(t, addr, "wrong")
	ctx := context.Background()

	_, err := client.ListRoutes(ctx)
	if err == nil {
		t.Fatalf("ListRoutes: expected error with wrong API key")
	}
	var apiErr *infraerrors.Error
	if !infraerrors.As(err, &apiErr) || apiErr.Code != http.StatusForbidden {
		t.Fatalf("ListRoutes: expected 403 *infraerrors.Error, got %v", err)
	}

	if _, _, err := client.CreateRoute(ctx, Route{URI: "/x"}); err == nil {
		t.Fatalf("CreateRoute: expected error with wrong API key")
	}
	if _, err := client.NewRoute(ctx, "1", Route{URI: "/x"}); err == nil {
		t.Fatalf("NewRoute: expected error with wrong API key")
	}
}

func TestClient_ConnectionError(t *testing.T) {
	client, err := NewClient(&ClientOptions{
		Server:           "127.0.0.1",
		Port:             19999,
		Protocol:         "http",
		APIKey:           "k",
		RetryCount:       1,
		RetryWaitTime:    10 * time.Millisecond,
		RetryMaxWaitTime: 20 * time.Millisecond,
		GlobalTimeout:    1,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.ListRoutes(context.Background())
	if err == nil {
		t.Fatalf("ListRoutes: expected connection error")
	}
	var apiErr *infraerrors.Error
	if !infraerrors.As(err, &apiErr) || apiErr.Code != http.StatusBadGateway {
		t.Fatalf("ListRoutes: expected 502 *infraerrors.Error wrapping the transport failure, got %v", err)
	}
}

func TestClient_DecodeError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/apisix/admin/routes/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `not json`)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	client := newTestClient(t, ln.Addr().String(), "secret")

	_, err = client.GetRoute(context.Background(), "1")
	if err == nil {
		t.Fatalf("GetRoute: expected decode error")
	}
	var apiErr *infraerrors.Error
	if !infraerrors.As(err, &apiErr) || apiErr.Code != http.StatusBadGateway {
		t.Fatalf("GetRoute: expected 502 *infraerrors.Error wrapping the decode failure, got %v", err)
	}
}

func TestClient_ErrorWithoutBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/apisix/admin/routes/1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	client := newTestClient(t, ln.Addr().String(), "secret")

	_, err = client.GetRoute(context.Background(), "1")
	if err == nil {
		t.Fatalf("GetRoute: expected error")
	}
	var apiErr *infraerrors.Error
	if !infraerrors.As(err, &apiErr) || apiErr.Code != http.StatusInternalServerError {
		t.Fatalf("GetRoute: expected 500 *infraerrors.Error, got %v", err)
	}
	if apiErr.Message != http.StatusText(http.StatusInternalServerError) {
		t.Fatalf("GetRoute: expected message to fall back to the status text, got %q", apiErr.Message)
	}
}
