package bridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/auth"
	"github.com/infrapi/lib/pkg/config"
	infraerrors "github.com/infrapi/lib/pkg/errors"
	"github.com/infrapi/lib/pkg/mcp"
	"github.com/infrapi/lib/pkg/mcp/bridge"
	"github.com/infrapi/lib/pkg/server"
)

const (
	root = "/apis/compute.infrapi.io/v1"
	base = root + "/namespaces/{namespace}"
)

// contract is the service's own API: one route, besides the bridged ones.
const contract = `{
  "openapi": "3.0.3",
  "info": {"title": "compute", "version": "v1"},
  "paths": {
    "/apis/compute.infrapi.io/v1/namespaces/{namespace}/vms/{vm}": {
      "parameters": [
        {"name": "namespace", "in": "path", "required": true, "schema": {"type": "string"}},
        {"name": "vm", "in": "path", "required": true, "schema": {"type": "string"}}
      ],
      "get": {"operationId": "getVm", "summary": "Read a VM", "responses": {"200": {"description": "The VM"}}}
    }
  }
}`

type flag = bool

func ptr(b flag) *flag { return &b }

// call is what the bridged server was asked.
type call struct {
	tool    string
	args    map[string]any
	token   string
	static  string
	request string
}

// upstreamServer is an MCP server of tools in families, as mcp-grafana's,
// recording each call and its headers.
type upstreamServer struct {
	mu         sync.Mutex
	calls      []call
	generation int
}

func (u *upstreamServer) record(req *sdk.CallToolRequest) map[string]any {
	args := map[string]any{}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			args["unreadable"] = err.Error()
		}
	}
	c := call{tool: req.Params.Name, args: args}
	if req.Extra != nil {
		c.token = req.Extra.Header.Get("X-Grafana-Service-Account-Token")
		c.static = req.Extra.Header.Get("Authorization")
		c.request = req.Extra.Header.Get("X-Request-Id")
	}
	u.mu.Lock()
	u.calls = append(u.calls, c)
	u.mu.Unlock()
	return args
}

func (u *upstreamServer) last(t *testing.T) call {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	require.NotEmpty(t, u.calls)
	return u.calls[len(u.calls)-1]
}

// restart drops every session of the server started, as a restart of the
// bridged server would: the bridge's next call opens another.
func (u *upstreamServer) restart() {
	u.mu.Lock()
	u.generation++
	u.mu.Unlock()
}

func (u *upstreamServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	s := sdk.NewServer(&sdk.Implementation{Name: "grafana", Version: "v1"}, nil)
	readOnly := &sdk.ToolAnnotations{ReadOnlyHint: true}
	object := func(props map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	add := func(name string, hints *sdk.ToolAnnotations, schema map[string]any, h func(map[string]any) *sdk.CallToolResult) {
		s.AddTool(&sdk.Tool{Name: name, Description: "the " + name + " tool", Annotations: hints, InputSchema: schema},
			func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return h(u.record(req)), nil
			})
	}
	add("query_prometheus", readOnly, object(map[string]any{"expr": map[string]any{"type": "string"}}, "expr"),
		func(args map[string]any) *sdk.CallToolResult {
			return &sdk.CallToolResult{
				Content:           []sdk.Content{&sdk.TextContent{Text: "a vector"}},
				StructuredContent: map[string]any{"expr": args["expr"], "result": []any{1.0}},
			}
		})
	add("list_prometheus_metric_names", readOnly, object(map[string]any{}),
		func(map[string]any) *sdk.CallToolResult {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: `["up","node_load1"]`}}}
		})
	add("query_loki_logs", readOnly, object(map[string]any{"logql": map[string]any{"type": "string"}}),
		func(map[string]any) *sdk.CallToolResult {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "parse error: unexpected }"}}}
		})
	add("get_panel_image", readOnly, object(map[string]any{}),
		func(map[string]any) *sdk.CallToolResult {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "not JSON"}}}
		})
	add("update_dashboard", &sdk.ToolAnnotations{DestructiveHint: ptr(false)},
		object(map[string]any{"uid": map[string]any{"type": "string"}}, "uid"),
		func(args map[string]any) *sdk.CallToolResult {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf(`{"updated":%q}`, args["uid"])}}}
		})
	add("search_docs", readOnly, object(map[string]any{}),
		func(map[string]any) *sdk.CallToolResult { return &sdk.CallToolResult{} })

	handlers := map[int]http.Handler{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		h, ok := handlers[u.generation]
		if !ok {
			// a new handler knows none of the sessions of the previous one
			h = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s }, nil)
			handlers[u.generation] = h
		}
		u.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

