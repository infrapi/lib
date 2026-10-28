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

```go
client, err := infraauth.NewClient(infraauth.ClientOptions{
	BaseURL:  "https://auth.example.com",
	CacheTTL: infraauth.DefaultCacheTTL, // 1h
})
```

With `CacheTTL` set, a successful `Verify` is kept in memory for that long, keyed by a
SHA-256 hash of the token - never the token itself - so a request reusing the same bearer
token within the window skips the HTTP round trip entirely. `Middleware` benefits from
this automatically, since it calls `Verify`.

- Only success is cached: a token the service refuses is checked again on every call, so
  a typo or an expired token is never one you have to wait `CacheTTL` out.
- Zero (the default) disables caching: every call reaches the authentication service, so
  a revoked token or a changed group membership takes effect immediately.
- Each `Verify` returns its own copy of the cached `UserInfo` - mutating `Groups` or
  `Attributes` in a handler never corrupts what the next cache hit serves.
- `CacheTTL` trades freshness for load: a revoked token, or a user removed from a group,
  stays valid in this process for up to `CacheTTL` after the change. Pick a value your
  service's security posture tolerates - `DefaultCacheTTL` (1h) matches what the
  authentication service itself typically caches a resolved user for.

### Gating every route at once

Pass it to [`server.WithAuth`](server.md) instead of wiring it onto each route:

```go
engine, err := server.NewServerGin(cfg, server.WithAuth(client.Middleware()))
```

Every route the service adds after this call requires a token the auth service accepts;
the engine's own endpoints under `/-/` - registered before it, same as `WithOpenAPI`'s
contract validation - are not gated.

`examples/auth` runs all of this against a stub authentication server.
