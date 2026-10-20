package server

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Alive while the process serves, whatever the readiness says.
func TestHealthz(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t), WithReadiness(func(context.Context) error { return errors.New("no") }))
	require.NoError(t, err)
	code, body := call(t, app, http.MethodGet, HealthzPath, "", nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ok", body["status"])
}

// Ready without checks; not ready, as a retryable problem document saying why,
// as soon as one fails — a service draining, a backend down — and ready again
// once it passes.
func TestReadyz(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t))
	require.NoError(t, err)
	code, _ := call(t, app, http.MethodGet, ReadyzPath, "", nil)
	assert.Equal(t, http.StatusOK, code, "no check: ready")

	var draining atomic.Bool
	backend := errors.New("backend unreachable")
	var backendErr atomic.Pointer[error]
	app, err = NewServerGin(testAppConfig(t), WithReadiness(
		func(context.Context) error {
			if draining.Load() {
				return errors.New("draining")
			}
			return nil
		},
		func(context.Context) error {
			if e := backendErr.Load(); e != nil {
				return *e
			}
			return nil
		},
	))
	require.NoError(t, err)

	code, body := call(t, app, http.MethodGet, ReadyzPath, "", nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ok", body["status"])

	backendErr.Store(&backend)
	code, body = call(t, app, http.MethodGet, ReadyzPath, "", map[string]string{RequestIDHeader: "req-r"})
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "not ready: backend unreachable", body["detail"])
	assert.Equal(t, true, body["retryable"])
	assert.Equal(t, "req-r", body["correlation_id"])

	draining.Store(true)
	_, body = call(t, app, http.MethodGet, ReadyzPath, "", nil)
	assert.Equal(t, "not ready: draining", body["detail"], "checks run in order: the first failure answers")

	draining.Store(false)
	backendErr.Store(nil)
	code, _ = call(t, app, http.MethodGet, ReadyzPath, "", nil)
	assert.Equal(t, http.StatusOK, code, "ready again")
}

// A check that does not answer in time counts as not ready.
func TestReadyz_timeout(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t), WithReadiness(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	require.NoError(t, err)
	start := time.Now()
	code, body := call(t, app, http.MethodGet, ReadyzPath, "", nil)
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Contains(t, body["detail"], "deadline exceeded")
	assert.Less(t, time.Since(start), ReadinessTimeout+time.Second)
}

func TestWithReadiness_refusesNil(t *testing.T) {
	_, err := NewServerGin(testAppConfig(t), WithReadiness(nil))
	assert.ErrorContains(t, err, "readiness check is nil")
}
