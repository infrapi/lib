package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var compute = Group{Name: "compute", Domain: "infrapi.io", Version: "v1"}

func TestGroup(t *testing.T) {
	assert.Equal(t, "compute.infrapi.io", compute.URN())
	assert.Equal(t, "/apis/compute.infrapi.io/v1", compute.Root())
	assert.Equal(t, "/apis/compute.infrapi.io/v1/namespaces/:namespace", compute.NamespacePath(":namespace"))
	require.NoError(t, compute.Validate())
	require.NoError(t, Group{Name: "a", Domain: "b", Version: "v2beta1"}.Validate())
}

func TestGroup_Validate(t *testing.T) {
	for name, g := range map[string]Group{
		"empty name":      {Domain: "infrapi.io", Version: "v1"},
		"upper name":      {Name: "Compute", Domain: "infrapi.io", Version: "v1"},
		"dotted name":     {Name: "a.b", Domain: "infrapi.io", Version: "v1"},
		"empty domain":    {Name: "compute", Version: "v1"},
		"bad domain":      {Name: "compute", Domain: "gi_services.io", Version: "v1"},
		"trailing dot":    {Name: "compute", Domain: "infrapi.io.", Version: "v1"},
		"empty version":   {Name: "compute", Domain: "infrapi.io"},
		"bare number":     {Name: "compute", Domain: "infrapi.io", Version: "1"},
		"v0":              {Name: "compute", Domain: "infrapi.io", Version: "v0"},
		"unknown level":   {Name: "compute", Domain: "infrapi.io", Version: "v1rc1"},
		"level no number": {Name: "compute", Domain: "infrapi.io", Version: "v1beta"},
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
		"empty namespace":   func() (string, error) { return compute.Path("", "vm") },
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
	assert.Equal(t, Path{API: compute.URN(), Version: "v1", Namespace: "core", Resource: "vm"}, p)

	for name, path := range map[string]string{
		"no prefix":         "/api/compute.infrapi.io/v1/namespaces/core/vm",
		"too short":         "/apis/compute.infrapi.io/v1/namespaces/core",
		"no namespaces":     "/apis/compute.infrapi.io/v1/projects/core/vm",
		"no domain":         "/apis/compute/v1/namespaces/core/vm",
		"bad version":       "/apis/compute.infrapi.io/1/namespaces/core/vm",
		"bad namespace":     "/apis/compute.infrapi.io/v1/namespaces/Core/vm",
		"empty resource":    "/apis/compute.infrapi.io/v1/namespaces/core/",
		"dot dot":           "/apis/compute.infrapi.io/v1/namespaces/core/vm/..",
		"cluster-wide path": "/apis/authorization.infrapi.io/v1/check",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(path)
			assert.Error(t, err)
		})
	}
}
