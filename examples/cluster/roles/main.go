// Several roles in one process, the Mimir layout: one key-value layer, one
// gossip mesh, and a ring per role separated by the key it is stored under.
//
//	cd examples/cluster/roles && go run .
//
// Different services can join the same mesh this way, each registering in the
// ring of its own role, and ignoring each other's.
package main

import (
	"errors"
	"fmt"
	"log"

	"github.com/infrapi/lib/examples/cluster/internal/demo"
	"github.com/infrapi/lib/pkg/cluster"
)

func main() {
	appConfig, clusterConfig := demo.Config("app.env")

	// Two roles, two modes. Both are declared here; the dotenv's
	// INFRAPI_CLUSTER_ROLES decides which this replica joins, the way Mimir's
	// -target does.
	c, stop := demo.Start(appConfig, clusterConfig, cluster.Options{
		Roles: []cluster.RoleSpec{
			{Name: "scheduler", Mode: cluster.ModeLeader},
			{Name: "collector", Mode: cluster.ModeBroadcast},

			// Declared but not selected by the dotenv: a binary legitimately
			// knows more roles than a deployment runs.
			{Name: "compactor", Mode: cluster.ModeLeader},
		},
	})
	defer stop()

	fmt.Printf("joined roles, in declaration order: %v\n\n", c.Roles())

	for _, name := range c.Roles() {
		role, err := c.Role(name)
		if err != nil {
			log.Fatalf("Role: %v", err)
		}

		should, err := role.ShouldRun()
		if err != nil {
			log.Fatalf("ShouldRun: %v", err)
		}

		leader := "n/a in this mode"
		if role.Mode() == cluster.ModeLeader {
			if l, err := role.Leader(); err == nil {
				leader = l.ID
			}
		}

		fmt.Printf("%-10s mode %-9s ring key %-12q acting %-5t leader %s\n",
			role.Name(), role.Mode(), role.Key(), should, leader)
	}

	fmt.Println()

	// A declared role the dotenv did not select is reported as such, not
	// silently absent: a service can tell "not my job" from "misconfigured".
	if _, err := c.Role("compactor"); errors.Is(err, cluster.ErrRoleNotJoined) {
		fmt.Printf("compactor  %v\n", err)
	}

	// The reverse is an error at startup: a role named in the dotenv that the
	// code does not declare is always a typo or a bad rollout, and accepting it
	// would mean a scheduler that never schedules with nothing in the logs.
	fmt.Println()
	fmt.Println("Add an undeclared name to INFRAPI_CLUSTER_ROLES and cluster.New")
	fmt.Println("refuses to build, naming both the role and the key.")
}
