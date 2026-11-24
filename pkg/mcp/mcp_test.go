package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/mcp"
)

// contract describes things: read one, create one (with a dry run), delete
// one.
const contract = `{
  "openapi": "3.0.3",
  "info": {"title": "things", "version": "v1"},
  "paths": {
    "/v1/namespaces/{namespace}/thing": {
      "parameters": [{"$ref": "#/components/parameters/namespace"}],
      "post": {
        "operationId": "createThing",
        "summary": "Create a thing",
        "parameters": [{"name": "dryRun", "in": "query", "schema": {"type": "boolean"}}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ThingSpec"}}}},
        "responses": {"201": {"description": "Created"}, "200": {"description": "The plan"}}
      }
    },
    "/v1/namespaces/{namespace}/thing/{id}": {
      "parameters": [{"$ref": "#/components/parameters/namespace"}, {"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}],
      "get": {
        "operationId": "getThing",
        "summary": "Read a thing",
        "responses": {"200": {"description": "The thing"}}
      },
      "delete": {
        "operationId": "deleteThing",
        "summary": "Delete a thing",
        "responses": {"204": {"description": "Deleted"}}
      }
    }
  },
  "components": {
    "parameters": {"namespace": {"name": "namespace", "in": "path", "required": true, "schema": {"type": "string"}}},
    "schemas": {"ThingSpec": {"type": "object", "required": ["title"], "properties": {"title": {"type": "string", "maxLength": 40}}}}
  }
}`

// service answers the contract, and records what it was asked. Every route
// requires "Bearer good".
type service struct {
	calls []string
	auth  []string
}

func (s *service) engine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(func(c *gin.Context) {
		s.calls = append(s.calls, c.Request.Method+" "+c.Request.URL.RequestURI())
		s.auth = append(s.auth, c.GetHeader("Authorization"))
		if c.GetHeader("Authorization") != "Bearer good" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"title": "Unauthorized", "status": 401})
		}
	})
	e.GET("/v1/namespaces/:namespace/thing/:id", func(c *gin.Context) {
		if c.Param("id") == "missing" {
			c.JSON(http.StatusNotFound, gin.H{"title": "Not Found", "status": 404, "detail": "no thing missing"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": c.Param("id"), "namespace": c.Param("namespace"), "title": "Ops"})
	})
	e.POST("/v1/namespaces/:namespace/thing", func(c *gin.Context) {
		var spec map[string]any
		if err := c.ShouldBindJSON(&spec); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		if c.Query("dryRun") == "true" {
			c.JSON(http.StatusOK, gin.H{"action": "create", "planned": spec})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"id": "1", "title": spec["title"]})
	})
	e.DELETE("/v1/namespaces/:namespace/thing/:id", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	return e
}

// bearer adds an Authorization header to every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// connect serves the MCP handler of the service and connects a client to it
// with token.
func connect(t *testing.T, s *service, opts mcp.Options, token string) *sdk.ClientSession {
	t.Helper()
	return connectContract(t, s, contract, opts, token)
}

// connectContract is connect with a contract of its own.
func connectContract(t *testing.T, s *service, contract string, opts mcp.Options, token string) *sdk.ClientSession {
	t.Helper()
	e := s.engine()
	h, err := mcp.New([]byte(contract), e, opts)
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: srv.URL, HTTPClient: &http.Client{Transport: bearer{token}},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, session.Close()) })
	return session
}

func toolNames(t *testing.T, session *sdk.ClientSession) map[string]*sdk.Tool {
	t.Helper()
	res, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	out := map[string]*sdk.Tool{}
	for _, tool := range res.Tools {
		out[tool.Name] = tool
	}
	return out
}

