# AGENT.md

Working notes for coding agents on this repository. Read before touching code.

## Project

`github.com/infrapi/lib` is a Go 1.26.7 library, no binary. Eight packages that
InfraPI services import:

```text
pkg/cluster   dskit clustering: pluggable KV, a ring per role, leader or broadcast
pkg/config    dotenv loading, go-playground validation, typed AppConfig + ClusterConfig
pkg/errors    application error: RFC 9457 payload, cause, remediation
pkg/gateway   typed CRUD client for the APISIX Admin API (routes/services/upstreams/consumers)
pkg/http      resty v3 API client (TLS/mTLS, retry, hedging)
pkg/listener  service socket, TCP or systemd socket activation
pkg/server    gin.Engine factory (CORS, proxies, /-/metadata /-/metrics /-/openapi.json /-/docs)
pkg/tofu      drive any OpenTofu provider plugin/resource type, and a declarative apply/destroy on top
examples/     one runnable program per package
docs/         MkDocs Material site
```

Three Go modules, not one: `.`, `pkg/cluster` and `examples/cluster`. The split
exists so that a service importing only config/http/listener/server does not
inherit dskit's dependency tree (grpc, etcd, consul, otel, hashicorp/memberlist).
Both submodules reach the parent through a `replace => ../..`, which is ignored
by anyone who imports them.

## Commands

```bash
make all                      # setup clean tidy fmt lint security test
make fmt lint security test   # the gate to run before handing work off
go test ./pkg/config          # a single package of the root module
make docs                     # build the site into site/
make docs-serve               # live reload on :8000
```

`tidy`, `lint`, `security` and `test` walk `MODULES` themselves, because none of
those tools crosses a module boundary. The submodules are their own working
directories:

```bash
cd pkg/cluster && go test ./...      # ~15s, real gossip clusters
cd examples/cluster/roles && go run .    # one directory per use case, each
                                         # with its own app.env; see that
                                         # module's README.md
cd examples/cluster/node && \
  INFRAPI_CONFIG_DOTENV_FILE=node1.env go run .   # a long-lived replica;
                                                  # node2.env in a second
                                                  # terminal makes a cluster
go run ./examples/http               # the root-module examples, from the root
make verify-submodules               # builds them without the replace
```

CI runs `make fmt` then `git diff --exit-code`, so unformatted code fails the
build. `mkdocs build --strict` also runs in CI: a warning is an error.

## Conventions

- Every exported func, type, const and field carries a godoc comment. Keep it
  that way when adding public API.
- Configuration is read from a dotenv file, never from the process environment.
  The single exception is `INFRAPI_CONFIG_DOTENV_FILE`, which names that file.
  New settings go through `VariableOpts` (key, default, validator) so the error
  message keeps naming the key and the file.
- Tests use real HTTP servers and real dotenv fixtures, not mocks. `pkg/` sits
  at 100% statement coverage on exported functions; new code is expected to
  arrive with tests in the same style. `pkg/cluster` is the exception at ~94%:
  the gap is error branches of dskit constructors that cannot be triggered
  without fault injection, and it is covered by real two-node clusters instead.
- Conventional commit messages. `go-semantic-release` cuts versions from pushes
  to `main`, so `feat:` and `fix:` prefixes decide the next tag.
- `site/`, `cover*.out` and `coverage.html` are build output and gitignored.

## Documentation

`README.md` is the only copy of the home page text: `docs/index.md` is a
one-line `--8<-- "README.md"` snippet include. Edit the README, not the include.
Because the file is rendered from two different roots, cross-links in it must be
absolute URLs.

A new page must be added to `nav:` in `mkdocs.yml` or the strict build fails.
The palette and type live in `docs/assets/stylesheets/theme.css`.

## Gotchas

- resty is pinned to `v3.0.0-rc.3`, where hedging is configured with
  `SetHedging(resty.NewHedging()...)`. The older `EnableHedging` and
  `SetHedgingAllowNonReadOnly` calls no longer exist.
