---
title: mcp
description: Serves a service's API to AI agents over the Model Context Protocol, generated from its OpenAPI document, with its documents and prompts as context.
weight: 75
---

# mcp

```go
import "github.com/infrapi/lib/pkg/mcp"
```

Serves a service's API to AI agents over the [Model Context
Protocol](https://modelcontextprotocol.io), generated from its OpenAPI
document. Each operation is a tool, and each call runs the service's own
handler in process with the caller's `Authorization` header: the contract's
validation, the authentication, the RBAC and the handlers apply exactly as
for any HTTP client. Nothing is written per operation; a new operation in
the contract is a new tool.

```go
root := "/apis/orders.infrapi.io/v1"
engine, err := server.NewServerGin(cfg, []string{root}, server.WithOpenAPI(spec))
// ... the service's routes on engine

h, err := mcp.New(spec.Content, engine, mcp.Options{
	Name:         "orders",
	Version:      "v1.2.0",
	Instructions: "An order belongs to its namespace. Plan a change before you make it.",
	Documents:    []mcp.Document{{Name: "orders", Title: "Orders", Content: guide}},
	Prompts:      []mcp.Prompt{{Name: "audit", Description: "Check an order", Text: "Read the order and plan it with its spec."}},
})
engine.Any(root+server.MCPPath, gin.WrapH(h)) // <root>/-/mcp

srv := &http.Server{Handler: engine}
srv.RegisterOnShutdown(h.Close) // see Shutdown below
```

An agent then connects to `https://<service><root>/-/mcp` over Streamable HTTP
with its user's bearer token. Mounted at `server.MCPPath`, the endpoint is
documented in `<root>/-/openapi.json` and linked in `/.well-known/api.json`
(`links.mcp`).

## Tools

| Operation | Tool |
|-----------|------|
| `GET` | always; read only |
| a write (`POST`, `PUT`, `PATCH`, `DELETE`) with a `dryRun` query parameter | `plan_<name>`, `dryRun` fixed to `true`: says what the change would do, applies nothing; and, with `Options.Writes`, the write itself |
| any other write | only with `Options.Writes`; a `DELETE` is marked destructive |

`Options.NoPlans` leaves out the `plan_` tools; with `Writes`, `dryRun` is
then a parameter of the write's tool.

- The name is the `operationId` in snake case (`createOrder` is
  `create_order`), or the method and path without one.
- The description is the operation's summary and description, with its
  method and path.
- The input is an object: the path and query parameters by name, and the
  JSON request body as `body`. The contract's component schemas come along as
  `$defs`, so the agent sees the same constraints as the contract enforces.
- The result is the response body: as structured content when it is a JSON
  object. A status of 400 or more is a tool error whose text is the body, an
  `application/problem+json` the agent can read and act on.
- `Options.Include` selects operations, by method, path, `operationId` or
  tags.
- An operation whose `x-infrapi-mcp` extension is `false` is not a tool.
- `Options.Tools` adds tools of the service's own: a name, an input schema,
  hints, and `Request`, which turns the agent's arguments into the HTTP
  request to the service's handler. Their calls go through the handler like
  the operations' tools: the contract, the authentication and the RBAC
  apply. A name two tools share is an error.

## Context

| Option | Offered as |
|--------|------------|
| `Instructions` | the server's instructions, which agents usually keep in their context |
| `Documents` | resources `doc://<name>`, Markdown unless a `MIMEType` is set |
| `Prompts` | prompts: ready-made requests the agent's user can pick |
| the contract | the resource `openapi://contract` |

## Authentication

The MCP endpoint passes the caller's `Authorization`, `X-Request-Id` and
`Traceparent` headers to every call (`Options.Forward` sets others). The
endpoint itself checks nothing more: a token the service refuses is a
tool error with the service's 401 or 403. Mounted after `server.WithAuth`,
the endpoint also requires a valid token to be reached at all.

The handler is stateless: any instance of the service answers any request.

## Shutdown

A client may hold a request open to be notified (`subscriptions/listen`),
until it leaves: a graceful `http.Server.Shutdown` would wait for it until
its deadline. `Handler.Close` ends these requests, and the others in
progress; register it with `srv.RegisterOnShutdown(h.Close)`.

## Bridging another MCP server

```go
import "github.com/infrapi/lib/pkg/mcp/bridge"
```

`bridge` serves the tools of another MCP server, mcp-grafana say, as part of
the service's API. Such a server knows nothing of the platform's namespaces
or RBAC: it runs with whatever credential it is given. Bridged, each of its
tools is called in a namespace of the API, with the credential the service
holds for that namespace, after the same checks as any other route.

Rules select the tools, a regular expression on the whole tool name, first
match wins, and give each one a resource of the API. A tool no rule selects
is not bridged; a tool that is not read-only only with `Config.Writes`.

```go
b, err := bridge.New(ctx, bridge.Config{
	Name: "grafana",
	URL:  "http://127.0.0.1:8000/mcp",
	Rules: []bridge.Rule{
		{Match: "query_prometheus.*|list_prometheus_.*", Resource: "prometheus"},
		{Match: "query_loki.*|list_loki_.*", Resource: "loki"},
	},
	// sent on every request: the bearer the server expects of its only client
	Headers: http.Header{"Authorization": {"Bearer " + serverToken}},
	// the headers of one call: the namespace's own credential
	CallHeaders: func(r *http.Request, namespace string) (http.Header, error) {
		token, err := tokenOf(r.Context(), namespace)
		if err != nil {
			return nil, err // an *errors.Error is the answer to the request
		}
		return http.Header{"X-Grafana-Service-Account-Token": {token}}, nil
	},
})
defer b.Close()

base := "/apis/compute.infrapi.io/v1/namespaces/{namespace}"
contract, err = b.Contract(contract, base) // the bridged routes added, before server.WithOpenAPI
engine, err := server.NewServerGin(cfg, roots, server.WithOpenAPI(server.OpenAPISpec{
	Name: "compute v1", Path: "compute/v1.json", Content: contract}))
err = b.Register(engine, base, nil) // or the handlers checking a resource's RBAC, run first
h, err := mcp.New(contract, engine, mcp.Options{Name: "compute", Tools: b.Tools(base)})
```

`New` lists the server's tools once: the contract they make is fixed while
the service runs. Each resource gets one route, `{tool}` an enum of its
tools:

| Route | Tools | RBAC |
|-------|-------|------|
| `GET <base>/<resource>/{tool}?arguments=<JSON object>` | the read-only ones | `get` on `<resource>` |
| `POST <base>/<resource>/{tool}`, the arguments the body | the others, with `Writes` | `create` on `<resource>` |

Kayra reads these routes in the contract as any others: a role granting
`prometheus` and not `loki` on the API gives the Prometheus tools and not
the Loki ones.

The answer is the tool's structured content, or its text when that is
JSON, or `{"content": [...]}`. A tool's own error is a `422` problem,
`tool_error`, its text the detail; the server unreachable a `502`,
retryable.

`Register`'s guard gives the handlers run before a resource's routes, for a
service that checks its RBAC per route rather than in a middleware of the
engine. `Config.NamespacePattern` gives the routes' namespace parameter the
pattern of the API's other routes.

The routes are kept out of the service's MCP tools (`x-infrapi-mcp: false`).
`b.Tools(base)` are the tools the agent sees instead: one per bridged tool,
with its own input schema, its hints and a `namespace` argument, each call
a request to its route through the service's handler. A bridged tool with
an argument of its own named `namespace` is refused by `New`.

The bridge keeps one session with the server, shared by every call, each
call with its own headers; a session the server dropped (a restart) is
opened again. The request's `X-Request-Id` is passed on.

## Behind ulgen

[ulgen](https://infrapi.github.io/ulgen/) serves several services' MCP
endpoints, and other MCP servers, behind a single endpoint, after a single
login at Kayra. It passes the caller's token on, and the service keeps
checking every call itself.