var rules = []bridge.Rule{
	{Match: "query_prometheus.*|list_prometheus_.*", Resource: "prometheus"},
	{Match: "query_loki.*", Resource: "loki"},
	{Match: "get_panel_image|update_dashboard", Resource: "dashboards"},
}

// callHeaders gives each namespace its own token; ghost has none.
func callHeaders(_ *http.Request, namespace string) (http.Header, error) {
	if namespace == "ghost" {
		return nil, infraerrors.NotOnboarded("namespace %s has no Grafana", namespace)
	}
	return http.Header{"X-Grafana-Service-Account-Token": {"token-" + namespace}}, nil
}

func newBridge(t *testing.T, up *httptest.Server, writes bool) *bridge.Bridge {
	t.Helper()
	b, err := bridge.New(context.Background(), bridge.Config{
		Name: "grafana", URL: up.URL, Rules: rules, Writes: writes,
		Headers:     http.Header{"Authorization": {"Bearer server-token"}},
		CallHeaders: callHeaders,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, b.Close()) })
	return b
}

func appConfig(t *testing.T) *config.AppConfig {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.env")
	require.NoError(t, err)
	_, err = f.WriteString("INFRAPI_APP_NAME=compute\nINFRAPI_APP_PLATFORM=sandbox\nINFRAPI_APP_REGION=eu-west-1\n" +
		"INFRAPI_APP_LOCATION=dc1\nINFRAPI_APP_FQDN=compute.example.com\nINFRAPI_APP_URL=http://compute.example.com\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", f.Name())
	cfg, err := config.NewConfig()
	require.NoError(t, err)
	app, err := cfg.GetAppConfig()
	require.NoError(t, err)
	return app
}

// service is the API with the bridge: its contract enforced, its MCP
// endpoint at <root>/-/mcp.
func service(t *testing.T, b *bridge.Bridge) (*httptest.Server, []byte) {
	t.Helper()
	merged, err := b.Contract([]byte(contract), base)
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	engine, err := server.NewServerGin(appConfig(t), []string{root},
		server.WithOpenAPI(server.OpenAPISpec{Name: "compute v1", Path: "compute/v1.json", Content: merged}))
	require.NoError(t, err)
	engine.GET(root+"/namespaces/:namespace/vms/:vm", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"name": c.Param("vm")})
	})
	require.NoError(t, b.Register(engine, base, nil))
	h, err := mcp.New(merged, engine, mcp.Options{Name: "compute", Tools: b.Tools(base)})
	require.NoError(t, err)
	engine.Any(root+"/-/mcp", gin.WrapH(h))
	srv := httptest.NewServer(engine)
	t.Cleanup(func() {
		h.Close()
		srv.Close()
	})
	return srv, merged
}

func get(t *testing.T, srv *httptest.Server, path string, args string) (int, map[string]any) {
	t.Helper()
	target := srv.URL + path
	if args != "" {
		target += "?" + url.Values{"arguments": {args}}.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	require.NoError(t, err)
	req.Header.Set("X-Request-Id", "req-1")
	return do(t, req)
}

func do(t *testing.T, req *http.Request) (int, map[string]any) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body), string(raw))
	return resp.StatusCode, body
}

