// Package server builds the gin engine shared by InfraPI services.
package server

import (
	"net/http"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/config"
)

// NewServerGin creates a gin.Engine pre-configured from the supplied AppConfig:
// CORS, recovery, logging, request metrics, and four endpoints every InfraPI
// service exposes without asking for them.
//
// Access logs and recovered panics go to gin.DefaultWriter, as gin's text
// lines unless cfg.AppLogFormat is "json", which writes one JSON object per
// line instead.
//
//	GET /-/metadata      the application identity
//	GET /-/metrics       Prometheus exposition format
//	GET /-/openapi.json  OpenAPI 3.1, generated from the route table
//	GET /-/docs          Swagger UI reading that specification
//
// Options adjust it: WithOpenAPI documents the service with its own OpenAPI
// documents rather than the generated specification.
func NewServerGin(cfg *config.AppConfig, opts ...Option) (*gin.Engine, error) {
	var o options
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return nil, err
		}
	}

	app := gin.New()
	app.Use(logMiddlewares(cfg.AppLogFormat, gin.DefaultWriter)...)

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

	if err := openAPIHandlers(app, cfg, o.specs); err != nil {
		return nil, err
	}

	return app, nil
}
