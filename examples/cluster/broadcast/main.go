// Broadcast mode: every healthy replica of the role acts, rather than one.
//
//	cd examples/cluster/broadcast && go run .
//
// Use it when the work is already partitioned by something else, or when the
// action is a local one every replica must perform: reloading a cache,
// refreshing a certificate on disk, re-reading configuration.
package main

import (
	"fmt"
	"log"

	"github.com/infrapi/lib/examples/cluster/internal/demo"
	"github.com/infrapi/lib/pkg/cluster"
)

func main() {
	appConfig, clusterConfig := demo.Config("app.env")

	c, stop := demo.Start(appConfig, clusterConfig, cluster.Options{
		Roles: []cluster.RoleSpec{
			{Name: "collector", Mode: cluster.ModeBroadcast},
		},
	})
	defer stop()

	role, err := c.Role("collector")
	if err != nil {
		log.Fatalf("Role: %v", err)
	}

	should, err := role.ShouldRun()
	if err != nil {
		log.Fatalf("ShouldRun: %v", err)
	}
	fmt.Printf("role                      %s, mode %s\n", role.Name(), role.Mode())
	fmt.Printf("should this replica act?  %t\n\n", should)

	// There is no leader in this mode, and the package says so rather than
	// nominating one. A service that asks has a bug, and gets told.
	if _, err := role.Leader(); err != nil {
		fmt.Printf("Leader()   %v\n", err)
	}
	if _, err := role.IsLeader(); err != nil {
		fmt.Printf("IsLeader() %v\n", err)
	}

	fmt.Println()
	fmt.Println("The difference from leader mode shows up on shutdown: a replica")
	fmt.Println("that is draining leaves the eligible set at once and stops")
	fmt.Println("working, where a leader keeps its token until its heartbeat")
	fmt.Println("goes stale. Watch both in ../node.")
}
