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
present in it — and closes every provider before returning. `Destroy` does
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
- `Update` cannot detect that a provider requires replacement (destroy then
  re-create) for a given attribute change, because the underlying
  `provider-client` library does not currently expose that information. A
  provider that needs replacement typically returns incomplete state for an
  in-place update, which this package reports as an error rather than
  returning silently-wrong data; call `Delete` then `Create` instead for a
  resource type known to require replacement for the attributes being
  changed.

`examples/tofu` applies a plan with two resources, re-applies it to show the
second run updates in place, then destroys it.
