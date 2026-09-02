package config

import (
	"os"
)

// Default envvars

type DefaultConfig struct {
	AppName                 *VariableOpts
	AppLogLevel             *VariableOpts
	AppListenType           *VariableOpts
	AppListenAddress        *VariableOpts
	AppPlatform             *VariableOpts
	AppRegion               *VariableOpts
	AppLocation             *VariableOpts
	AppFqdn                 *VariableOpts
	AppUrl                  *VariableOpts
	AppTrustedProxies       *VariableOpts
	AppCorsAllowOrigins     *VariableOpts
	AppCorsAllowMethods     *VariableOpts
	AppCorsAllowHeaders     *VariableOpts
	AppCorsExposeHeaders    *VariableOpts
	AppCorsAllowCredentials *VariableOpts
	AppCorsMaxAge           *VariableOpts
}

// All validator to use, customizable by enduser
func NewDefaultConfig() *DefaultConfig {
	hostname, _ := os.Hostname()

	return &DefaultConfig{
		AppName: &VariableOpts{
			Key:       "INFRAPI_APP_NAME",
			Validator: "required",
		},
		AppLogLevel: &VariableOpts{
			Key:       "INFRAPI_APP_LOG_LEVEL",
			Validator: "oneof=trace debug info error",
			Default:   "info",
		},
		AppListenType: &VariableOpts{
			Key:       "INFRAPI_APP_LISTEN_TYPE",
			Validator: "oneof=tcp systemd",
			Default:   "tcp",
		},
		AppListenAddress: &VariableOpts{
			Key:       "INFRAPI_APP_LISTEN_ADDRESS",
			Validator: "hostname_port",
			Default:   "127.0.0.1:8080",
		},
		AppFqdn: &VariableOpts{
			Key:       "INFRAPI_APP_FQDN",
			Validator: "fqdn",
			Default:   hostname,
		},
		AppPlatform: &VariableOpts{
			Key:       "INFRAPI_APP_PLATFORM",
			Validator: "oneof=sandbox preprod prod",
		},
		AppRegion: &VariableOpts{
			Key:       "INFRAPI_APP_REGION",
			Validator: "oneof=eu-west-1 eu-west-2 au-southeast-1",
		},
		AppLocation: &VariableOpts{
			Key:       "INFRAPI_APP_LOCATION",
			Validator: "oneof=dc1 dc2 dc3",
		},
		AppUrl: &VariableOpts{
			Key:       "INFRAPI_APP_URL",
			Validator: "http_url",
		},
		AppTrustedProxies: &VariableOpts{
			Key:     "INFRAPI_APP_TRUSTED_PROXIES",
			Default: "[]",
		},
		AppCorsAllowOrigins: &VariableOpts{
			Key:     "INFRAPI_APP_CORS_ALLOW_ORIGINS",
			Default: "[\"*\"]",
		},
		AppCorsAllowMethods: &VariableOpts{
			Key:     "INFRAPI_APP_CORS_ALLOW_METHODS",
			Default: "[\"PUT\", \"PATCH\", \"HEAD\"]",
		},
		AppCorsAllowHeaders: &VariableOpts{
			Key:     "INFRAPI_APP_CORS_ALLOW_HEADERS",
			Default: "[\"Origin\"]",
		},
		AppCorsExposeHeaders: &VariableOpts{
			Key:     "INFRAPI_APP_CORS_EXPOSE_HEADERS",
			Default: "[\"Content-Length\"]",
		},
		AppCorsAllowCredentials: &VariableOpts{
			Key:     "INFRAPI_APP_CORS_ALLOW_CREDENTIALS",
			Default: "true",
		},
		AppCorsMaxAge: &VariableOpts{
			Key:     "INFRAPI_APP_CORS_MAX_AGE",
			Default: "12",
		},
	}
}
