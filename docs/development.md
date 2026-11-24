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
| `docs` | previews these pages in the InfraPI portal, see below |

CI is [infrapi/ci](https://github.com/infrapi/ci)'s Go workflow, called by
`.github/workflows/ci.yml`: gofmt, `go vet`, golangci-lint, gosec and
`go test -race`, then, on `main`, the release. It fails when a file is not
gofmt'ed, so run `make fmt` before pushing.

## Documentation

These pages are published by the InfraPI developer portal,
[infrapi/docs](https://github.com/infrapi/docs): a Hugo site that pulls
`docs/` from every product at build time. There is no site to build in this
repository. To preview a change, clone infrapi/docs next to this repository,
then:

```bash
make docs                  # make -C ../docs dev: links your checkouts, serves http://localhost:1313/docs/ with live reload
make -C ../docs check      # builds as the portal's CI does: fails on any broken link or anchor
```

The portal needs Hugo v0.158 or later (`make -C ../docs install-hugo`
installs it with `go install`).

There is no navigation file: the portal builds the menu from the folders,
and orders the pages within a folder by their `weight`. A new page is a new
file with its front matter.

The pages are plain, GitHub-native Markdown so they read the same on github.com and in
the portal:

- Every page starts with YAML front matter - `title` (the H1 text), a one-sentence
  `description` and a `weight` (10, 20, 30... the page's order within its folder) - and
  keeps its `#` heading in the body.
- Callouts are GitHub alerts (`> [!NOTE]`, `> [!TIP]`, `> [!IMPORTANT]`,
  `> [!WARNING]`, `> [!CAUTION]`); collapsible blocks use `<details>`/`<summary>`.
  Site-specific syntax - shortcodes, admonitions, attribute lists, snippet includes -
  is not used.
- Links between pages are relative `.md` paths, with GitHub-style heading anchors.
- `docs/index.md` is a real page holding the package table; it does not include the
  README. `README.md` stays the repository's front door and links into `docs/` with
  relative paths (`docs/getting-started.md`, `docs/packages/api.md`).

A push to `main` that changes `docs/` asks the portal to rebuild, and so does
every release (infrapi/ci's workflow, `notify-docs: true`); the portal also
rebuilds every night. There is no manual deploy step.

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
