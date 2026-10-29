// Package server builds the gin engine shared by InfraPI services.
package server

import (
	"net/http"
	"os"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/infrapi/lib/pkg/config"
)

// NewServerGin creates a gin.Engine pre-configured from the supplied AppConfig:
// CORS, recovery, logging, request metrics, and the endpoints every InfraPI
// service exposes without asking for them.
//
// Access logs and recovered panics go to gin.DefaultWriter, as gin's text
// lines unless cfg.AppLogFormat is "json", which writes one JSON object per
// line instead.
//
//	GET /-/metadata      the application identity
//	GET /-/healthz       200 while the process serves
//	GET /-/readyz        200 when ready for traffic, 503 when a WithReadiness check fails
//	GET /-/metrics       Prometheus exposition format
//	GET /-/openapi.json  OpenAPI 3.1, generated from the route table
//	GET /-/docs          Swagger UI reading that specification
//
// Every request carries its X-Request-Id (RequestIDHeader) in its context,
// for the problem documents, and has an OpenTelemetry span with the tracer
// provider pkg/tracing sets up. Gin runs in release mode unless the
// service logs at debug or trace.
//
// Options adjust it: WithOpenAPI documents the service with its own OpenAPI
// documents rather than the generated specification, and validates every
// request against them before any handler runs; WithReadiness adds the checks
// /-/readyz runs; WithAuth puts an authentication middleware - typically
// pkg/auth.Client.Middleware - in front of every route the service adds
// after this call.
func NewServerGin(cfg *config.AppConfig, opts ...Option) (*gin.Engine, error) {
	var o options
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return nil, err
		}
	}

	ginMode(cfg.AppLogLevel)
	app := gin.New()
	app.Use(logMiddlewares(cfg.AppLogFormat, gin.DefaultWriter)...)
	app.Use(spans(cfg.AppName), correlate)

	if err := app.SetTrustedProxies(cfg.AppTrustedProxies); err != nil {
		return nil, err
	}

	corsConfig := cors.DefaultConfig()
	corsConfig.AllowOrigins = cfg.AppCorsAllowOrigins
	corsConfig.AllowMethods = cfg.AppCorsAllowMethods
	corsConfig.AllowHeaders = cfg.AppCorsAllowHeaders
	corsConfig.ExposeHeaders = cfg.AppCorsExposeHeaders
	corsConfig.AllowCredentials = cfg.AppCorsAllowCredentials
	corsConfig.MaxAge = time.Duration(cfg.AppCorsMaxAge) * time.Hour

	app.Use(cors.New(corsConfig))

	m := newMetrics(cfg.AppFqdn)
	app.Use(m.middleware())

	app.GET("/-/metadata", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"appName":     cfg.AppName,
			"appPlatform": cfg.AppPlatform,
			"appRegion":   cfg.AppRegion,
			"appLocation": cfg.AppLocation,
			"appFqdn":     cfg.AppFqdn,
			"appUrl":      cfg.AppUrl,
		})
	})

	app.GET(MetricsPath, m.handler())
	healthHandlers(app, o.readiness)

	if err := openAPIHandlers(app, cfg, o.specs, o.operations); err != nil {
		return nil, err
	}

	// The contracts supplied with WithOpenAPI are enforced, not only
	// documented: on every route the service adds after this.
	if len(o.specs) > 0 {
		contracts, err := newContractRouter(o.specs)
		if err != nil {
			return nil, err
		}
		app.Use(contracts.validate)
	}

	// Same reasoning as the contracts above: registered after the
	// engine's own endpoints, so it never gates /-/healthz, /-/metrics
	// and the rest, only the routes the service adds next.
	if o.auth != nil {
		app.Use(o.auth)
	}

	return app, nil
}

// ginMode switches gin to release mode — no debug line per route at start,
// no warning — unless the service logs at debug or trace, or the mode was
// chosen already: GIN_MODE, or gin.SetMode before NewServerGin (a test's
// gin.TestMode).
func ginMode(level log.Level) {
	if os.Getenv(gin.EnvGinMode) != "" || gin.Mode() != gin.DebugMode || level >= log.DebugLevel {
		return
	}
	gin.SetMode(gin.ReleaseMode)
}
