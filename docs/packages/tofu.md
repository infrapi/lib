---
title: tofu
description: Drives OpenTofu-protocol provider plugins for any resource type and applies a declarative set of providers and resources.
weight: 90
---

# tofu

```go
import "github.com/infrapi/lib/pkg/tofu"
```

Drives OpenTofu-protocol provider plugins — [`terraform-provider-local`](https://registry.terraform.io/providers/hashicorp/local),
any provider from the registry, or a custom one — for any resource type,
using [`github.com/opentofu/provider-client`](https://github.com/opentofu/provider-client)
as its transport, and layers a declarative, data-described apply/destroy on
top.

`provider-client` models the wire protocol closely: a caller builds requests
and decodes responses as `cty.Value` against a schema it fetched itself, and
reproduces the small slice of OpenTofu core business logic (proposed new
state, destroy plans, ...) needed to drive a resource through create, read,
update and delete. This package does that once, generically, so callers
describe resources with plain `map[string]any` configuration instead:

```go
provider, err := tofu.Start(ctx, "local", "./terraform-provider-local")
if err != nil {
	return err
}
defer provider.Close()

if err := provider.Configure(ctx, "1.15.5", nil); err != nil {
	return err
}

res, err := provider.Create(ctx, "local_file", map[string]any{
	"content":  "foo!",
	"filename": "./foo.bar",
})
```

`res` is a [`Resource`](https://pkg.go.dev/github.com/infrapi/lib/pkg/tofu#Resource):
its attributes as a plain Go map, plus the bookkeeping data the provider
needs to see again in `Read`, `Update` or `Delete`. Persisting it between
runs is the caller's job; this package keeps none of its own state.

## Schema and import

`ResourceSchema` returns a resource type's schema as the provider reported
it: attributes and nested blocks, which are required, optional or computed,
sensitive or deprecated, with their descriptions. It is what a caller needs
to describe a resource type to its own users, such as in an API contract.
The schema is plain values (`Block`, `Attribute`, `NestedBlock`), so a
caller can also build one in its tests:

```go
schema, err := provider.ResourceSchema("local_file")
if err != nil {
	return err
}
for name, attr := range schema.Attributes {
	if attr.Usage == tofu.UsageComputed {
		continue // set by the provider, never by the caller
	}
	fmt.Println(name, attr.Type.FriendlyName())
}
```

`Import` brings an existing object under management from its id, as
`terraform import` does: the provider turns the id into a state, which is
then read. A caller that keeps only ids rebuilds a `Resource` this way
before `Update` or `Delete`:

```go
res, err := provider.Import(ctx, "random_string", "s3cret-value")
```

An id the provider cannot find is a `404`.

## Errors

Errors are [`infraerrors.Error`](errors.md) values. When a provider reports
diagnostics, the status says whose fault it is:

| step | status | code |
|---|---|---|
| validating a configuration, planning a change | `422` | `invalid_configuration` |
| applying, reading, importing, deleting, configuring | `502` | `upstream_error` |

The diagnostics themselves, and the step, are in a wrapped
[`DiagnosticsError`](https://pkg.go.dev/github.com/infrapi/lib/pkg/tofu#DiagnosticsError):

```go
var de *tofu.DiagnosticsError
if errors.As(err, &de) {
	log.Printf("%s failed: %v", de.Phase, de.Diagnostics)
}
```

## Replacement

Some changes cannot be made in place: the resource has to be deleted and
created again. When the plan shows it, `Update` applies nothing and returns
a `409` wrapping `ErrReplaceRequired`; `Apply` deletes and creates the
resource instead.

```go
res, err := provider.Update(ctx, res, config)
if errors.Is(err, tofu.ErrReplaceRequired) {
	// delete, then create
}
```

`provider-client` does not expose the plan's "requires replacement" list,
so this package infers it from the plan making the resource's `id`
unknown. Most resource types plan a replacement that way; one that keeps its
prior `id` and computed values while still requiring replacement (a plugin
framework resource using `UseStateForUnknown`, such as `random_string`) is
not detected, and is updated in place with state that does not match its
configuration.

## Planning without applying

`Plan` is `terraform plan` for one resource: what `Update` (or `Create`,
without a state) would do, with nothing applied. Give it the resource as it
is now, from `Read` or `Import`, and the configuration you want it to have:

```go
current, err := provider.Import(ctx, "grafana_folder", id)
plan, err := provider.Plan(ctx, current, map[string]any{"title": "Ops"})

switch plan.Action {
case tofu.ActionNone:    // it matches: no drift
case tofu.ActionUpdate:  // it differs: Update would change it in place
case tofu.ActionReplace: // it differs in a way that needs deleting and creating it again
case tofu.ActionCreate:  // no state was given: Create would make this
}
for _, c := range plan.Changes {
	fmt.Println(c.Path, c.Before, "→", c.After) // "title Renamed by hand → Ops"
}
```

| Field | Holds |
|-------|-------|
| `Changes` | one `Change` per attribute that would change, by path: `title`, `settings.theme`, `permission[0].role`, `labels["env"]`; a set changes as a whole |
| `Planned` | the planned state, as `Resource.Attributes`, with what only applying knows left null |
| `Unknown` | the paths of those values: `id`, `url`… |

A `Change` of a sensitive attribute has `Sensitive` set and neither value; a
value only applying knows has `Unknown` set and no `After`. Null and empty
(`""`, `[]`) differ, as in Terraform. The diff hides nothing else: leaving
out what a caller does not show, or what a provider cannot read back, is
the caller's choice, by path.

Since `Changes` compares the resource as it is now with the plan, it
includes what was changed outside of this package: planning the
configuration a resource was created with shows its drift. The replacement
is inferred as for `Update` (below), with the same limit.

## Multiple providers

A [`Registry`](https://pkg.go.dev/github.com/infrapi/lib/pkg/tofu#Registry)
keeps a set of named, running providers, so a caller can drive resources
across several plugins at once and close every one of them together:

```go
registry := tofu.NewRegistry()
defer registry.Close()

local, err := registry.Start(ctx, "local", "./terraform-provider-local")
random, err := registry.Start(ctx, "random", "./terraform-provider-random")
```

## Declarative apply

A [`Plan`](https://pkg.go.dev/github.com/infrapi/lib/pkg/tofu#Plan) lists the
provider plugins to launch and the resources each one owns, as data:

```go
plan := tofu.Plan{
	Providers: []tofu.ProviderSpec{
		{Name: "local", Executable: "./terraform-provider-local"},
	},
	Resources: []tofu.ResourceSpec{
		{
			Provider: "local", Address: "local_file.foo", ResourceType: "local_file",
			Config: map[string]any{"content": "foo!", "filename": "./foo.bar"},
		},
	},
}

result, err := tofu.Apply(ctx, plan, nil)
```

`Apply` starts every provider the plan names, applies every resource in
declaration order — creating an address absent from `prior`, updating one
present in it, replacing it when the update requires — and closes every
provider before returning. `Destroy` does
the same in reverse, deleting instead of creating or updating:

```go
if err := tofu.Destroy(ctx, plan, result.Resources); err != nil {
	return err
}
```

## Scope

This package stops at single-resource operations and a thin declarative
layer on top of them:

- No state persistence. A `Resource` is a snapshot, and `Result.Resources`
  from `Apply` is how a caller carries state between runs; saving and
  loading it is up to the caller.
- No dependency graph across resources. `Plan.Resources` is processed in the
  order it is listed (reverse order for `Destroy`); order it so anything a
  resource's config depends on comes earlier in the slice.
- No plan preview or approval step: `Apply` and `Destroy` both act
  immediately.
- Replacement is inferred, not read from the plan: see
  [Replacement](#replacement) for the resource types it misses.

`examples/tofu` applies a plan with two resources, re-applies it to show the
second run updates in place, then destroys it.
