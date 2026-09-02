package config

import (
	"os"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Default environment variables

// loglevel
func TestGetAppLogLevel_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_LOG_LEVEL": "info",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppLogLevel()
	require.NoError(t, err)
	assert.Equal(t, log.InfoLevel, v)
}

func TestGetAppLogLevel_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_LOG_LEVEL": "fail",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetAppLogLevel()
	assert.Error(t, err)
}

// listen type

func TestGetAppListenType_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_LISTEN_TYPE": "tcp",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppListenType()
	require.NoError(t, err)
	assert.Equal(t, "tcp", v)
}

func TestGetAppListenType_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_LISTEN_TYPE": "fail",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetAppListenType()
	assert.Error(t, err)
}

// listen address

func TestGetAppListenAddress_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_LISTEN_ADDRESS": "127.0.0.1:80",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppListenAddress()
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:80", v)
}

func TestGetAppListenAddress_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_LISTEN_ADDRESS": "fail",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetAppListenAddress()
	assert.Error(t, err)
}

// platform

func TestGetAppPlatform_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_PLATFORM": "sandbox",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppPlatform()
	require.NoError(t, err)
	assert.Equal(t, "sandbox", v)
}

func TestGetAppPlatform_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_PLATFORM": "fail",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetAppPlatform()
	assert.Error(t, err)
}

// region

func TestGetAppRegion_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_REGION": "eu-west-1",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppRegion()
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", v)
}

func TestGetAppRegion_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_REGION": "fail",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetAppRegion()
	assert.Error(t, err)
}

// location

func TestGetAppLocation_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_LOCATION": "dc1",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppLocation()
	require.NoError(t, err)
	assert.Equal(t, "dc1", v)
}

func TestGetAppLocation_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_LOCATION": "fail",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetAppLocation()
	assert.Error(t, err)
}

// Fqdn

func TestGetAppFqdn_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_FQDN": "example.com",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppFqdn()
	require.NoError(t, err)
	assert.Equal(t, "example.com", v)
}

func TestGetAppFqdn_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_FQDN": "-fail",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetAppFqdn()
	assert.Error(t, err)
}

// url

func TestGetAppUrl_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_URL": "http://testing.local:443",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppUrl()
	require.NoError(t, err)
	assert.Equal(t, "http://testing.local:443", v)
}

func TestGetAppUrl_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_URL": "fail",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetAppUrl()
	assert.Error(t, err)
}

// name

func TestGetAppName_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_NAME": "lib",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppName()
	require.NoError(t, err)
	assert.Equal(t, "lib", v)
}

// trusted proxies

func TestGetAppTrustedProxies_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_TRUSTED_PROXIES": "[\"192.168.1.1\",\"10.0.0.1\"]",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppTrustedProxies()
	require.NoError(t, err)
	assert.Equal(t, []string{"192.168.1.1", "10.0.0.1"}, v)
}

func TestGetAppTrustedProxies_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppTrustedProxies()
	require.NoError(t, err)
	assert.Equal(t, []string{}, v)
}

// cors allow origins

func TestGetAppCorsAllowOrigins_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_CORS_ALLOW_ORIGINS": "[\"http://example.com\"]",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppCorsAllowOrigins()
	require.NoError(t, err)
	assert.Equal(t, []string{"http://example.com"}, v)
}

func TestGetAppCorsAllowOrigins_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppCorsAllowOrigins()
	require.NoError(t, err)
	assert.Equal(t, []string{"*"}, v)
}

// cors allow methods

func TestGetAppCorsAllowMethods_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_CORS_ALLOW_METHODS": "[\"GET\",\"POST\"]",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppCorsAllowMethods()
	require.NoError(t, err)
	assert.Equal(t, []string{"GET", "POST"}, v)
}

func TestGetAppCorsAllowMethods_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppCorsAllowMethods()
	require.NoError(t, err)
	assert.Equal(t, []string{"PUT", "PATCH", "HEAD"}, v)
}

// cors allow headers

func TestGetAppCorsAllowHeaders_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_CORS_ALLOW_HEADERS": "[\"Content-Type\"]",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppCorsAllowHeaders()
	require.NoError(t, err)
	assert.Equal(t, []string{"Content-Type"}, v)
}

func TestGetAppCorsAllowHeaders_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppCorsAllowHeaders()
	require.NoError(t, err)
	assert.Equal(t, []string{"Origin"}, v)
}

// cors expose headers

func TestGetAppCorsExposeHeaders_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_CORS_EXPOSE_HEADERS": "[\"X-Custom-Header\"]",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppCorsExposeHeaders()
	require.NoError(t, err)
	assert.Equal(t, []string{"X-Custom-Header"}, v)
}

func TestGetAppCorsExposeHeaders_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppCorsExposeHeaders()
	require.NoError(t, err)
	assert.Equal(t, []string{"Content-Length"}, v)
}

// cors allow credentials

func TestGetAppCorsAllowCredentials_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_CORS_ALLOW_CREDENTIALS": "false",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppCorsAllowCredentials()
	require.NoError(t, err)
	assert.Equal(t, false, v)
}

