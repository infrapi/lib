# api

```go
import "github.com/infrapi/lib/pkg/api"
```

The URL layout every InfraPI service exposes its resources under. [`server`](server.md)
mounts it, [`client`](client.md) calls it and [`auth`](auth.md) authorizes it, all through
this package, so the three never disagree on a path:

```text
/apis/{name}.{domain}/{version}/namespaces/{namespace}/{resource}[/{id}[/{action}[/{id}...]]]
```

```text
/apis/compute.giservices.io/v1/namespaces/core/vm
/apis/compute.giservices.io/v1/namespaces/core/vm/123
/apis/compute.giservices.io/v1/namespaces/core/vm/123/reboot
```

After the namespace, segments alternate a resource or action name and an instance id.
`auth` relies on that to tell `vm` (list) from `vm/123` (get), and to grant
`vm/123/reboot` as resource `vm/reboot`.

The package has no dependency, so a client importing it does not pull in gin.

## Group

```go
compute := api.Group{Name: "compute", Domain: "giservices.io", Version: "v1"}
```

| Field | Form | Example |
|-------|------|---------|
| `Name` | lower case DNS label | `compute` |
| `Domain` | lower case DNS name | `giservices.io` |
| `Version` | `v{n}`, optionally `alpha{n}` or `beta{n}` | `v1`, `v2beta1` |

| Method | Returns |
|--------|---------|
| `URN()` | `compute.giservices.io`, the API name `auth` authorizes against |
| `Validate()` | the first field out of form |
| `Root()` | `/apis/compute.giservices.io/v1` |
| `NamespacePath(ns)` | `/apis/compute.giservices.io/v1/namespaces/{ns}`, `ns` as given, so `":namespace"` makes a route pattern |
| `Path(ns, resource)` | the full path, each segment of `resource` escaped, or an error |

## Parse

```go
p, err := api.Parse(r.URL.Path)
// p.API       "compute.giservices.io"
// p.Version   "v1"
// p.Namespace "core"
// p.Resource  "vm/123/reboot"
```

`Parse` takes an unescaped path, `net/http`'s `URL.Path`, and refuses anything outside the
layout: another prefix, a missing `namespaces` segment, an API without a domain, an
invalid version or namespace, an empty resource, or a `.` or `..` segment.

A namespace is a lower case DNS label; `ValidateNamespace` checks one on its own.

`examples/api` builds and parses paths, shows what the layout refuses, and serves a group
with `server.APIGroup` that `client` then calls.
