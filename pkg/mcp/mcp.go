// Package mcp serves a service's API to AI agents over the Model Context
// Protocol, generated from its OpenAPI document: one tool per operation,
// each calling the service's own HTTP handler in process with the caller's
// Authorization header, so that the contract's validation, authentication,
// RBAC and the handlers themselves apply exactly as for any HTTP client.
// Documents and prompts the service supplies are offered as context.
//
// Read only by default: GET operations become tools, and a write operation
// only if it takes a dryRun query parameter, then fixed to true, so that an
// agent can see what a change would do without making it. Options.Writes
// exposes the writes themselves.
//
//	h, err := mcp.New(contract, engine, mcp.Options{
//		Name: "orders", Version: "v1.2.0",
//		Instructions: "An order belongs to its namespace. Plan before you change one.",
//		Documents: []mcp.Document{{Name: "orders", Title: "Orders", Content: guide}},
//	})
//	engine.Any("/-/mcp", gin.WrapH(h))
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options says what to serve besides the contract's operations.
type Options struct {
	// Name and Version identify the server to the agent; Name is required.
	Name    string
	Version string
	// Instructions tell the agent how to use the service: its concepts,
	// what to do first, what to avoid. Agents usually put them in their
	// context.
	Instructions string
	// Documents are offered as resources, doc://<name>; the contract itself
	// is offered as openapi://contract.
	Documents []Document
	// Prompts are offered as prompts: ready-made requests an agent's user
	// can pick.
	Prompts []Prompt
	// Writes exposes the operations that change something (POST, PUT,
	// PATCH, DELETE). A write taking a dryRun query parameter is also a
	// plan_ tool, dryRun fixed to true, with or without Writes.
	Writes bool
	// NoPlans leaves out the plan_ tools: with Writes, a write's dryRun is
	// then a parameter of its tool.
	NoPlans bool
	// Include, when set, selects the operations to expose, after Writes.
	Include func(Operation) bool
	// Forward lists the request headers passed on to the service's handler
	// for every call; Authorization, X-Request-Id and Traceparent unless
	// set.
	Forward []string
}

// Document is context an agent can read, in Markdown or any text type.
type Document struct {
	Name        string
	Title       string
	Description string
	// MIMEType is text/markdown unless set.
	MIMEType string
	Content  []byte
}

// Prompt is a ready-made request, sent as the user's message.
type Prompt struct {
	Name        string
	Title       string
	Description string
	Text        string
}

// Operation is an operation of the contract, as Include sees it.
type Operation struct {
	Method      string
	Path        string
	OperationID string
	Tags        []string
}

// tool is an operation as it is called: how to turn the agent's arguments
// into the HTTP request.
type tool struct {
	method string
	path   string
	params []param
	body   bool
	// dryRun is set when a write is exposed as its dry run only.
	dryRun bool
}

type param struct {
	name     string
	in       string // path or query
	required bool
}