func keys(m map[string]*sdk.Tool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Read only by default: the GET, and the POST as its dry run; not the DELETE.
func TestReadOnlyByDefault(t *testing.T) {
	tools := toolNames(t, connect(t, &service{}, mcp.Options{Name: "things"}, "good"))
	assert.Equal(t, []string{"get_thing", "plan_create_thing"}, keys(tools))
	assert.True(t, tools["plan_create_thing"].Annotations.ReadOnlyHint)
	assert.Contains(t, tools["plan_create_thing"].Description, "Dry run only")
}

// With Writes, every operation is a tool, a DELETE a destructive one; a
// write with a dry run keeps its plan_ tool.
func TestWrites(t *testing.T) {
	tools := toolNames(t, connect(t, &service{}, mcp.Options{Name: "things", Writes: true}, "good"))
	assert.Equal(t, []string{"create_thing", "delete_thing", "get_thing", "plan_create_thing"}, keys(tools))
	assert.True(t, *tools["delete_thing"].Annotations.DestructiveHint)
	assert.False(t, tools["create_thing"].Annotations.ReadOnlyHint)
}

// NoPlans: no plan_ tool; with Writes, the dry run is a parameter of the
// write's tool.
func TestNoPlans(t *testing.T) {
	tools := toolNames(t, connect(t, &service{}, mcp.Options{Name: "things", NoPlans: true}, "good"))
	assert.Equal(t, []string{"get_thing"}, keys(tools))

	tools = toolNames(t, connect(t, &service{}, mcp.Options{Name: "things", Writes: true, NoPlans: true}, "good"))
	assert.Equal(t, []string{"create_thing", "delete_thing", "get_thing"}, keys(tools))
	raw, err := json.Marshal(tools["create_thing"].InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"dryRun"`)
}

// The input schema has the parameters and the body, the contract's
// components as $defs.
func TestInputSchema(t *testing.T) {
	tools := toolNames(t, connect(t, &service{}, mcp.Options{Name: "things", Writes: true}, "good"))
	raw, err := json.Marshal(tools["create_thing"].InputSchema)
	require.NoError(t, err)
	var s map[string]any
	require.NoError(t, json.Unmarshal(raw, &s))
	props := s["properties"].(map[string]any)
	assert.Contains(t, props, "namespace")
	assert.NotContains(t, props, "dryRun", "the plan_ tool is the dry run")
	assert.Equal(t, map[string]any{"$ref": "#/$defs/ThingSpec"}, props["body"])
	assert.ElementsMatch(t, []any{"body", "namespace"}, s["required"])
	assert.Contains(t, s["$defs"], "ThingSpec")
}

// A call is the HTTP request to the service, with the caller's token: its
// answer is the result.
func TestCall(t *testing.T) {
	s := &service{}
	session := connect(t, s, mcp.Options{Name: "things"}, "good")

	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name: "get_thing", Arguments: map[string]any{"namespace": "acme", "id": "0:a b"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, map[string]any{"id": "0:a b", "namespace": "acme", "title": "Ops"}, res.StructuredContent)
	assert.Equal(t, "GET /v1/namespaces/acme/thing/0:a%20b", s.calls[len(s.calls)-1])
	assert.Equal(t, "Bearer good", s.auth[len(s.auth)-1], "the caller's token")

	res, err = session.CallTool(context.Background(), &sdk.CallToolParams{
		Name: "plan_create_thing", Arguments: map[string]any{"namespace": "acme", "body": map[string]any{"title": "Ops"}},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, "create", res.StructuredContent.(map[string]any)["action"])
	assert.Equal(t, "POST /v1/namespaces/acme/thing?dryRun=true", s.calls[len(s.calls)-1], "always a dry run")
}

// The service's error is a tool error, with its problem for the agent to
// read; so is the service refusing the token.
func TestCallErrors(t *testing.T) {
	session := connect(t, &service{}, mcp.Options{Name: "things"}, "good")
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name: "get_thing", Arguments: map[string]any{"namespace": "acme", "id": "missing"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].(*sdk.TextContent).Text, "no thing missing")

	session = connect(t, &service{}, mcp.Options{Name: "things"}, "bad")
	res, err = session.CallTool(context.Background(), &sdk.CallToolParams{
		Name: "get_thing", Arguments: map[string]any{"namespace": "acme", "id": "1"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].(*sdk.TextContent).Text, "Unauthorized")
}

// The contract, the documents and the prompts are offered as context.
func TestContext(t *testing.T) {
	session := connect(t, &service{}, mcp.Options{
		Name:         "things",
		Instructions: "A thing belongs to its namespace.",
		Documents:    []mcp.Document{{Name: "things", Title: "Things", Content: []byte("# Things\nPlan first.")}},
		Prompts:      []mcp.Prompt{{Name: "check", Description: "Check a thing", Text: "Read the thing and plan it."}},
	}, "good")
	assert.Equal(t, "A thing belongs to its namespace.", session.InitializeResult().Instructions)

	resources, err := session.ListResources(context.Background(), nil)
	require.NoError(t, err)
	var uris []string
	for _, r := range resources.Resources {
		uris = append(uris, r.URI)
	}
	assert.ElementsMatch(t, []string{"openapi://contract", "doc://things"}, uris)

	doc, err := session.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: "doc://things"})
	require.NoError(t, err)
	assert.Equal(t, "# Things\nPlan first.", doc.Contents[0].Text)
	assert.Equal(t, "text/markdown", doc.Contents[0].MIMEType)

	p, err := session.GetPrompt(context.Background(), &sdk.GetPromptParams{Name: "check"})
	require.NoError(t, err)
	assert.Equal(t, "Read the thing and plan it.", p.Messages[0].Content.(*sdk.TextContent).Text)
}

func TestNewRefuses(t *testing.T) {
	e := (&service{}).engine()
	_, err := mcp.New([]byte(contract), e, mcp.Options{})
	assert.ErrorContains(t, err, "Name is required")
	_, err = mcp.New([]byte("not a contract"), e, mcp.Options{Name: "x"})
	assert.Error(t, err)
	_, err = mcp.New([]byte(contract), nil, mcp.Options{Name: "x"})
	assert.Error(t, err)
}

// A request held open to be notified lasts until Close.
func TestCloseEndsListen(t *testing.T) {
	h, err := mcp.New([]byte(contract), (&service{}).engine(), mcp.Options{Name: "things"})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true}}}`))
		if err != nil {
			done <- err
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			err = errors.Join(err, resp.Body.Close())
		}
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("listen ended before Close: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	h.Close()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("listen still open after Close")
	}
}

