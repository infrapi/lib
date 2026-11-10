---
title: Development
description: The make quality gate, how the documentation is built and published, and the repository layout.
weight: 40
---

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
```

Pages live in `docs/` and the navigation is declared in `mkdocs.yml`. A new page must be
added to `nav:`, otherwise the CI build - which runs `mkdocs build --strict` - fails.

The pages are plain, GitHub-native Markdown so they read the same on github.com, on
this site and in the InfraPI developer portal, which pulls `docs/` at build time:

- Every page starts with YAML front matter - `title` (the H1 text), a one-sentence
  `description` and a `weight` (10, 20, 30... in `nav` order within its folder) - and
  keeps its `#` heading in the body.
- Callouts are GitHub alerts (`> [!NOTE]`, `> [!TIP]`, `> [!IMPORTANT]`,
  `> [!WARNING]`, `> [!CAUTION]`); collapsible blocks use `<details>`/`<summary>`.
  MkDocs-only syntax - admonitions, content tabs, attribute lists, snippet includes -
  is not used.
- Links between pages are relative `.md` paths, with GitHub-style heading anchors.
- `docs/index.md` is a real page holding the package table; it does not include the
  README. `README.md` stays the repository's front door and links into `docs/` with
  relative paths (`docs/getting-started.md`, `docs/packages/api.md`).

`.github/workflows/docs.yml` builds the site on every push and PR, and on `main` also
publishes it through GitHub Pages (Settings > Pages > Source = "GitHub Actions").
There is no manual deploy step.

## Layout

```text
pkg/api       URL layout every service exposes, shared by server, client and auth
pkg/client    resty-based client of the APIs pkg/server exposes
pkg/config    dotenv loading, validation, AppConfig
pkg/errors    RFC 9457 error carried back to API clients
pkg/listener  service socket, TCP or systemd socket activation
pkg/server    gin engine factory, metrics and OpenAPI endpoints
examples/     one runnable program per package
docs/         this site
```

Tests sit next to the code they cover and use real HTTP servers and real dotenv files
rather than mocks. New code is expected to arrive with tests in the same style.
