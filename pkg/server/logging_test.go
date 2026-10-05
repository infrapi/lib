package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// engineLoggingTo builds an engine with the logging NewServerGin installs, on
// a buffer instead of gin.DefaultWriter.
func engineLoggingTo(format string, out *bytes.Buffer) *gin.Engine {
	gin.SetMode(gin.TestMode)
	app := gin.New()
	app.Use(logMiddlewares(format, out)...)
	app.GET("/users/:id", func(c *gin.Context) { c.String(http.StatusOK, "hello") })
	app.GET("/boom", func(*gin.Context) { panic("kaboom") })
	app.GET("/gone", func(*gin.Context) { panic(http.ErrAbortHandler) })
	app.GET("/fields", func(c *gin.Context) {
		LogField(c, "tenant", "acme")
		LogField(c, "count", 3)
		LogField(c, "status", "overridden?") // reserved: dropped
		LogField(c, "tenant", "acme-corp")   // replaced
		LogField(c, "bad", func() {})        // does not marshal: its text
		c.Status(http.StatusNoContent)
	})
	// A net/http handler, as a service mounts its existing router.
	app.GET("/wrapped", gin.WrapH(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		LogField(r.Context(), "resource", map[string]string{"kind": "folder", "id": "f1"})
		w.WriteHeader(http.StatusCreated)
	})))
	return app
}

func serve(app *gin.Engine, path string, header http.Header) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	app.ServeHTTP(w, req)
	return w
}

func lines(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var all []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), "not one JSON object per line: %q", line)
		all = append(all, m)
	}
	return all
}

func TestJSONAccessLog(t *testing.T) {
	var out bytes.Buffer
	app := engineLoggingTo(LogFormatJSON, &out)

	serve(app, "/users/42?x=1", http.Header{"X-Request-Id": {"req-1"}, "User-Agent": {"probe"}})
	serve(app, "/nowhere", nil)

	got := lines(t, &out)
	require.Len(t, got, 2)

	assert.Equal(t, "info", got[0]["level"])
	assert.Equal(t, "request", got[0]["msg"])
	assert.Equal(t, "GET", got[0]["method"])
	assert.Equal(t, "/users/42", got[0]["path"], "the query string stays out of the log")
	assert.Equal(t, "/users/:id", got[0]["route"])
	assert.EqualValues(t, 200, got[0]["status"])
	assert.EqualValues(t, 5, got[0]["size"])
	assert.Equal(t, "req-1", got[0]["request_id"])
	assert.Equal(t, "probe", got[0]["user_agent"])
	assert.Contains(t, got[0], "latency_ms")
	assert.Contains(t, got[0], "time")

	assert.Equal(t, "warning", got[1]["level"])
	assert.EqualValues(t, 404, got[1]["status"])
	assert.Equal(t, "", got[1]["route"])
}

func TestJSONRecovery(t *testing.T) {
	var out bytes.Buffer
	app := engineLoggingTo(LogFormatJSON, &out)

	w := serve(app, "/boom", nil)
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	got := lines(t, &out)
	require.Len(t, got, 2, "the panic, then the request it ended")

	assert.Equal(t, "panic recovered", got[0]["msg"])
	assert.Equal(t, "error", got[0]["level"])
	assert.Equal(t, "kaboom", got[0]["panic"])
	// gin's format, starting where the panic was raised: the handler first,
	// with its source line, and no frame of the recovery itself.
	stack, _ := got[0]["stack"].(string)
	first := strings.SplitN(stack, "\n", 3)
	require.Len(t, first, 3)
	assert.Regexp(t, `logging_test\.go:\d+ \(0x[0-9a-f]+\)$`, first[0])
	assert.Equal(t, "\tengineLoggingTo.func2: app.GET(\"/boom\", func(*gin.Context) { panic(\"kaboom\") })", first[1])
	// The recovery shows up only as a caller (its c.Next), never through its
	// deferred function or panicStack.
	assert.NotContains(t, stack, "panicStack")
	assert.NotContains(t, stack, "gopanic")
	assert.NotContains(t, stack, "runtime/debug")

	assert.Equal(t, "request", got[1]["msg"])
	assert.Equal(t, "error", got[1]["level"])
	assert.EqualValues(t, 500, got[1]["status"])
}

