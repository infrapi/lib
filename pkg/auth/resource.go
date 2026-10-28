package auth

import (
	"fmt"
	"net/http"
	"strings"
)

// ParseResource splits rawResource — everything after a request's
// /stacks/:stack/ segment (e.g. gin's "*resource" wildcard param, which
// includes the leading "/": "/vm" or "/vm/123/reboot") into the Resource
// an RBAC rule matches and whether the path names a specific instance of
// it.
//
// Segments alternate name/id/name/id/...: a 0-indexed even position is
// always a resource-or-action name, an odd position is always an
// instance id. Resource joins every even-positioned segment with "/",
// dropping every id — "vm" for "/vm" or "/vm/123", "vm/reboot" for
// "/vm/123/reboot" (a sub-resource action's name is just part of
// Resource; the id in between is irrelevant to what the rule grants).
// hasID reports whether the path ends on an id (an even total segment
// count) rather than a name — this is what tells "list" (no id) apart
// from "get" (has one) for the same GET method in VerbFor.
func ParseResource(rawResource string) (resource string, hasID bool, err error) {
	trimmed := strings.Trim(rawResource, "/")
	if trimmed == "" {
		return "", false, fmt.Errorf("auth: empty resource")
	}

	segments := strings.Split(trimmed, "/")
	names := make([]string, 0, (len(segments)+1)/2)
	for i, seg := range segments {
		if i%2 == 0 {
			names = append(names, seg)
		}
	}
	return strings.Join(names, "/"), len(segments)%2 == 0, nil
}

// VerbFor maps method (an HTTP method, any case) and hasID (see
// ParseResource) to the RBAC verb a Rule grants. Only GET splits by
// hasID ("list" vs "get"); every other method maps 1:1 regardless — an
// action like PATCH .../vm/:id/reboot is exactly as unambiguous as PATCH
// .../vm/:id, there's only one verb PATCH could mean either way.
func VerbFor(method string, hasID bool) (string, error) {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		if hasID {
			return "get", nil
		}
		return "list", nil
	case http.MethodPost:
		return "create", nil
	case http.MethodPut:
		return "update", nil
	case http.MethodPatch:
		return "patch", nil
	case http.MethodDelete:
		return "delete", nil
	default:
		return "", fmt.Errorf("auth: method %q has no RBAC verb", method)
	}
}
