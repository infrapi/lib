# Development

## Quality gate

```bash
make all
```

runs `setup clean tidy fmt lint security test`. The individual targets:

| Target | Command |
|--------|---------|
| `setup` | installs `golangci-lint`, `gosec` and `goreleaser` if missing |
| `tidy` | `go mod tidy` |
| `fmt` | `gofmt -w` on every tracked Go file |
| `lint` | `golangci-lint run` |
| `security` | `gosec -exclude-dir _local -quiet ./...` |
| `test` | `go test` with coverage, writes `cover.out` and `coverage.html` |

CI runs the same targets and fails when `make fmt` produces a diff, so format before
pushing.

## Documentation

The site is MkDocs Material. Install the toolchain once:

```bash
pip install mkdocs mkdocs-material pymdown-extensions mkdocs-git-revision-date-localized-plugin
```

Then:

```bash
make docs         # build into site/
make docs-serve   # live reload on http://localhost:8000
make docs-deploy  # push to the gh-pages branch
```

Pages live in `docs/` and the navigation is declared in `mkdocs.yml`. A new page must be
added to `nav:`, otherwise the CI build - which runs `mkdocs build --strict` - fails.

Pushing to the default branch publishes the site through GitHub Pages; `make docs-deploy`
is only needed for a manual out-of-band publish.

## Layout

```text
pkg/config    dotenv loading, validation, AppConfig
pkg/http      resty-based API client
pkg/listener  service socket, TCP or systemd socket activation
pkg/server    gin engine factory
examples/     one runnable program per package
docs/         this site
```

Tests sit next to the code they cover and use real HTTP servers and real dotenv files
rather than mocks. New code is expected to arrive with tests in the same style.
