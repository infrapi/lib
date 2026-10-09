# contract

```go
import "github.com/infrapi/lib/pkg/contract"
```

Writes a service's OpenAPI contract from a short description of its resource,
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
id: service                     # service (default): the service gives the id; caller: the caller may
fields:
  url:        {type: string, required: true, max: 2000}
  title:      {type: string, required: true, min: 1, max: 200}
  tags:       {type: list, of: string, max: 10, pattern: "^[a-z0-9-]{1,30}$"}
  created_at: {type: datetime, computed: true}
filters:
  tag: {type: string, description: Only the bookmarks with this tag.}

operations: [list, create, get, replace, delete]
example: {url: "https://grafana.com/docs/", title: Grafana documentation}

writes: everybody               # the ring every write runs on (x-infrapi-writes)
rings: {delete: cache}          # one operation's own ring (x-infrapi-ring)
broadcast: strict               # how a broadcast write answers (x-infrapi-broadcast)
broadcasts: {create: async}     # one operation's own
```

A key the format does not know, at the top or in a field, is refused: a typo
does not vanish.

### Fields

| Option | |
|---|---|
| `type` | `string`, `integer`, `number`, `boolean`, `datetime`, `date`, or `list` with `of` |
| `of` | a list's items: `string`, `integer`, `number` or `boolean` |
| `required` | the caller must send it |
| `min`, `max` | a string's length, a list's size, a number's value |
| `pattern` | a regular expression for a string, or for each item of a list |
| `enum` | the only values allowed |
| `default`, `example`, `description` | documentation |
| `computed` | set by the service: in the answers only, never sent |

Filters are fields too, as query parameters of the list.

## The contract it writes

| Operation | Method and path | operationId | Answers |
|---|---|---|---|
| `list` | `GET /{name}/v{version}/{path}/{name}` | `list<Names>` | `200`, a list of `<Resource>` |
| `create` | `POST /{name}/v{version}/{path}/{name}` | `create<Resource>` | `201`, the `<Resource>` |
| `get` | `GET …/{name}/{id}` | `get<Resource>` | `200` |
| `replace` | `PUT …/{name}/{id}` | `replace<Resource>` | `200` |
| `delete` | `DELETE …/{name}/{id}` | `delete<Resource>` | `204`, gone or already gone |

- `<Resource>Spec` is what the caller sends: the fields that are not computed,
  with their rules, and no other (`additionalProperties: false`). With
  `id: caller`, an optional `id` too.
- `<Resource>` is what the service answers: the `id` and every field, always
  present.
- The error format and the `apikey` header are not written: [`app`](app.md)
  adds them to every contract it serves.
- The file starts with `# Generated from … DO NOT EDIT`: change the description
  and run `go generate` again.

## When it is not enough

An operation that is not one of the five needs the contract written in OpenAPI
by hand: start from the generated one, drop the `infrapi-contract` line, and
own the file from then on. `examples/app` is a hand-written contract.
