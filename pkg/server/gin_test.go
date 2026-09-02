package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/debug"
	"testing"

	"github.com/gin-gonic/gin"
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

func TestNewServerGin_metrics(t *testing.T) {
	app, err := NewServerGin(testAppConfig(t))
	require.NoError(t, err)

	// a handled request, and one that matches no route
	app.GET("/widgets/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
	for _, path := range []string{"/widgets/42", "/nowhere"} {
		w := httptest.NewRecorder()
		app.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	}

	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest(http.MethodGet, MetricsPath, nil))
	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	assert.Contains(t, body, `http_requests_total{method="GET",name="test.example.com",path="/widgets/:id",status="200"} 1`)
	assert.Contains(t, body, `path="`+unmatchedPath+`",status="404"} 1`)
	assert.Contains(t, body, `http_request_duration_seconds_bucket{method="GET",name="test.example.com",path="/widgets/:id"`)

	// the default registry is gathered too, so promauto keeps working
	assert.Contains(t, body, "go_goroutines")
}

func TestNewServerGin_openAPI(t *testing.T) {
	cfg := testAppConfig(t)
	app, err := NewServerGin(cfg)
	require.NoError(t, err)

	// routes registered after the engine was built must show up
	app.GET("/widgets/:id", func(c *gin.Context) {})
	app.POST("/widgets", func(c *gin.Context) {})

	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest(http.MethodGet, OpenAPIPath, nil))
	require.Equal(t, http.StatusOK, w.Code)

	var spec openAPI
	require.NoError(t, json.NewDecoder(w.Body).Decode(&spec))

	assert.Equal(t, "3.1.0", spec.OpenAPI)
	assert.Equal(t, cfg.AppName, spec.Info.Title)
	assert.NotEmpty(t, spec.Info.Version)
	require.Len(t, spec.Servers, 1)
	assert.Equal(t, cfg.AppUrl, spec.Servers[0].URL)

	assert.Contains(t, spec.Paths, "/-/metadata")
	assert.Contains(t, spec.Paths, MetricsPath)
	assert.Contains(t, spec.Paths["/widgets"], "post")

	widget := spec.Paths["/widgets/{id}"]["get"]
	require.Len(t, widget.Parameters, 1)
	assert.Equal(t, parameter{
		Name:     "id",
		In:       "path",
		Required: true,
		Schema:   map[string]string{"type": "string"},
	}, widget.Parameters[0])
	assert.Contains(t, widget.Responses, "default")
}

func TestNewServerGin_openAPI_noServerURL(t *testing.T) {
	cfg := testAppConfig(t)
	cfg.AppUrl = ""

	app, err := NewServerGin(cfg)
	require.NoError(t, err)

	assert.Empty(t, openAPISpec(app, cfg).Servers)
}

func TestOpenAPIPath(t *testing.T) {
	for _, tc := range []struct {
		route  string
		want   string
		params []string
	}{
		{route: "/widgets", want: "/widgets"},
		{route: "/widgets/:id", want: "/widgets/{id}", params: []string{"id"}},
		{route: "/tenants/:tenant/widgets/:id", want: "/tenants/{tenant}/widgets/{id}", params: []string{"tenant", "id"}},
		{route: "/files/*filepath", want: "/files/{filepath}", params: []string{"filepath"}},
		// a lone marker names nothing, gin would reject the route anyway
		{route: "/oops/:", want: "/oops/:"},
	} {
		path, params := openAPIPath(tc.route)
		assert.Equal(t, tc.want, path)

		names := []string{}
		for _, p := range params {
			names = append(names, p.Name)
		}
		assert.Equal(t, tc.params, nilIfEmpty(names), tc.route)
	}
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func TestModuleVersion(t *testing.T) {
	assert.Equal(t, defaultVersion, moduleVersion(nil))
	assert.Equal(t, defaultVersion, moduleVersion(&debug.BuildInfo{}))

	info := &debug.BuildInfo{}
	info.Main.Version = "v1.2.3"
	assert.Equal(t, "v1.2.3", moduleVersion(info))

	// whatever the test binary reports, the document never claims an empty version
	assert.NotEmpty(t, buildVersion())
}

func TestNewServerGin_docs(t *testing.T) {
	cfg := testAppConfig(t)
	app, err := NewServerGin(cfg)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest(http.MethodGet, DocsPath, nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))

	body := w.Body.String()
	assert.Contains(t, body, "<title>"+cfg.AppName+" API</title>")
	assert.Contains(t, body, "swagger-ui-dist@"+swaggerUI)
	assert.Contains(t, body, `url: "`+OpenAPIPath+`"`)
}

// The application name reaches the docs page, so it must not be able to close
// the title tag or open a script one.
func TestNewServerGin_docs_escapesAppName(t *testing.T) {
	cfg := testAppConfig(t)
	cfg.AppName = "</title><script>alert(1)</script>"

	assert.NotContains(t, string(newDocsPage(cfg)), "<script>alert(1)</script>")
}