// An operation whose x-infrapi-mcp extension is false is not a tool; true,
// or anything else, changes nothing.
func TestExtensionExcludes(t *testing.T) {
	excluded := strings.Replace(contract, `"operationId": "getThing",`, `"operationId": "getThing", "x-infrapi-mcp": false,`, 1)
	require.NotEqual(t, contract, excluded)
	tools := toolNames(t, connectContract(t, &service{}, excluded, mcp.Options{Name: "things"}, "good"))
	assert.Equal(t, []string{"plan_create_thing"}, keys(tools))

	kept := strings.Replace(contract, `"operationId": "getThing",`, `"operationId": "getThing", "x-infrapi-mcp": "no",`, 1)
	tools = toolNames(t, connectContract(t, &service{}, kept, mcp.Options{Name: "things"}, "good"))
	assert.Equal(t, []string{"get_thing", "plan_create_thing"}, keys(tools))
}

// Options.Tools: tools of the service's own, called through its handler
// with the caller's headers, as the operations' tools are.
func TestTools(t *testing.T) {
	s := &service{}
	session := connect(t, s, mcp.Options{Name: "things", Tools: []mcp.Tool{
		{
			Name: "thing_of_the_day", Description: "Today's thing", ReadOnly: true, Idempotent: true,
			Request: func(ctx context.Context, args map[string]any) (*http.Request, error) {
				ns, _ := args["namespace"].(string)
				if ns == "" {
					return nil, errors.New("namespace is required")
				}
				return http.NewRequestWithContext(ctx, http.MethodGet, "/v1/namespaces/"+ns+"/thing/today", nil)
			},
		},
	}}, "good")

	tools := toolNames(t, session)
	assert.Equal(t, []string{"get_thing", "plan_create_thing", "thing_of_the_day"}, keys(tools))
	day := tools["thing_of_the_day"]
	assert.True(t, day.Annotations.ReadOnlyHint)
	assert.False(t, *day.Annotations.DestructiveHint)
	assert.True(t, day.Annotations.IdempotentHint)
	raw, err := json.Marshal(day.InputSchema)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object"}`, string(raw), "an object unless set")

	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "thing_of_the_day", Arguments: map[string]any{"namespace": "acme"}})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, "today", res.StructuredContent.(map[string]any)["id"])
	assert.Equal(t, "GET /v1/namespaces/acme/thing/today", s.calls[len(s.calls)-1])
	assert.Equal(t, "Bearer good", s.auth[len(s.auth)-1], "the caller's token")

	res, err = session.CallTool(context.Background(), &sdk.CallToolParams{Name: "thing_of_the_day", Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Equal(t, "namespace is required", res.Content[0].(*sdk.TextContent).Text)
}

// A tool of Options.Tools needs a name and a Request, and a name no other
// tool has.
func TestToolsRefused(t *testing.T) {
	request := func(context.Context, map[string]any) (*http.Request, error) { return nil, errors.New("unused") }
	for name, tools := range map[string][]mcp.Tool{
		"no name":    {{Request: request}},
		"no request": {{Name: "x"}},
		"taken":      {{Name: "get_thing", Request: request}},
		"twice":      {{Name: "x", Request: request}, {Name: "x", Request: request}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := mcp.New([]byte(contract), (&service{}).engine(), mcp.Options{Name: "things", Tools: tools})
			require.Error(t, err)
		})
	}
}
