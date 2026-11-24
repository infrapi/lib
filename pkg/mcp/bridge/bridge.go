// Package bridge serves the tools of another MCP server as part of a
// service's API: each one a route of the contract, authorized as one of the
// API's resources, and an MCP tool of the service's own.
//
// An MCP server knows nothing of the platform's namespaces, nor of its
// RBAC: mcp-grafana, say, runs with whatever credential it is given. Bridged
// into a service, its tools are called in a namespace of the service's API,
// with the credential the service holds for that namespace, after the same
// checks as any other route of the API:
//
//	GET  <base>/<resource>/{tool}?arguments=<JSON>   a read-only tool: verb get on <resource>
//	POST <base>/<resource>/{tool}                    any other tool, with Config.Writes: verb create
//
// base is the API's namespaced path, /apis/compute.infrapi.io/v1/namespaces/{namespace}.
// The resource of a tool is the one of the first Config.Rules entry its name
// matches; a tool no entry matches is not bridged. A role granting
// prometheus and not loki on the API then gives the Prometheus tools of
// mcp-grafana and not its Loki ones.
//
// The routes are generic, one per resource, and kept out of the service's
// own MCP tools (x-infrapi-mcp: false); [Bridge.Tools] are the tools an agent
// sees instead, one per bridged tool with its own input schema and a
// namespace argument, each call going through the route:
//
//	b, err := bridge.New(ctx, bridge.Config{
//		Name: "grafana", URL: "http://127.0.0.1:8000/mcp",
//		Rules: []bridge.Rule{
//			{Match: "query_prometheus.*|list_prometheus_.*", Resource: "prometheus"},
//			{Match: "query_loki.*|list_loki_.*", Resource: "loki"},
//		},
//		CallHeaders: func(r *http.Request, namespace string) (http.Header, error) {
//			// the namespace's credential
//		},
//	})
//	contract, err = b.Contract(contract, base)  // before server.WithOpenAPI
//	b.Register(engine, base)
//	h, err := mcp.New(contract, engine, mcp.Options{Name: "compute", Tools: b.Tools(base)})
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	infraerrors "github.com/infrapi/lib/pkg/errors"
	"github.com/infrapi/lib/pkg/mcp"
	"github.com/infrapi/lib/pkg/server"
)

// DefaultProtocolVersion is the MCP protocol version a bridge opens its
// session with unless Config.ProtocolVersion is set: stateful servers such as
// mcp-grafana refuse newer ones.
const DefaultProtocolVersion = "2025-06-18"

// DefaultTimeout bounds one call to the bridged server unless Config.Timeout
// is set: long enough for a heavy query, short enough not to hang.
const DefaultTimeout = 5 * time.Minute

// Config is the MCP server to bridge, and which of its tools.
type Config struct {
	// Name names the bridged server in errors and in its routes' tags,
	// "grafana"; required.
	Name string
	// URL is its Streamable HTTP endpoint; required.
	URL string
	// ProtocolVersion is DefaultProtocolVersion unless set.
	ProtocolVersion string
	// Rules select the tools bridged, first match wins, and give each its
	// resource; required.
	Rules []Rule
	// Writes bridges the tools that are not read-only too, as POST routes;
	// only the read-only ones otherwise.
	Writes bool
	// Headers are sent on every request to the bridged server: the bearer
	// it expects of its only client, say.
	Headers http.Header
	// CallHeaders, optional, are the headers of one call, from the request
	// to the service and its namespace: the namespace's own credential for
	// the bridged server. An *errors.Error it returns is the answer to the
	// request.
	CallHeaders func(r *http.Request, namespace string) (http.Header, error)
	// Transport is http.DefaultTransport unless set.
	Transport http.RoundTripper
	// Timeout bounds one call; DefaultTimeout unless set.
	Timeout time.Duration
	// NamespacePattern, optional, is the pattern of the routes' namespace
	// parameter in the contract: the one the API's other routes have.
	NamespacePattern string
}

// Rule selects tools by name and names their resource.
type Rule struct {
	// Match is a regular expression on the whole tool name.
	Match string
	// Resource is the API resource its calls are authorized on: lower case
	// letters, digits and dashes.
	Resource string
}

// Bridge is a bridged MCP server: its tools, listed once when it is built.
type Bridge struct {
	cfg      Config
	client   *upstream
	tools    []bridged
	byPath   map[string]bridged // resource/tool
	resource []string           // sorted, each once
}

// bridged is one tool of the bridged server.
type bridged struct {
	tool     *sdk.Tool
	resource string
	readOnly bool
}

