package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/config"
)

const testEnvContent = `
INFRAPI_APP_NAME=test-app
INFRAPI_APP_PLATFORM=sandbox
INFRAPI_APP_REGION=eu-west-1
INFRAPI_APP_LOCATION=dc1
INFRAPI_APP_FQDN=test.example.com
INFRAPI_APP_URL=http://test.example.com
`

func testAppConfig(t *testing.T) *config.AppConfig {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "*.env")
	require.NoError(t, err)
	_, err = f.WriteString(testEnvContent)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", f.Name())

	cfg, err := config.NewConfig()
	require.NoError(t, err)
	appConfig, err := cfg.GetAppConfig()
	require.NoError(t, err)
	return appConfig
}

func TestNewServerGin_routes(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t))
	require.NoError(t, err)
	require.NotNil(t, app)

	found := false
	for _, route := range app.Routes() {
		if route.Path == "/-/metadata" && route.Method == "GET" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected /-/metadata GET route to be registered")
}

func TestNewServerGin_metadata(t *testing.T) {
	cfg := testAppConfig(t)
	app, err := NewServerGin(cfg)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/-/metadata", nil)
	app.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var body map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, cfg.AppName, body["appName"])
	assert.Equal(t, cfg.AppPlatform, body["appPlatform"])
	assert.Equal(t, cfg.AppRegion, body["appRegion"])
	assert.Equal(t, cfg.AppLocation, body["appLocation"])
	assert.Equal(t, cfg.AppFqdn, body["appFqdn"])
	assert.Equal(t, cfg.AppUrl, body["appUrl"])
}

func TestNewServerGin_trustedProxies_invalid(t *testing.T) {
	cfg := testAppConfig(t)
	// gin rejects CIDR notation that is unparseable — inject a bad value to
	// exercise the error path in NewServerGin.
	cfg.AppTrustedProxies = []string{"not-a-valid-cidr/99"}

	_, err := NewServerGin(cfg)
	assert.Error(t, err)
}