func TestNewSelectsTools(t *testing.T) {
	up := (&upstreamServer{}).start(t)

	b := newBridge(t, up, false)
	assert.Equal(t, []string{"dashboards", "loki", "prometheus"}, b.Resources())
	var names []string
	for _, tool := range b.Tools(base) {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	// update_dashboard is not read-only: only with Writes; search_docs no
	// rule selects
	assert.Equal(t, []string{"get_panel_image", "list_prometheus_metric_names", "query_loki_logs", "query_prometheus"}, names)

	w := newBridge(t, up, true)
	assert.Len(t, w.Tools(base), 5)
}

func TestNewRefuses(t *testing.T) {
	up := (&upstreamServer{}).start(t)
	for name, cfg := range map[string]bridge.Config{
		"no name":         {URL: up.URL, Rules: rules},
		"no rule":         {Name: "grafana", URL: up.URL},
		"bad expression":  {Name: "grafana", URL: up.URL, Rules: []bridge.Rule{{Match: "(", Resource: "x"}}},
		"empty match":     {Name: "grafana", URL: up.URL, Rules: []bridge.Rule{{Resource: "x"}}},
		"bad resource":    {Name: "grafana", URL: up.URL, Rules: []bridge.Rule{{Match: ".*", Resource: "Prom/etheus"}}},
		"nothing matches": {Name: "grafana", URL: up.URL, Rules: []bridge.Rule{{Match: "nope", Resource: "x"}}},
		"unreachable":     {Name: "grafana", URL: "http://127.0.0.1:1/mcp", Rules: rules},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := bridge.New(context.Background(), cfg)
			require.Error(t, err)
		})
	}
}

func TestContract(t *testing.T) {
	up := (&upstreamServer{}).start(t)
	b := newBridge(t, up, true)
	_, merged := service(t, b)

	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(merged, &doc))
	var paths []string
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	assert.Equal(t, []string{
		base + "/dashboards/{tool}", base + "/loki/{tool}", base + "/prometheus/{tool}", base + "/vms/{vm}",
	}, paths)
	assert.Contains(t, doc.Paths[base+"/dashboards/{tool}"], "get")
	assert.Contains(t, doc.Paths[base+"/dashboards/{tool}"], "post")
	assert.NotContains(t, doc.Paths[base+"/prometheus/{tool}"], "post")

	// Kayra authorizes a call on the resource of its family, verb get for
	// a read, create for a write: as lib's auth reads the request's path.
	for path, verb := range map[string]string{
		"prometheus/query_prometheus": "get", "dashboards/update_dashboard": "create",
	} {
		resource, hasID, err := auth.ParseResource(path)
		require.NoError(t, err)
		assert.Equal(t, strings.Split(path, "/")[0], resource)
		method := http.MethodGet
		if verb == "create" {
			method = http.MethodPost
		}
		got, err := auth.VerbFor(method, hasID)
		require.NoError(t, err)
		assert.Equal(t, verb, got)
	}

	// added twice: the routes exist already
	_, err := b.Contract(merged, base)
	require.ErrorContains(t, err, "already has")
	_, err = b.Contract(merged, "/apis/compute.infrapi.io/v1")
	require.ErrorContains(t, err, "{namespace}")
	_, err = b.Contract([]byte("not a contract"), base)
	require.Error(t, err)
	require.ErrorContains(t, b.Register(gin.New(), "relative/{namespace}", nil), "{namespace}")
}

