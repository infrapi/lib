---
title: std
description: Small helpers, standard library only, that lib's packages and the services share; the User-Agent of lib's HTTP requests.
weight: 95
---

# std

```go
import "github.com/infrapi/lib/pkg/std"
```

Small helpers that lib's packages and the services share. They use the
standard library only, so importing them brings in no other dependency.

## User-Agent

Every HTTP request lib makes for a service carries the service's name and
version as its `User-Agent`, as one product token (RFC 9110). This covers the
requests to Kayra ([`auth`](auth.md), [`events`](events.md)), to a bridged MCP
server ([`mcp/bridge`](mcp.md#bridging-another-mcp-server)) and to another API
([`client`](client.md)). The logs of whoever is called then show which
service called, at which version. Go's default, `Go-http-client/1.1`, shows
neither.

```text
tofu-api/v0.13.0
```

The name is the binary's: the last element of its main package's path
(`github.com/infrapi/kayra/cmd/kayra` gives `kayra`), skipping a `/v2`
major version suffix. The version is the one Go stamps in the binary: the tag
the binary was built from, or `devel` when there is none.

A service's own HTTP clients can carry it too:

```go
httpClient := std.UserAgentClient(&http.Client{Timeout: 10 * time.Second}) // a copy, its transport wrapped
transport := std.UserAgentTransport(nil)                                    // around http.DefaultTransport
ua := std.UserAgent()
```

`UserAgentTransport` sets the header only on a request that has none, and on
a clone of the request, so a header the caller set is kept.
`client.ClientOptions.UserAgent` replaces it for that client.