var resourceName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// New connects to the bridged server and lists its tools, keeping those the
// rules select. The tools are read once: the contract they make is the
// service's, fixed while it runs.
func New(ctx context.Context, cfg Config) (*Bridge, error) {
	if cfg.Name == "" || cfg.URL == "" {
		return nil, errors.New("bridge: Name and URL are required")
	}
	if len(cfg.Rules) == 0 {
		return nil, fmt.Errorf("bridge %s: no rule selects a tool", cfg.Name)
	}
	type rule struct {
		re       *regexp.Regexp
		resource string
	}
	rules := make([]rule, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		re, err := regexp.Compile("^(?:" + r.Match + ")$")
		if err != nil || r.Match == "" {
			return nil, fmt.Errorf("bridge %s: rule %q: want a regular expression", cfg.Name, r.Match)
		}
		if !resourceName.MatchString(r.Resource) {
			return nil, fmt.Errorf("bridge %s: rule %q: resource %q: want lower case letters, digits and dashes", cfg.Name, r.Match, r.Resource)
		}
		rules = append(rules, rule{re, r.Resource})
	}
	if cfg.ProtocolVersion == "" {
		cfg.ProtocolVersion = DefaultProtocolVersion
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}

	b := &Bridge{cfg: cfg, byPath: map[string]bridged{}}
	b.client = newUpstream(cfg)
	list, err := b.client.listTools(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("bridge %s: list its tools: %w", cfg.Name, err), b.client.close())
	}
	seen := map[string]bool{}
	for _, t := range list {
		readOnly := t.Annotations != nil && t.Annotations.ReadOnlyHint
		if !readOnly && !cfg.Writes {
			continue
		}
		for _, r := range rules {
			if !r.re.MatchString(t.Name) {
				continue
			}
			if strings.ContainsAny(t.Name, "/?#") {
				return nil, errors.Join(fmt.Errorf("bridge %s: tool %q: its name cannot be a path segment", cfg.Name, t.Name), b.client.close())
			}
			if props, _ := inputSchema(t.InputSchema)["properties"].(map[string]any); props["namespace"] != nil {
				return nil, errors.Join(fmt.Errorf("bridge %s: tool %q has an argument named namespace, which its bridged tool takes for the service's namespace", cfg.Name, t.Name), b.client.close())
			}
			bt := bridged{tool: t, resource: r.resource, readOnly: readOnly}
			b.tools = append(b.tools, bt)
			b.byPath[r.resource+"/"+t.Name] = bt
			if !seen[r.resource] {
				seen[r.resource] = true
				b.resource = append(b.resource, r.resource)
			}
			break
		}
	}
	sort.Strings(b.resource)
	if len(b.tools) == 0 {
		return nil, errors.Join(fmt.Errorf("bridge %s: none of its %d tools matches a rule", cfg.Name, len(list)), b.client.close())
	}
	return b, nil
}

// Close ends the session with the bridged server.
func (b *Bridge) Close() error {
	return b.client.close()
}

// Resources are the API resources the bridged tools are authorized on.
func (b *Bridge) Resources() []string {
	return append([]string(nil), b.resource...)
}

// splitBase checks base, the API's namespaced path with a {namespace}
// parameter, and returns it.
func splitBase(base string) (string, error) {
	base = strings.TrimRight(base, "/")
	if !strings.Contains(base, "/{namespace}") || !strings.HasPrefix(base, "/") {
		return "", fmt.Errorf("bridge: base %q: want the API's namespaced path, /apis/<group>/<version>/namespaces/{namespace}", base)
	}
	return base, nil
}