func TestGetAppCorsAllowCredentials_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppCorsAllowCredentials()
	require.NoError(t, err)
	assert.Equal(t, true, v)
}

// cors max age

func TestGetAppCorsMaxAge_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_CORS_MAX_AGE": "24",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppCorsMaxAge()
	require.NoError(t, err)
	assert.Equal(t, int64(24), v)
}

func TestGetAppCorsMaxAge_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppCorsMaxAge()
	require.NoError(t, err)
	assert.Equal(t, int64(12), v)
}

// GetAppConfig

func TestGetAppConfig_success(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "lib_test.env")

	v, err := NewConfig()
	require.NoError(t, err)

	config, err := v.GetAppConfig()
	require.NoError(t, err)
	assert.NotNil(t, config)
	assert.Equal(t, "testapp", config.AppName)
	assert.Equal(t, "sandbox", config.AppPlatform)
	assert.Equal(t, "eu-west-1", config.AppRegion)
}

func TestGetAppConfig_nil_variables(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "lib_test.env")

	var v *Variables
	config, err := v.GetAppConfig()
	require.NoError(t, err)
	assert.NotNil(t, config)
	assert.Equal(t, "testapp", config.AppName)
}

func TestGetAppConfig_error_missing_name(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetAppConfig()
	assert.Error(t, err)
}

func TestGetAppConfig_error_newconfig_fails(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "nonexistent.env")

	var v *Variables
	_, err := v.GetAppConfig()
	assert.Error(t, err)
}

