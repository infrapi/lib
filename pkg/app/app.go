package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/config"
	"github.com/infrapi/lib/pkg/listener"
	"github.com/infrapi/lib/pkg/server"

	"github.com/infrapi/lib/pkg/app/idempotency"
)

// Spec is what a service is made of. Everything else comes from the
// configuration.
type Spec struct {
	// Name is the default of INFRAPI_APP_NAME, and names what the service
	// owns elsewhere: its ring's key prefix and gossip label, its APISIX
	// objects.
	Name string

	// Service receives the file's service section, decoded with unknown keys
	// refused. A pointer to the service's own configuration struct; nil when
	// it has none.
	Service any

	// LeaderWrites makes the leader of the writer ring the only replica that
	// performs writes: the others relay them to it. Without a cluster, the one
	// replica is its own leader.
	LeaderWrites bool

	// IdempotencyScope says whose an Idempotency-Key is. Nil is
	// [idempotency.DefaultScope], the caller the gateway authenticated.
	IdempotencyScope idempotency.Scope

	// LeaderCheck is one more condition for this replica to lead the writer
	// ring, next to cluster.leader_checks: nil when it may. It runs on the
	// same interval, timeout and threshold; see [cluster.RoleSpec].
	LeaderCheck func(ctx context.Context) error

	// API builds the service once the configuration is loaded, and returns
	// its contract versions with their routes.
	API func(ctx context.Context, rt *Runtime) ([]Version, error)
}

// Version is one major version of the service's contract.
type Version struct {
	// OpenAPI is the contract, served under /-/openapi/ and read by /-/docs.
	// Requests to Routes are validated against it.
	OpenAPI server.OpenAPISpec

	// Prefix is the path every route of the version starts with, which the
	// gateway routes on: "/orders/v1/". Empty derives it from the contract's
	// paths.
	Prefix string

	// Routes registers the version's handlers. They run behind the API's
	// middlewares; their paths are the contract's, in gin's syntax.
	Routes func(r gin.IRouter)
}

// Runtime is what the service gets to build itself.
type Runtime struct {
	// App is the configuration every InfraPI service shares.
	App *config.AppConfig
	// Cluster is this replica's membership, disabled when not configured.
	Cluster *cluster.Cluster
}

// Main runs the service until SIGINT or SIGTERM, reading its configuration
// from the file named by -config or INFRAPI_CONFIG_FILE, if any, and the
// environment. It exits the process: 1 on any error.
func Main(spec Spec) {
	path := flag.String("config", os.Getenv("INFRAPI_CONFIG_FILE"), "configuration file (YAML)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := Run(ctx, spec, *path)
	stop()
	if err != nil {
		slog.Error("exiting", "err", err)
		os.Exit(1)
	}
}

// Run is Main without the flags and the exit: it serves until ctx ends, then
// shuts down.
func Run(ctx context.Context, spec Spec, configFile string) error {
	if spec.Name == "" {
		return errors.New("app: Spec.Name is required")
	}
	if spec.API == nil {
		return errors.New("app: Spec.API is required")
	}

	cfg, err := loadConfig(spec, configFile)
	if err != nil {
		return err
	}
	setupLogging(cfg.app)

	var store idempotency.Store
	if cfg.Database.DSN != "" {
		pg, closeDB, err := openStore(ctx, cfg.Database.DSN)
		if err != nil {
			return err
		}
		defer func() {
			if err := closeDB(); err != nil {
				slog.Warn("closing the idempotency store", "err", err)
			}
		}()
		store = pg
	} else {
		slog.Warn("no database: Idempotency-Key is IGNORED, and a retried write runs twice")
	}

	cl, err := newCluster(spec, cfg)
	if err != nil {
		return fmt.Errorf("app: cluster: %w", err)
	}

	rt := &Runtime{App: cfg.app, Cluster: cl}
	versions, err := spec.API(ctx, rt)
	if err != nil {
		return err
	}

	ready := &readiness{}
	engine, prefixes, err := newEngine(spec, cfg, rt, versions, store, ready)
	if err != nil {
		return err
	}

	socket, err := listener.Listen(cfg.app)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: engine, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", socket.Addr().String(), "versions", len(versions), "idempotency", store != nil)
		errc <- srv.Serve(socket)
	}()

	// Joined once the API listens: the ring then advertises an address that
	// answers. Until then a write is refused as retryable (no leader yet).
	if err := cl.Start(ctx); err != nil {
		return errors.Join(fmt.Errorf("app: cluster: %w", err), srv.Close())
	}
	ready.joined.Store(true)
	if cl.Enabled() {
		slog.Info("joined the cluster", "instance", cl.InstanceID(), "addr", cl.InstanceAddr(), "store", cl.Store())
	}

	if err := startGatewaySync(ctx, cfg, cl, prefixes); err != nil {
		return errors.Join(err, cl.Stop(context.Background()), srv.Close())
	}

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		return errors.Join(err, cl.Stop(context.Background()))
	case err := <-cl.Err():
		slog.Error("cluster failed", "err", err)
	case <-ctx.Done():
	}

	// Stop being routed to before stopping: the gateway notices /-/readyz
	// first. Leave the ring first, so that leadership moves while this
	// replica still finishes the writes it has in hand.
	ready.draining.Store(true)
	if err := cl.Stop(context.Background()); err != nil {
		slog.Warn("leaving the cluster", "err", err)
	}
	time.Sleep(cfg.drainDelay)
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
