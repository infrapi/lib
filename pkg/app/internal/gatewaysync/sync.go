// Package gatewaysync keeps APISIX routing to the replicas that are actually
// up.
//
// The leader of the writer ring writes, through APISIX's Admin API:
//
//   - two upstreams: "read" holds every replica in the ring, "write" holds the
//     leader alone, so a write reaches the replica that performs it without
//     the relay hop between replicas;
//   - one service carrying the plugins every API call goes through;
//   - per contract, a read route and a write route on its prefix, attached to
//     that service;
//   - optionally, a route for the documentation, without the service's plugins.
//
// What each carries — ids, methods, health check, timeouts, plugins — is the
// Config; DefaultConfig is what it is when nothing is said.
//
// Each object has a fixed id and is written only when it differs from what
// APISIX holds, so a sync that changes nothing writes nothing. A replica that
// joins or leaves, or a new leader, is reflected at the next sync.
package gatewaysync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	infraerrors "github.com/infrapi/lib/pkg/errors"
	infragateway "github.com/infrapi/lib/pkg/gateway"
)

// Admin is the part of the Admin API client the sync uses.
type Admin interface {
	GetUpstream(ctx context.Context, id string) (*infragateway.Upstream, error)
	NewUpstream(ctx context.Context, id string, u infragateway.Upstream) (*infragateway.Upstream, error)
	GetService(ctx context.Context, id string) (*infragateway.Service, error)
	NewService(ctx context.Context, id string, s infragateway.Service) (*infragateway.Service, error)
	GetRoute(ctx context.Context, id string) (*infragateway.Route, error)
	NewRoute(ctx context.Context, id string, r infragateway.Route) (*infragateway.Route, error)
}

// Ring is what the sync needs from the cluster: who is in it, who leads.
type Ring interface {
	// Members are the HTTP addresses ("host:port") of the replicas.
	Members() ([]string, error)
	// Leader returns the leader's id and HTTP address.
	Leader() (id, addr string, self bool, err error)
}

// Config is everything the sync writes, resolved: no default is applied
// here — DefaultConfig gives them.
type Config struct {
	// Prefixes are the contracts' path prefixes, "/orders/v1/" and so on.
	Prefixes []string
	// Labels go on every object, so a listing can tell what the sync owns.
	Labels map[string]string

	// ReadUpstream and WriteUpstream are the two upstreams' ids, Service the
	// service's.
	ReadUpstream, WriteUpstream, Service string
	// RouteID names a contract's routes: {version} is its prefix as a slug
	// ("orders-v1"), {kind} read or write.
	RouteID string
	// ReadMethods go to every replica, WriteMethods to the leader alone when
	// LeaderWrites, else to every replica too.
	ReadMethods, WriteMethods []string
	LeaderWrites              bool

	// Upstream is the template of both upstreams: type, timeout, health
	// checks, keepalive pool. Its name, nodes and labels are the sync's.
	Upstream infragateway.Upstream
	// Plugins are the service's, every API call's.
	Plugins map[string]any

	// DocsRoute is the documentation route's id; empty writes none.
	DocsRoute string
	DocsURIs  []string
}

// DefaultConfig is the configuration when nothing is said: ids under name,
// the API's key, a rate limit per consumer, a body size limit, the request id,
// and replicas health-checked on readyPath.
func DefaultConfig(name, readyPath string) Config {
	return Config{
		Labels:        map[string]string{"managed-by": name},
		ReadUpstream:  name + "-read",
		WriteUpstream: name + "-write",
		Service:       name + "-api",
		RouteID:       name + "-{version}-{kind}",
		ReadMethods:   []string{http.MethodGet, http.MethodHead, http.MethodOptions},
		WriteMethods:  []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete},
		Upstream: infragateway.Upstream{
			Type: "roundrobin",
			// A write with its permissions takes a few calls to the backend.
			Timeout: &infragateway.Timeout{Connect: 2, Send: 30, Read: 120},
			// A replica draining or still joining answers 503 on its
			// readiness path: out of rotation until it answers 200 again.
			Checks: &infragateway.HealthChecks{Active: &infragateway.ActiveCheck{
				HTTPPath:  readyPath,
				Healthy:   &infragateway.ActiveHealthy{Interval: 2, Successes: 1},
				Unhealthy: &infragateway.ActiveUnhealthy{Interval: 1, HTTPFailures: 2, TCPFailures: 2},
			}},
		},
		Plugins: map[string]any{
			// The contract's own header. The key does not reach the replicas,
			// which trust what APISIX let through and are reachable only from it.
			"key-auth": map[string]any{"header": "apikey", "hide_credentials": true},
			// One counter per consumer for the whole API: the routes share
			// this service's plugin configuration, hence its counter.
			"limit-count": map[string]any{
				"count": 600, "time_window": 60, "key_type": "var", "key": "consumer_name",
				"rejected_code": http.StatusTooManyRequests, "policy": "local",
			},
			"client-control": map[string]any{"max_body_size": 1 << 20},
			// The replicas take X-Correlation-ID as the call's id and echo it.
			"request-id": map[string]any{"header_name": "X-Correlation-ID", "include_in_response": false},
		},
		DocsRoute: name + "-docs",
		DocsURIs:  []string{"/-/docs", "/-/openapi/*"},
	}
}

