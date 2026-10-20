package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

const (
	// HealthzPath answers 200 while the process serves: what a gateway or a
	// load balancer checks to know a replica is alive.
	HealthzPath = "/-/healthz"

	// ReadyzPath answers 200 when the service is ready for traffic, and 503
	// when one of its readiness checks says otherwise: what takes a replica
	// out of rotation while it starts, stops or misses its backend.
	ReadyzPath = "/-/readyz"

	// ReadinessTimeout bounds each readiness check: a check that does not
	// answer in time counts as not ready.
	ReadinessTimeout = 2 * time.Second
)

// ReadinessCheck says whether the service is ready for traffic: nil when it
// is, an error saying why when it is not. It is called on every GET
// ReadyzPath, with a context that ends after ReadinessTimeout.
type ReadinessCheck func(ctx context.Context) error

// WithReadiness adds checks to ReadyzPath, run in order until one fails. A
// service stopping makes one of them fail as soon as its shutdown starts, so
// that the gateway takes it out of rotation before it stops serving; one that
// needs its backend checks it here.
func WithReadiness(checks ...ReadinessCheck) Option {
	return func(o *options) error {
		for _, c := range checks {
			if c == nil {
				return errors.New("server: a readiness check is nil")
			}
		}
		o.readiness = append(o.readiness, checks...)
		return nil
	}
}

// healthHandlers registers HealthzPath and ReadyzPath.
func healthHandlers(app *gin.Engine, checks []ReadinessCheck) {
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) }
	app.GET(HealthzPath, ok)
	app.GET(ReadyzPath, func(c *gin.Context) {
		for _, check := range checks {
			ctx, cancel := context.WithTimeout(c.Request.Context(), ReadinessTimeout)
			err := check(ctx)
			if err == nil && ctx.Err() != nil {
				err = ctx.Err()
			}
			cancel()
			if err != nil {
				AbortError(c, infraerrors.Unavailable("not ready: %v", err))
				return
			}
		}
		ok(c)
	})
}