// Contract returns contract, an OpenAPI 3 document in JSON or YAML, with
// the bridged tools' routes added, in JSON: what the service serves and
// enforces (server.WithOpenAPI), and what Kayra reads the API's resources
// from. A route the contract already has is an error.
func (b *Bridge) Contract(contract []byte, base string) ([]byte, error) {
	base, err := splitBase(base)
	if err != nil {
		return nil, err
	}
	doc, err := openapi3.NewLoader().LoadFromData(contract)
	if err != nil {
		return nil, fmt.Errorf("bridge %s: load the contract: %w", b.cfg.Name, err)
	}
	if doc.Paths == nil {
		doc.Paths = openapi3.NewPaths()
	}
	for _, res := range b.resource {
		path := base + "/" + res + "/{tool}"
		if doc.Paths.Value(path) != nil {
			return nil, fmt.Errorf("bridge %s: the contract already has %s", b.cfg.Name, path)
		}
		var reads, writes []string
		for _, t := range b.tools {
			if t.resource != res {
				continue
			}
			if t.readOnly {
				reads = append(reads, t.tool.Name)
			} else {
				writes = append(writes, t.tool.Name)
			}
		}
		item := &openapi3.PathItem{Parameters: openapi3.Parameters{namespaceParam(b.cfg.NamespacePattern)}}
		ext := map[string]any{mcp.Extension: false}
		responses := openapi3.NewResponses(
			openapi3.WithStatus(http.StatusOK, &openapi3.ResponseRef{Value: openapi3.NewResponse().
				WithDescription("The tool's result").WithJSONSchema(openapi3.NewSchema())}),
		)
		if len(reads) > 0 {
			item.Get = &openapi3.Operation{
				OperationID: operationID("call", b.cfg.Name, res),
				Tags:        []string{b.cfg.Name},
				Summary:     fmt.Sprintf("Call a read-only %s tool of %s", res, b.cfg.Name),
				Description: fmt.Sprintf("Bridged from the MCP server %s. Tools: %s.", b.cfg.Name, strings.Join(reads, ", ")),
				Parameters: openapi3.Parameters{toolParam(reads), {Value: &openapi3.Parameter{
					Name: "arguments", In: openapi3.ParameterInQuery,
					Description: "The tool's arguments, a JSON object",
					Content:     openapi3.NewContentWithJSONSchema(openapi3.NewObjectSchema()),
				}}},
				Responses:  responses,
				Extensions: ext,
			}
		}
		if len(writes) > 0 {
			item.Post = &openapi3.Operation{
				OperationID: operationID("call", b.cfg.Name, res+"-write"),
				Tags:        []string{b.cfg.Name},
				Summary:     fmt.Sprintf("Call a %s tool of %s that changes something", res, b.cfg.Name),
				Description: fmt.Sprintf("Bridged from the MCP server %s. Tools: %s.", b.cfg.Name, strings.Join(writes, ", ")),
				Parameters:  openapi3.Parameters{toolParam(writes)},
				RequestBody: &openapi3.RequestBodyRef{Value: openapi3.NewRequestBody().
					WithDescription("The tool's arguments").WithJSONSchema(openapi3.NewObjectSchema())},
				Responses:  responses,
				Extensions: ext,
			}
		}
		doc.Paths.Set(path, item)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("bridge %s: encode the contract: %w", b.cfg.Name, err)
	}
	return out, nil
}

func namespaceParam(pattern string) *openapi3.ParameterRef {
	s := openapi3.NewStringSchema()
	s.Pattern = pattern
	return &openapi3.ParameterRef{Value: openapi3.NewPathParameter("namespace").WithSchema(s)}
}

func toolParam(names []string) *openapi3.ParameterRef {
	s := openapi3.NewStringSchema()
	for _, n := range names {
		s.Enum = append(s.Enum, n)
	}
	return &openapi3.ParameterRef{Value: openapi3.NewPathParameter("tool").WithSchema(s)}
}

// operationID is callGrafanaPrometheus for ("call", "grafana", "prometheus").
func operationID(parts ...string) string {
	var b strings.Builder
	for i, p := range parts {
		for j, w := range strings.FieldsFunc(p, func(r rune) bool { return r == '-' || r == '_' }) {
			if i == 0 && j == 0 {
				b.WriteString(w)
				continue
			}
			b.WriteString(strings.ToUpper(w[:1]) + w[1:])
		}
	}
	return b.String()
}

// Register adds the routes of Contract to r, under base. guard, optional,
// gives the handlers that run before a resource's routes: a service that
// checks its RBAC per route, rather than in a middleware of the engine,
// checks the resource there.
func (b *Bridge) Register(r gin.IRoutes, base string, guard func(resource string) []gin.HandlerFunc) error {
	base, err := splitBase(base)
	if err != nil {
		return err
	}
	ginBase := regexp.MustCompile(`\{([^}]+)\}`).ReplaceAllString(base, ":$1")
	for _, res := range b.resource {
		var chain []gin.HandlerFunc
		if guard != nil {
			chain = guard(res)
		}
		chain = append(chain, b.handler(res))
		r.GET(ginBase+"/"+res+"/:tool", chain...)
		if b.cfg.Writes {
			r.POST(ginBase+"/"+res+"/:tool", chain...)
		}
	}
	return nil
}

