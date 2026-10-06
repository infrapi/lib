// One long-lived replica, over real gossip. Two terminals make a cluster.
//
//	cd examples/cluster/node
//	INFRAPI_CONFIG_DOTENV_FILE=node1.env go run .
//	INFRAPI_CONFIG_DOTENV_FILE=node2.env go run .
//
// This is the only example that shows what the package is for: an election
// between two replicas, the two modes parting company, and a failover when the
// leader is interrupted. Everything else runs on the in-memory store, where a
// single replica always wins.
package main

import (
	"context"
	"errors"
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
	appConfig, clusterConfig := demo.Config("node1.env")

	c, stop := demo.Start(appConfig, clusterConfig, cluster.Options{
		Roles: []cluster.RoleSpec{
			{Name: "scheduler", Mode: cluster.ModeLeader},
			{Name: "collector", Mode: cluster.ModeBroadcast},
		},
	})

	port, err := c.GossipPort()
	if err != nil {
		log.Fatalf("GossipPort: %v", err)
	}
	fmt.Printf("%s up: gossip on port %d, ring address %s\n", c.InstanceID(), port, c.InstanceAddr())

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
	go func() {
		if err := srv.Serve(socket); !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Serve: %v", err)
		}
	}()

	fmt.Printf("%s status pages:\n", c.InstanceID())
	for _, path := range []string{cluster.StatusPath, cluster.StatusPath + "/ring/scheduler", cluster.MemberlistPath} {
		fmt.Printf("  http://%s%s\n", socket.Addr(), path)
	}
	fmt.Println()

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
			if err := srv.Shutdown(shutCtx); err != nil {
				log.Printf("Shutdown: %v", err)
			}

			// demo.Start's stop function applies the configured stop budget,
			// which has to cover the gossip leave plus the broadcast drain.
			stop()
			fmt.Printf("%s left cleanly\n", c.InstanceID())
			return

		// A value here means a dskit service failed for good. The cluster
		// cannot be restarted, so the honest response is to exit.
		case err := <-c.Err():
			log.Fatalf("a cluster service failed: %v", err)

		case <-ticker.C:
			fmt.Printf("%s | %s | %s\n", time.Now().Format("15:04:05"),
				describe(c, "scheduler"), describe(c, "collector"))
		}
	}
}

// describe renders one role's view in a single line, so that two terminals side
// by side show the election and the failover.
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
		return fmt.Sprintf("%s: members=%d acting=? (%v)", name, len(members), err)
	}

	verdict := "passive"
	if should {
		verdict = "ACTING"
	}

	// A leader role counts a LEAVING replica as a member, which is what keeps a
	// rolling restart from changing leader at every step. A broadcast role does
	// not: a draining replica must stop working. Interrupt the leader and watch
	// the two member counts diverge.
	if role.Mode() == cluster.ModeLeader {
		leader := "none"
		if l, err := role.Leader(); err == nil {
			leader = l.ID
		}
		return fmt.Sprintf("%s(leader): members=%d leader=%s -> %s", name, len(members), leader, verdict)
	}

	return fmt.Sprintf("%s(broadcast): members=%d -> %s", name, len(members), verdict)
}
