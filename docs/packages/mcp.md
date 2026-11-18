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
engine, err := server.NewServerGin(cfg, server.WithOpenAPI(spec))
// ... the service's routes on engine

h, err := mcp.New(spec.Content, engine, mcp.Options{
	Name:         "orders",
	Version:      "v1.2.0",
	Instructions: "An order belongs to its namespace. Plan a change before you make it.",
	Documents:    []mcp.Document{{Name: "orders", Title: "Orders", Content: guide}},
	Prompts:      []mcp.Prompt{{Name: "audit", Description: "Check an order", Text: "Read the order and plan it with its spec."}},
})
engine.Any("/-/mcp", gin.WrapH(h))
```

An agent then connects to `https://<service>/-/mcp` over Streamable HTTP with
its user's bearer token.

## Tools

| Operation | Tool |
|-----------|------|
| `GET` | always; read only |
| a write (`POST`, `PUT`, `PATCH`, `DELETE`) with a `dryRun` query parameter | `plan_<name>`, `dryRun` fixed to `true`: says what the change would do, applies nothing |
| any other write | only with `Options.Writes`; a `DELETE` is marked destructive |

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