// handler calls a tool of resource: its arguments from the query (GET) or
// the body (POST), its result as the response.
func (b *Bridge) handler(resource string) gin.HandlerFunc {
	return func(c *gin.Context) {
		r := c.Request
		name := c.Param("tool")
		t, ok := b.byPath[resource+"/"+name]
		if !ok || t.readOnly != (r.Method == http.MethodGet) {
			server.WriteError(c.Writer, r, infraerrors.NotFound("no %s tool %q to %s", resource, name, strings.ToLower(r.Method)))
			return
		}
		args := map[string]any{}
		var raw []byte
		if r.Method == http.MethodGet {
			raw = []byte(c.Query("arguments"))
		} else {
			var err error
			if raw, err = c.GetRawData(); err != nil {
				server.WriteError(c.Writer, r, infraerrors.Invalid("read the arguments: %v", err))
				return
			}
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				server.WriteError(c.Writer, r, infraerrors.Invalid("the arguments: want a JSON object: %v", err))
				return
			}
		}
		var headers http.Header
		if b.cfg.CallHeaders != nil {
			var err error
			if headers, err = b.cfg.CallHeaders(r, c.Param("namespace")); err != nil {
				server.WriteError(c.Writer, r, err)
				return
			}
		}
		if id := r.Header.Get("X-Request-Id"); id != "" {
			headers = headers.Clone()
			if headers == nil {
				headers = http.Header{}
			}
			headers.Set("X-Request-Id", id)
		}
		res, err := b.client.callTool(r.Context(), headers, name, args)
		if err != nil {
			server.WriteError(c.Writer, r, infraerrors.Upstream(true, "%s: %s: %v", b.cfg.Name, name, err))
			return
		}
		body := result(res)
		if res.IsError {
			server.WriteError(c.Writer, r, infraerrors.New(http.StatusUnprocessableEntity, "bridge."+b.cfg.Name+"."+name,
				"%s", text(res)).WithSlug("tool_error").WithTitle("The tool failed").WithRetryable(false))
			return
		}
		c.Data(http.StatusOK, "application/json", body)
	}
}

// result is a tool's result as a JSON document: its structured content;
// else its text when that is JSON; else {"content": [...]}.
func result(res *sdk.CallToolResult) []byte {
	if res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			return b
		}
	}
	if len(res.Content) == 1 {
		if tc, ok := res.Content[0].(*sdk.TextContent); ok && json.Valid([]byte(tc.Text)) {
			return []byte(tc.Text)
		}
	}
	b, err := json.Marshal(map[string]any{"content": res.Content})
	if err != nil {
		return []byte(`{"content":[]}`)
	}
	return b
}

// text is a tool error's text.
func text(res *sdk.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	if len(parts) == 0 {
		return "the tool reported an error"
	}
	return strings.Join(parts, "\n")
}

// Tools are the bridged tools as tools of the service's MCP server
// (mcp.Options.Tools): each with its own input schema and a namespace
// argument, each call a request to its route under base.
func (b *Bridge) Tools(base string) []mcp.Tool {
	base = strings.TrimRight(base, "/")
	out := make([]mcp.Tool, 0, len(b.tools))
	for _, t := range b.tools {
		t := t
		schema := inputSchema(t.tool.InputSchema)
		props, _ := schema["properties"].(map[string]any)
		const ns = "namespace"
		props[ns] = map[string]any{"type": "string", "description": "The namespace to call the tool in"}
		required, _ := schema["required"].([]any)
		schema["required"] = append(required, ns)

		var hints sdk.ToolAnnotations
		if t.tool.Annotations != nil {
			hints = *t.tool.Annotations
		}
		out = append(out, mcp.Tool{
			Name:        t.tool.Name,
			Title:       t.tool.Title,
			Description: t.tool.Description,
			InputSchema: schema,
			ReadOnly:    t.readOnly,
			Destructive: !t.readOnly && (hints.DestructiveHint == nil || *hints.DestructiveHint),
			Idempotent:  hints.IdempotentHint,
			Request: func(ctx context.Context, args map[string]any) (*http.Request, error) {
				namespace, _ := args[ns].(string)
				if namespace == "" {
					return nil, fmt.Errorf("%s is required", ns)
				}
				delete(args, ns)
				path := strings.Replace(base, "{namespace}", url.PathEscape(namespace), 1) + "/" + t.resource + "/" + url.PathEscape(t.tool.Name)
				encoded, err := json.Marshal(args)
				if err != nil {
					return nil, fmt.Errorf("arguments: %w", err)
				}
				if t.readOnly {
					r, err := http.NewRequestWithContext(ctx, http.MethodGet, path+"?"+url.Values{"arguments": {string(encoded)}}.Encode(), nil)
					if err != nil {
						return nil, err
					}
					r.Header.Set("Accept", "application/json")
					return r, nil
				}
				r, err := http.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(string(encoded)))
				if err != nil {
					return nil, err
				}
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Accept", "application/json")
				return r, nil
			},
		})
	}
	return out
}

// inputSchema is a copy of a tool's input schema as a JSON object, with
// properties.
func inputSchema(s any) map[string]any {
	out := map[string]any{}
	if raw, err := json.Marshal(s); err == nil {
		if err := json.Unmarshal(raw, &out); err != nil || out == nil {
			out = map[string]any{}
		}
	}
	out["type"] = "object"
	if _, ok := out["properties"].(map[string]any); !ok {
		out["properties"] = map[string]any{}
	}
	return out
}
