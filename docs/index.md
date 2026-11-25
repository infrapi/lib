---
title: infrapi/lib
description: Opinionated Go building blocks shared by InfraPI services, one package per concern.
weight: 10
---

# infrapi/lib

Opinionated Go building blocks shared by InfraPI services. Each package covers one
concern - configuration, serving, calling, authenticating, logging, tracing, errors -
and they are designed to be used together from the same `AppConfig`.

## Where to start

| Page | What it covers |
|------|----------------|
| [Getting started](getting-started.md) | Add the dependency, write a dotenv file, serve an API group and call another service. |
| [Development](development.md) | The quality gate, building these docs, and the repository layout. |

Install with `go get github.com/infrapi/lib@v0.21.0` (Go 1.27.4 or later), the
version pinned: `@latest` resolves to old v1 tags.

## Packages

| Package | Import path | What it does |
|---------|-------------|--------------|
| [`api`](packages/api.md) | `github.com/infrapi/lib/pkg/api` | The URL layout every service exposes, `/apis/{group}/{version}/[namespaces/{namespace}/]{resource}`, shared by `server`, `client` and `auth`. |
| [`auth`](packages/auth.md) | `github.com/infrapi/lib/pkg/auth` | Authenticates requests against an upstream authentication service's `/oauth2/userinfo` endpoint, with an in-memory cache, as a gin middleware. |
| [`client`](packages/client.md) | `github.com/infrapi/lib/pkg/client` | Resty-based client of the APIs `server` exposes, with TLS/mTLS, retry and hedging. |
| [`config`](packages/config.md) | `github.com/infrapi/lib/pkg/config` | Reads a dotenv file, validates values, exposes a typed `AppConfig`. |
| [`errors`](packages/errors.md) | `github.com/infrapi/lib/pkg/errors` | RFC 9457 application error with an HTTP status, a cause, a remediation hint, a stable code and an explicit retryable flag. |
| [`events`](packages/events.md) | `github.com/infrapi/lib/pkg/events` | The service's events to Kayra's `events.infrapi.io`: a recorder that batches, merges series and limits spam, as Kubernetes' `EventRecorder`. |
| [`identity`](packages/identity.md) | `github.com/infrapi/lib/pkg/identity` | A service's own identity at Kayra, `system:api:<group>`: its EC P-256 key, served at `-/jwks.json`, signing its assertions for `auth.ClientCredentials`. |
| [`listener`](packages/listener.md) | `github.com/infrapi/lib/pkg/listener` | Opens the service socket, TCP or systemd socket activation. |
| [`logger`](packages/logger.md) | `github.com/infrapi/lib/pkg/logger` | Builds the `*logger.Logger` a service's own code logs through, from the same `AppConfig` log level and format, with one shared set of conventions. |
| [`mcp`](packages/mcp.md) | `github.com/infrapi/lib/pkg/mcp` | Serves a service's API to AI agents over the Model Context Protocol, generated from its OpenAPI document, with its documents and prompts as context; `mcp/bridge` serves another MCP server's tools as routes of the API, authorized per namespace. |
| [`server`](packages/server.md) | `github.com/infrapi/lib/pkg/server` | `gin.Engine` pre-wired with CORS, trusted proxies, Prometheus metrics, a span per request, generated OpenAPI docs or the service's own contracts — enforced on every request — and rendering an `errors.Error` as `application/problem+json`. |
| [`std`](packages/std.md) | `github.com/infrapi/lib/pkg/std` | Small helpers, standard library only, that lib's packages and the services share: the `User-Agent` of the HTTP requests lib makes for a service, `tofu-api/v0.13.0`. |
| [`tofu`](packages/tofu.md) | `github.com/infrapi/lib/pkg/tofu` | Drives any OpenTofu provider plugin and resource type, and applies a declarative set of providers and resources on top. |
| [`tracing`](packages/tracing.md) | `github.com/infrapi/lib/pkg/tracing` | Sends the traces over OTLP (HTTP or gRPC), from the `INFRAPI_TRACING_*` keys. |
| [`vault`](packages/vault.md) | `github.com/infrapi/lib/pkg/vault` | Wraps `vault-client-go` with TLS/mTLS, retry, AppRole/token login, and an opinionated KV v2 and PKI surface. |