// New builds the MCP server of the contract, an OpenAPI 3 document in JSON
// or YAML, and returns its Streamable HTTP handler, stateless so that any
// instance of the service answers any request. Every tool calls handler, the
// service's own, in process.
func New(contract []byte, handler http.Handler, opts Options) (*Handler, error) {
	if opts.Name == "" {
		return nil, errors.New("mcp: Options.Name is required")
	}
	if handler == nil {
		return nil, errors.New("mcp: no handler to call")
	}
	if opts.Forward == nil {
		opts.Forward = []string{"Authorization", "X-Request-Id", "Traceparent"}
	}

	doc, err := openapi3.NewLoader().LoadFromData(contract)
	if err != nil {
		return nil, fmt.Errorf("mcp: load the contract: %w", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("mcp: encode the contract: %w", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("mcp: decode the contract: %w", err)
	}
	defs := definitions(generic)

	server := sdk.NewServer(&sdk.Implementation{Name: opts.Name, Version: opts.Version}, &sdk.ServerOptions{Instructions: opts.Instructions})

	names := map[string]string{}
	for _, path := range doc.Paths.InMatchingOrder() {
		item := doc.Paths.Value(path)
		for method, op := range item.Operations() {
			if opts.Include != nil && !opts.Include(Operation{Method: method, Path: path, OperationID: op.OperationID, Tags: op.Tags}) {
				continue
			}
			for _, v := range variants(method, item, op, opts) {
				t, def, err := build(method, path, item, op, defs, v)
				if err != nil {
					return nil, fmt.Errorf("mcp: %s %s: %w", method, path, err)
				}
				if other, taken := names[def.Name]; taken {
					return nil, fmt.Errorf("mcp: %s %s and %s are both the tool %q: give them distinct operationIds", method, path, other, def.Name)
				}
				names[def.Name] = method + " " + path
				server.AddTool(def, call(t, handler, opts.Forward))
			}
		}
	}

	server.AddResource(&sdk.Resource{URI: "openapi://contract", Name: "contract", Title: "OpenAPI contract",
		Description: "The service's OpenAPI document: every operation, its parameters and schemas.", MIMEType: "application/json"},
		text("openapi://contract", "application/json", raw))
	for _, d := range opts.Documents {
		if d.Name == "" {
			return nil, errors.New("mcp: a document has no name")
		}
		mime := d.MIMEType
		if mime == "" {
			mime = "text/markdown"
		}
		uri := "doc://" + d.Name
		server.AddResource(&sdk.Resource{URI: uri, Name: d.Name, Title: d.Title, Description: d.Description, MIMEType: mime},
			text(uri, mime, d.Content))
	}
	for _, p := range opts.Prompts {
		if p.Name == "" {
			return nil, errors.New("mcp: a prompt has no name")
		}
		server.AddPrompt(&sdk.Prompt{Name: p.Name, Title: p.Title, Description: p.Description}, prompt(p))
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Handler{
		next: sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server },
			&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}),
		closed: ctx, close: cancel,
	}, nil
}

// Handler is the MCP endpoint, an [http.Handler].
//
// A client may hold a request open to be notified (subscriptions/listen):
// it lasts until the client leaves, or until [Handler.Close]. A server
// stopping calls Close first, or its graceful shutdown waits for these
// requests until its deadline:
//
//	srv.RegisterOnShutdown(h.Close)
type Handler struct {
	next   http.Handler
	closed context.Context
	close  context.CancelFunc
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(h.closed, cancel)
	defer stop()
	h.next.ServeHTTP(w, r.WithContext(ctx))
}

// Close ends the requests in progress, those held open to be notified
// among them, and those coming after: a tool call's request to the
// service is cancelled with them. Close may be called more than once.
func (h *Handler) Close() {
	h.close()
}

// variant is one tool an operation makes: the operation itself, or its dry
// run.
type variant struct {
	// plan: the dry run, dryRun fixed to true.
	plan bool
	// hideDryRun: the operation itself, its dry run being a plan_ tool.
	hideDryRun bool
}

// variants are the tools an operation makes, per Options: a read one tool;
// a write with a dryRun parameter its plan_ tool, and itself with Writes;
// another write itself with Writes only.
func variants(method string, item *openapi3.PathItem, op *openapi3.Operation, opts Options) []variant {
	if method == http.MethodGet || method == http.MethodHead {
		return []variant{{}}
	}
	dry := false
	for _, p := range append(append(openapi3.Parameters{}, item.Parameters...), op.Parameters...) {
		if p.Value != nil && p.Value.In == openapi3.ParameterInQuery && p.Value.Name == "dryRun" {
			dry = true
		}
	}
	var out []variant
	plans := dry && !opts.NoPlans
	if opts.Writes {
		out = append(out, variant{hideDryRun: plans})
	}
	if plans {
		out = append(out, variant{plan: true})
	}
	return out
}

