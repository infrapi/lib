package app

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	// Roles are the service's own rings, for work of its own: a periodic job
	// on one replica (cluster.ModeLeader, with Role.Run), an action on every
	// one (cluster.ModeBroadcast). cluster.roles chooses which of them a
	// replica joins, all by default; rt.Cluster.Role reaches them. "writer"
	// is the library's own ring, joined when something needs a leader.
	Roles []cluster.RoleSpec

	// IdempotencyScope says whose an Idempotency-Key is: the same key under
	// two scopes names two different requests. Nil is the caller the gateway
	// authenticated (X-Consumer-Username); a service whose keys belong to
	// something narrower returns it, e.g. c.Param("tenant").
	IdempotencyScope func(c *Context) string

	// API builds the service once the configuration is loaded, and returns
	// its contract versions with their routes. The cluster is not joined yet:
	// work on its roles belongs in Background.
	API func(ctx context.Context, rt *Runtime) ([]Version, error)

	// Background, if set, runs once the replica serves and has joined the
	// cluster: the place for work on the service's Roles, such as
	//
	//	purger, err := rt.Cluster.Role("purger")
	//	...
	//	return purger.Run(ctx, time.Hour, purge)
	//
	// Its context ends when the service starts shutting down, before the
	// replica leaves the cluster; the shutdown waits for it to return. An
	// error stops the service; returning early without one does not.
	Background func(ctx context.Context, rt *Runtime) error
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

	// Routes registers the version's handlers, behind the API's middlewares,
	// on paths written as in the contract.
	Routes func(r Router)
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
	for _, r := range spec.Roles {
		if r.Name == writerRole {
			return fmt.Errorf("app: Spec.Roles: %q is the library's own ring", writerRole)
		}
	}

	cfg, err := loadConfig(spec, configFile)
	if err != nil {
		return err
	}
	setupLogging(cfg.app)

	var store idempotency.Store
	var db *sql.DB
	if cfg.Database.DSN != "" {
		pg, opened, closeDB, err := openStore(ctx, cfg.Database.DSN)
		if err != nil {
			return err
		}
		defer func() {
			if err := closeDB(); err != nil {
				slog.Warn("closing the idempotency store", "err", err)
			}
		}()
		store, db = pg, opened
	} else {
		slog.Warn("no database: Idempotency-Key is IGNORED, and a retried write runs twice")
	}

	cl, err := newCluster(cfg)
	if err != nil {
		return fmt.Errorf("app: %w", err)
	}

	rt := &Runtime{App: cfg.app, Cluster: cl}
	versions, err := spec.API(ctx, rt)
	if err != nil {
		return err
	}

	ready := &readiness{}
	engine, prefixes, err := newEngine(ctx, spec, cfg, rt, versions, store, db, ready)
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
		return errors.Join(fmt.Errorf("app: %w", err), srv.Close())
	}
	ready.joined.Store(true)
	if cl.Enabled() {
		slog.Info("joined the cluster", "instance", cl.InstanceID(), "addr", cl.InstanceAddr(), "store", cl.Store())
	}

	if err := startGatewaySync(ctx, cfg, cl, prefixes); err != nil {
		return errors.Join(err, cl.Stop(context.Background()), srv.Close())
	}

	bgCtx, stopBackground := context.WithCancel(ctx)
	defer stopBackground()
	var bgDone chan error // nil when there is nothing to wait for
	if spec.Background != nil {
		bgDone = make(chan error, 1)
		go func() { bgDone <- spec.Background(bgCtx, rt) }()
	}
	// endBackground stops the background work and waits for it to return.
	endBackground := func() error {
		stopBackground()
		if bgDone == nil {
			return nil
		}
		err := <-bgDone
		bgDone = nil
		return backgroundErr(err)
	}

	var failed error
wait:
	for {
		select {
		case err := <-errc:
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			return errors.Join(err, endBackground(), cl.Stop(context.Background()))
		case err := <-cl.Err():
			slog.Error("cluster failed", "err", err)
			break wait
		case err := <-bgDone: // a nil channel: never, without a background
			bgDone = nil
			if failed = backgroundErr(err); failed != nil {
				slog.Error("background failed", "err", err)
				break wait
			}
		case <-ctx.Done():
			break wait
		}
	}

	// Stop being routed to before stopping: the gateway notices /-/readyz
	// first. The background work ends, then the replica leaves the ring, so
	// that leadership moves while it still finishes the writes it has in hand.
	ready.draining.Store(true)
	failed = errors.Join(failed, endBackground())
	if err := cl.Stop(context.Background()); err != nil {
		slog.Warn("leaving the cluster", "err", err)
	}
	time.Sleep(cfg.drainDelay)
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return errors.Join(failed, srv.Shutdown(shutdown))
}

// backgroundErr is what a background's return means for the service: nothing
// when it returned early or because it was asked to stop.
func backgroundErr(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("app: background: %w", err)
}
