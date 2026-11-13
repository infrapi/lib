package server

import (
	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/api"
	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// NamespaceParam is the route parameter APIGroup puts the namespace in.
const NamespaceParam = "namespace"

// APIGroup returns the router group g's resources are served under,
// /apis/{name}.{domain}/{version}/namespaces/:namespace, for the service to
// add its routes to, relative to it:
//
//	vms, err := server.APIGroup(engine, api.Group{Name: "compute", Domain: "infrapi.io", Version: "v1"})
//	vms.GET("/vm", list)               // .../namespaces/:namespace/vm
//	vms.GET("/vm/:id", get)            // .../namespaces/:namespace/vm/:id
//	vms.POST("/vm/:id/reboot", reboot) // .../namespaces/:namespace/vm/:id/reboot
//
// Segments after the namespace alternate a resource or action name and an
// instance id, the layout pkg/auth's RequireAccess authorizes. A request
// whose namespace is not a lower case DNS label is answered 404 before any
// handler of the group runs. Namespace reads it in a handler. An invalid g
// is an error, at start.
func APIGroup(r gin.IRouter, g api.Group) (*gin.RouterGroup, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return r.Group(g.NamespacePath(":"+NamespaceParam), validNamespace), nil
}

// Namespace is the namespace of a request routed through APIGroup.
func Namespace(c *gin.Context) string {
	return c.Param(NamespaceParam)
}

// validNamespace answers 404 for a namespace no resource could live in.
func validNamespace(c *gin.Context) {
	if err := api.ValidateNamespace(Namespace(c)); err != nil {
		AbortError(c, infraerrors.NotFound("%s", err))
		return
	}
	c.Next()
}