func TestREST(t *testing.T) {
	u := &upstreamServer{}
	b := newBridge(t, u.start(t), true)
	srv, _ := service(t, b)

	code, body := get(t, srv, root+"/namespaces/acme/prometheus/query_prometheus", `{"expr":"up"}`)
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, map[string]any{"expr": "up", "result": []any{1.0}}, body)
	c := u.last(t)
	assert.Equal(t, call{tool: "query_prometheus", args: map[string]any{"expr": "up"},
		token: "token-acme", static: "Bearer server-token", request: "req-1"}, c)

	// a JSON text is the body as it is; another text, wrapped
	code, raw := get(t, srv, root+"/namespaces/acme/dashboards/get_panel_image", "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []any{map[string]any{"type": "text", "text": "not JSON"}}, raw["content"])

	// a tool's error: 422, its text the detail
	code, body = get(t, srv, root+"/namespaces/acme/loki/query_loki_logs", `{"logql":"{"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Equal(t, "tool_error", body["code"])
	assert.Equal(t, "parse error: unexpected }", body["detail"])

	// the contract refuses a tool not of the family, and arguments that are
	// not an object
	code, _ = get(t, srv, root+"/namespaces/acme/prometheus/query_loki_logs", "")
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = get(t, srv, root+"/namespaces/acme/prometheus/query_prometheus", `[1]`)
	assert.Equal(t, http.StatusBadRequest, code)

	// a namespace without a credential: CallHeaders' answer
	code, body = get(t, srv, root+"/namespaces/ghost/prometheus/query_prometheus", `{"expr":"up"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, code, body)
	assert.Equal(t, "tenant_not_onboarded", body["code"])

	// a write: POST, its arguments the body
	req, err := http.NewRequest(http.MethodPost, srv.URL+root+"/namespaces/acme/dashboards/update_dashboard", strings.NewReader(`{"uid":"abc"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	code, body = do(t, req)
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, map[string]any{"updated": "abc"}, body)
	assert.Equal(t, "token-acme", u.last(t).token)

	// a read tool is not POSTed, nor a write read: the contract's tool
	// enum, per method
	req, err = http.NewRequest(http.MethodPost, srv.URL+root+"/namespaces/acme/dashboards/get_panel_image", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	code, _ = do(t, req)
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = get(t, srv, root+"/namespaces/acme/dashboards/update_dashboard", "")
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestMCP(t *testing.T) {
	u := &upstreamServer{}
	b := newBridge(t, u.start(t), false)
	srv, _ := service(t, b)

	client := sdk.NewClient(&sdk.Implementation{Name: "agent", Version: "v1"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: srv.URL + root + "/-/mcp"}, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, session.Close()) }()

	tools := map[string]*sdk.Tool{}
	for tool, err := range session.Tools(context.Background(), nil) {
		require.NoError(t, err)
		tools[tool.Name] = tool
	}
	// the service's own operation, and the bridged tools: not the generic
	// routes they go through
	var names []string
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"get_panel_image", "get_vm", "list_prometheus_metric_names", "query_loki_logs", "query_prometheus"}, names)

	schema, err := json.Marshal(tools["query_prometheus"].InputSchema)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object","properties":{"expr":{"type":"string"},
		"namespace":{"type":"string","description":"The namespace to call the tool in"}},"required":["expr","namespace"]}`, string(schema))
	assert.True(t, tools["query_prometheus"].Annotations.ReadOnlyHint)

	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "query_prometheus",
		Arguments: map[string]any{"namespace": "acme", "expr": "rate(x[5m])"}})
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Equal(t, map[string]any{"expr": "rate(x[5m])", "result": []any{1.0}}, res.StructuredContent)
	c := u.last(t)
	assert.Equal(t, map[string]any{"expr": "rate(x[5m])"}, c.args)
	assert.Equal(t, "token-acme", c.token)

	// the route's problem, as the tool error the agent reads
	res, err = session.CallTool(context.Background(), &sdk.CallToolParams{Name: "query_loki_logs",
		Arguments: map[string]any{"namespace": "acme", "logql": "{"}})
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].(*sdk.TextContent).Text, "parse error")

	res, err = session.CallTool(context.Background(), &sdk.CallToolParams{Name: "query_prometheus",
		Arguments: map[string]any{"expr": "up"}})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}

func TestToolsCollide(t *testing.T) {
	b := newBridge(t, (&upstreamServer{}).start(t), false)
	_, err := mcp.New([]byte(contract), http.NotFoundHandler(), mcp.Options{Name: "compute",
		Tools: append(b.Tools(base), mcp.Tool{Name: "query_prometheus", Request: b.Tools(base)[0].Request})})
	require.ErrorContains(t, err, "query_prometheus")
	_, err = mcp.New([]byte(contract), http.NotFoundHandler(), mcp.Options{Name: "compute", Tools: []mcp.Tool{{Name: "x"}}})
	require.ErrorContains(t, err, "Request")
}

