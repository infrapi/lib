// Shutdown: what Stop does, and what every query says afterwards.
//
//	cd examples/cluster/shutdown && go run .
//
// A cluster is single use. dskit services cannot be restarted, so a failed or
// finished cluster means the process should exit rather than rebuild it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/infrapi/lib/examples/cluster/internal/demo"
	"github.com/infrapi/lib/pkg/cluster"
)

func main() {
	appConfig, clusterConfig := demo.Config("app.env")

	c, _ := demo.Start(appConfig, clusterConfig, cluster.Options{
		Roles: []cluster.RoleSpec{
			{Name: "scheduler", Mode: cluster.ModeLeader},
		},
	})

	role, err := c.Role("scheduler")
	if err != nil {
		log.Fatalf("Role: %v", err)
	}

	should, err := role.ShouldRun()
	if err != nil {
		log.Fatalf("ShouldRun: %v", err)
	}
	fmt.Printf("before Stop: acting=%t\n\n", should)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Stop leaves every role ring, then the store, in reverse order. It ignores
	// cancellation of its context on purpose: a shutdown is normally triggered
	// because the caller's context was cancelled, and honouring it would
	// abandon the gossip leave and leave this replica in every ring until its
	// heartbeat went stale.
	if err := c.Stop(ctx); err != nil {
		log.Fatalf("Stop: %v", err)
	}
	fmt.Println("Stop: left every ring that had been joined")

	// Idempotent, so a service with more than one shutdown path is safe.
	if err := c.Stop(ctx); err == nil {
		fmt.Println("Stop: a second call is a no-op")
	}

	fmt.Println()

	// Four distinct answers, because a service reacts differently to each.
	for _, probe := range []struct {
		what string
		err  error
	}{
		{"Role(\"scheduler\")", roleErr(c)},
		{"role.ShouldRun()", shouldRunErr(role)},
		{"role.Leader()", leaderErr(role)},
		{"c.Start(ctx)", c.Start(ctx)},
	} {
		fmt.Printf("%-22s %v\n", probe.what, probe.err)
	}

	fmt.Println()
	if _, err := c.Role("scheduler"); errors.Is(err, cluster.ErrStopped) {
		fmt.Println("Every one reports ErrStopped rather than an empty ring, so")
		fmt.Println("application code can tell a torn-down cluster from a")
		fmt.Println("cluster that simply has no leader right now.")
	}

	fmt.Println()
	fmt.Println("The other three answers worth distinguishing: ErrDisabled when")
	fmt.Println("INFRAPI_CLUSTER_ENABLED is off, ErrNotStarted before Start, and")
	fmt.Println("ErrRoleNotJoined for a role this replica does not run.")
}

func roleErr(c *cluster.Cluster) error {
	_, err := c.Role("scheduler")
	return err
}

func shouldRunErr(role *cluster.Role) error {
	_, err := role.ShouldRun()
	return err
}

func leaderErr(role *cluster.Role) error {
	_, err := role.Leader()
	return err
}
