package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tracingVars(values map[string]string) *Variables {
	return &Variables{Values: values, Path: ".env", DefaultConfig: NewDefaultConfig()}
}

// Off unless asked for: no key, no export.
func TestGetTracingConfig_defaults(t *testing.T) {
	c, err := tracingVars(map[string]string{}).GetTracingConfig()
	require.NoError(t, err)
	assert.Equal(t, &TracingConfig{Protocol: "http", SampleRatio: 1, Headers: map[string]string{}}, c)
}

func TestGetTracingConfig_assign(t *testing.T) {
	c, err := tracingVars(map[string]string{
		"INFRAPI_TRACING_ENABLED":      "true",
		"INFRAPI_TRACING_ENDPOINT":     "http://collector:4317",
		"INFRAPI_TRACING_PROTOCOL":     "grpc",
		"INFRAPI_TRACING_SAMPLE_RATIO": "0.25",
		"INFRAPI_TRACING_HEADERS":      `["X-Scope-OrgID=acme", "Authorization=Bearer a=b"]`,
	}).GetTracingConfig()
	require.NoError(t, err)
	assert.Equal(t, &TracingConfig{Enabled: true, Endpoint: "http://collector:4317", Protocol: "grpc",
		SampleRatio: 0.25, Headers: map[string]string{"X-Scope-OrgID": "acme", "Authorization": "Bearer a=b"}}, c)
}

func TestGetTracingConfig_errors(t *testing.T) {
	for name, c := range map[string]struct {
		values map[string]string
		want   string
	}{
		"enabled without an endpoint": {map[string]string{"INFRAPI_TRACING_ENABLED": "true"}, "INFRAPI_TRACING_ENDPOINT is required"},
		"an endpoint that is no URL":  {map[string]string{"INFRAPI_TRACING_ENDPOINT": "collector"}, "INFRAPI_TRACING_ENDPOINT"},
		"an unknown protocol":         {map[string]string{"INFRAPI_TRACING_PROTOCOL": "udp"}, "INFRAPI_TRACING_PROTOCOL"},
		"a ratio above 1":             {map[string]string{"INFRAPI_TRACING_SAMPLE_RATIO": "1.5"}, "between 0 and 1"},
		"a ratio that is no number":   {map[string]string{"INFRAPI_TRACING_SAMPLE_RATIO": "half"}, "INFRAPI_TRACING_SAMPLE_RATIO"},
		"a header without a value":    {map[string]string{"INFRAPI_TRACING_HEADERS": `["X-Scope-OrgID"]`}, "Name=value"},
		"headers that are no list":    {map[string]string{"INFRAPI_TRACING_HEADERS": `X-Scope-OrgID=acme`}, "INFRAPI_TRACING_HEADERS"},
	} {
		_, err := tracingVars(c.values).GetTracingConfig()
		assert.ErrorContains(t, err, c.want, name)
	}
}

// A header's value is a secret: it never shows in an error.
func TestGetTracingHeaders_masked(t *testing.T) {
	_, err := tracingVars(map[string]string{"INFRAPI_TRACING_HEADERS": `["Authorization"]`}).GetTracingConfig()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "Authorization")
}

func TestGetTracingConfig_tls(t *testing.T) {
	c, err := tracingVars(map[string]string{
		"INFRAPI_TRACING_ENDPOINT":         "https://collector:4318",
		"INFRAPI_TRACING_CA_CERT_PATH":     "/etc/ssl/ca.pem",
		"INFRAPI_TRACING_CLIENT_CERT_PATH": "/etc/tracing/client.pem",
		"INFRAPI_TRACING_CLIENT_KEY_PATH":  "/etc/tracing/client-key.pem",
	}).GetTracingConfig()
	require.NoError(t, err)
	assert.Equal(t, "/etc/ssl/ca.pem", c.CACertPath)
	assert.Equal(t, "/etc/tracing/client.pem", c.ClientCertPath)
	assert.Equal(t, "/etc/tracing/client-key.pem", c.ClientKeyPath)

	for name, values := range map[string]map[string]string{
		"a certificate without its key": {"INFRAPI_TRACING_ENDPOINT": "https://c:4318", "INFRAPI_TRACING_CLIENT_CERT_PATH": "/c.pem"},
		"a key without its certificate": {"INFRAPI_TRACING_ENDPOINT": "https://c:4318", "INFRAPI_TRACING_CLIENT_KEY_PATH": "/k.pem"},
	} {
		_, err := tracingVars(values).GetTracingConfig()
		assert.ErrorContains(t, err, "set both or neither", name)
	}
	_, err = tracingVars(map[string]string{"INFRAPI_TRACING_ENDPOINT": "http://c:4318",
		"INFRAPI_TRACING_CA_CERT_PATH": "/ca.pem"}).GetTracingConfig()
	assert.ErrorContains(t, err, "are for an https://", "TLS files with a clear endpoint")
}
