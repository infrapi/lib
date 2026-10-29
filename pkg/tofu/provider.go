package tofu

import (
	"context"
	"net/http"

	"github.com/zclconf/go-cty/cty"

	"github.com/opentofu/provider-client/tofuprovider"
	"github.com/opentofu/provider-client/tofuprovider/providerops"
	"github.com/opentofu/provider-client/tofuprovider/providerschema"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// Provider is a running connection to a single OpenTofu-protocol provider
// plugin, with its schema fetched and cached. Create one with [Start],
// configure it once with [Provider.Configure], then call [Provider.Create],
// [Provider.Read], [Provider.Update] and [Provider.Delete] for any resource
// type it reports. Call [Provider.Close] when done.
//
// A Provider's resource-type schemas are fixed at [Start] and never
// refreshed, so it is safe for concurrent use by multiple goroutines as long
// as they operate on independent [Resource] values.
type Provider struct {
	name   string
	client tofuprovider.GRPCPluginProvider
	schema providerschema.ProviderSchema
	caps   providerops.ServerCapabilities
	types  map[string]resourceType
}

// resourceType bundles a managed resource type's schema with the cty.Type
// it implies, computed once at Start so every later call reuses it.
type resourceType struct {
	name   string
	schema providerschema.Schema
	ty     cty.Type
}

// Start launches exe as a child process speaking the OpenTofu provider
// plugin protocol, under the given name, and fetches its schema. The
// returned Provider is unconfigured: call [Provider.Configure] before
// [Provider.Create], [Provider.Read], [Provider.Update] or
// [Provider.Delete].
//
// name identifies the provider only for this package's own error messages
// and for [Registry]; it does not need to match the provider's registry
// address.
func Start(ctx context.Context, name, exe string, args ...string) (*Provider, error) {
	const origin = "tofu.Start"

	client, err := tofuprovider.StartGRPCPlugin(ctx, exe, args...)
	if err != nil {
		return nil, infraerrors.Wrap(err, http.StatusBadGateway, origin, "launch provider %q", name)
	}

	resp, err := client.GetProviderSchema(ctx, &providerops.GetProviderSchemaRequest{})
	if err != nil {
		_ = client.Close()
		return nil, infraerrors.Wrap(err, http.StatusBadGateway, origin, "get schema of provider %q", name)
	}
	if err := diagnosticsError(origin, PhaseSchema, resp.Diagnostics()); err != nil {
		_ = client.Close()
		return nil, err
	}

	types := map[string]resourceType{}
	for rtName, rSchema := range resp.ProviderSchema().ManagedResourceTypeSchemas() {
		ty, err := schemaType(rSchema)
		if err != nil {
			_ = client.Close()
			return nil, infraerrors.Wrap(err, http.StatusBadGateway, origin,
				"resource type %q of provider %q has a schema this client cannot use", rtName, name)
		}
		types[rtName] = resourceType{name: rtName, schema: rSchema, ty: ty}
	}

	return &Provider{
		name:   name,
		client: client,
		schema: resp.ProviderSchema(),
		caps:   resp.ServerCapabilities(),
		types:  types,
	}, nil
}

// Name returns the name Start was called with.
func (p *Provider) Name() string { return p.name }

// ResourceTypes returns the names of the managed resource types this
// provider supports, in no particular order.
func (p *Provider) ResourceTypes() []string {
	names := make([]string, 0, len(p.types))
	for name := range p.types {
		names = append(names, name)
	}
	return names
}

// ResourceSchema returns the schema of a managed resource type, as the
// provider reported it at [Start]: its attributes and nested blocks, which
// of them are required, optional or computed, sensitive or deprecated, and
// their descriptions. It is what a caller needs to describe the resource
// type to its own users, such as in an API contract. Each call returns a
// new copy, which the caller may change.
func (p *Provider) ResourceSchema(resourceType string) (Block, error) {
	origin := "tofu.Provider.ResourceSchema(" + p.name + ")"

	rt, err := p.resourceTypeByName(origin, resourceType)
	if err != nil {
		return Block{}, err
	}
	b, err := blockFrom(rt.schema)
	if err != nil {
		return Block{}, infraerrors.Wrap(err, http.StatusBadGateway, origin,
			"resource type %q of provider %q has a schema this client cannot describe", resourceType, p.name)
	}
	b.Description, _ = rt.schema.DocDescription() // the second value is the text's format
	return b, nil
}

// Configure validates and applies the provider-level configuration. Call it
// once, before any resource method. terraformVersion is reported to the
// provider as the calling Terraform/OpenTofu version; providers generally
// use it only to decide which compatibility workarounds to apply, so "" is
// fine unless a specific provider's documentation says otherwise. config may
// be nil for a provider whose configuration block is entirely optional.
func (p *Provider) Configure(ctx context.Context, terraformVersion string, config map[string]any) error {
	origin := "tofu.Provider.Configure(" + p.name + ")"

	ty, err := schemaType(p.schema.ProviderConfigSchema())
	if err != nil {
		return infraerrors.Wrap(err, http.StatusBadGateway, origin, "provider %q has a config schema this client cannot use", p.name)
	}

	val, err := valueFromGo(ty, config)
	if err != nil {
		return infraerrors.Wrap(err, http.StatusUnprocessableEntity, origin, "invalid configuration for provider %q", p.name)
	}
	dv := providerschema.NewDynamicValue(val, ty)

	validateResp, err := p.client.ValidateProviderConfig(ctx, &providerops.ValidateProviderConfigRequest{Config: dv})
	if err != nil {
		return infraerrors.Wrap(err, http.StatusBadGateway, origin, "validate configuration for provider %q", p.name)
	}
	if err := diagnosticsError(origin, PhaseValidate, validateResp.Diagnostics()); err != nil {
		return err
	}

	configureResp, err := p.client.ConfigureProvider(ctx, &providerops.ConfigureProviderRequest{
		TerraformVersion: terraformVersion,
		Config:           dv,
	})
	if err != nil {
		return infraerrors.Wrap(err, http.StatusBadGateway, origin, "configure provider %q", p.name)
	}
	return diagnosticsError(origin, PhaseConfigure, configureResp.Diagnostics())
}

// Close terminates the provider plugin's child process. It is safe to call
// more than once.
func (p *Provider) Close() error {
	return p.client.Close()
}

// resourceTypeByName looks up a cached resource type schema, or returns an
// error naming what this provider does support.
func (p *Provider) resourceTypeByName(origin, name string) (resourceType, error) {
	rt, ok := p.types[name]
	if !ok {
		return resourceType{}, infraerrors.
			New(http.StatusNotFound, origin, "provider %q has no resource type %q", p.name, name).
			WithSolution("call ResourceTypes to see what this provider supports")
	}
	return rt, nil
}