// Anything but "json" keeps gin's text lines, which is what an AppConfig built
// by hand, with no format at all, gets.
func TestTextLogByDefault(t *testing.T) {
	for _, format := range []string{"", "text"} {
		var out bytes.Buffer
		serve(engineLoggingTo(format, &out), "/users/42", nil)
		assert.Contains(t, out.String(), "[GIN]")
		assert.NotContains(t, out.String(), `"msg"`)
	}
}

func TestNewServerGin_jsonLogs(t *testing.T) {
	cfg := testAppConfig(t)
	cfg.AppLogFormat = LogFormatJSON

	var out bytes.Buffer
	previous := gin.DefaultWriter
	gin.DefaultWriter = &out
	t.Cleanup(func() { gin.DefaultWriter = previous })

	app, err := NewServerGin(cfg)
	require.NoError(t, err)
	serve(app, "/-/metadata", nil)

	got := lines(t, &out)
	require.Len(t, got, 1)
	assert.Equal(t, "/-/metadata", got[0]["route"])
}

func TestLogFields(t *testing.T) {
	var out bytes.Buffer
	app := engineLoggingTo(LogFormatJSON, &out)

	serve(app, "/fields", nil)
	serve(app, "/wrapped", nil)

	got := lines(t, &out)
	require.Len(t, got, 2)

	assert.Equal(t, "acme-corp", got[0]["tenant"])
	assert.EqualValues(t, 3, got[0]["count"])
	assert.EqualValues(t, 204, got[0]["status"], "a service field cannot hide the engine's")
	assert.IsType(t, "", got[0]["bad"])

	assert.Equal(t, map[string]any{"kind": "folder", "id": "f1"}, got[1]["resource"])
	assert.EqualValues(t, 201, got[1]["status"])

	// The engine's members come first, the service's after them.
	assert.Less(t, strings.Index(out.String(), `"msg"`), strings.Index(out.String(), `"tenant"`))
}

// Outside a JSON-logged request, LogField is a no-op rather than a panic.
func TestLogFieldWithoutJSONLog(t *testing.T) {
	assert.NotPanics(t, func() { LogField(context.Background(), "k", "v") })

	var out bytes.Buffer
	serve(engineLoggingTo("text", &out), "/fields", nil)
	assert.NotContains(t, out.String(), "acme")
}

// A client that went away leaves a warning, no stack, and no 500 written to a
// connection that is gone — as gin does.
func TestJSONRecoveryBrokenConnection(t *testing.T) {
	var out bytes.Buffer
	serve(engineLoggingTo(LogFormatJSON, &out), "/gone", nil)

	got := lines(t, &out)
	require.Len(t, got, 2)
	assert.Equal(t, "client connection broken", got[0]["msg"])
	assert.Equal(t, "warning", got[0]["level"])
	assert.Equal(t, http.ErrAbortHandler.Error(), got[0]["error"])
	assert.NotContains(t, got[0], "stack")
	assert.Contains(t, got[1]["errors"], http.ErrAbortHandler.Error())
}

// Without the source files, as in an image that ships only the binary, every
// frame still names its function.
func TestPanicStackWithoutSources(t *testing.T) {
	stack := func() string { return panicStack(1) }()
	assert.Contains(t, stack, "TestPanicStackWithoutSources")
	for _, line := range strings.Split(strings.TrimSpace(stack), "\n") {
		if strings.HasPrefix(line, "\t") {
			assert.Regexp(t, `^\t\S+: `, line)
		}
	}
	assert.Equal(t, "???", functionName(0))
	assert.Nil(t, sourceLines("/nonexistent/file.go"))
}
