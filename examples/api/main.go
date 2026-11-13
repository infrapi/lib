package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/api"
	infraclient "github.com/infrapi/lib/pkg/client"
	infraserver "github.com/infrapi/lib/pkg/server"
)

// compute is the API every example below builds, parses or serves.
var compute = api.Group{Name: "compute", Domain: "infrapi.io", Version: "v1"}

func main() {
	fmt.Println("=== InfraPI api Package Examples ===")
	fmt.Println()

	// Example 1: A group and the paths it builds
	example1()

	// Example 2: Groups and paths the layout refuses
	example2()

	// Example 3: Taking a request path apart
	example3()

	// Example 4: Server and client agree on the layout
	example4()
}

// Example 1: the URN, the root and the full paths of a group.
func example1() {
	fmt.Println("--- Example 1: Building Paths ---")

	fmt.Printf("URN            : %s\n", compute.URN())
	fmt.Printf("Root           : %s\n", compute.Root())
	fmt.Printf("Route pattern  : %s\n", compute.NamespacePath(":namespace"))

	for _, resource := range []string{"vm", "vm/123", "vm/123/reboot", "vm/web 01"} {
		p, err := compute.Path("core", resource)
		if err != nil {
			log.Fatalf("Path(%q): %v", resource, err)
		}
		fmt.Printf("%-15s: %s\n", resource, p)
	}
	fmt.Println()
}

// Example 2: what Validate and Path refuse, before any request is sent.
func example2() {
	fmt.Println("--- Example 2: Refused Groups and Paths ---")

	for _, g := range []api.Group{
		{Name: "Compute", Domain: "infrapi.io", Version: "v1"}, // upper case
		{Name: "compute", Domain: "infrapi.io", Version: "1"},  // no v
		{Name: "compute", Version: "v1"},                       // no domain
	} {
		fmt.Printf("%-47s → %v\n", fmt.Sprintf("%+v", g), g.Validate())
	}

	for _, c := range []struct{ namespace, resource string }{
		{"Core", "vm"},       // namespace is not a DNS label
		{"core", ""},         // no resource
		{"core", "vm/../db"}, // escapes the namespace
	} {
		_, err := compute.Path(c.namespace, c.resource)
		fmt.Printf("%-47s → %v\n", fmt.Sprintf("Path(%q, %q)", c.namespace, c.resource), err)
	}
	fmt.Println()
}

// Example 3: Parse, what pkg/auth's RequireAccess runs on every request.
func example3() {
	fmt.Println("--- Example 3: Parsing Request Paths ---")

	for _, path := range []string{
		"/apis/compute.infrapi.io/v1/namespaces/core/vm/123/reboot",
		"/apis/storage.infrapi.io/v2beta1/namespaces/edge/volume",
		"/apis/authorization.infrapi.io/v1/check", // cluster-wide: not namespaced
		"/api/v1/widgets", // the old layout
	} {
		p, err := api.Parse(path)
		if err != nil {
			fmt.Printf("%s\n  refused: %v\n", path, err)
			continue
		}
		fmt.Printf("%s\n  api=%s version=%s namespace=%s resource=%s\n", path, p.API, p.Version, p.Namespace, p.Resource)
	}
	fmt.Println()
}

// Example 4: a server mounted with server.APIGroup answers the paths
// pkg/client builds, both from the same api.Group.
func example4() {
	fmt.Println("--- Example 4: Server and Client on the Same Group ---")

	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	vms, err := infraserver.APIGroup(engine, compute)
	if err != nil {
		log.Fatalf("APIGroup: %v", err)
	}
	vms.GET("/vm/:id", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"namespace": infraserver.Namespace(c), "id": c.Param("id")})
	})
	vms.POST("/vm/:id/reboot", func(c *gin.Context) {
		c.JSON(http.StatusAccepted, gin.H{"rebooting": c.Param("id")})
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Listen: %v", err)
	}
	srv := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	client, err := infraclient.NewClient(&infraclient.ClientOptions{
		Server:     "127.0.0.1",
		Protocol:   "http",
		Port:       ln.Addr().(*net.TCPAddr).Port,
		API:        compute,
		Namespace:  "core",
		RetryCount: 1,
	})
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}

	resp, err := client.Get("vm/123", nil)
	if err != nil {
		log.Fatalf("GET: %v", err)
	}
	fmt.Printf("GET  vm/123             → %d %s\n", resp.Code, resp.Body)

	resp, err = client.Post("vm/123/reboot", &infraclient.RequestOptions{Namespace: "edge"})
	if err != nil {
		log.Fatalf("POST: %v", err)
	}
	fmt.Printf("POST vm/123/reboot edge → %d %s\n", resp.Code, resp.Body)

	// A namespace the server would refuse never leaves the client.
	_, err = client.Get("vm/123", &infraclient.RequestOptions{Namespace: "Core"})
	fmt.Printf("GET  vm/123 Core        → %v\n\n", err)
}
