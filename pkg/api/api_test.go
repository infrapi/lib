package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var compute = Group{Name: "compute.infrapi.io", Version: "v1"}

func TestGroup(t *testing.T) {
	assert.Equal(t, "/apis/compute.infrapi.io/v1", compute.Root())
	assert.Equal(t, "/apis/compute.infrapi.io/v1/namespaces/:namespace", compute.NamespacePath(":namespace"))
	require.NoError(t, compute.Validate())
	require.NoError(t, Group{Name: "a.b", Version: "v2beta1"}.Validate())
	require.NoError(t, Group{Name: "vault.infrapi.io", Version: "v1"}.Validate())
}

func TestGroup_Validate(t *testing.T) {
	for name, g := range map[string]Group{
		"empty name":      {Version: "v1"},
		"single label":    {Name: "compute", Version: "v1"},
		"upper name":      {Name: "Compute.infrapi.io", Version: "v1"},
		"bad label":       {Name: "compute.gi_services.io", Version: "v1"},
		"trailing dot":    {Name: "compute.infrapi.io.", Version: "v1"},
		"leading dot":     {Name: ".infrapi.io", Version: "v1"},
		"empty version":   {Name: "compute.infrapi.io"},
		"bare number":     {Name: "compute.infrapi.io", Version: "1"},
		"v0":              {Name: "compute.infrapi.io", Version: "v0"},
		"unknown level":   {Name: "compute.infrapi.io", Version: "v1rc1"},
		"level no number": {Name: "compute.infrapi.io", Version: "v1beta"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, g.Validate())
		})
	}
}

func TestGroup_Path(t *testing.T) {
	for resource, want := range map[string]string{
		"vm":            "/apis/compute.infrapi.io/v1/namespaces/core/vm",
		"/vm/123/":      "/apis/compute.infrapi.io/v1/namespaces/core/vm/123",
		"vm/123/reboot": "/apis/compute.infrapi.io/v1/namespaces/core/vm/123/reboot",
		"vm/a b?c":      "/apis/compute.infrapi.io/v1/namespaces/core/vm/a%20b%3Fc",
	} {
		got, err := compute.Path("core", resource)
		require.NoError(t, err, resource)
		assert.Equal(t, want, got, resource)
	}

	for name, call := range map[string]func() (string, error){
		"invalid group":     func() (string, error) { return Group{}.Path("core", "vm") },
		"invalid namespace": func() (string, error) { return compute.Path("Core", "vm") },
		"empty resource":    func() (string, error) { return compute.Path("core", "/") },
		"empty segment":     func() (string, error) { return compute.Path("core", "vm//reboot") },
		"dot dot":           func() (string, error) { return compute.Path("core", "vm/../secret") },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := call()
			assert.Error(t, err)
		})
	}
}

func TestParse(t *testing.T) {
	p, err := Parse("/apis/compute.infrapi.io/v1/namespaces/core/vm/123/reboot/")
	require.NoError(t, err)
	assert.Equal(t, Path{API: "compute.infrapi.io", Version: "v1", Namespace: "core", Resource: "vm/123/reboot"}, p)

	// What Group.Path builds, Parse reads back once unescaped.
	built, err := compute.Path("core", "vm")
	require.NoError(t, err)
	p, err = Parse(built)
	require.NoError(t, err)
	assert.Equal(t, Path{API: compute.Name, Version: "v1", Namespace: "core", Resource: "vm"}, p)

	// cluster-scoped: no namespaces after the version, or no resource after
	// the namespace
	for path, want := range map[string]Path{
		"/apis/compute.infrapi.io/v1/flavors/large":         {API: "compute.infrapi.io", Version: "v1", Resource: "flavors/large"},
		"/apis/authorization.infrapi.io/v1/check":           {API: "authorization.infrapi.io", Version: "v1", Resource: "check"},
		"/apis/system.infrapi.io/v1/namespaces":             {API: "system.infrapi.io", Version: "v1", Resource: "namespaces"},
		"/apis/system.infrapi.io/v1/namespaces/core":        {API: "system.infrapi.io", Version: "v1", Resource: "namespaces/core"},
		"/apis/compute.infrapi.io/v1/projects/core/vm":      {API: "compute.infrapi.io", Version: "v1", Resource: "projects/core/vm"},
		"/apis/compute.infrapi.io/v1/namespaces/core/vm/12": {API: "compute.infrapi.io", Version: "v1", Namespace: "core", Resource: "vm/12"},
	} {
		got, err := Parse(path)
		require.NoError(t, err, path)
		assert.Equal(t, want, got, path)
	}
	cluster, err := compute.Path("", "flavors/large")
	require.NoError(t, err)
	assert.Equal(t, "/apis/compute.infrapi.io/v1/flavors/large", cluster)

	for name, path := range map[string]string{
		"no prefix":      "/api/compute.infrapi.io/v1/namespaces/core/vm",
		"too short":      "/apis/compute.infrapi.io/v1",
		"single label":   "/apis/compute/v1/namespaces/core/vm",
		"bad version":    "/apis/compute.infrapi.io/1/namespaces/core/vm",
		"bad namespace":  "/apis/compute.infrapi.io/v1/namespaces/Core/vm",
		"empty resource": "/apis/compute.infrapi.io/v1/",
		"dot dot":        "/apis/compute.infrapi.io/v1/namespaces/core/vm/..",
		"cluster dot":    "/apis/compute.infrapi.io/v1/flavors/..",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(path)
			assert.Error(t, err)
		})
	}
}