// Comprehensive test for all GetAppConfig error paths
func TestGetAppConfig_all_error_paths(t *testing.T) {
	hostname, _ := os.Hostname()

	tests := []struct {
		name   string
		values map[string]string
	}{
		{
			name:   "missing app name",
			values: map[string]string{},
		},
		{
			name: "missing platform",
			values: map[string]string{
				"INFRAPI_APP_NAME": "test",
			},
		},
		{
			name: "missing region",
			values: map[string]string{
				"INFRAPI_APP_NAME":     "test",
				"INFRAPI_APP_PLATFORM": "sandbox",
			},
		},
		{
			name: "invalid fqdn",
			values: map[string]string{
				"INFRAPI_APP_NAME":     "test",
				"INFRAPI_APP_PLATFORM": "sandbox",
				"INFRAPI_APP_REGION":   "eu-west-1",
				"INFRAPI_APP_FQDN":     "invalid_fqdn_!@#",
			},
		},
		{
			name: "missing location",
			values: map[string]string{
				"INFRAPI_APP_NAME":     "test",
				"INFRAPI_APP_PLATFORM": "sandbox",
				"INFRAPI_APP_REGION":   "eu-west-1",
				"INFRAPI_APP_FQDN":     hostname,
			},
		},
		{
			name: "missing url",
			values: map[string]string{
				"INFRAPI_APP_NAME":     "test",
				"INFRAPI_APP_PLATFORM": "sandbox",
				"INFRAPI_APP_REGION":   "eu-west-1",
				"INFRAPI_APP_FQDN":     hostname,
				"INFRAPI_APP_LOCATION": "dc1",
			},
		},
		{
			name: "invalid log level",
			values: map[string]string{
				"INFRAPI_APP_NAME":      "test",
				"INFRAPI_APP_PLATFORM":  "sandbox",
				"INFRAPI_APP_REGION":    "eu-west-1",
				"INFRAPI_APP_FQDN":      hostname,
				"INFRAPI_APP_LOCATION":  "dc1",
				"INFRAPI_APP_URL":       "http://test.local",
				"INFRAPI_APP_LOG_LEVEL": "invalidlevel",
			},
		},
		{
			name: "invalid listen type",
			values: map[string]string{
				"INFRAPI_APP_NAME":        "test",
				"INFRAPI_APP_PLATFORM":    "sandbox",
				"INFRAPI_APP_REGION":      "eu-west-1",
				"INFRAPI_APP_FQDN":        hostname,
				"INFRAPI_APP_LOCATION":    "dc1",
				"INFRAPI_APP_URL":         "http://test.local",
				"INFRAPI_APP_LISTEN_TYPE": "invalid",
			},
		},
		{
			name: "invalid listen address",
			values: map[string]string{
				"INFRAPI_APP_NAME":           "test",
				"INFRAPI_APP_PLATFORM":       "sandbox",
				"INFRAPI_APP_REGION":         "eu-west-1",
				"INFRAPI_APP_FQDN":           hostname,
				"INFRAPI_APP_LOCATION":       "dc1",
				"INFRAPI_APP_URL":            "http://test.local",
				"INFRAPI_APP_LISTEN_ADDRESS": "invalid",
			},
		},
		{
			name: "invalid trusted proxies",
			values: map[string]string{
				"INFRAPI_APP_NAME":            "test",
				"INFRAPI_APP_PLATFORM":        "sandbox",
				"INFRAPI_APP_REGION":          "eu-west-1",
				"INFRAPI_APP_FQDN":            hostname,
				"INFRAPI_APP_LOCATION":        "dc1",
				"INFRAPI_APP_URL":             "http://test.local",
				"INFRAPI_APP_TRUSTED_PROXIES": "invalid_json",
			},
		},
		{
			name: "invalid cors allow origins",
			values: map[string]string{
				"INFRAPI_APP_NAME":               "test",
				"INFRAPI_APP_PLATFORM":           "sandbox",
				"INFRAPI_APP_REGION":             "eu-west-1",
				"INFRAPI_APP_FQDN":               hostname,
				"INFRAPI_APP_LOCATION":           "dc1",
				"INFRAPI_APP_URL":                "http://test.local",
				"INFRAPI_APP_CORS_ALLOW_ORIGINS": "invalid_json",
			},
		},
		{
			name: "invalid cors allow methods",
			values: map[string]string{
				"INFRAPI_APP_NAME":               "test",
				"INFRAPI_APP_PLATFORM":           "sandbox",
				"INFRAPI_APP_REGION":             "eu-west-1",
				"INFRAPI_APP_FQDN":               hostname,
				"INFRAPI_APP_LOCATION":           "dc1",
				"INFRAPI_APP_URL":                "http://test.local",
				"INFRAPI_APP_CORS_ALLOW_METHODS": "invalid_json",
			},
		},
		{
			name: "invalid cors allow headers",
			values: map[string]string{
				"INFRAPI_APP_NAME":               "test",
				"INFRAPI_APP_PLATFORM":           "sandbox",
				"INFRAPI_APP_REGION":             "eu-west-1",
				"INFRAPI_APP_FQDN":               hostname,
				"INFRAPI_APP_LOCATION":           "dc1",
				"INFRAPI_APP_URL":                "http://test.local",
				"INFRAPI_APP_CORS_ALLOW_HEADERS": "invalid_json",
			},
		},
		{
			name: "invalid cors expose headers",
			values: map[string]string{
				"INFRAPI_APP_NAME":                "test",
				"INFRAPI_APP_PLATFORM":            "sandbox",
				"INFRAPI_APP_REGION":              "eu-west-1",
				"INFRAPI_APP_FQDN":                hostname,
				"INFRAPI_APP_LOCATION":            "dc1",
				"INFRAPI_APP_URL":                 "http://test.local",
				"INFRAPI_APP_CORS_EXPOSE_HEADERS": "invalid_json",
			},
		},
		{
			name: "invalid cors allow credentials",
			values: map[string]string{
				"INFRAPI_APP_NAME":                   "test",
				"INFRAPI_APP_PLATFORM":               "sandbox",
				"INFRAPI_APP_REGION":                 "eu-west-1",
				"INFRAPI_APP_FQDN":                   hostname,
				"INFRAPI_APP_LOCATION":               "dc1",
				"INFRAPI_APP_URL":                    "http://test.local",
				"INFRAPI_APP_CORS_ALLOW_CREDENTIALS": "invalid_bool",
			},
		},
		{
			name: "invalid cors max age",
			values: map[string]string{
				"INFRAPI_APP_NAME":         "test",
				"INFRAPI_APP_PLATFORM":     "sandbox",
				"INFRAPI_APP_REGION":       "eu-west-1",
				"INFRAPI_APP_FQDN":         hostname,
				"INFRAPI_APP_LOCATION":     "dc1",
				"INFRAPI_APP_URL":          "http://test.local",
				"INFRAPI_APP_CORS_MAX_AGE": "invalid_int",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Variables{
				Values:        tt.values,
				Path:          ".env",
				DefaultConfig: NewDefaultConfig(),
			}

			_, err := c.GetAppConfig()
			assert.Error(t, err, "expected error for test case: %s", tt.name)
		})
	}
}

// GetAppLogLevel edge cases

func TestGetAppLogLevel_missing_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetAppLogLevel()
	require.NoError(t, err)
	assert.Equal(t, log.InfoLevel, v)
}

func TestGetAppLogLevel_parse_level_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_LOG_LEVEL": "validbutnotlogrusvalid",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	// This should fail validator first, but let's test parse error path
	_, err := c.GetAppLogLevel()
	assert.Error(t, err)
}

func TestGetAppLogLevel_getstring_error(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: &DefaultConfig{}, // Empty config to trigger error
	}

	v, err := c.GetAppLogLevel()
	// Should return InfoLevel and error from GetString
	assert.Error(t, err)
	assert.Equal(t, log.InfoLevel, v)
}

func TestGetAppLogLevel_parselevel_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"INFRAPI_APP_LOG_LEVEL": "notavalidloglevel",
		},
		Path: ".env",
		DefaultConfig: &DefaultConfig{
			AppLogLevel: &VariableOpts{
				Key:     "INFRAPI_APP_LOG_LEVEL",
				Default: "info",
			},
		},
	}

	v, err := c.GetAppLogLevel()
	// Should return InfoLevel and error from ParseLevel
	assert.Error(t, err)
	assert.Equal(t, log.InfoLevel, v)
}
