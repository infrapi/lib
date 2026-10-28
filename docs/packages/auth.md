# auth

```go
import infraauth "github.com/infrapi/lib/pkg/auth"
```

Authenticates requests against an upstream authentication service: it forwards the
caller's bearer token to that service's `POST /oauth2/userinfo` endpoint - the same
check it runs on every request it protects itself - and turns the result into a
[gin](https://gin-gonic.com) middleware.

## Creating a client

```go
client, err := infraauth.NewClient(infraauth.ClientOptions{
	BaseURL: "https://auth.example.com",
})
```

`BaseURL` is mandatory. `Timeout` (default `infraauth.DefaultTimeout`, 5s) bounds each
call; `HTTPClient` overrides it entirely, for TLS, proxy or transport settings `Timeout`
alone cannot express.

## Verifying a token directly

```go
user, err := client.Verify(ctx, token)
if err != nil {
	return err // *errors.Error: 401 from the auth service, or an upstream failure
}

fmt.Println(user.Subject, user.Provider, user.Groups)
```

```go
type UserInfo struct {
	Subject       string            // "sub": the user id the service resolved the token to
	Provider      string            // the identity provider that verified the token's signature
	Authorization string            // the backend that resolved Subject's groups and attributes
	Groups        []string
	Attributes    map[string]string // directory fields: email, name, employee_id, ...
}
```

A token the service itself refuses - missing, malformed, expired, signed by an unknown
provider - comes back as a `401` [`*errors.Error`](errors.md). The service answering
with anything else comes back as `errors.Upstream`, and not answering at all as
`errors.Unavailable`, so a caller can tell "this caller is not who they claim" from
"the auth service is having a bad day".

## Gating gin routes

`Client.Middleware()` is a `gin.HandlerFunc` that runs `Verify` against the request's
`Authorization` header and stores the result for downstream handlers:

```go
engine.GET("/widgets", client.Middleware(), func(c *gin.Context) {
	user, _ := infraauth.GetUser(c)
	c.JSON(http.StatusOK, gin.H{"owner": user.Subject})
})
```

A missing or empty bearer header, or a token the service refuses, answers with a
problem-detail response via [`server.AbortError`](server.md#answering-with-an-error) and
stops the chain - no handler downstream runs.

## Caching Verify results

A successful `Verify` is kept in memory for `ClientOptions.CacheTTL`, keyed by a SHA-256
hash of the token - never the token itself - so a request reusing the same bearer token
within the window skips the HTTP round trip entirely. `Middleware` benefits from this
automatically, since it calls `Verify`.

```go
client, err := infraauth.NewClient(infraauth.ClientOptions{
	BaseURL: "https://auth.example.com",
})
```

Caching is on by default: leaving `CacheTTL` unset (`0`) caches for `DefaultCacheTTL`
(1h). Set it to another `time.Duration` to tune that, or to a negative value to turn
caching off entirely - every call then reaches the authentication service, so a revoked
token or a changed group membership takes effect immediately.

```go
infraauth.ClientOptions{BaseURL: "...", CacheTTL: 10 * time.Minute} // tune it
infraauth.ClientOptions{BaseURL: "...", CacheTTL: -1}               // disable it
```

- Only success is cached: a token the service refuses is checked again on every call, so
  a typo or an expired token is never one you have to wait `CacheTTL` out.
- Each `Verify` returns its own copy of the cached `UserInfo` - mutating `Groups` or
  `Attributes` in a handler never corrupts what the next cache hit serves.
- `CacheTTL` trades freshness for load: a revoked token, or a user removed from a group,
  stays valid in this process for up to `CacheTTL` after the change. Pick a value your
  service's security posture tolerates, or disable caching if it tolerates none.

### Gating every route at once

Pass it to [`server.WithAuth`](server.md) instead of wiring it onto each route:

```go
engine, err := server.NewServerGin(cfg, server.WithAuth(client.Middleware()))
```

Every route the service adds after this call requires a token the auth service accepts;
the engine's own endpoints under `/-/` - registered before it, same as `WithOpenAPI`'s
contract validation - are not gated.

`examples/auth` runs all of this against a stub authentication server.

## Authorizing stack/resource/verb access

Any app whose routes are shaped
`/api/v1/namespaces/:namespace/stacks/:stack/<resource>` — Genesis's own
convention for building RBAC-protected APIs on top of this package — can
gate them on `Client.RequireAccess()` instead of hand-rolling its own RBAC
storage and matching:

```go
engine.Any(
	"/api/v1/namespaces/:namespace/stacks/:stack/*resource",
	client.RequireAccess(),
	handler,
)
```

`RequireAccess` reads `:namespace`/`:stack` from the route and the rest
of the path from gin's `*resource` wildcard, derives `Resource`/`Verb`
from it (see below), and calls the authentication/authorization
service's `POST /api/v1/authorize` with the caller's own bearer token to
get an allow/deny decision — the actual RBAC rules never leave that
service, this package only shapes the request. A denial answers `403`
with the service's own reason in `detail`; it does not depend on
`Middleware` having run first (each guards a different concern — identity
vs. access — and reaches the service independently), and it is never
cached, so an edited role or rolebinding takes effect on the very next
request.

```go
resource, hasID, err := infraauth.ParseResource(c.Param("resource"))
verb, err := infraauth.VerbFor(c.Request.Method, hasID)
```

These are what `RequireAccess` calls, exported in case a caller needs the
same parsing without the HTTP round trip. The path after `:stack`
alternates resource/action names and instance ids —
`/vm` → resource `"vm"`, no id; `/vm/123` → resource `"vm"`, id `"123"`;
`/vm/123/reboot` → resource `"vm/reboot"` (a sub-resource action's name is
just part of `Resource`), id `"123"`. `VerbFor` maps the HTTP method to
the verb an RBAC rule grants: `GET` splits into `"list"` (no id) or
`"get"` (has one); every other method maps 1:1 regardless of id
(`POST`→`"create"`, `PUT`→`"update"`, `PATCH`→`"patch"`,
`DELETE`→`"delete"`) — an action call like `PATCH .../vm/123/reboot` is
just as unambiguous as `PATCH .../vm/123`.

```go
allowed, reason, err := client.Authorize(ctx, token, infraauth.AuthorizeRequest{
	Namespace: "core", Stack: "compute", Resource: "vm", Verb: "list",
})
```

The lower-level call `RequireAccess` wraps, for a caller that already has
`Namespace`/`Stack`/`Resource`/`Verb` in hand and wants the raw decision
instead of a gin middleware.