// build turns an operation into the tool of variant v.
func build(method, path string, item *openapi3.PathItem, op *openapi3.Operation, defs map[string]any, vr variant) (tool, *sdk.Tool, error) {
	t := tool{method: method, path: path, dryRun: vr.plan}
	hide := vr.hideDryRun
	write := method != http.MethodGet && method != http.MethodHead
	params := append(openapi3.Parameters{}, item.Parameters...)
	params = append(params, op.Parameters...)

	props := map[string]any{}
	var required []string
	for _, p := range params {
		v := p.Value
		if v == nil || (v.In != openapi3.ParameterInPath && v.In != openapi3.ParameterInQuery) {
			continue
		}
		if (t.dryRun || hide) && v.Name == "dryRun" {
			continue
		}
		s, err := schemaOf(v.Schema, defs)
		if err != nil {
			return tool{}, nil, fmt.Errorf("parameter %s: %w", v.Name, err)
		}
		if v.Description != "" {
			s["description"] = v.Description
		}
		props[v.Name] = s
		t.params = append(t.params, param{name: v.Name, in: v.In, required: v.Required})
		if v.Required {
			required = append(required, v.Name)
		}
	}
	if rb := op.RequestBody; rb != nil && rb.Value != nil {
		if mt := rb.Value.Content.Get("application/json"); mt != nil {
			s, err := schemaOf(mt.Schema, defs)
			if err != nil {
				return tool{}, nil, fmt.Errorf("request body: %w", err)
			}
			if rb.Value.Description != "" {
				s["description"] = rb.Value.Description
			}
			props["body"] = s
			t.body = true
			if rb.Value.Required {
				required = append(required, "body")
			}
		}
	}
	sort.Strings(required)

	input := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		input["required"] = required
	}
	if len(defs) > 0 {
		input["$defs"] = defs
	}

	name := toolName(op.OperationID, method, path)
	desc := strings.TrimSpace(strings.Join(nonEmpty(op.Summary, op.Description), ". "))
	if desc == "" {
		desc = method + " " + path
	}
	desc += " (" + method + " " + path + ")"
	if t.dryRun {
		name = "plan_" + name
		desc += ". Dry run only: says what the change would do, and applies nothing."
	}
	destructive := method == http.MethodDelete && !t.dryRun
	def := &sdk.Tool{
		Name:        name,
		Description: desc,
		InputSchema: input,
		Annotations: &sdk.ToolAnnotations{
			ReadOnlyHint:    !write || t.dryRun,
			DestructiveHint: &destructive,
			IdempotentHint:  !write || t.dryRun || method == http.MethodPut || method == http.MethodDelete,
		},
	}
	return t, def, nil
}

// call is a tool's handler: the agent's arguments as an HTTP request to the
// service's handler, its answer as the result; an error status is a tool
// error, its body the problem the agent reads.
func call(t tool, handler http.Handler, forward []string) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return failed(fmt.Sprintf("arguments: %v", err)), nil
			}
		}
		r, err := request(ctx, t, args)
		if err != nil {
			return failed(err.Error()), nil
		}
		if req.Extra != nil {
			for _, h := range forward {
				if v := req.Extra.Header.Get(h); v != "" {
					r.Header.Set(h, v)
				}
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		body := rec.Body.Bytes()
		res := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(body)}}}
		if rec.Code >= http.StatusBadRequest {
			res.IsError = true
			if len(body) == 0 {
				res.Content = []sdk.Content{&sdk.TextContent{Text: http.StatusText(rec.Code)}}
			}
			return res, nil
		}
		var structured any
		if json.Unmarshal(body, &structured) == nil {
			if _, ok := structured.(map[string]any); ok {
				res.StructuredContent = structured
			}
		}
		if len(body) == 0 {
			res.Content = []sdk.Content{&sdk.TextContent{Text: http.StatusText(rec.Code)}}
		}
		return res, nil
	}
}

