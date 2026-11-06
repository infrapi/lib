package tofu

import (
	"context"
	"net/http"
	"sort"

	"github.com/opentofu/provider-client/tofuprovider/providerops"
	"github.com/opentofu/provider-client/tofuprovider/providerschema"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// DataSourceTypes returns the names of the data sources this provider
// supports, sorted.
func (p *Provider) DataSourceTypes() []string {
	names := make([]string, 0, len(p.dataSources))
	for name := range p.dataSources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DataSourceSchema returns the schema of a data source, as the provider
// reported it at [Start], in the same shape as [Provider.ResourceSchema]:
// its arguments are its required and optional attributes, what it reads
// its computed ones. Each call returns a new copy, which the caller may
// change.
func (p *Provider) DataSourceSchema(dataSource string) (Block, error) {
	origin := "tofu.Provider.DataSourceSchema(" + p.name + ")"

	ds, err := p.dataSourceByName(origin, dataSource)
	if err != nil {
		return Block{}, err
	}
	b, err := blockFrom(ds.schema)
	if err != nil {
		return Block{}, infraerrors.Wrap(err, http.StatusBadGateway, origin,
			"data source %q of provider %q has a schema this client cannot describe", dataSource, p.name)
	}
	b.Description, _ = ds.schema.DocDescription() // the second value is the text's format
	return b, nil
}

// ReadDataSource validates config against the data source's schema, then
// reads it, as a data block is read: nothing is created, and nothing needs
// keeping. config holds its arguments; the result is every attribute the
// provider reports, arguments and what it read, JSON-shaped as
// [Resource.Attributes] are. What it reads is what the provider's
// configuration lets it see.
func (p *Provider) ReadDataSource(ctx context.Context, dataSource string, config map[string]any) (map[string]any, error) {
	origin := "tofu.Provider.ReadDataSource(" + p.name + "." + dataSource + ")"

	ds, err := p.dataSourceByName(origin, dataSource)
	if err != nil {
		return nil, err
	}

	val, err := valueFromGo(ds.ty, config)
	if err != nil {
		return nil, infraerrors.Wrap(err, http.StatusUnprocessableEntity, origin, "invalid configuration for data source %q", dataSource)
	}
	dv := providerschema.NewDynamicValue(val, ds.ty)

	validateResp, err := p.client.ValidateDataResourceConfig(ctx, &providerops.ValidateDataResourceConfigRequest{
		ResourceType: dataSource,
		Config:       dv,
	})
	if err != nil {
		return nil, infraerrors.Wrap(err, http.StatusBadGateway, origin, "validate configuration for data source %q", dataSource)
	}
	if err := diagnosticsError(origin, PhaseValidate, validateResp.Diagnostics()); err != nil {
		return nil, err
	}

	resp, err := p.client.ReadDataResource(ctx, &providerops.ReadDataResourceRequest{
		ResourceType: dataSource,
		Config:       dv,
	})
	if err != nil {
		return nil, infraerrors.Wrap(err, http.StatusBadGateway, origin, "read data source %q", dataSource)
	}
	if err := diagnosticsError(origin, PhaseRead, resp.Diagnostics()); err != nil {
		return nil, err
	}
	if resp.Deferred() != nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, origin,
			"provider deferred reading data source %q", dataSource).
			WithSolution("retry later; the provider does not yet have enough information to answer")
	}

	state, err := resp.State().AsCtyValue(ds.ty)
	if err != nil {
		return nil, infraerrors.Wrap(err, http.StatusBadGateway, origin, "decode data source %q", dataSource)
	}
	attrs, err := valueToGo(state, ds.ty)
	if err != nil {
		return nil, infraerrors.Wrap(err, http.StatusBadGateway, origin, "decode data source %q", dataSource)
	}
	return attrs, nil
}

// dataSourceByName looks up a cached data source schema, or returns an
// error naming what this provider does support.
func (p *Provider) dataSourceByName(origin, name string) (resourceType, error) {
	ds, ok := p.dataSources[name]
	if !ok {
		return resourceType{}, infraerrors.
			New(http.StatusNotFound, origin, "provider %q has no data source %q", p.name, name).
			WithSolution("call DataSourceTypes to see what this provider supports")
	}
	return ds, nil
}
