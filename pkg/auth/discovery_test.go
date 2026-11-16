package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// The document is read once, then kept: two decisions, one discovery.
func TestDiscover_keptAfterFirstRead(t *testing.T) {
	var reads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == DiscoveryPath {
			reads.Add(1)
		}
		withDiscovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"allowed":true}`))
		})).ServeHTTP(w, r)
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	doc, err := client.Discover(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "infrapi.io", doc.Domain)
	assert.Equal(t, testCheckPath, doc.Endpoints.Check)

	for range 2 {
		allowed, _, err := client.Authorize(context.Background(), "token", AuthorizeRequest{})
		require.NoError(t, err)
		assert.True(t, allowed)
	}
	assert.Equal(t, int64(1), reads.Load())
}

// A failed read is not kept: the service is discovered once it answers.
func TestDiscover_retriesAfterFailure(t *testing.T) {
	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		withDiscovery(http.NotFoundHandler()).ServeHTTP(w, r)
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	_, err = client.Discover(context.Background())
	require.Error(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, infraerrors.ErrorCode(err))

	up.Store(true)
	doc, err := client.Discover(context.Background())
	require.NoError(t, err)
	assert.Equal(t, testCheckPath, doc.Endpoints.Check)
}

// A service that is not Kayra, or names no check endpoint: a retryable
// 503 naming the document's URL, from Authorize as from Discover.
func TestDiscover_notKayra(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"no document": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) },
		"no check": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"domain":"infrapi.io","endpoints":{}}`))
		},
		"not json": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()

			client, err := NewClient(ClientOptions{BaseURL: srv.URL})
			require.NoError(t, err)

			_, _, err = client.Authorize(context.Background(), "token", AuthorizeRequest{})
			e := infraerrors.ErrorCast(err)
			require.NotNil(t, e)
			assert.Equal(t, http.StatusServiceUnavailable, e.Code)
			assert.Contains(t, e.Message, srv.URL+DiscoveryPath)
			require.NotNil(t, e.Retryable)
			assert.True(t, *e.Retryable)
		})
	}
}
