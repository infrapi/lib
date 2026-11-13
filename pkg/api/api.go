// Package api is the URL layout every InfraPI service exposes its resources
// under, shared by pkg/server, which mounts it, pkg/client, which calls it,
// and pkg/auth, which authorizes it:
//
//	/apis/{name}.{domain}/{version}/namespaces/{namespace}/{resource}[/{id}[/{action}[/{id}...]]]
//
// for instance /apis/compute.infrapi.io/v1/namespaces/core/vm/123/reboot.
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
	// label is a DNS label, RFC 1123, lower case: an API name, each part of
	// its domain, a namespace.
	label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	// version is v1, v2, ... with an optional alpha or beta level, v1beta2.
	version = regexp.MustCompile(`^v[1-9][0-9]*((alpha|beta)[1-9][0-9]*)?$`)
)

// Group is an API: what a service exposes, named {Name}.{Domain} and served
// at Version.
type Group struct {
	// Name of the API, a DNS label: compute.
	Name string
	// Domain it belongs to, a DNS name: infrapi.io.
	Domain string
	// Version served: v1, v2beta1.
	Version string
}

// URN is the full name of the group, {Name}.{Domain}: compute.infrapi.io.
func (g Group) URN() string {
	return g.Name + "." + g.Domain
}

// Validate reports the first field that does not fit the layout.
func (g Group) Validate() error {
	if !label.MatchString(g.Name) {
		return fmt.Errorf("api: name %q is not a lower case DNS label", g.Name)
	}
	if err := validateDomain(g.Domain); err != nil {
		return err
	}
	if !version.MatchString(g.Version) {
		return fmt.Errorf("api: version %q is not like v1 or v2beta1", g.Version)
	}
	return nil
}

// Root is the path of the group: /apis/compute.infrapi.io/v1.
func (g Group) Root() string {
	return Prefix + "/" + g.URN() + "/" + g.Version
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
	// API is the group's URN: compute.infrapi.io.
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
	urn, ver, ns := parts[0], parts[1], parts[3]

	name, domain, _ := strings.Cut(urn, ".")
	if err := (Group{Name: name, Domain: domain, Version: ver}).Validate(); err != nil {
		return Path{}, err
	}
	if err := ValidateNamespace(ns); err != nil {
		return Path{}, err
	}
	segments, err := splitResource(parts[4])
	if err != nil {
		return Path{}, err
	}
	return Path{API: urn, Version: ver, Namespace: ns, Resource: strings.Join(segments, "/")}, nil
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

// validateDomain checks domain is dot separated DNS labels.
func validateDomain(domain string) error {
	if domain == "" {
		return fmt.Errorf("api: empty domain")
	}
	for _, l := range strings.Split(domain, ".") {
		if !label.MatchString(l) {
			return fmt.Errorf("api: domain %q is not a lower case DNS name", domain)
		}
	}
	return nil
}