// The bridged server restarting, its session is gone: the call opens
// another and goes through.
func TestReconnects(t *testing.T) {
	u := &upstreamServer{}
	b := newBridge(t, u.start(t), false)
	srv, _ := service(t, b)

	code, _ := get(t, srv, root+"/namespaces/acme/prometheus/query_prometheus", `{"expr":"up"}`)
	require.Equal(t, http.StatusOK, code)
	u.restart()
	code, body := get(t, srv, root+"/namespaces/acme/prometheus/query_prometheus", `{"expr":"up"}`)
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "token-acme", u.last(t).token)
}

// The bridged server gone: 502, retryable.
func TestUpstreamDown(t *testing.T) {
	u := &upstreamServer{}
	up := u.start(t)
	b := newBridge(t, up, false)
	srv, _ := service(t, b)
	up.Close()

	code, body := get(t, srv, root+"/namespaces/acme/prometheus/query_prometheus", `{"expr":"up"}`)
	assert.Equal(t, http.StatusBadGateway, code)
	assert.Equal(t, "upstream_error", body["code"])
	assert.Equal(t, true, body["retryable"])
}

// Without the contract in front, the handler checks the tool and the
// arguments itself.
func TestHandlerAlone(t *testing.T) {
	b := newBridge(t, (&upstreamServer{}).start(t), false)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	var guarded []string
	require.NoError(t, b.Register(engine, base, func(resource string) []gin.HandlerFunc {
		return []gin.HandlerFunc{func(c *gin.Context) {
			guarded = append(guarded, resource+" "+c.Param("tool"))
			if c.Param("namespace") == "forbidden" {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"status": 403})
			}
		}}
	}))
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)

	code, body := get(t, srv, root+"/namespaces/acme/prometheus/nope", "")
	assert.Equal(t, http.StatusNotFound, code, body)
	code, body = get(t, srv, root+"/namespaces/acme/prometheus/query_prometheus", `[1]`)
	assert.Equal(t, http.StatusBadRequest, code, body)
	code, body = get(t, srv, root+"/namespaces/acme/prometheus/query_prometheus", `{"expr":"up"}`)
	require.Equal(t, http.StatusOK, code, body)

	// the guard runs first, and may refuse
	code, _ = get(t, srv, root+"/namespaces/forbidden/loki/query_loki_logs", "")
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, []string{"prometheus nope", "prometheus query_prometheus", "prometheus query_prometheus", "loki query_loki_logs"}, guarded)
}

// NamespacePattern: the routes' namespace parameter has the API's pattern.
func TestNamespacePattern(t *testing.T) {
	up := (&upstreamServer{}).start(t)
	b, err := bridge.New(context.Background(), bridge.Config{Name: "grafana", URL: up.URL, Rules: rules, NamespacePattern: "^[a-z]+$"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, b.Close()) })
	merged, err := b.Contract([]byte(contract), base)
	require.NoError(t, err)
	assert.Contains(t, string(merged), `"pattern":"^[a-z]+$"`)
}

// A write tool, with Config.Writes: its call is a POST, its arguments the
// body; not read-only, destructive unless the bridged tool says otherwise.
func TestMCPWrite(t *testing.T) {
	u := &upstreamServer{}
	b := newBridge(t, u.start(t), true)
	srv, _ := service(t, b)

	client := sdk.NewClient(&sdk.Implementation{Name: "agent", Version: "v1"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: srv.URL + root + "/-/mcp"}, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, session.Close()) }()

	var update *sdk.Tool
	for tool, err := range session.Tools(context.Background(), nil) {
		require.NoError(t, err)
		if tool.Name == "update_dashboard" {
			update = tool
		}
	}
	require.NotNil(t, update)
	assert.False(t, update.Annotations.ReadOnlyHint)
	assert.False(t, *update.Annotations.DestructiveHint)

	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "update_dashboard",
		Arguments: map[string]any{"namespace": "acme", "uid": "abc"}})
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Equal(t, map[string]any{"updated": "abc"}, res.StructuredContent)
	assert.Equal(t, call{tool: "update_dashboard", args: map[string]any{"uid": "abc"}, token: "token-acme", static: "Bearer server-token"}, u.last(t))
}
