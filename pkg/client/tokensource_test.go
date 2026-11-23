package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/api"
)

// TokenSource: asked for each request, its token the bearer; its error
// fails the request before it is sent.
func TestTokenSource(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)

	var n atomic.Int32
	var fail error
	c, err := NewClient(&ClientOptions{Protocol: "http", Server: host, Port: p, Namespace: "core", RetryCount: 1,
		API: api.Group{Name: "compute.infrapi.io", Version: "v1"}, Token: "ignored",
		TokenSource: func(context.Context) (string, error) {
			return "token-" + strconv.Itoa(int(n.Add(1))), fail
		}})
	require.NoError(t, err)
	_, err = c.Get("vms", nil)
	require.NoError(t, err)
	assert.Equal(t, "Bearer token-1", got.Load())
	_, err = c.Get("vms", nil)
	require.NoError(t, err)
	assert.Equal(t, "Bearer token-2", got.Load())

	fail = errors.New("kayra is down")
	got.Store("")
	_, err = c.Get("vms", nil)
	require.ErrorContains(t, err, "kayra is down")
	assert.Equal(t, "", got.Load())
}
