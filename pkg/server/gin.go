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
//	GET /-/metadata      the application identity
//	GET /-/metrics       Prometheus exposition format
//	GET /-/openapi.json  OpenAPI 3.1, generated from the route table
//	GET /-/docs          Swagger UI reading that specification
func NewServerGin(cfg *config.AppConfig) (*gin.Engine, error) {
	app := gin.Default()

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

	openAPIHandlers(app, cfg)

	return app, nil
}
