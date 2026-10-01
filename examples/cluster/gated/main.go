// Gating a periodic action on the role predicate: Run calls the action only
// while this replica is the one that should act.
//
//	cd examples/cluster/gated && go run .
//
// This is the ergonomic payoff of the package, and the piece that needs the
// most care: read the warning below before relying on it.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/infrapi/lib/examples/cluster/internal/demo"
	"github.com/infrapi/lib/pkg/cluster"
)

func main() {
	appConfig, clusterConfig := demo.Config("app.env")

	c, stop := demo.Start(appConfig, clusterConfig, cluster.Options{
		Roles: []cluster.RoleSpec{
			{Name: "scheduler", Mode: cluster.ModeLeader},
		},
	})
	defer stop()

	role, err := c.Role("scheduler")
	if err != nil {
		log.Fatalf("Role: %v", err)
	}

	// Run returns only when the context is done, so a service gives it its own
	// lifetime context and lets it own a goroutine. Here a short deadline keeps
	// the example finite.
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()

	runs := 0
	err = role.Run(ctx, 250*time.Millisecond, func(runCtx context.Context) error {
		runs++
		fmt.Printf("tick %d: %s is acting\n", runs, c.InstanceID())

		// The action must honour its context: Run cancels it the moment this
		// replica stops being eligible. Real work goes here, and it must also
		// be idempotent — see the warning printed below.
		select {
		case <-runCtx.Done():
			return runCtx.Err()
		case <-time.After(20 * time.Millisecond):
			return nil
		}
	})

	fmt.Printf("\nRun returned %v after %d runs\n", err, runs)
	fmt.Println()
	fmt.Println("Two things Run does not do, and cannot:")
	fmt.Println("  - It is not a distributed lock. The ring has no quorum and no")
	fmt.Println("    lease, so during a partition two replicas can each believe")
	fmt.Println("    they are the leader for up to HEARTBEAT_TIMEOUT.")
	fmt.Println("  - Its cancellation is cooperative. An action that ignores its")
	fmt.Println("    context keeps running after it has lost the token.")
	fmt.Println()
	fmt.Println("So the action must be idempotent, or fenced by whatever it")
	fmt.Println("writes to: a compare-and-swap, a conditional write, a unique")
	fmt.Println("constraint. Nothing in this package can do that for you.")
}
