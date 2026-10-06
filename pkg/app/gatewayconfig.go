package app

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	infragateway "github.com/infrapi/lib/pkg/gateway"

	"github.com/infrapi/lib/pkg/app/internal/gatewaysync"
)

// syncConfig is the gateway section resolved over gatewaysync.DefaultConfig,
// for the contracts' prefixes.
func (g *GatewayConfig) syncConfig(prefixes []string) (gatewaysync.Config, error) {
	cfg := gatewaysync.DefaultConfig(g.Name, ReadyPath)
	cfg.Prefixes = prefixes
	named := func(s string) string { return strings.ReplaceAll(s, "{name}", g.Name) }

	if g.Labels != nil {
		cfg.Labels = g.Labels
	}
	if g.Read.Upstream != "" {
		cfg.ReadUpstream = named(g.Read.Upstream)
	}
	if g.Write.Upstream != "" {
		cfg.WriteUpstream = named(g.Write.Upstream)
	}
	if g.Read.Methods != nil {
		cfg.ReadMethods = upper(g.Read.Methods)
	}
	if g.Write.Methods != nil {
		cfg.WriteMethods = upper(g.Write.Methods)
	}
	if g.Service != "" {
		cfg.Service = named(g.Service)
	}
	if g.RouteID != "" {
		cfg.RouteID = named(g.RouteID)
	}

	if err := g.Upstream.apply(&cfg.Upstream); err != nil {
		return cfg, err
	}

	// The shortcuts first, then the plugins named in full over them.
	limit := cfg.Plugins["limit-count"].(map[string]any)
	if g.RateLimit.Count > 0 {
		limit["count"] = g.RateLimit.Count
	}
	if g.RateLimit.Window > 0 {
		limit["time_window"] = g.RateLimit.Window
	}
	if g.MaxBodyBytes > 0 {
		cfg.Plugins["client-control"] = map[string]any{"max_body_size": g.MaxBodyBytes}
	}
	for name, plugin := range g.Plugins {
		if plugin == nil {
			delete(cfg.Plugins, name)
			continue
		}
		cfg.Plugins[name] = plugin
	}

	if g.Docs.Enabled != nil && !*g.Docs.Enabled {
		cfg.DocsRoute = ""
	} else {
		if g.Docs.Route != "" {
			cfg.DocsRoute = named(g.Docs.Route)
		}
		if g.Docs.URIs != nil {
			cfg.DocsURIs = g.Docs.URIs
		}
	}

	return cfg, validateSync(cfg)
}

// apply writes the upstream section over the default upstream.
func (u GatewayUpstream) apply(up *infragateway.Upstream) error {
	if u.Type != "" {
		up.Type = u.Type
	}
	if t := u.Timeout; t != nil {
		up.Timeout = &infragateway.Timeout{Connect: t.Connect.Seconds(), Send: t.Send.Seconds(), Read: t.Read.Seconds()}
	}
	if k := u.KeepalivePool; k != nil {
		pool := &infragateway.KeepalivePool{Size: k.Size, Requests: k.Requests}
		if k.IdleTimeout != nil {
			idle := k.IdleTimeout.Seconds()
			pool.IdleTimeout = &idle
		}
		up.KeepalivePool = pool
	}

	h := u.HealthCheck
	if h.Enabled != nil && !*h.Enabled {
		up.Checks = nil
		return nil
	}
	// A copy: the default's pointers must not be written through.
	active := *up.Checks.Active
	healthy, unhealthy := *active.Healthy, *active.Unhealthy
	if h.Path != "" {
		active.HTTPPath = h.Path
	}
	if h.Timeout != 0 {
		active.Timeout = h.Timeout.Seconds()
	}
	var err error
	if healthy.Interval, err = seconds("health_check.healthy.interval", h.Healthy.Interval, healthy.Interval); err != nil {
		return err
	}
	if unhealthy.Interval, err = seconds("health_check.unhealthy.interval", h.Unhealthy.Interval, unhealthy.Interval); err != nil {
		return err
	}
	set := func(dst *int, v int) {
		if v != 0 {
			*dst = v
		}
	}
	set(&healthy.Successes, h.Healthy.Successes)
	set(&unhealthy.HTTPFailures, h.Unhealthy.HTTPFailures)
	set(&unhealthy.TCPFailures, h.Unhealthy.TCPFailures)
	set(&unhealthy.Timeouts, h.Unhealthy.Timeouts)
	if h.Healthy.HTTPStatuses != nil {
		healthy.HTTPStatuses = h.Healthy.HTTPStatuses
	}
	if h.Unhealthy.HTTPStatuses != nil {
		unhealthy.HTTPStatuses = h.Unhealthy.HTTPStatuses
	}
	active.Healthy, active.Unhealthy = &healthy, &unhealthy
	up.Checks = &infragateway.HealthChecks{Active: &active}
	return nil
}

// seconds is an interval APISIX takes in whole seconds; zero keeps def.
func seconds(key string, d time.Duration, def int) (int, error) {
	switch {
	case d == 0:
		return def, nil
	case d < time.Second || d%time.Second != 0:
		return 0, fmt.Errorf("%s is %s: APISIX counts in whole seconds, 1s or more", key, d)
	}
	return int(d / time.Second), nil
}

func upper(methods []string) []string {
	out := make([]string, len(methods))
	for i, m := range methods {
		out[i] = strings.ToUpper(m)
	}
	return out
}

var knownMethods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions}

// validateSync refuses what APISIX would, or what would route a call wrong.
func validateSync(cfg gatewaysync.Config) error {
	var errs []error
	ids := map[string]string{"read.upstream": cfg.ReadUpstream, "write.upstream": cfg.WriteUpstream,
		"service": cfg.Service, "route_id": cfg.RouteID}
	if cfg.DocsRoute != "" {
		ids["docs.route"] = cfg.DocsRoute
	}
	for _, key := range slices.Sorted(maps.Keys(ids)) {
		if ids[key] == "" {
			errs = append(errs, fmt.Errorf("%s is empty", key))
		}
	}
	if cfg.ReadUpstream == cfg.WriteUpstream {
		errs = append(errs, fmt.Errorf("read.upstream and write.upstream are both %q: the leader's upstream would hold every replica", cfg.ReadUpstream))
	}
	if !strings.Contains(cfg.RouteID, "{version}") || !strings.Contains(cfg.RouteID, "{kind}") {
		errs = append(errs, fmt.Errorf("route_id %q must contain {version} and {kind}, or two routes share an id", cfg.RouteID))
	}
	for _, kind := range []struct {
		key     string
		methods []string
	}{{"read.methods", cfg.ReadMethods}, {"write.methods", cfg.WriteMethods}} {
		if len(kind.methods) == 0 {
			errs = append(errs, fmt.Errorf("%s is empty", kind.key))
		}
		for _, m := range kind.methods {
			if !slices.Contains(knownMethods, m) {
				errs = append(errs, fmt.Errorf("%s: %q is not an HTTP method", kind.key, m))
			}
		}
	}
	for _, m := range cfg.ReadMethods {
		if slices.Contains(cfg.WriteMethods, m) {
			errs = append(errs, fmt.Errorf("%s is both read and written: it cannot go to both upstreams", m))
		}
	}
	if cfg.DocsRoute != "" && len(cfg.DocsURIs) == 0 {
		errs = append(errs, errors.New("docs.uris is empty"))
	}
	return errors.Join(errs...)
}
