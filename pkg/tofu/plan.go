package tofu

// ProviderSpec describes how to launch and configure one named provider
// plugin, for use by the [ResourceSpec] values that reference it.
type ProviderSpec struct {
	// Name identifies this provider within a Plan. ResourceSpec.Provider
	// refers to it by this name; it does not need to match the provider's
	// registry address.
	Name string

	// Executable is the path to the provider plugin binary, and Args are
	// any arguments it needs, exactly as passed to [Start].
	Executable string
	Args       []string

	// TerraformVersion is reported to the provider as the calling
	// Terraform/OpenTofu version. Leave it empty unless a specific
	// provider's documentation asks for a particular value.
	TerraformVersion string

	// Config is the provider-level configuration block. Leave it nil for
	// a provider whose configuration is entirely optional.
	Config map[string]any
}

// ResourceSpec declares one managed resource instance to reconcile.
type ResourceSpec struct {
	// Provider is the Name of the ProviderSpec that manages this
	// resource.
	Provider string

	// Address uniquely identifies this resource within a Plan and its
	// Result, independent of provider or resource type. Choose something
	// stable across runs, such as "local_file.foo"; Apply and Destroy use
	// it to match a resource against its prior state.
	Address string

	// ResourceType is the provider's name for this resource's type, e.g.
	// "local_file".
	ResourceType string

	// Config is the resource's configuration.
	Config map[string]any
}

// Plan is a declarative description of a set of provider plugins and the
// resources they own.
//
// [Apply] and [Destroy] process Resources in the order it lists them (in
// reverse, for Destroy): this package resolves no dependency graph of its
// own, so a Plan listing a resource before one it depends on will process
// them in the wrong order. Order Resources so that anything a resource's
// config depends on appears earlier in the slice.
type Plan struct {
	Providers []ProviderSpec
	Resources []ResourceSpec
}
