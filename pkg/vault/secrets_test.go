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

// kv2Stub stands in for Vault's KV v2 engine mounted at "secret": it keeps
// one in-memory secret at "creds" and answers read, write, delete and list
// against it.
func kv2Stub(t *testing.T) *httptest.Server {
	t.Helper()
	data := map[string]any{"user": "svc", "password": "s3cr3t"}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/secret/data/creds", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			if data == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":["no value"]}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"data": data, "metadata": map[string]any{"version": 1}},
			})
		case http.MethodPost:
			var body struct {
				Data map[string]any `json:"data"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			data = body.Data
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"created_time": "2026-01-01T00:00:00Z", "version": 2},
			})
		case http.MethodDelete:
			data = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/secret/metadata/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"keys": []string{"creds", "other"}}})
	})

	return httptest.NewServer(mux)
}

func testClient(t *testing.T, addr string) *Client {
	t.Helper()
	client, err := NewClient(ClientOptions{Address: addr})
	require.NoError(t, err)
	return client
}

func TestGetSecret(t *testing.T) {
	srv := kv2Stub(t)
	defer srv.Close()
	client := testClient(t, srv.URL)

	data, err := client.GetSecret(context.Background(), "secret", "creds")
	require.NoError(t, err)
	assert.Equal(t, "svc", data["user"])
	assert.Equal(t, "s3cr3t", data["password"])
}

func TestGetSecret_notFound(t *testing.T) {
	srv := kv2Stub(t)
	defer srv.Close()
	client := testClient(t, srv.URL)

	require.NoError(t, client.DeleteSecret(context.Background(), "secret", "creds"))

	_, err := client.GetSecret(context.Background(), "secret", "creds")
	assert.Error(t, err)
}

func TestSetSecret(t *testing.T) {
	srv := kv2Stub(t)
	defer srv.Close()
	client := testClient(t, srv.URL)

	data, err := client.SetSecret(context.Background(), "secret", "creds", map[string]any{"user": "rotated"})
	require.NoError(t, err)
	assert.Equal(t, "rotated", data["user"])

	// The write is visible to a later, independent read.
	data, err = client.GetSecret(context.Background(), "secret", "creds")
	require.NoError(t, err)
	assert.Equal(t, "rotated", data["user"])
}

func TestDeleteSecret(t *testing.T) {
	srv := kv2Stub(t)
	defer srv.Close()
	client := testClient(t, srv.URL)

	require.NoError(t, client.DeleteSecret(context.Background(), "secret", "creds"))
}

func TestListSecrets(t *testing.T) {
	srv := kv2Stub(t)
	defer srv.Close()
	client := testClient(t, srv.URL)

	keys, err := client.ListSecrets(context.Background(), "secret", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"creds", "other"}, keys)
}
