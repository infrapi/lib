// Zone-aware gossip across two data centres, with bridges.
//
//	cd examples/cluster/zones
//	INFRAPI_CONFIG_DOTENV_FILE=dc1-bridge.env go run .
//	INFRAPI_CONFIG_DOTENV_FILE=dc1-member.env go run .
//	INFRAPI_CONFIG_DOTENV_FILE=dc2-bridge.env go run .
//	INFRAPI_CONFIG_DOTENV_FILE=dc2-member.env go run .
//
// Four terminals, two zones, one cluster. Gossip stays inside a zone: a member
// only ever gossips with peers in its own, and the two bridges are what carry
// state between dc1 and dc2.
//
// The point is what happens when that link goes: kill dc2-bridge and watch dc2
// drift into a cluster of its own, electing its own leader, while every replica
// still reports itself healthy. See the README.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/examples/cluster/internal/demo"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/listener"
	infraserver "github.com/infrapi/lib/pkg/server"
)

func main() {
	appConfig, clusterConfig := demo.Config("dc1-bridge.env")

	c, stop := demo.Start(appConfig, clusterConfig, cluster.Options{
		Roles: []cluster.RoleSpec{
			{Name: "scheduler", Mode: cluster.ModeLeader},
		},
	})

	port, err := c.GossipPort()
	if err != nil {
		log.Fatalf("GossipPort: %v", err)
	}

	bind := "0.0.0.0"
	if len(clusterConfig.GossipBindAddrs) > 0 {
		bind = clusterConfig.GossipBindAddrs[0]
	}
	fmt.Printf("%s up in zone %s as a %s, gossiping on %s:%d\n",
		c.InstanceID(), clusterConfig.Zone, clusterConfig.GossipZoneRole, bind, port)

	gin.SetMode(gin.ReleaseMode)
	engine, err := infraserver.NewServerGin(appConfig)
	if err != nil {
		log.Fatalf("NewServerGin: %v", err)
	}
	if err := c.RegisterGin(engine, cluster.HandlerOptions{}); err != nil {
		log.Fatalf("RegisterGin: %v", err)
	}

	socket, err := listener.Listen(appConfig)
	if err != nil {
		log.Fatalf("Listen: %v", err)
	}
	srv := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(socket) }()
	fmt.Printf("%s status pages on http://%s%s\n\n", c.InstanceID(), socket.Addr(), cluster.StatusPath)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-signals:
			fmt.Printf("\n%s leaving the cluster...\n", c.InstanceID())

			shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutCancel()
			_ = srv.Shutdown(shutCtx)

			stop()
			fmt.Printf("%s left cleanly\n", c.InstanceID())
			return

		case err := <-c.Err():
			log.Fatalf("a cluster service failed: %v", err)

		case <-ticker.C:
			fmt.Printf("%s | %-11s %-7s | %s\n",
				time.Now().Format("15:04:05"),
				clusterConfig.Zone, clusterConfig.GossipZoneRole,
				describe(c, "scheduler"))
		}
	}
}

// describe renders what this replica believes about the role, which is the
// whole point: four replicas that agree are one cluster, and four replicas that
// disagree are two.
func describe(c *cluster.Cluster, name string) string {
	role, err := c.Role(name)
	if err != nil {
		return fmt.Sprintf("%s: %v", name, err)
	}

	members, err := role.Members()
	if err != nil {
		return fmt.Sprintf("%s: %v", name, err)
	}

	should, err := role.ShouldRun()
	if err != nil {
		return fmt.Sprintf("members=%d acting=? (%v)", len(members), err)
	}

	verdict := "passive"
	if should {
		verdict = "ACTING"
	}

	leader := "none"
	if l, err := role.Leader(); err == nil {
		leader = l.ID
	}

	// The zones this replica can currently see. A member that has lost its
	// bridge sees only its own, which is the symptom to watch for.
	zones := map[string]int{}
	for _, member := range members {
		zones[member.Zone]++
	}

	return fmt.Sprintf("members=%d %v leader=%-11s -> %s", len(members), zones, leader, verdict)
}
