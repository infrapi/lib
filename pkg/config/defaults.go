package config

import (
	"os"
)

// DefaultConfig holds the read and validation rules of every INFRAPI_APP_* and
// INFRAPI_TRACING_* key.
type DefaultConfig struct {
	AppName                 *VariableOpts
	AppLogLevel             *VariableOpts
	AppLogFormat            *VariableOpts
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

	TracingEnabled        *VariableOpts
	TracingEndpoint       *VariableOpts
	TracingProtocol       *VariableOpts
	TracingSampleRatio    *VariableOpts
	TracingHeaders        *VariableOpts
	TracingCACertPath     *VariableOpts
	TracingClientCertPath *VariableOpts
	TracingClientKeyPath  *VariableOpts
}

// NewDefaultConfig returns the rules applied to the INFRAPI_APP_* keys. A
// service may tighten or relax any of them before reading its configuration.
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
		AppLogFormat: &VariableOpts{
			Key:       "INFRAPI_APP_LOG_FORMAT",
			Validator: "oneof=text json",
			Default:   "text",
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

		TracingEnabled: &VariableOpts{
			Key:     "INFRAPI_TRACING_ENABLED",
			Default: "false",
		},
		TracingEndpoint: &VariableOpts{
			Key:       "INFRAPI_TRACING_ENDPOINT",
			Validator: "omitempty,url",
			Optional:  true,
		},
		TracingProtocol: &VariableOpts{
			Key:       "INFRAPI_TRACING_PROTOCOL",
			Validator: "oneof=http grpc",
			Default:   "http",
		},
		TracingSampleRatio: &VariableOpts{
			Key:       "INFRAPI_TRACING_SAMPLE_RATIO",
			Validator: "numeric",
			Default:   "1",
		},
		TracingHeaders: &VariableOpts{
			Key:      "INFRAPI_TRACING_HEADERS",
			Default:  "[]",
			Sensible: true,
		},
		TracingCACertPath: &VariableOpts{
			Key:      "INFRAPI_TRACING_CA_CERT_PATH",
			Optional: true,
		},
		TracingClientCertPath: &VariableOpts{
			Key:      "INFRAPI_TRACING_CLIENT_CERT_PATH",
			Optional: true,
		},
		TracingClientKeyPath: &VariableOpts{
			Key:      "INFRAPI_TRACING_CLIENT_KEY_PATH",
			Optional: true,
		},
	}
}
