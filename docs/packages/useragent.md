---
title: useragent
description: Names the service, and its version, in the User-Agent of the HTTP requests lib makes for it.
weight: 105
---

# useragent

```go
import "github.com/infrapi/lib/pkg/useragent"
```

Every HTTP request lib makes for a service carries the service's name and
version as its `User-Agent`, one product token (RFC 9110): the requests to
Kayra ([`auth`](auth.md), [`events`](events.md)), to a bridged MCP server
([`mcp/bridge`](mcp.md#bridging-another-mcp-server)) and to another API
([`client`](client.md)). The logs of whoever is called then tell which
service, at which version, called, where Go's default, `Go-http-client/1.1`,
tells nothing.

```text
tofu-api/v0.13.0
```

The name is the main module's last path element (a `/v2` major version
suffix skipped), the version its version as Go stamps it in the binary: the
tag the binary was built from, `devel` when there is none.

A service's own HTTP clients can carry it too:

```go
httpClient := useragent.Client(&http.Client{Timeout: 10 * time.Second}) // a copy, its transport wrapped
transport := useragent.Transport(nil)                                    // around http.DefaultTransport
ua := useragent.String()
```

`Transport` sets the header only on a request that has none, on a clone of
the request: one the caller set is kept. `client.ClientOptions.UserAgent`
replaces it for that client.
