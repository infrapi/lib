// The status pages, mounted on the engine the server package builds, next to
// the /-/metadata and /-/metrics every InfraPI service already serves.
//
//	cd examples/cluster/pages && go run .
//
// The example fetches each page and prints what came back. Uncomment the block
// at the end to leave the server up and look at them in a browser.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/examples/cluster/internal/demo"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/listener"
	infraserver "github.com/infrapi/lib/pkg/server"
)

func main() {
	appConfig, clusterConfig := demo.Config("app.env")

	// Port 0 lets the kernel pick, so the example runs anywhere.
	appConfig.AppListenAddress = "127.0.0.1:0"

	c, stop := demo.Start(appConfig, clusterConfig, cluster.Options{
		Roles: []cluster.RoleSpec{
			{Name: "scheduler", Mode: cluster.ModeLeader},
			{Name: "collector", Mode: cluster.ModeBroadcast},
		},
	})
	defer stop()

	gin.SetMode(gin.ReleaseMode)
	engine, err := infraserver.NewServerGin(appConfig)
	if err != nil {
		log.Fatalf("NewServerGin: %v", err)
	}

	// Everything lives behind /-/cluster. HandlerOptions.PathPrefix relocates
	// the set; ForgetEnabled additionally mounts the ring page's POST route,
	// which evicts a replica over an unauthenticated endpoint and is therefore
	// off by default.
	if err := c.RegisterGin(engine, cluster.HandlerOptions{}); err != nil {
		log.Fatalf("RegisterGin: %v", err)
	}

	socket, err := listener.Listen(appConfig)
	if err != nil {
		log.Fatalf("Listen: %v", err)
	}
	srv := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(socket) }()

	base := fmt.Sprintf("http://%s", socket.Addr())
	fmt.Printf("serving on %s\n\n", base)

	// Each page renders HTML by default and returns its own model as JSON when
	// the request asks for it, which is the negotiation dskit's pages perform.
	for _, probe := range []struct {
		path   string
		accept string
	}{
		{cluster.StatusPath, ""},
		{cluster.StatusPath, "application/json"},
		{cluster.StatusPath + "/ring/scheduler", ""},
		{cluster.StatusPath + "/ring/scheduler", "application/json"},
		{cluster.StatusPath + "/ring/collector", ""},
		{cluster.StatusPath + "/ring/nope", ""},
		{cluster.MemberlistPath, ""},
		{"/-/metadata", ""},
	} {
		status, kind, size := fetch(base+probe.path, probe.accept)
		label := probe.path
		if probe.accept != "" {
			label += "  (Accept: " + probe.accept + ")"
		}
		fmt.Printf("%-56s %d  %-10s %5d bytes\n", label, status, kind, size)
	}

	fmt.Println()
	fmt.Println("/-/cluster/ring/nope is 404 because this replica does not run")
	fmt.Println("that role; /-/cluster/memberlist is 404 because this example")
	fmt.Println("uses the in-memory store, and that page is dskit's own view of")
	fmt.Println("the gossip layer. Run ../node to see it.")

	// To look at the pages yourself, replace the shutdown below with a block:
	//
	//	fmt.Println("browse", base+cluster.StatusPath)
	//	select {}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("shutdown: %v", err)
	}
}

// fetch reports the status, the kind of body and its size, which is all this
// example needs to show.
func fetch(url, accept string) (int, string, int) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		log.Fatalf("request %s: %v", url, err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("read %s: %v", url, err)
	}

	kind := "html"
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		kind = "json"
	}

	return resp.StatusCode, kind, len(body)
}
