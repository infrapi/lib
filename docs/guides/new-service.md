# Build a new service, step by step

This guide builds a small but complete API from nothing: **bookmarks-api**, which
lets a team save and find links. Every command and every file below was run and
built as written. You do not need to know Go beforehand: each step says what the
code does and why it is there.

By the end you will have:

- a **contract**, the OpenAPI file that describes the API;
- Go code **generated** from it;
- a **backend** that saves the bookmarks, here as JSON files on disk;
- **handlers** that connect the two;
- a running service, with its documentation, its checks and its error format,
  all provided by the library;
- then **two replicas** of it, every write performed on both, and a way to check
  that each one took it.

```text
   the resource (you write)        the contract           the server code
     api/bookmarks.yaml ──go generate──► api/bookmarks/v1.yaml ──► internal/bookmarksv1/gen.go
                    │                                    │
                    │ validates every request            │ the interface to implement
                    ▼                                    ▼
 request ──► pkg/app ──────────────────────────► handlers.go ──► internal/storage
            (logs, auth, docs, errors…)          (you write)      (you write: the backend)
```

## Words you will meet

| Word | Meaning |
|---|---|
| **module** | A Go project. Its name and its dependencies are in `go.mod`. |
| **package** | A folder of `.go` files. `internal/storage` is a package; `main` is the program itself. |
| **struct** | A named group of fields, like a JSON object with a fixed shape. |
| **method** | A function attached to a struct: `func (h *handlers) GetBookmark(...)`. |
| **interface** | A list of methods a struct must have. The generated code defines one; your handlers implement it. |
| **error** | Go functions return their failures as a value, `err`, which the caller checks: `if err != nil { ... }`. |
| **operationId** | The name of one operation in the contract. It becomes the name of a Go method. |
| **replica** | One running copy of the service. Several replicas share the load and survive one stopping. |
| **ring** | A group of replicas that work together on something. A ring either has a **leader** — one replica acts — or **broadcasts** — every replica acts. |

## 0. Before you start

- **Go 1.26** or later: `go version`.
- **Access to `infrapi/lib`**, a private repository. Tell Go not to look for it on
  the public proxy, and let git reach GitHub with your credentials:

    ```bash
    go env -w GOPRIVATE='github.com/infrapi/*'
    gh auth setup-git        # or any git credential that can read infrapi/lib
    ```

- **curl**, to try the API.

## 1. Create the project

```bash
mkdir bookmarks-api && cd bookmarks-api
go mod init example.com/bookmarks-api
mkdir -p api/bookmarks internal/bookmarksv1 internal/storage
```

`go mod init` creates `go.mod`. The name, `example.com/bookmarks-api`, is how
the project's own packages refer to each other; use your repository's path in a
real project.

When you are done, the project will look like this:

```text
bookmarks-api/
├── api/bookmarks.yaml           2. the resource: its fields and operations
├── api/bookmarks/v1.yaml        3. generated: the contract
├── oapi-codegen.yaml            3. what to generate from it
├── generate.go                  3. the generate command
├── internal/bookmarksv1/gen.go  2. generated: never edit it
├── internal/storage/storage.go  4. the backend
├── handlers.go                  5. the contract's operations
├── main.go                      6. starts the service
├── config.yaml                  7. the configuration
└── handlers_test.go             9. a test
```

## 2. Describe the resource

