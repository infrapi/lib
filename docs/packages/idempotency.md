# idempotency

```go
import "github.com/infrapi/lib/pkg/idempotency"
```

Makes a write safe to repeat. The caller is usually a workflow engine that
retries activities: a create whose response was lost is sent again, and must
not create a second thing. The same `Idempotency-Key` with the same request
replays the first response and does nothing; the same key with a different
request is refused, because replaying an answer to a question that was not
asked is worse than an error. Only answers that will not change are
remembered - a `5xx` is forgotten, so the retry it invites actually runs
again.

```go
store, err := idempotency.NewPostgres(ctx, db)
if err != nil {
	return err
}

engine.Use(idempotency.Middleware(store, nil, 0))
```

## Middleware

`Middleware(store, scopeOf, maxBody)` applies to writes that carry an
`Idempotency-Key` header; reads, and writes without one, pass through
untouched.

- `scopeOf` says whose a key is: the same key under two scopes names two
  different requests. `nil` is `DefaultScope`, which keys a request to the
  caller the gateway authenticated (`X-Consumer-Username`); a service whose
  keys belong to something narrower - a tenant - passes its own, e.g.
  `func(c *gin.Context) string { return c.Param("tenant") }`.
- `maxBody` bounds the body read to hash it; `0` is `DefaultMaxBody` (1 MiB).
  A larger body is refused with `413` rather than cut, so a truncated body
  never reaches the handler as if it were the request.

A replay sets `Idempotent-Replayed: true` and answers with the first response,
without running the handler again.

## Stores

`Store` is the interface the middleware drives: `Begin`, `Finish`, `Abandon`.
Two implementations:

- `Postgres` keeps keys in one table, created if needed by `NewPostgres`.
  `Stale` (10 minutes) is how long a key may stay `in_progress` before another
  call may take it over - the process that took it died; `Retention` (24
  hours) is how long a completed key is remembered. `Prune` deletes completed
  keys older than `Retention`; call it on a timer.
- `Memory` is for tests: it remembers nothing across restarts, which is
  exactly why production does not use it - a retried activity usually arrives
  after something went wrong, a restart included.

## Errors

A mismatched key is a `422` (`idempotency_key_reused`); a key still in
progress is a `409`, `retryable: true` (`in_progress`) - both
[`*errors.Error`](errors.md), rendered by whatever calls
[`server.WriteError`/`server.AbortError`](server.md) around this middleware.
