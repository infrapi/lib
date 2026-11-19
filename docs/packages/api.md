---
title: api
description: The URL layout every InfraPI service exposes its resources under, shared by server, client and auth.
weight: 10
---

# api

```go
import "github.com/infrapi/lib/pkg/api"
```

The URL layout every InfraPI service exposes its resources under. [`server`](server.md)
mounts it, [`client`](client.md) calls it and [`auth`](auth.md) authorizes it, all through
this package, so the three never disagree on a path:

```text
/apis/{group}/{version}/namespaces/{namespace}/{resource}[/{id}[/{action}[/{id}...]]]
/apis/{group}/{version}/{resource}[/{id}[/{action}[/{id}...]]]
```

A resource lives in a namespace, or in none: cluster-scoped, the platform's rather than
a tenant's (`/apis/compute.infrapi.io/v1/flavors/large`). A path is namespaced when
`namespaces` follows the version, then a namespace and a resource; any other is
cluster-scoped, its resource everything after the version.

A group is named in full, like a Kubernetes API group — `compute.infrapi.io` — and
that name is the one it is registered under in Kayra. Only
Kayra has a domain of its own, `app.api_domain`, for its built-in groups (`system`,
`catalog`, `authorization`), which [`auth`](auth.md) discovers.

```text
/apis/compute.infrapi.io/v1/namespaces/core/vm
/apis/compute.infrapi.io/v1/namespaces/core/vm/123
/apis/compute.infrapi.io/v1/namespaces/core/vm/123/reboot
```

After the namespace, or the version for a cluster-scoped resource, segments alternate a
resource or action name and an instance id.
`auth` relies on that to tell `vm` (list) from `vm/123` (get), and to grant
`vm/123/reboot` as resource `vm/reboot`.

The package has no dependency, so a client importing it does not pull in gin.

## Group

```go
compute := api.Group{Name: "compute.infrapi.io", Version: "v1"}
```

| Field | Form | Example |
|-------|------|---------|
| `Name` | lower case DNS name, two labels or more | `compute.infrapi.io`, `vault.infrapi.io` |
| `Version` | `v{n}`, optionally `alpha{n}` or `beta{n}` | `v1`, `v2beta1` |

| Method | Returns |
|--------|---------|
| `Validate()` | the first field out of form |
| `Root()` | `/apis/compute.infrapi.io/v1` |
| `NamespacePath(ns)` | `/apis/compute.infrapi.io/v1/namespaces/{ns}`, `ns` as given, so `":namespace"` makes a route pattern |
| `Path(ns, resource)` | the full path, each segment of `resource` escaped, or an error |

## Parse

```go
p, err := api.Parse(r.URL.Path)
// p.API       "compute.infrapi.io"
// p.Version   "v1"
// p.Namespace "core"
// p.Resource  "vm/123/reboot"
```

`Parse` takes an unescaped path, `net/http`'s `URL.Path`, and refuses anything outside the
layout: another prefix, a missing `namespaces` segment, a group that is not a full name, an
invalid version or namespace, an empty resource, or a `.` or `..` segment.

A namespace is a lower case DNS label; `ValidateNamespace` checks one on its own.

`examples/api` builds and parses paths, shows what the layout refuses, and serves a group
with `server.APIGroup` that `client` then calls.
