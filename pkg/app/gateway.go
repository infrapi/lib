package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"

	"github.com/infrapi/lib/pkg/cluster"
	infragateway "github.com/infrapi/lib/pkg/gateway"

	"github.com/infrapi/lib/pkg/app/internal/gatewaysync"
	"github.com/infrapi/lib/pkg/app/internal/relay"
)

// startGatewaySync keeps APISIX in line with the ring, from the leader only,
// every gateway.interval until ctx ends. A failed sync is logged and retried
// at the next tick; the API keeps serving either way.
func startGatewaySync(ctx context.Context, cfg *loaded, cl *cluster.Cluster, prefixes []string) error {
	g := cfg.Gateway
	if g.AdminURL == "" {
		return nil
	}
	if !cl.Enabled() {
		return fmt.Errorf("app: gateway needs the cluster (cluster.enabled), whose members are its upstream nodes")
	}
	if g.AdminKey == "" {
		return fmt.Errorf("app: gateway.admin_key (or INFRAPI_GATEWAY_ADMIN_KEY) is not set")
	}
	u, err := url.Parse(g.AdminURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("app: gateway.admin_url %q is not a URL", g.AdminURL)
	}
	opts := &infragateway.ClientOptions{Server: u.Hostname(), Protocol: u.Scheme, APIKey: g.AdminKey}
	if p := u.Port(); p != "" {
		if opts.Port, err = strconv.Atoi(p); err != nil {
			return fmt.Errorf("app: gateway.admin_url %q: %w", g.AdminURL, err)
		}
	}
	admin, err := infragateway.NewClient(opts)
	if err != nil {
		return fmt.Errorf("app: gateway: %w", err)
	}
	role, err := cl.Role(writerRole)
	if err != nil {
		return err
	}

	syncer := gatewaysync.New(admin, relay.Cluster{C: cl, Role: writerRole}, gatewaysync.Config{
		Name: g.Name, Prefixes: prefixes, ReadyPath: ReadyPath,
		RateCount: g.RateLimit.Count, RateWindow: g.RateLimit.Window, MaxBodyBytes: g.MaxBodyBytes,
		Timeout: infragateway.Timeout{Connect: 2, Send: 30, Read: 120},
	})
	go func() {
		// Run calls Sync on the leader only, and logs what fails.
		if err := role.Run(ctx, g.Interval, syncer.Sync); err != nil && ctx.Err() == nil {
			slog.Error("gateway sync stopped", "err", err)
		}
	}()
	slog.Info("gateway sync", "admin_url", g.AdminURL, "interval", g.Interval.String(), "name", g.Name)
	return nil
}
