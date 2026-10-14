// Package tofu drives OpenTofu-protocol provider plugins for any provider
// and any resource type, using [github.com/opentofu/provider-client] as its
// transport, and layers a declarative, data-described apply/destroy on top.
//
// [github.com/opentofu/provider-client] models the wire protocol closely: a
// caller builds requests and decodes responses as [cty.Value] against a
// schema it fetched itself, and reproduces the small slice of OpenTofu core
// business logic (proposed new state, destroy plans, ...) needed to drive a
// resource through create, read, update and delete. This package does that
// once, generically, so callers describe resources as plain
// map[string]any configuration instead.
//
// Start a provider, configure it, then call the resource methods for any
// resource type it reports in its schema:
//
//	provider, err := tofu.Start(ctx, "local", "./terraform-provider-local")
//	if err != nil {
//		return err
//	}
//	defer provider.Close()
//
//	if err := provider.Configure(ctx, "1.15.5", nil); err != nil {
//		return err
//	}
//
//	res, err := provider.Create(ctx, "local_file", map[string]any{
//		"content":  "foo!",
//		"filename": "./foo.bar",
//	})
//
// Use a [Registry] to run several providers side by side:
//
//	registry := tofu.NewRegistry()
//	defer registry.Close()
//
//	local, err := registry.Start(ctx, "local", "./terraform-provider-local")
//	random, err := registry.Start(ctx, "random", "./terraform-provider-random")
//
// # Declarative apply
//
// [Apply] and [Destroy] turn a static [Plan] - the provider plugins to
// launch and the resources each one owns, as data - into calls against the
// above: start every provider the plan names, apply every resource in
// declaration order, and close every provider before returning. [Destroy]
// does the same in reverse, deleting instead of creating or updating.
//
//	plan := tofu.Plan{
//		Providers: []tofu.ProviderSpec{
//			{Name: "local", Executable: "./terraform-provider-local"},
//		},
//		Resources: []tofu.ResourceSpec{
//			{Provider: "local", Address: "local_file.foo", ResourceType: "local_file", Config: map[string]any{
//				"content":  "foo!",
//				"filename": "./foo.bar",
//			}},
//		},
//	}
//
//	result, err := tofu.Apply(ctx, plan, nil)
//
// This package resolves no dependency graph across resources and persists
// no state of its own, whether driven one resource at a time or through a
// [Plan]: the prior parameter to [Apply] and [Destroy], and the [Result] an
// [Apply] returns, are how a caller carries resource state between runs. It
// exists to drive provider plugins and, on top of that, a static description
// of providers and resources - not to replace OpenTofu's own dependency
// resolution, state storage or plan/apply review workflow.
package tofu