The **contract** is the API's single source of truth: the library validates every
request against it, shows it as documentation at `/-/docs`, and the Go code is
generated from it. It is an [OpenAPI](https://spec.openapis.org/oas/v3.0.3) file,
long and exacting to write by hand. You do not write it: you **describe the
resource** in a few lines, and the contract is generated from that.

Create `api/bookmarks.yaml`:

```yaml
# The Bookmarks API: go generate writes the full contract,
# api/bookmarks/v1.yaml, from this file.
name: bookmarks
version: 1
description: Save and find the links a team cares about.
path: tenants/{tenant}
params:
  tenant: {type: string, pattern: "^[a-z0-9-]{1,40}$", description: Who the bookmarks belong to.}

resource: bookmark
fields:
  url:        {type: string, required: true, max: 2000}
  title:      {type: string, required: true, min: 1, max: 200}
  tags:       {type: list, of: string, max: 10, pattern: "^[a-z0-9-]{1,30}$"}
  created_at: {type: datetime, computed: true}
filters:
  tag: {type: string, description: Only the bookmarks with this tag.}

operations: [list, create, get, replace, delete]
example: {url: "https://grafana.com/docs/", title: Grafana documentation, tags: [docs, grafana]}
```

Line by line:

| Key | What it says | Here |
|---|---|---|
| `name` | the collection, plural: the URL's first segment and the collection's | `bookmarks` |
| `version` | the contract's major version, in the URL | `/bookmarks/v1/…` |
| `path` | what comes between the version and the collection, with its parameters | `/bookmarks/v1/tenants/{tenant}/bookmarks` |
| `params` | the path parameters' rules; a plain string when not given | a tenant is lower-case, 1 to 40 characters |
| `resource` | one item, singular: the Go types are named after it | `Bookmark`, `BookmarkSpec` |
| `fields` | the item's fields, in order | |
| `filters` | the list's query parameters | `?tag=docs` |
| `operations` | what the API offers, among `list`, `create`, `get`, `replace`, `delete` | all five |
| `example` | a body to create one, shown in `/-/docs` | |

A field is `{type: …}` — `string`, `integer`, `number`, `boolean`, `datetime`,
`date`, or `list` with `of: string` — and says who sets it, the way a Terraform
schema does:

| Option | The caller | The answer |
|---|---|---|
| `required: true` | must send it | has it |
| *(none)* | may send it | has it |
| `computed: true` | never sends it: the service sets it, as a creation date | has it |
| `optional: true, computed: true` | may send it; the service sets it otherwise | has it |
| `sensitive: true` | sends it | never has it: a password, a token |
| `immutable: true` | sends it on creation only: replacing keeps it | has it |

Its rules, as needed: `min` and `max` (a string's length, a list's size, a
number's value), `pattern` (a regular expression a string — or each item of a
list — must match), `enum: [a, b]`, `default` (when the caller leaves it out),
and `example` and `description` for the documentation.

What the generated contract holds, without your writing it:

- five operations, named after the resource: `listBookmarks`, `createBookmark`,
  `getBookmark`, `replaceBookmark`, `deleteBookmark` — they become the Go methods
  of step 5;
- `BookmarkSpec`, what the caller sends: the fields that are not computed, and
  nothing else — a typo like `"titel"` is refused instead of ignored;
- `Bookmark`, what the API answers: the `id` the service gives it, and every
  field;
- the answers' codes: `201` for a creation, `204` for a deletion, `200` otherwise.

And what every contract shares, the library adds when it serves it: the error
format (every error is a *problem document*, step 5) and the `apikey` header
every call carries (step 11). You will see them in `/-/docs`.

!!! tip "When the short form is not enough"
    An operation that is not one of the five — an action, a search across
    resources — needs the contract written by hand, in OpenAPI: write
    `api/bookmarks/v1.yaml` yourself, without `api/bookmarks.yaml`, and drop
    the `infrapi-contract` line of step 3. Everything else in this guide is
    the same. `examples/app` in the library is such a hand-written contract.

## 3. Generate the contract and the Go code

Two generators, one after the other: `infrapi-contract` writes the contract from
the resource, then [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen)
reads the contract and writes the Go side — the types, the router, and the
interface your handlers must implement.

Create `oapi-codegen.yaml`, which says what to generate and where:

```yaml
package: bookmarksv1
output: internal/bookmarksv1/gen.go
generate:
  models: true          # the schemas as Go structs
  std-http-server: true # the router, on Go's standard net/http
  strict-server: true   # typed requests in, typed responses out
```

Create `generate.go`, which records the two commands in the project itself, in
the order they run:

```go
package main

//go:generate go tool infrapi-contract api/bookmarks.yaml api/bookmarks/v1.yaml
//go:generate go tool oapi-codegen -config oapi-codegen.yaml api/bookmarks/v1.yaml
```

Add the generators to the project as tools, then run them:

```bash
go get -tool github.com/infrapi/lib/cmd/infrapi-contract@latest
go get -tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0
go generate ./...
```

`go get -tool` records each generator's version in `go.mod`: everyone who clones
the project generates the same files, with nothing to install. `go generate`
runs the `//go:generate` lines: `api/bookmarks/v1.yaml`, then
`internal/bookmarksv1/gen.go`. Open the contract: about 150 lines, from your 20.

Open it and find two things. The types of the schemas:

```go
type BookmarkSpec struct {
	Tags  *[]string `json:"tags,omitempty"` // optional: a pointer, nil when absent
	Title string    `json:"title"`
	Url   string    `json:"url"`
}
```

And the interface: one method per `operationId`, which is the list of what you
have to write.

```go
type StrictServerInterface interface {
	ListBookmarks(ctx context.Context, request ListBookmarksRequestObject) (ListBookmarksResponseObject, error)
	CreateBookmark(ctx context.Context, request CreateBookmarkRequestObject) (CreateBookmarkResponseObject, error)
	DeleteBookmark(ctx context.Context, request DeleteBookmarkRequestObject) (DeleteBookmarkResponseObject, error)
	GetBookmark(ctx context.Context, request GetBookmarkRequestObject) (GetBookmarkResponseObject, error)
	ReplaceBookmark(ctx context.Context, request ReplaceBookmarkRequestObject) (ReplaceBookmarkResponseObject, error)
}
```

!!! warning "Never edit `v1.yaml` or `gen.go`"
    Both are rewritten every time you run `go generate`. Change
    `api/bookmarks.yaml` instead, then generate again.

## 4. Write the backend

The backend is what the service actually does: here, save bookmarks as JSON
files, one folder per tenant. In your service it might call another API, a
database, a cloud provider. Keep it in its own package, with **no HTTP in it**:
it can then be tested and reused on its own.

Create `internal/storage/storage.go`:

```go
// Package storage keeps bookmarks on disk: one JSON file per bookmark, in one
// folder per tenant. It is the service's backend, and knows nothing of HTTP.
package storage

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ErrNotFound is returned when a bookmark does not exist.
var ErrNotFound = errors.New("bookmark not found")

// Bookmark is what is saved.
type Bookmark struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
}

// Store saves bookmarks under a folder.
type Store struct {
	dir string
}

// New returns a store that writes under dir, creating it if needed.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Create saves a new bookmark and returns it. It keeps the id and date it is
// given, and draws them only when they are empty.
func (s *Store) Create(tenant string, b Bookmark) (Bookmark, error) {
	if b.ID == "" {
		b.ID = "bm-" + rand.Text()[:10]
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}
	return b, s.write(tenant, b)
}

// Get reads one bookmark.
func (s *Store) Get(tenant, id string) (Bookmark, error) {
	var b Bookmark
	data, err := os.ReadFile(s.path(tenant, id))
	if errors.Is(err, os.ErrNotExist) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, fmt.Errorf("storage: %w", err)
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, fmt.Errorf("storage: %s: %w", id, err)
	}
	return b, nil
}

// List reads every bookmark of a tenant, oldest first.
func (s *Store) List(tenant string) ([]Bookmark, error) {
	files, err := filepath.Glob(filepath.Join(s.dir, tenant, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	out := []Bookmark{}
	for _, f := range files {
		id := filepath.Base(f[:len(f)-len(".json")])
		b, err := s.Get(tenant, id)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Replace overwrites an existing bookmark, keeping its id and creation date.
func (s *Store) Replace(tenant, id string, b Bookmark) (Bookmark, error) {
	old, err := s.Get(tenant, id)
	if err != nil {
		return b, err
	}
	b.ID, b.CreatedAt = old.ID, old.CreatedAt
	return b, s.write(tenant, b)
}

// Delete removes a bookmark. One already gone is not an error.
func (s *Store) Delete(tenant, id string) error {
	err := os.Remove(s.path(tenant, id))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage: %w", err)
	}
	return nil
}

func (s *Store) write(tenant string, b Bookmark) error {
	if err := os.MkdirAll(filepath.Join(s.dir, tenant), 0o750); err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	if err := os.WriteFile(s.path(tenant, b.ID), data, 0o600); err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	return nil
}

func (s *Store) path(tenant, id string) string {
	return filepath.Join(s.dir, tenant, filepath.Base(id)+".json")
}
```

Three habits worth copying:

- **Every error is checked** and returned, wrapped with where it happened
  (`fmt.Errorf("storage: %w", err)`). Nothing fails silently.
- **"Not found" is a named error**, `ErrNotFound`, so the handlers can recognise
  it and answer 404 instead of 500.
- **Delete of something already gone succeeds**: a caller that retries a delete
  must not get an error because the first attempt worked.
- **`Create` takes the id it is given**, and draws one only when there is none:
  step 5 explains why.

## 5. Write the handlers

The handlers implement the generated interface. Each one does the same three
things: **read the request, call the backend, return the answer**.

Create `handlers.go`:

```go
package main

import (
	"context"
	"errors"
	"slices"

	"github.com/infrapi/lib/pkg/app"
	"github.com/infrapi/lib/pkg/app/problem"

	api "example.com/bookmarks-api/internal/bookmarksv1"
	"example.com/bookmarks-api/internal/storage"
)

// handlers answers the API's operations: one method per operationId of the
// contract. Each one reads the request, asks the store, and returns the answer.
type handlers struct {
	store *storage.Store
}

// The build fails here while an operation of the contract has no method.
var _ api.StrictServerInterface = (*handlers)(nil)

// ListBookmarks answers GET /bookmarks/v1/tenants/{tenant}/bookmarks.
func (h *handlers) ListBookmarks(_ context.Context, req api.ListBookmarksRequestObject) (api.ListBookmarksResponseObject, error) {
	all, err := h.store.List(req.Tenant)
	if err != nil {
		return nil, err
	}
	out := api.ListBookmarks200JSONResponse{}
	for _, b := range all {
		if req.Params.Tag == nil || slices.Contains(b.Tags, *req.Params.Tag) {
			out = append(out, toAPI(b))
		}
	}
	return out, nil
}

// CreateBookmark answers POST /bookmarks/v1/tenants/{tenant}/bookmarks.
func (h *handlers) CreateBookmark(ctx context.Context, req api.CreateBookmarkRequestObject) (api.CreateBookmarkResponseObject, error) {
	// The id and the date come from the write, not from chance or this
	// machine's clock: if the write runs on several replicas, every one
	// creates the same bookmark.
	b := fromAPI(req.Body)
	if id := app.WriteID(ctx); id != "" { // empty when the handler is called on its own, as in a test
		b.ID = "bm-" + id[:10]
	}
	b.CreatedAt = app.WriteTime(ctx) // zero then too: the store sets both
	saved, err := h.store.Create(req.Tenant, b)
	if err != nil {
		return nil, err
	}
	return api.CreateBookmark201JSONResponse(toAPI(saved)), nil
}

// GetBookmark answers GET /bookmarks/v1/tenants/{tenant}/bookmarks/{id}.
func (h *handlers) GetBookmark(_ context.Context, req api.GetBookmarkRequestObject) (api.GetBookmarkResponseObject, error) {
	b, err := h.store.Get(req.Tenant, req.Id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, problem.NotFound("no bookmark %q", req.Id)
	}
	if err != nil {
		return nil, err
	}
	return api.GetBookmark200JSONResponse(toAPI(b)), nil
}

// ReplaceBookmark answers PUT /bookmarks/v1/tenants/{tenant}/bookmarks/{id}.
func (h *handlers) ReplaceBookmark(_ context.Context, req api.ReplaceBookmarkRequestObject) (api.ReplaceBookmarkResponseObject, error) {
	b, err := h.store.Replace(req.Tenant, req.Id, fromAPI(req.Body))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, problem.NotFound("no bookmark %q", req.Id)
	}
	if err != nil {
		return nil, err
	}
	return api.ReplaceBookmark200JSONResponse(toAPI(b)), nil
}

// DeleteBookmark answers DELETE /bookmarks/v1/tenants/{tenant}/bookmarks/{id}.
func (h *handlers) DeleteBookmark(_ context.Context, req api.DeleteBookmarkRequestObject) (api.DeleteBookmarkResponseObject, error) {
	if err := h.store.Delete(req.Tenant, req.Id); err != nil {
		return nil, err
	}
	return api.DeleteBookmark204Response{}, nil
}

// fromAPI turns what the caller sent into what the store saves.
func fromAPI(in *api.BookmarkSpec) storage.Bookmark {
	b := storage.Bookmark{URL: in.Url, Title: in.Title, Tags: []string{}}
	if in.Tags != nil {
		b.Tags = *in.Tags
	}
	return b
}

// toAPI turns what the store holds into what the caller receives.
func toAPI(b storage.Bookmark) api.Bookmark {
	return api.Bookmark{Id: b.ID, Url: b.URL, Title: b.Title, Tags: b.Tags, CreatedAt: b.CreatedAt}
}
```

How to read one handler, `GetBookmark`:

1. `req` holds the request, already checked against the contract: `req.Tenant`
   and `req.Id` come from the path.
2. `h.store.Get(...)` is the backend action.
3. Not found → `problem.NotFound(...)`, any other failure → return `err` as it is.
4. Success → `api.GetBookmark200JSONResponse(...)`: the type says which status
   code is sent. The generator made one such type per response of the contract.

**Errors are returned, never written by hand.** The library turns them into the
API's single error format:

| You return | The caller gets |
|---|---|
| `problem.Invalid("…")` | 400 `invalid_request` |
| `problem.NotFound("…")` | 404 `not_found` |
| `problem.AlreadyExists(current, "…")` | 409 `already_exists` |
| `problem.Unavailable("…")` | 503 `upstream_unavailable`, `retryable: true` |
| `problem.Upstream(retryable, "…")` | 502 `upstream_error`: the system behind the service failed |
| any other error | 500 `internal_error`; its text is hidden from the caller and written in the log, under the call's `correlation_id` |

Requests that do not match the contract never reach your code: the library
answers 400 for them.

**Why `CreateBookmark` takes its id from `app.WriteID`.** Every write the library
handles has an identity: `app.WriteID(ctx)`, 26 letters and digits, and
`app.WriteTime(ctx)`, when it arrived. With one replica it changes nothing. In
step 10, the same write runs on every replica: if each drew its own random id,
each would create a different bookmark; with the write's id, they all create the
same one. Take the habit now: what a handler would draw by chance or read from
the clock, it takes from the write.

## 6. Start the service

Create `main.go`. It is short: everything else — logs, metrics, documentation,
validation, authentication, shutdown — is the library's.

```go
// Command bookmarks-api serves the Bookmarks API.
package main

import (
	"context"
	_ "embed"
	"net/http"

	"github.com/infrapi/lib/pkg/app"
	"github.com/infrapi/lib/pkg/app/problem"
	"github.com/infrapi/lib/pkg/server"

	api "example.com/bookmarks-api/internal/bookmarksv1"
	"example.com/bookmarks-api/internal/storage"
)

// The contract, built into the binary.
//
//go:embed api/bookmarks/v1.yaml
var contractV1 []byte

// Config is the "service:" section of config.yaml.
type Config struct {
	DataDir string `yaml:"data_dir"`
}

func main() {
	var cfg Config
	app.Main(app.Spec{
		Name:    "bookmarks-api",
		Service: &cfg,
		API: func(_ context.Context, _ *app.Runtime) ([]app.Version, error) {
			store, err := storage.New(cfg.DataDir)
			if err != nil {
				return nil, err
			}
			return []app.Version{{
				OpenAPI: server.OpenAPISpec{Name: "bookmarks v1", Path: "bookmarks/v1.yaml", Content: contractV1},
				Routes:  func(r app.Router) { r.Mount(handlerV1(&handlers{store: store})) },
			}}, nil
		},
	})
}

// handlerV1 plugs the handlers into the generated router, and turns every
// error into the API's error format.
func handlerV1(h api.StrictServerInterface) http.Handler {
	invalid := func(w http.ResponseWriter, r *http.Request, err error) {
		problem.Write(w, r, problem.Invalid("%v", err))
	}
	strict := api.NewStrictHandlerWithOptions(h, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  invalid,
		ResponseErrorHandlerFunc: problem.Write,
	})
	return api.HandlerWithOptions(strict, api.StdHTTPServerOptions{ErrorHandlerFunc: invalid})
}
```

What happens when it starts:

1. `app.Main` reads `config.yaml` and puts its `service:` section into `cfg`.
2. It calls `API`, which builds the backend and returns the contract with its
   routes.
3. It serves them, behind authentication and validation, until it is stopped
   (Ctrl+C), then shuts down cleanly.

`//go:embed` copies the contract into the program at build time: the binary
needs no file next to it to validate requests and show the documentation.

Now add the library, and let Go fetch everything the code uses. Take the
latest release.

```bash
VERSION=$(git ls-remote --tags --sort=-v:refname https://github.com/infrapi/lib.git 'refs/tags/v*' | head -1 | sed 's#.*/##')
go get github.com/infrapi/lib/pkg/app@$VERSION
```

Then add this line at the end of `go.mod`, and run the `go get` again:

```text
replace github.com/hashicorp/memberlist => github.com/grafana/memberlist v0.3.1-0.20260921102813-8c522db348af
```

The cluster library uses Grafana's fork of memberlist, and Go does not pass a
dependency's `replace` on to you: every service on `pkg/app` carries this line.

!!! note "With v0.4.0 only"
    v0.4.0's `pkg/app` refers to versions of its sibling modules that do not
    exist, so `go get` fails with `unknown revision v1.6.0`. Take a later
    release, or add these two lines to the `replace` too:

    ```text
    replace github.com/infrapi/lib => github.com/infrapi/lib v0.4.0
    replace github.com/infrapi/lib/pkg/cluster => github.com/infrapi/lib/pkg/cluster v0.4.0
    ```

```bash
go mod tidy
go build ./...
```

`go build` succeeding means the handlers implement every operation of the
contract.

## 7. Configure it

Create `config.yaml`:

```yaml
app:
  platform: sandbox
  region: eu-west-1
  location: dc1
  url: http://localhost:8080
  fqdn: bookmarks.localhost
  listen_address: 127.0.0.1:8080
  log_format: text        # json in production

shutdown:
  drain_delay: 0s         # in production, leave the default: the load balancer needs time

service:                  # your own settings: the Config struct of main.go
  data_dir: ./data
```

Any `app` setting can be overridden by an environment variable:
`INFRAPI_APP_LISTEN_ADDRESS=0.0.0.0:9090`. A key the library does not know is
refused at start, so a typo is caught at once. [app](../packages/app.md) lists
them all.

## 8. Run it and try it

```bash
go run . -config config.yaml
```

```text
level=warning msg="no database: Idempotency-Key is IGNORED, and a retried write runs twice"
level=warning msg="no API key configured: the API authenticates nobody, and must only be reachable through the gateway"
level=info msg=listening addr=127.0.0.1:8080 versions=1 idempotency=false
```

The two warnings are expected for now; step 11 removes them.

Open <http://localhost:8080/> for the service's page, and
<http://localhost:8080/-/docs> to read and try the API in Swagger UI.

In another terminal:

```bash
B=http://localhost:8080/bookmarks/v1/tenants/acme/bookmarks

# Create
curl -s -X POST $B -H 'Content-Type: application/json' \
  -d '{"url":"https://grafana.com/docs/","title":"Grafana documentation","tags":["docs","grafana"]}'
```
```json
{"created_at":"2026-10-08T10:21:37.60369068Z","id":"bm-XSXT5JLDTF","tags":["docs","grafana"],"title":"Grafana documentation","url":"https://grafana.com/docs/"}
```

```bash
# List, filtered by tag
curl -s "$B?tag=docs"

# Read one (use the id you got)
curl -s $B/bm-XSXT5JLDTF

# Replace it
curl -s -X PUT $B/bm-XSXT5JLDTF -H 'Content-Type: application/json' \
  -d '{"url":"https://grafana.com/docs/grafana/latest/","title":"Grafana docs (latest)","tags":["docs"]}'

# See what the backend wrote
cat data/acme/bm-XSXT5JLDTF.json

# Delete it — twice: both answer 204
curl -s -o /dev/null -w '%{http_code}\n' -X DELETE $B/bm-XSXT5JLDTF
```

And what the API refuses, without a line of your code:

```bash
curl -s -X POST $B -H 'Content-Type: application/json' -d '{"url":"https://x","title":""}'
```
```json
{"title":"Invalid request","status":400,"detail":"request body has an error: doesn't match schema #/components/schemas/BookmarkSpec: Error at \"/title\": minimum string length is 1","code":"invalid_request","retryable":false,"correlation_id":"HX6XS46D5SOLCKNNAXP5J46PX6"}
```

```bash
curl -s $B/bm-nope
```
```json
{"title":"Not found","status":404,"detail":"no bookmark \"bm-nope\"","code":"not_found","retryable":false,"correlation_id":"RABQENJIBFBLI3RZHI673J3MRI"}
```

Every answer carries a `correlation_id`: the same id is in the log line of the
call, which is how a caller's report is found in the logs.

## 9. Test it

A test calls the handlers through the generated router, without starting a
server. Create `handlers_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/bookmarks-api/internal/storage"
)

func TestBookmarks(t *testing.T) {
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	api := handlerV1(&handlers{store: store})

	call := func(method, path, body string) (int, string) {
		req := httptest.NewRequest(method, "/bookmarks/v1/tenants/acme/bookmarks"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	code, body := call(http.MethodPost, "", `{"url":"https://grafana.com","title":"Grafana"}`)
	if code != http.StatusCreated || !strings.Contains(body, `"title":"Grafana"`) {
		t.Fatalf("create: %d %s", code, body)
	}

	code, body = call(http.MethodGet, "/bm-nope", "")
	if code != http.StatusNotFound {
		t.Fatalf("read a missing bookmark: %d %s", code, body)
	}
}
```

```bash
go test ./...
```
```text
ok  	example.com/bookmarks-api	0.036s
```

`t.TempDir()` gives each test an empty folder, deleted afterwards: tests never
touch real data.

## 10. Run several replicas

One replica is one machine to lose. This step runs two, and keeps a copy of
every bookmark on each: a write that reaches either is performed on both.

### Declare the rings

A ring is a group of replicas working together. Add two to `app.Spec` in
`main.go`:

```go
import "github.com/infrapi/lib/pkg/cluster"

	app.Main(app.Spec{
		Name:    "bookmarks-api",
		Service: &cfg,
		// The service's rings: the contract gives its writes to them.
		Roles: []cluster.RoleSpec{
			{Name: "everybody", Mode: cluster.ModeBroadcast}, // every replica acts
			{Name: "cache", Mode: cluster.ModeLeader},        // one replica acts: the leader
		},
		API: ...
```

`everybody` is a **broadcast** ring: all its members act. `cache` is a
**leader** ring: its members elect one, which alone acts. This guide uses
`everybody`; `cache` is there to show the other kind.

### Give the writes to a ring

Add one line to `api/bookmarks.yaml`, and run `go generate ./...`:

```yaml
writes: everybody    # every write runs on every replica of the "everybody" ring
```

That is all the code there is. The library now runs every write — create,
replace, delete — on the replica that received it, then sends the same request
to the other members of `everybody`, which run the same handler. Reads stay where
they arrive: each replica reads its own copy.

One operation can be given another ring of its own:

```yaml
rings:
  delete: cache      # a delete: on the leader of "cache" only, relayed there from the others
```

| The operation's ring | It runs |
|---|---|
| a broadcast ring | here, then on every other member of the ring |
| a leader ring | on that ring's leader; relayed there from the other replicas — reads too |
| `local` | on the replica that received it |

### Configure two replicas

Two configuration files, one per replica. On one machine they need two
addresses: Linux answers on all of `127.0.0.0/8` (on macOS, add one first with
`sudo ifconfig lo0 alias 127.0.0.2`).

`node1.yaml`:

```yaml
app:
  platform: sandbox
  region: eu-west-1
  location: dc1
  url: http://127.0.0.1:8080
  fqdn: bookmarks.localhost
  listen_address: 127.0.0.1:8080
  log_format: text
shutdown:
  drain_delay: 0s
service:
  data_dir: ./data
cluster:
  enabled: true
  instance_id: node-1             # unique per replica
  join_members: [127.0.0.1:7946]  # where to find the others
  roles: [everybody, cache]       # the rings this replica joins
```

`node2.yaml` is the same with `127.0.0.2` in `url` and `listen_address`,
`data_dir: ./data2` and `instance_id: node-2`.

Each replica gossips with the others on port 7946 of its own address, and calls
their API on its port: both must be open between replicas.

### Try it

In two terminals:

```bash
go run . -config node1.yaml
go run . -config node2.yaml
```

Create a bookmark on the second replica, and look at both disks:

```bash
curl -si -X POST http://127.0.0.2:8080/bookmarks/v1/tenants/acme/bookmarks \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://grafana.com/docs/","title":"Grafana documentation"}'
```
```text
HTTP/1.1 201 Created
X-Infrapi-Write-Id: bujkubzpvqcxv24co3oxuortcq
{"created_at":"2026-10-08T15:58:54.818186545Z","id":"bm-bujkubzpvq",…}
```
```bash
ls data/acme data2/acme
```
```text
data/acme:
bm-bujkubzpvq.json

data2/acme:
bm-bujkubzpvq.json
```

The same bookmark, same id, on both: the id came from `app.WriteID`.

### Check that every replica took it

Every write is answered with `X-Infrapi-Write-Id`. Any replica says how it went
— also in Swagger UI, at `/-/docs`, under **infrapi (replica routes)**:

```bash
curl -s http://127.0.0.1:8080/-/broadcast/bujkubzpvqcxv24co3oxuortcq
```
```json
{"write_id":"bujkubzpvqcxv24co3oxuortcq","status":"complete","by":"node-2",
 "replicas":{"node-1":{"state":"done","status":201},"node-2":{"state":"done","status":201}}}
```

`status` is `complete` when every replica took the write, `partial` when one did
not, `pending` while one has not answered, and `error` when the write could not
be sent to the others at all.

### When a replica fails

By default the caller waits for every replica, and when one did not take the
write it gets `502 broadcast_incomplete`, retryable, naming it. Send the same
request again **with the same `Idempotency-Key` header**: the write's id comes
from the key, so every replica writes the same bookmark again, and the one that
missed it catches up. To choose otherwise, for every write or per operation:

```yaml
broadcast: async        # strict (the default), tolerant or async
broadcasts:
  create: tolerant      # this one otherwise
```

| | The caller waits | A replica that failed |
|---|---|---|
| `strict` | for every replica | `502`: retry with the same key |
| `tolerant` | for every replica | the first replica's answer; the failure is in the status and the logs |
| `async` | for nobody | the first replica's answer; the failure is in the status and the logs |

### What to know

- A replica that is stopped when a write happens does not get it later. When
  every replica must hold the same data whatever happens, a store they share — a
  database, a shared volume — is simpler than keeping copies in step.
- The logs follow a write across replicas by its `write_id`: `broadcast_to` on the
  replica that received it, `broadcast_from` on the others.
- [app](../packages/app.md) has the details: rings per operation, the three
  modes, the status, and `leader_writes`, which sends every write to one leader
  without a line in the contract.

## 11. Before production

Each of these is configuration, not code.

**Require a key.** Store only the key's SHA-256, never the key itself:

```bash
printf %s 'secret-key' | sha256sum
```
```yaml
auth:
  apikey_sha256: 85dbe15d75ef9308c7ae0f33c7a324cc6f4bf519a2ed2f3027bd33c140a4f9aa
```

Callers then send `apikey: secret-key`; without it, 401. Behind the APISIX
gateway, which checks keys itself, leave it out. Better still, set it from the
environment rather than the file: `INFRAPI_AUTH_APIKEY_SHA256`.

**Make retries safe.** With a Postgres database, a write sent twice with the same
`Idempotency-Key` header runs once, and the second call gets the first answer:

```yaml
database:
  dsn: postgres://bookmarks:…@db:5432/bookmarks   # or INFRAPI_DATABASE_DSN
```

**Choose how the replicas share the work**, in [app](../packages/app.md):

- `cluster.leader_writes` makes one replica perform every write, without a line
  in the contract;
- `gateway.admin_url` lets the service configure APISIX in front of itself;
- `Spec.Background` runs work of your own — a purge, a refresh — on a ring's
  leader or on every member.

## Changing the API later

The loop is always the same:

1. Edit `api/bookmarks.yaml`: a new field, a new operation.
2. `go generate ./...`
3. `go build ./...`: the compiler lists what the handlers must now do — a missing
   method, a renamed field.
4. Write it, test it.

A change that would break existing callers — a field removed or renamed, a
required field added — belongs in a new version: a second resource file with
`version: 2`, its own generated contract and package, and a second entry in the
list `API` returns. Both versions are then
served side by side.
