package tofu

import (
	"context"
	"net/http"

	"github.com/zclconf/go-cty/cty"

	"github.com/opentofu/provider-client/tofuprovider/providerops"
	"github.com/opentofu/provider-client/tofuprovider/providerschema"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// Resource is a snapshot of one managed resource instance: its attribute
// values, plus the bookkeeping data a provider needs to operate on the same
// object again. Callers that need it to survive past the current process
// are responsible for persisting it; this package does not.
type Resource struct {
	// ResourceType is the provider's name for this resource's type, e.g.
	// "local_file".
	ResourceType string

	// Attributes holds the resource's attribute values, keyed by schema
	// attribute name. Values are JSON-shaped: string, float64, bool,
	// []any, map[string]any or nil.
	Attributes map[string]any

	// Internal is an opaque blob the provider returned alongside
	// Attributes. It must be carried forward, unmodified, into the next
	// call this package makes for the same resource instance.
	Internal []byte

	// SchemaVersion is the provider schema version Attributes conforms
	// to, as reported when this Resource was produced.
	SchemaVersion int64
}

// Create validates config against resourceType's schema, then plans and
// applies bringing a new instance of it into existence.
func (p *Provider) Create(ctx context.Context, resourceType string, config map[string]any) (Resource, error) {
	origin := "tofu.Provider.Create(" + p.name + "." + resourceType + ")"

	rt, err := p.resourceTypeByName(origin, resourceType)
	if err != nil {
		return Resource{}, err
	}

	return p.planApply(ctx, origin, rt, cty.NullVal(rt.ty), nil, config)
}

// Read asks the provider for the current state of an existing resource
// instance, to detect any change made outside of this package.
func (p *Provider) Read(ctx context.Context, state Resource) (Resource, error) {
	origin := "tofu.Provider.Read(" + p.name + "." + state.ResourceType + ")"

	rt, err := p.resourceTypeByName(origin, state.ResourceType)
	if err != nil {
		return Resource{}, err
	}

	curVal, err := valueFromGo(rt.ty, state.Attributes)
	if err != nil {
		return Resource{}, infraerrors.Wrap(err, http.StatusUnprocessableEntity, origin, "invalid current state for resource type %q", state.ResourceType)
	}

	resp, err := p.client.ReadManagedResource(ctx, &providerops.ReadManagedResourceRequest{
		ResourceType:     state.ResourceType,
		CurrentState:     providerschema.NewDynamicValue(curVal, rt.ty),
		ProviderInternal: state.Internal,
	})
	if err != nil {
		return Resource{}, infraerrors.Wrap(err, http.StatusBadGateway, origin, "read resource type %q", state.ResourceType)
	}
	if err := diagnosticsError(origin, resp.Diagnostics()); err != nil {
		return Resource{}, err
	}
	if resp.Deferred() != nil {
		return Resource{}, infraerrors.New(http.StatusServiceUnavailable, origin,
			"provider deferred reading resource type %q", state.ResourceType).
			WithSolution("retry later; the provider does not yet have enough information to answer")
	}

	newVal, err := resp.NewState().AsCtyValue(rt.ty)
	if err != nil {
		return Resource{}, infraerrors.Wrap(err, http.StatusBadGateway, origin, "decode state for resource type %q", state.ResourceType)
	}
	attrs, err := valueToGo(newVal, rt.ty)
	if err != nil {
		return Resource{}, infraerrors.Wrap(err, http.StatusBadGateway, origin, "decode state for resource type %q", state.ResourceType)
	}

	return Resource{
		ResourceType:  state.ResourceType,
		Attributes:    attrs,
		Internal:      resp.ProviderInternal(),
		SchemaVersion: rt.schema.SchemaVersion(),
	}, nil
}

// Update validates config against state's resource type schema, then plans
// and applies changing the existing instance described by state to match
// it.
//
// Some resource types require replacement (destroy then re-create) rather
// than an in-place update for a change to certain attributes. The
// underlying provider-client library does not currently expose the plan
// response's "requires replacement" information for this package to detect,
// so Update always attempts an in-place change; a provider that needs
// replacement for the given config change typically responds with
// incomplete state, which this method reports as an error rather than
// returning silently-wrong data. Call Delete then Create instead of Update
// for a resource type known to require replacement for the attributes
// being changed.
func (p *Provider) Update(ctx context.Context, state Resource, config map[string]any) (Resource, error) {
	origin := "tofu.Provider.Update(" + p.name + "." + state.ResourceType + ")"

	rt, err := p.resourceTypeByName(origin, state.ResourceType)
	if err != nil {
		return Resource{}, err
	}

	priorVal, err := valueFromGo(rt.ty, state.Attributes)
	if err != nil {
		return Resource{}, infraerrors.Wrap(err, http.StatusUnprocessableEntity, origin, "invalid current state for resource type %q", state.ResourceType)
	}

	return p.planApply(ctx, origin, rt, priorVal, state.Internal, config)
}

// Delete plans and applies destroying the existing instance described by
// state.
func (p *Provider) Delete(ctx context.Context, state Resource) error {
	origin := "tofu.Provider.Delete(" + p.name + "." + state.ResourceType + ")"

	rt, err := p.resourceTypeByName(origin, state.ResourceType)
	if err != nil {
		return err
	}

	priorVal, err := valueFromGo(rt.ty, state.Attributes)
	if err != nil {
		return infraerrors.Wrap(err, http.StatusUnprocessableEntity, origin, "invalid current state for resource type %q", state.ResourceType)
	}
	priorDV := providerschema.NewDynamicValue(priorVal, rt.ty)
	nullDV := providerschema.NewDynamicValue(cty.NullVal(rt.ty), rt.ty)

	// A provider that cannot plan its own destroy expects the caller to
	// build the "destroy" plan itself: a null planned state, reusing
	// whatever internal blob it already returned. See
	// [providerops.ServerCapabilities.CanPlanDestroy].
	plannedDV := nullDV
	plannedInternal := state.Internal

	if p.caps.CanPlanDestroy() {
		planResp, err := p.client.PlanManagedResourceChange(ctx, &providerops.PlanManagedResourceChangeRequest{
			ResourceType:          state.ResourceType,
			PriorState:            priorDV,
			PriorProviderInternal: state.Internal,
			Config:                nullDV,
			ProposedNewState:      nullDV,
		})
		if err != nil {
			return infraerrors.Wrap(err, http.StatusBadGateway, origin, "plan delete of resource type %q", state.ResourceType)
		}
		if err := diagnosticsError(origin, planResp.Diagnostics()); err != nil {
			return err
		}

		plannedVal, err := planResp.PlannedNewState().AsCtyValue(rt.ty)
		if err != nil {
			return infraerrors.Wrap(err, http.StatusBadGateway, origin, "decode delete plan for resource type %q", state.ResourceType)
		}
		plannedDV = providerschema.NewDynamicValue(plannedVal, rt.ty)
		plannedInternal = planResp.PlannedProviderInternal()
	}

	applyResp, err := p.client.ApplyManagedResourceChange(ctx, &providerops.ApplyManagedResourceChangeRequest{
		ResourceType:            state.ResourceType,
		PriorState:              priorDV,
		Config:                  nullDV,
		PlannedNewState:         plannedDV,
		PlannedProviderInternal: plannedInternal,
	})
	if err != nil {
		return infraerrors.Wrap(err, http.StatusBadGateway, origin, "apply delete of resource type %q", state.ResourceType)
	}
	return diagnosticsError(origin, applyResp.Diagnostics())
}

// planApply runs the validate, plan, apply sequence shared by Create and
// Update: only the prior state and internal blob differ between bringing a
// new instance into existence and changing an existing one.
func (p *Provider) planApply(ctx context.Context, origin string, rt resourceType, priorVal cty.Value, priorInternal []byte, config map[string]any) (Resource, error) {
	priorDV := providerschema.NewDynamicValue(priorVal, rt.ty)

	configVal, err := valueFromGo(rt.ty, config)
	if err != nil {
		return Resource{}, infraerrors.Wrap(err, http.StatusUnprocessableEntity, origin, "invalid configuration for resource type %q", rt.name)
	}
	configDV := providerschema.NewDynamicValue(configVal, rt.ty)

	validateResp, err := p.client.ValidateManagedResourceConfig(ctx, &providerops.ValidateManagedResourceConfigRequest{
		ResourceType: rt.name,
		Config:       configDV,
	})
	if err != nil {
		return Resource{}, infraerrors.Wrap(err, http.StatusBadGateway, origin, "validate configuration for resource type %q", rt.name)
	}
	if err := diagnosticsError(origin, validateResp.Diagnostics()); err != nil {
		return Resource{}, err
	}

	proposed := proposeNewState(rt.schema, rt.ty, priorVal, configVal)
	proposedDV := providerschema.NewDynamicValue(proposed, rt.ty)

	planResp, err := p.client.PlanManagedResourceChange(ctx, &providerops.PlanManagedResourceChangeRequest{
		ResourceType:          rt.name,
		PriorState:            priorDV,
		PriorProviderInternal: priorInternal,
		Config:                configDV,
		ProposedNewState:      proposedDV,
	})
	if err != nil {
		return Resource{}, infraerrors.Wrap(err, http.StatusBadGateway, origin, "plan resource type %q", rt.name)
	}
	if err := diagnosticsError(origin, planResp.Diagnostics()); err != nil {
		return Resource{}, err
	}

	plannedVal, err := planResp.PlannedNewState().AsCtyValue(rt.ty)
	if err != nil {
		return Resource{}, infraerrors.Wrap(err, http.StatusBadGateway, origin, "decode plan for resource type %q", rt.name)
	}

	applyResp, err := p.client.ApplyManagedResourceChange(ctx, &providerops.ApplyManagedResourceChangeRequest{
		ResourceType:            rt.name,
		PriorState:              priorDV,
		Config:                  configDV,
		PlannedNewState:         providerschema.NewDynamicValue(plannedVal, rt.ty),
		PlannedProviderInternal: planResp.PlannedProviderInternal(),
	})
	if err != nil {
		return Resource{}, infraerrors.Wrap(err, http.StatusBadGateway, origin, "apply resource type %q", rt.name)
	}
	if err := diagnosticsError(origin, applyResp.Diagnostics()); err != nil {
		return Resource{}, err
	}

	newVal, err := applyResp.PlannedNewState().AsCtyValue(rt.ty)
	if err != nil {
		return Resource{}, infraerrors.Wrap(err, http.StatusBadGateway, origin, "decode new state for resource type %q", rt.name)
	}
	attrs, err := valueToGo(newVal, rt.ty)
	if err != nil {
		return Resource{}, infraerrors.Wrap(err, http.StatusBadGateway, origin, "decode new state for resource type %q", rt.name)
	}

	return Resource{
		ResourceType:  rt.name,
		Attributes:    attrs,
		Internal:      applyResp.ProviderInternal(),
		SchemaVersion: rt.schema.SchemaVersion(),
	}, nil
}