// request builds the HTTP request of a call.
func request(ctx context.Context, t tool, args map[string]any) (*http.Request, error) {
	path := t.path
	query := url.Values{}
	for _, p := range t.params {
		v, ok := args[p.name]
		if !ok || v == nil {
			if p.required {
				return nil, fmt.Errorf("%s is required", p.name)
			}
			continue
		}
		switch p.in {
		case openapi3.ParameterInPath:
			path = strings.ReplaceAll(path, "{"+p.name+"}", url.PathEscape(scalar(v)))
		case openapi3.ParameterInQuery:
			if list, ok := v.([]any); ok {
				for _, e := range list {
					query.Add(p.name, scalar(e))
				}
			} else {
				query.Set(p.name, scalar(v))
			}
		}
	}
	if t.dryRun {
		query.Set("dryRun", "true")
	}
	target := path
	if q := query.Encode(); q != "" {
		target += "?" + q
	}

	var body []byte
	if t.body {
		if b, ok := args["body"]; ok && b != nil {
			var err error
			if body, err = json.Marshal(b); err != nil {
				return nil, fmt.Errorf("body: %w", err)
			}
		}
	}
	r, err := http.NewRequestWithContext(ctx, t.method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Accept", "application/json")
	return r, nil
}

func scalar(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

func failed(msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: msg}}}
}

func text(uri, mime string, content []byte) sdk.ResourceHandler {
	return func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: uri, MIMEType: mime, Text: string(content)}}}, nil
	}
}

func prompt(p Prompt) sdk.PromptHandler {
	return func(context.Context, *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
		return &sdk.GetPromptResult{Description: p.Description, Messages: []*sdk.PromptMessage{
			{Role: "user", Content: &sdk.TextContent{Text: p.Text}},
		}}, nil
	}
}

// definitions are the contract's component schemas, as JSON Schema $defs.
func definitions(contract map[string]any) map[string]any {
	components, _ := contract["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	out := map[string]any{}
	for name, s := range schemas {
		out[name] = rewriteRefs(s)
	}
	return out
}

// schemaOf is an OpenAPI schema as JSON Schema, its references to the
// contract's components pointing to $defs.
func schemaOf(ref *openapi3.SchemaRef, defs map[string]any) (map[string]any, error) {
	if ref == nil {
		return map[string]any{}, nil
	}
	if ref.Ref != "" {
		name, ok := strings.CutPrefix(ref.Ref, "#/components/schemas/")
		if !ok {
			return nil, fmt.Errorf("reference %q: only #/components/schemas/ is supported", ref.Ref)
		}
		if _, ok := defs[name]; !ok {
			return nil, fmt.Errorf("reference %q: no such schema", ref.Ref)
		}
		return map[string]any{"$ref": "#/$defs/" + name}, nil
	}
	raw, err := json.Marshal(ref.Value)
	if err != nil {
		return nil, err
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	out, _ := rewriteRefs(s).(map[string]any)
	return out, nil
}

// rewriteRefs points every #/components/schemas/ reference of v to $defs.
func rewriteRefs(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			if s, ok := e.(string); ok && k == "$ref" {
				out[k] = strings.Replace(s, "#/components/schemas/", "#/$defs/", 1)
				continue
			}
			out[k] = rewriteRefs(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = rewriteRefs(e)
		}
		return out
	}
	return v
}

var notName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// toolName is the operation's id in snake case, or its method and path when
// it has none: createFolder is create_folder.
func toolName(operationID, method, path string) string {
	if operationID == "" {
		operationID = strings.ToLower(method) + "_" + path
	}
	var b strings.Builder
	for i, r := range operationID {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	name := strings.Trim(notName.ReplaceAllString(b.String(), "_"), "_")
	if len(name) > 58 { // room for "plan_"
		name = name[:58]
	}
	return name
}

func nonEmpty(s ...string) []string {
	var out []string
	for _, e := range s {
		if strings.TrimSpace(e) != "" {
			out = append(out, strings.TrimSpace(strings.TrimSuffix(e, ".")))
		}
	}
	return out
}
