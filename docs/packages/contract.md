# contract

```go
import "github.com/infrapi/lib/pkg/contract"
```

Writes a service's OpenAPI contract from a short description of its resources,
so that nobody writes OpenAPI by hand for an ordinary API. Used through its
command, `infrapi-contract`, from `go generate`:

```go
//go:generate go tool infrapi-contract api/bookmarks.yaml api/bookmarks/v1.yaml
//go:generate go tool oapi-codegen -config oapi-codegen.yaml api/bookmarks/v1.yaml
```

```bash
go get -tool github.com/infrapi/lib/cmd/infrapi-contract@latest
```

The [guide](../guides/new-service.md) builds a service with it, step by step.

## The description

```yaml
name: bookmarks                 # the collection, plural
version: 1
title: Bookmarks                # default: the name
description: Save and find the links a team cares about.
path: tenants/{tenant}          # between the version and the collection
params:
  tenant: {type: string, pattern: "^[a-z0-9-]{1,40}$", description: Who the bookmarks belong to.}

resource: bookmark              # one item, singular
id: service                     # service (default): the service gives the id; caller: the caller may;
                                # infrapi: the library answers UUIDs and pairs them with the backend's (x-infrapi-ids)
fields:
  url:        {type: string, required: true, max: 2000}
  title:      {type: string, required: true, min: 1, max: 200}
  tags:       {type: list, of: string, max: 10, pattern: "^[a-z0-9-]{1,30}$"}
  created_at: {type: datetime, computed: true}
filters:
  tag: {type: string, description: Only the bookmarks with this tag.}

operations: [list, create, get, replace, delete]
example: {url: "https://grafana.com/docs/", title: Grafana documentation}

cluster:                        # the resource's place in the service's rings (Spec.Roles)
  writes: everybody             # the ring every write runs on (x-infrapi-writes)
  rings: {delete: cache}        # one operation's own ring; a read can have a leader's (x-infrapi-ring)
  broadcast: strict             # how a broadcast write answers (x-infrapi-broadcast)
  broadcasts: {create: async}   # one operation's own
```

A key the format does not know, at the top, in a resource or in a field, is
refused: a typo does not vanish.

### Fields

Who sets a field, and when, is said as a Terraform schema says it:

| Option | Like Terraform's | The caller | The answer |
|---|---|---|---|
| `required: true` | `Required` | must send it | has it |
| *(none)*, or `optional: true` | `Optional` | may send it | has it |
| `computed: true` | `Computed` | never sends it: the service sets it | has it |
| `optional: true, computed: true` | `Optional` + `Computed` | may send it; the service sets it otherwise | has it |
| `sensitive: true` | `Sensitive`, `WriteOnly` | sends it | never has it: a password, a token |
| `immutable: true` | `ForceNew` | sends it on creation only: replacing keeps it | has it |

And for any field:

