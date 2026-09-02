// Package server builds the gin engine shared by InfraPI services.
package server

import (
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/config"
)

// NewServerGin creates a gin.Engine pre-configured with CORS, recovery, logging,
// and a /-/metadata health endpoint derived from the supplied AppConfig.
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
	app.Use(gin.Recovery())
	app.Use(gin.Logger())

	app.GET("/-/metadata", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"appName":     cfg.AppName,
			"appPlatform": cfg.AppPlatform,
			"appRegion":   cfg.AppRegion,
			"appLocation": cfg.AppLocation,
			"appFqdn":     cfg.AppFqdn,
			"appUrl":      cfg.AppUrl,
		})
	})

	return app, nil
}
