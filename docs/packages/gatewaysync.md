# gatewaysync

```go
import "github.com/infrapi/lib/pkg/gatewaysync"
```

Keeps [Apache APISIX](https://apisix.apache.org/) routing to the replicas of
a clustered service that are actually up, through the Admin API client in
[`gateway`](gateway.md).

The leader of a service's writer ring writes:

- two upstreams - `read` holds every replica, `write` holds the leader alone,
  so a write reaches the replica that performs it without a relay hop;
- one service carrying the plugins every call goes through;
- per contract path prefix, a read route and a write route, attached to that
  service;
- optionally, a route for the documentation, without the service's plugins.

```go
syncer := gatewaysync.New(adminClient, ring, gatewaysync.DefaultConfig("orders-api", "/-/readyz"))

if err := syncer.Sync(ctx); err != nil {
	return err
}
```

Run `Sync` on the leader, on a timer: every write it makes converges, so two
leaders during a partition end where one would.

## Config

`DefaultConfig(name, readyPath)` is the configuration when nothing else is
said: object ids under `name`, read methods (`GET`/`HEAD`/`OPTIONS`) and write
methods (`POST`/`PUT`/`PATCH`/`DELETE`), a round-robin upstream health-checked
on `readyPath`, and a service carrying `key-auth`, a per-consumer rate limit,
a body size limit and `request-id`. Adjust the fields you need before passing
it to `New` - `Prefixes` (the contracts' path prefixes) and `LeaderWrites`
have no default and must be set.

## Admin and Ring

`New` takes two small interfaces instead of concrete types, so neither this
package's tests nor a caller's need a real APISIX or a real cluster:

- `Admin` is the six `gateway.Client` methods a sync needs
  (`Get`/`New` of `Upstream`, `Service`, `Route`); `*gateway.Client` satisfies
  it as it is.
- `Ring` is what the sync needs of the cluster's membership: `Members()` and
  `Leader()`. `*cluster.Cluster` satisfies it directly.

## What gets written

Each object has a fixed id and is written only when it differs from what
APISIX already holds, compared as APISIX would store it - so a sync that
changes nothing writes nothing, and only a write is logged. A replica that
joins or leaves, or a new leader, is reflected at the next tick.
