package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingStub is authStub plus a counter, to assert how many HTTP calls
// actually reached the authentication service.
func countingStub(t *testing.T, want, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	return srv, &calls
}

func TestVerify_cacheHitSkipsTheHTTPCall(t *testing.T) {
	srv, calls := countingStub(t, "good-token", `{"sub":"jdoe"}`)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL, CacheTTL: time.Hour})
	require.NoError(t, err)

	for range 3 {
		info, err := client.Verify(context.Background(), "good-token")
		require.NoError(t, err)
		assert.Equal(t, "jdoe", info.Subject)
	}

	assert.Equal(t, int64(1), calls.Load())
}

func TestVerify_cacheDisabledByDefault(t *testing.T) {
	srv, calls := countingStub(t, "good-token", `{"sub":"jdoe"}`)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	for range 3 {
		_, err := client.Verify(context.Background(), "good-token")
		require.NoError(t, err)
	}

	assert.Equal(t, int64(3), calls.Load())
}

func TestVerify_cacheExpires(t *testing.T) {
	srv, calls := countingStub(t, "good-token", `{"sub":"jdoe"}`)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL, CacheTTL: time.Millisecond})
	require.NoError(t, err)

	_, err = client.Verify(context.Background(), "good-token")
	require.NoError(t, err)

	time.Sleep(5 * time.Millisecond)

	_, err = client.Verify(context.Background(), "good-token")
	require.NoError(t, err)

	assert.Equal(t, int64(2), calls.Load())
}

func TestVerify_cacheIsPerToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		token := r.Header.Get("Authorization")[len("Bearer "):]
		_, _ = w.Write([]byte(`{"sub":"` + token + `"}`))
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL, CacheTTL: time.Hour})
	require.NoError(t, err)

	a, err := client.Verify(context.Background(), "alice")
	require.NoError(t, err)
	b, err := client.Verify(context.Background(), "bob")
	require.NoError(t, err)

	assert.Equal(t, "alice", a.Subject)
	assert.Equal(t, "bob", b.Subject)
}

// A caller mutating what Verify returned must never corrupt what the next
// cache hit serves.
func TestVerify_cachedResultIsNotSharedMutableState(t *testing.T) {
	srv, _ := countingStub(t, "good-token", `{"sub":"jdoe","groups":["a"],"email":"jdoe@example.com"}`)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL, CacheTTL: time.Hour})
	require.NoError(t, err)

	first, err := client.Verify(context.Background(), "good-token")
	require.NoError(t, err)
	first.Groups[0] = "tampered"
	first.Attributes["email"] = "tampered"

	second, err := client.Verify(context.Background(), "good-token")
	require.NoError(t, err)
	assert.Equal(t, "a", second.Groups[0])
	assert.Equal(t, "jdoe@example.com", second.Attributes["email"])
}

func TestVerify_doesNotCacheFailure(t *testing.T) {
	srv, calls := countingStub(t, "good-token", `{}`)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL, CacheTTL: time.Hour})
	require.NoError(t, err)

	_, err = client.Verify(context.Background(), "bad-token")
	require.Error(t, err)
	_, err = client.Verify(context.Background(), "bad-token")
	require.Error(t, err)

	assert.Equal(t, int64(2), calls.Load())
}
