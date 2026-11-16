// Package api is the URL layout every InfraPI service exposes its resources
// under, shared by pkg/server, which mounts it, pkg/client, which calls it,
// and pkg/auth, which authorizes it:
//
//	/apis/{group}/{version}/namespaces/{namespace}/{resource}[/{id}[/{action}[/{id}...]]]
//
// for instance /apis/compute.infrapi.io/v1/namespaces/core/vm/123/reboot. A
// group is named in full, like a Kubernetes API group: compute.infrapi.io.
// Only Kayra has a domain of its own, for its built-in
// groups (system, catalog, authorization), which clients discover.
// After the namespace, segments alternate a resource or action name and an
// instance id. The package has no dependency, so a client does not pull in
// the server's.
package api

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Prefix starts every API path.
const Prefix = "/apis"

// namespacesSegment sits between the version and the namespace.
const namespacesSegment = "namespaces"

var (
	// label is a DNS label, RFC 1123, lower case: each part of a group's
	// name, a namespace.
	label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	// version is v1, v2, ... with an optional alpha or beta level, v1beta2.
	version = regexp.MustCompile(`^v[1-9][0-9]*((alpha|beta)[1-9][0-9]*)?$`)
)

// Group is an API: what a service exposes, named in full and served at
// Version.
type Group struct {
	// Name of the group, a lower case DNS name of two labels or more, the
	// name the API is registered under in Kayra: compute.infrapi.io.
	Name string
	// Version served: v1, v2beta1.
	Version string
}

// Validate reports the first field that does not fit the layout.
func (g Group) Validate() error {
	if err := ValidateGroupName(g.Name); err != nil {
		return err
	}
	if !version.MatchString(g.Version) {
		return fmt.Errorf("api: version %q is not like v1 or v2beta1", g.Version)
	}
	return nil
}

// Root is the path of the group: /apis/compute.infrapi.io/v1.
func (g Group) Root() string {
	return Prefix + "/" + g.Name + "/" + g.Version
}

// NamespacePath is the path namespace's resources sit under:
// /apis/compute.infrapi.io/v1/namespaces/core. namespace is used as
// given, so that a router pattern such as ":namespace" goes through.
func (g Group) NamespacePath(namespace string) string {
	return g.Root() + "/" + namespacesSegment + "/" + namespace
}

// Path is the path of resource in namespace, each segment of resource
// escaped: Path("core", "vm/123") is
// /apis/compute.infrapi.io/v1/namespaces/core/vm/123.
func (g Group) Path(namespace, resource string) (string, error) {
	if err := g.Validate(); err != nil {
		return "", err
	}
	if err := ValidateNamespace(namespace); err != nil {
		return "", err
	}
	segments, err := splitResource(resource)
	if err != nil {
		return "", err
	}
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return g.NamespacePath(namespace) + "/" + strings.Join(segments, "/"), nil
}

// Path is an API path taken apart.
type Path struct {
	// API is the group's name: compute.infrapi.io.
	API string
	// Version of the group: v1.
	Version string
	// Namespace the resource lives in.
	Namespace string
	// Resource is everything after the namespace, without leading or
	// trailing slash: vm, vm/123, vm/123/reboot.
	Resource string
}

// Parse takes apart an unescaped request path, such as net/http's
// URL.Path, laid out as the package describes.
func Parse(p string) (Path, error) {
	rest, ok := strings.CutPrefix(p, Prefix+"/")
	if !ok {
		return Path{}, fmt.Errorf("api: path %q does not start with %s/", p, Prefix)
	}
	parts := strings.SplitN(rest, "/", 5)
	if len(parts) < 5 || parts[2] != namespacesSegment {
		return Path{}, fmt.Errorf("api: path %q is not %s/{api}/{version}/namespaces/{namespace}/{resource}", p, Prefix)
	}
	group, ver, ns := parts[0], parts[1], parts[3]

	if err := (Group{Name: group, Version: ver}).Validate(); err != nil {
		return Path{}, err
	}
	if err := ValidateNamespace(ns); err != nil {
		return Path{}, err
	}
	segments, err := splitResource(parts[4])
	if err != nil {
		return Path{}, err
	}
	return Path{API: group, Version: ver, Namespace: ns, Resource: strings.Join(segments, "/")}, nil
}

// ValidateNamespace reports whether namespace is a lower case DNS label, the
// form every namespace takes.
func ValidateNamespace(namespace string) error {
	if !label.MatchString(namespace) {
		return fmt.Errorf("api: namespace %q is not a lower case DNS label", namespace)
	}
	return nil
}

// splitResource splits resource on "/", ignoring a leading or trailing one,
// and refuses an empty, "." or ".." segment.
func splitResource(resource string) ([]string, error) {
	trimmed := strings.Trim(resource, "/")
	if trimmed == "" {
		return nil, fmt.Errorf("api: empty resource")
	}
	segments := strings.Split(trimmed, "/")
	for _, s := range segments {
		if s == "" || s == "." || s == ".." {
			return nil, fmt.Errorf("api: resource %q has an empty, . or .. segment", resource)
		}
	}
	return segments, nil
}

// ValidateGroupName reports whether name is a group's full name: a lower
// case DNS name of two labels or more, compute.infrapi.io.
func ValidateGroupName(name string) error {
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return fmt.Errorf("api: group %q is not a full name, like compute.infrapi.io", name)
	}
	for _, l := range labels {
		if !label.MatchString(l) {
			return fmt.Errorf("api: group %q is not a lower case DNS name, like compute.infrapi.io", name)
		}
	}
	return nil
}
