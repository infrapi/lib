# AGENT.md

Working notes for coding agents on this repository. Read before touching code.

## Project

`github.com/infrapi/lib` is a Go 1.25 library, no binary. Three packages that
InfraPI services import:

```text
pkg/config    dotenv loading, go-playground validation, typed AppConfig
pkg/http      resty v3 API client (TLS/mTLS, retry, hedging)
pkg/server    gin.Engine factory (CORS, trusted proxies, /-/metadata)
examples/     one runnable program per package
docs/         MkDocs Material site
```

## Commands

```bash
make all                      # setup clean tidy fmt lint security test
make fmt lint security test   # the gate to run before handing work off
go test ./pkg/config          # a single package
go run ./examples/http        # runnable example
make docs                     # build the site into site/
make docs-serve               # live reload on :8000
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
  arrive with tests in the same style.
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
- Known behaviours, deliberate until someone decides otherwise: `pkg/server`
  adds `gin.Logger()` and `gin.Recovery()` on top of `gin.Default()`, which
  already installs both; and the HTTP client's default success range is
  `200..400` inclusive, so a `400` response is not an error.