| Option | |
|---|---|
| `type` | `string`, `integer`, `number`, `boolean`, `datetime`, `date`; `list` with `of`; `object`, any JSON object; `map` with `of`, string keys; or one of the [types](#types) |
| `of` | a list's items — `string`, `integer`, `number`, `boolean` or one of the types — or a map's values |
| `ref` | the resource whose id the field holds — each item's, for a list: with `id: infrapi`, it is one of the API's UUIDs, turned into the backend's and back |
| `omit_empty` | left out of an answer when it has no value, instead of always there: a parent a top-level folder has not |
| `min`, `max` | a string's length, a list's size, a number's value |
| `pattern` | a regular expression for a string, or for each item of a list |
| `enum` | the only values allowed |
| `default` | the value when the caller leaves it out |
| `example`, `description` | documentation |

Contradictions are refused: `required` with `optional`, `computed` or a
`default`; `sensitive` with `computed`; `immutable` with `computed` alone.

Filters are fields too, as query parameters of the list. Objects and maps
belong in a body: not in a filter, nor in a path parameter.

## The contract it writes

| Operation | Method and path | operationId | Answers |
|---|---|---|---|
| `list` | `GET /{name}/v{version}/{path}/{name}` | `list<Names>` | `200`, a list of `<Resource>` |
| `create` | `POST /{name}/v{version}/{path}/{name}` | `create<Resource>` | `201`, the `<Resource>` |
| `get` | `GET …/{name}/{id}` | `get<Resource>` | `200` |
| `replace` | `PUT …/{name}/{id}` | `replace<Resource>` | `200` |
| `delete` | `DELETE …/{name}/{id}` | `delete<Resource>` | `204`, gone or already gone |

- `<Resource>Spec` is what the caller sends to create: the fields it may send,
  with their rules, and no other (`additionalProperties: false`). With
  `id: caller`, an optional `id` too.
- `<Resource>Update` is what it sends to replace, when immutable fields make it
  differ: the same without them. Without immutable fields, replacing sends a
  `<Resource>Spec` too.
- `<Resource>` is what the service answers: the `id` and every field but the
  sensitive ones, always present.
- The error answers — `400`, `404`, the service's `errors` — and the `Problem`
  they carry are written; the `apikey` header and a `default` error answer are
  not: [`app`](app.md) adds them to every contract it serves.
- The file starts with `# Generated from … DO NOT EDIT`: change the description
  and run `go generate` again.

## A service of several resources

Resources that share a path, a tenant, their errors, go in one description:
the shared keys at the top, each resource under `resources`.

```yaml
name: shop
version: 1
path: tenants/{tenant}
params:
  tenant: {type: string, pattern: "^[a-z0-9-]{1,40}$"}
id: infrapi                     # every resource's, unless one says its own

errors:                         # what every operation may answer, by status
  401: No key, or one that is not known. Not retryable.
  503: The backend could not be reached. Retryable.
problem:                        # what the errors carry besides the library's fields
  codes: [not_found, already_exists, in_use]
  fields:
    current: {type: object, description: On already_exists, the item as it is.}

types:                          # objects the fields hold
  line:
    product_id: {type: string, required: true, ref: product}
    quantity:   {type: integer, required: true, min: 1}

resources:
  - name: products              # the collection, plural
    resource: product           # one item: the types are named after it
    description: What is sold.  # the resource's section in /-/docs
    fields:
      name:  {type: string, required: true}
      notes: {type: string, omit_empty: true}
      url:   {type: string, computed: true}
    list: {paging: true, fields: [name, url]}
    operations: [list, create, get, replace, delete]
    query:                      # an operation's own parameters
      delete:
        force: {type: boolean, default: false, description: Delete it even when ordered.}
    docs:                       # each operation's documentation
      list: Sorted by name. A full page carries next_page.
      create:
        summary: Add a product
        description: Its name is unique.
        errors:
          409: '`already_exists`, with `current`.'

  - name: orders
    resource: order
    fields:
      lines: {type: list, of: line, required: true}
    filters:
      product_id: {type: string, ref: product}
    list: {}
    operations: [list, create, get]
```

- **URLs**: `/{name}/v{version}/{path}/{collection}`, here
  `/shop/v1/tenants/{tenant}/products`. A single resource's description is the
  same, with the resource at the top: its collection is the service's name.
- **`id`**: `service`, `caller` or `infrapi`, at the top or per resource. With
  `infrapi`, the contract marks every id and every `ref` (see
  [app](app.md#the-apis-own-ids-x-infrapi-ids)).
- **`types`**: an object a field holds — `type: line` — or the items of a list
  hold — `type: list, of: line`. Written once, under `components/schemas`.
- **`list`**: without it, a list answers a plain array of the items. With it, an
  object with `items`; with `paging: true`, the `limit` (1 to 1000, default 100)
  and `page` (from 1) parameters, and `page`, `limit` and `next_page` in the
  answer; with `fields`, each item is a summary of those fields and its id
  (`<Resource>Summary`).
- **`docs`**: an operation's `summary`, its `description` — a string alone is the
  description — and the `errors` it answers besides those every operation does.
- **`errors`**: answered by every operation, written once under
  `components/responses`. `400` goes only where a body, a parameter or an id can
  be wrong, `404` only on an item (`get`, `replace`); a description given here
  replaces the generic one.
- **`problem`**: the error format is written in the contract — the library's
  fields (`status`, `code`, `title`, `detail`, `retryable`, `correlation_id`),
  `codes` documented on `code`, and the service's own fields.
- **`cluster`**: at the top, `writes` and `broadcast` for every write; in a
  resource, `writes` and `broadcast` for its writes, `rings` and `broadcasts` for
  one operation.
- Every operation takes the `X-Correlation-ID` header, every write the
  `Idempotency-Key` header: what [app](app.md) does with every call, in the
  generated client too.

## When it is not enough

An operation that is not one of the five needs the contract written in OpenAPI
by hand: start from the generated one, drop the `infrapi-contract` line, and
own the file from then on. `examples/app` is a hand-written contract.
