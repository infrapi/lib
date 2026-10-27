package tofu

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// Result is the outcome of a successful [Apply]: the resulting state of
// every resource in the plan, keyed by [ResourceSpec.Address].
type Result struct {
	Resources map[string]Resource
}

// Apply starts every provider in plan.Providers and configures it, then
// walks plan.Resources in order: an address present in prior is updated
// from its recorded state via [Provider.Update], any other address is
// created via [Provider.Create]. An update that requires replacing the
// resource ([ErrReplaceRequired]) deletes it, then creates it again: the
// old object is gone before the new one exists, as a resource whose name
// must be unique needs. Every provider Apply started is closed before it
// returns, whether or not it succeeds.
//
// prior may be nil, meaning every resource in the plan is created.
func Apply(ctx context.Context, plan Plan, prior map[string]Resource) (Result, error) {
	const origin = "tofu.Apply"

	registry := NewRegistry()
	defer func() { _ = registry.Close() }()

	if err := startAndConfigure(ctx, registry, plan.Providers); err != nil {
		return Result{}, err
	}

	result := Result{Resources: make(map[string]Resource, len(plan.Resources))}
	for _, rs := range plan.Resources {
		provider, ok := registry.Provider(rs.Provider)
		if !ok {
			return Result{}, infraerrors.New(http.StatusUnprocessableEntity, origin,
				"resource %q references provider %q, which plan.Providers does not declare", rs.Address, rs.Provider)
		}

		var (
			state Resource
			err   error
		)
		if existing, ok := prior[rs.Address]; ok {
			state, err = provider.Update(ctx, existing, rs.Config)
			if errors.Is(err, ErrReplaceRequired) {
				if err = provider.Delete(ctx, existing); err == nil {
					state, err = provider.Create(ctx, rs.ResourceType, rs.Config)
				}
			}
		} else {
			state, err = provider.Create(ctx, rs.ResourceType, rs.Config)
		}
		if err != nil {
			return Result{}, fmt.Errorf("resource %q: %w", rs.Address, err)
		}
		result.Resources[rs.Address] = state
	}

	return result, nil
}

// Destroy deletes every resource recorded in prior, in the reverse of the
// order its address appears in plan.Resources, using the providers
// described in plan.Providers. Addresses present in prior but not in
// plan.Resources are deleted last, since this package has no ordering
// information for them; give them a ResourceSpec in plan.Resources so
// deletion order is predictable when a whole Plan is being torn down.
func Destroy(ctx context.Context, plan Plan, prior map[string]Resource) error {
	const origin = "tofu.Destroy"

	registry := NewRegistry()
	defer func() { _ = registry.Close() }()

	if err := startAndConfigure(ctx, registry, plan.Providers); err != nil {
		return err
	}

	providerOf := make(map[string]string, len(plan.Resources))
	order := make([]string, 0, len(prior))
	seen := make(map[string]bool, len(prior))
	for i := len(plan.Resources) - 1; i >= 0; i-- {
		rs := plan.Resources[i]
		providerOf[rs.Address] = rs.Provider
		if _, ok := prior[rs.Address]; ok && !seen[rs.Address] {
			order = append(order, rs.Address)
			seen[rs.Address] = true
		}
	}
	for address := range prior {
		if !seen[address] {
			order = append(order, address)
		}
	}

	for _, address := range order {
		providerName, ok := providerOf[address]
		if !ok {
			return infraerrors.New(http.StatusUnprocessableEntity, origin,
				"resource %q is not declared in plan.Resources, so its owning provider is unknown", address)
		}
		provider, ok := registry.Provider(providerName)
		if !ok {
			return infraerrors.New(http.StatusUnprocessableEntity, origin,
				"resource %q references provider %q, which plan.Providers does not declare", address, providerName)
		}
		if err := provider.Delete(ctx, prior[address]); err != nil {
			return fmt.Errorf("resource %q: %w", address, err)
		}
	}

	return nil
}

// startAndConfigure launches and configures every provider a Plan
// describes, the shared first step of Apply and Destroy.
func startAndConfigure(ctx context.Context, registry *Registry, providers []ProviderSpec) error {
	for _, ps := range providers {
		provider, err := registry.Start(ctx, ps.Name, ps.Executable, ps.Args...)
		if err != nil {
			return err
		}
		if err := provider.Configure(ctx, ps.TerraformVersion, ps.Config); err != nil {
			return err
		}
	}
	return nil
}