// Syncer writes Config and the ring's membership to APISIX.
type Syncer struct {
	admin Admin
	ring  Ring
	cfg   Config
}

// New builds a Syncer.
func New(admin Admin, ring Ring, cfg Config) *Syncer {
	return &Syncer{admin: admin, ring: ring, cfg: cfg}
}

// Sync makes APISIX match the ring once. It is meant to run on the leader, on
// a timer; every write it makes converges, so two leaders during a partition
// end where one would.
func (s *Syncer) Sync(ctx context.Context) error {
	members, err := s.ring.Members()
	if err != nil {
		return fmt.Errorf("gateway: ring members: %w", err)
	}
	_, leaderAddr, _, err := s.ring.Leader()
	if err != nil {
		return fmt.Errorf("gateway: ring leader: %w", err)
	}
	readNodes, err := nodes(members)
	if err != nil {
		return err
	}
	writeNodes := readNodes
	if s.cfg.LeaderWrites {
		if writeNodes, err = nodes([]string{leaderAddr}); err != nil {
			return err
		}
	}

	readID, writeID, serviceID := s.cfg.ReadUpstream, s.cfg.WriteUpstream, s.cfg.Service
	if err := apply(ctx, s.admin.GetUpstream, s.admin.NewUpstream, readID, s.upstream(readID, readNodes)); err != nil {
		return err
	}
	if err := apply(ctx, s.admin.GetUpstream, s.admin.NewUpstream, writeID, s.upstream(writeID, writeNodes)); err != nil {
		return err
	}
	if err := apply(ctx, s.admin.GetService, s.admin.NewService, serviceID, s.service(serviceID)); err != nil {
		return err
	}

	for _, prefix := range s.cfg.Prefixes {
		slug := strings.Trim(strings.ReplaceAll(prefix, "/", "-"), "-") // "/orders/v1/" → "orders-v1"
		for _, r := range []struct {
			kind     string
			methods  []string
			upstream string
		}{
			{"read", s.cfg.ReadMethods, readID},
			{"write", s.cfg.WriteMethods, writeID},
		} {
			id := strings.NewReplacer("{version}", slug, "{kind}", r.kind).Replace(s.cfg.RouteID)
			route := infragateway.Route{
				Name: id, Desc: r.kind + " calls of " + prefix,
				URIs: []string{prefix + "*"}, Methods: r.methods,
				ServiceID: serviceID, UpstreamID: r.upstream,
				Labels: s.cfg.Labels,
			}
			if err := apply(ctx, s.admin.GetRoute, s.admin.NewRoute, id, route); err != nil {
				return err
			}
		}
	}

	if s.cfg.DocsRoute == "" {
		return nil
	}
	docs := infragateway.Route{
		Name: s.cfg.DocsRoute, Desc: "the contracts and Swagger UI, unauthenticated",
		URIs: s.cfg.DocsURIs, Methods: []string{http.MethodGet, http.MethodHead},
		UpstreamID: readID, Labels: s.cfg.Labels,
	}
	return apply(ctx, s.admin.GetRoute, s.admin.NewRoute, s.cfg.DocsRoute, docs)
}

func (s *Syncer) upstream(id string, nodes []infragateway.Node) infragateway.Upstream {
	u := s.cfg.Upstream
	u.Name, u.Nodes, u.Labels = id, nodes, s.cfg.Labels
	return u
}

func (s *Syncer) service(id string) infragateway.Service {
	return infragateway.Service{Name: id, Labels: s.cfg.Labels, Plugins: s.cfg.Plugins}
}

// nodes turns "host:port" addresses into upstream nodes, sorted so that the
// same membership always reads the same.
func nodes(addrs []string) ([]infragateway.Node, error) {
	out := make([]infragateway.Node, 0, len(addrs))
	for _, a := range addrs {
		host, p, err := net.SplitHostPort(a)
		if err != nil {
			return nil, fmt.Errorf("gateway: replica address %q: %w", a, err)
		}
		port, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("gateway: replica address %q: %w", a, err)
		}
		out = append(out, infragateway.Node{Host: host, Port: port, Weight: 1})
	}
	sort.Slice(out, func(i, j int) bool {
		return net.JoinHostPort(out[i].Host, strconv.Itoa(out[i].Port)) <
			net.JoinHostPort(out[j].Host, strconv.Itoa(out[j].Port))
	})
	return out, nil
}

// apply writes want under id unless APISIX already holds the same thing.
func apply[T any](ctx context.Context, get func(context.Context, string) (*T, error),
	put func(context.Context, string, T) (*T, error), id string, want T) error {
	have, err := get(ctx, id)
	reason := "changed"
	switch {
	case err == nil && same(have, &want):
		return nil
	case err != nil && infraerrors.ErrorCode(err) != http.StatusNotFound:
		return fmt.Errorf("gateway: read %s: %w", id, err)
	case err != nil:
		reason = "missing"
	}
	if _, err := put(ctx, id, want); err != nil {
		return fmt.Errorf("gateway: write %s: %w", id, err)
	}
	// Only writes are logged: a sync that finds APISIX up to date is silent.
	// The same id written at every tick means APISIX stores it differently
	// from how it was sent.
	slog.InfoContext(ctx, "gateway object written", "id", id, "reason", reason)
	return nil
}

// same compares two objects as APISIX would store them.
func same(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}
