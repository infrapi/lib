// Leader mode: of every replica that joined the role, exactly one acts.
//
//	cd examples/cluster/leader && go run .
//
// One replica is trivially its own leader, so what this example really shows is
// the shape of the API. Run ../node in two terminals to watch an election.
package main

import (
	"fmt"
	"log"

	"github.com/infrapi/lib/examples/cluster/internal/demo"
	"github.com/infrapi/lib/pkg/cluster"
)

func main() {
	appConfig, clusterConfig := demo.Config("app.env")

	// The role is declared in Go, not in the dotenv, because the mode decides
	// what the application code does. The dotenv only lists which of the
	// declared roles this replica joins.
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

	fmt.Printf("instance   %s at %s\n", c.InstanceID(), c.InstanceAddr())
	fmt.Printf("store      %s\n", c.Store())
	fmt.Printf("role       %s, mode %s, ring key %q\n\n", role.Name(), role.Mode(), role.Key())

	// ShouldRun is the whole point of the package: one predicate, whose meaning
	// depends on the mode. In leader mode it is true only for the replica that
	// owns the leader token.
	should, err := role.ShouldRun()
	if err != nil {
		log.Fatalf("ShouldRun: %v", err)
	}
	fmt.Printf("should this replica act?  %t\n", should)

	leader, err := role.Leader()
	if err != nil {
		log.Fatalf("Leader: %v", err)
	}
	fmt.Printf("leader                    %s (%s)\n", leader.ID, leader.Addr)
	fmt.Printf("its ring state            %s, %d token(s)\n", leader.State, len(leader.Tokens))

	isLeader, err := role.IsLeader()
	if err != nil {
		log.Fatalf("IsLeader: %v", err)
	}
	fmt.Printf("am I the leader?          %t\n\n", isLeader)

	// Members is the set an action may reason about: eligible replicas only. A
	// replica whose heartbeat has gone stale is deliberately absent.
	members, err := role.Members()
	if err != nil {
		log.Fatalf("Members: %v", err)
	}
	fmt.Printf("eligible members          %d\n", len(members))
	for _, member := range members {
		fmt.Printf("  %-12s %-8s %s\n", member.ID, member.State, member.Addr)
	}

	fmt.Println()
	fmt.Println("A single replica always wins. Run ../node in two terminals to")
	fmt.Println("watch an election, and a failover when the leader is killed.")
}