- `examples/config` and `examples/server` point `INFRAPI_CONFIG_DOTENV_FILE` at
  paths relative to the repository root, so run them from there.
- `pkg/http` tests take around 37s of the Makefile's 60s budget, most of it real
  retry backoff. Keep new timing tests short.
- `/-/docs` serves Swagger UI from `cdn.jsdelivr.net`, pinned by version in
  `pkg/server/openapi.go`. The specification itself is generated in process and
  needs nothing external.
- Known behaviour, deliberate until someone decides otherwise: the HTTP client's
  default success range is `200..400` inclusive, so a `400` response is not an
  error.
- `pkg/tofu` tests run against a real `terraform-provider-local` binary:
  `github.com/opentofu/provider-client`'s `Provider` interface is sealed, so
  it cannot be mocked. The tests download and cache the binary under
  `$TMPDIR/infrapi-lib-tofu-testcache` on first use and `t.Skip` if there is
  no network access, so `make test` still passes offline once the cache is
  warm, or skips cleanly if it never was.
- `provider-client` does not expose a plan response's "requires replacement"
  attribute list, so `tofu.Provider.Update` cannot detect that a resource
  type needs a destroy+recreate for a given attribute change; it surfaces
  the provider's resulting incomplete state as an error instead of silently
  returning it. See that method's godoc before changing it.
- `pkg/gateway`'s own tests run against an in-process stand-in Admin API, no real
  APISIX needed. `TestIntegration_RealAdminAPI` is the exception: it is skipped
  unless `APISIX_ADMIN_SERVER` and `APISIX_ADMIN_APIKEY` name a reachable one,
  so `make test` still passes with no APISIX around. See that test's godoc to
  run it for real.

### pkg/cluster and dskit

- **`pkg/cluster/go.mod` repeats dskit's `replace` of `hashicorp/memberlist`**
  with Grafana's fork. A `replace` in a non-main module is ignored, and without
  that line `dskit/kv/memberlist` does not compile at all. Move it in step with
  dskit's own go.mod on every upgrade.
- **The context passed to dskit's `StartAsync` is the service's lifetime**, not
  a start deadline. Handing it a context that is cancelled when `Start` returns
  terminates the gossip store and every ring the moment startup succeeds. The
  deadline belongs on `AwaitRunning`; see `Cluster.startService`.
- **`flagext.DefaultValues` must never be handed a `kv.Config`**: it has no
  `RegisterFlags` and would panic. Start from `ring.Config`, which fills in its
  `KVStore` for you. Call it once per struct, too: it shares one flag set across
  its arguments, and `ring.Config` and `etcd.Config` both register `etcd.*`.
- **`dns.NewProvider` takes `(resolverType, maxIdleConnections, logger, reg)`**.
  The argument order changed; code copied from an older caller will not compile.
- **Two key-value clients on one registerer collide.** `kv_request_duration_seconds`
  is labelled by backend only, so every client needs its own
  `kv.RegistererWithKVName`. Ring and lifecycler metrics are already told apart
  by a `name` const label, which is why two roles of the same name panic inside
  promauto and `cluster.New` refuses them.
- **One `dns.Provider` per registerer**: `dns_lookups_total` carries no
  distinguishing label, so two clusters on `prometheus.DefaultRegisterer` would
  panic. `New` refuses the second one instead.
- **Codecs must be registered before the replica joins.** The gossip store only
  learns them at construction, hence `Options.Codecs` rather than a setter.
- **`KVInitService.GetMemberlistKV` only starts the store asynchronously**, so
  the init service reaching Running does not mean gossip is up. `startStore`
  awaits the inner store explicitly.
- `make verify-submodules` and its CI step are **not wired in yet**: they cannot
  pass until a tag of the parent carries the `INFRAPI_CLUSTER_*` keys. Enable
  both once it does, and bump the `require github.com/infrapi/lib` line in both
  submodules to that tag.
