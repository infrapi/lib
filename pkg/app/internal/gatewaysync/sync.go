// Package gateway keeps APISIX routing to the replicas that are actually up.
//
// The leader of the writer ring writes, through APISIX's Admin API:
//
//   - two upstreams: "read" holds every replica in the ring, "write" holds the
//     leader alone, so a write reaches the replica that performs it without
//     the relay hop between replicas;
//   - one service carrying the plugins every API call goes through — key-auth,
//     rate limiting per consumer, the body size limit, the request id;
//   - per contract, a read route (GET, HEAD) and a write route (POST, PUT,
//     PATCH, DELETE) on its prefix, attached to that service;
//   - a route for the documentation — Swagger UI and the contracts — without
//     authentication.
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

// Config is what the routes carry.
type Config struct {
	// Name prefixes every object id, so several services share one APISIX.
	Name string
	// Prefixes are the contracts' path prefixes, "/orders/v1/" and so on.
	Prefixes []string
	// ReadyPath is probed by APISIX's active health check.
	ReadyPath string
	// RateCount calls per RateWindow seconds, per consumer, across every route.
	RateCount, RateWindow int
	// MaxBodyBytes is the largest request body accepted.
	MaxBodyBytes int
	// Timeout to the replicas. A write with its permissions takes a few calls.
	Timeout infragateway.Timeout
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

// IDs of what the sync owns, under cfg.Name.
func (s *Syncer) id(parts ...string) string {
	return strings.Join(append([]string{s.cfg.Name}, parts...), "-")
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
	writeNodes, err := nodes([]string{leaderAddr})
	if err != nil {
		return err
	}

	readID, writeID, serviceID := s.id("read"), s.id("write"), s.id("api")
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
			{"read", []string{http.MethodGet, http.MethodHead}, readID},
			{"write", []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}, writeID},
		} {
			id := s.id(slug, r.kind)
			route := infragateway.Route{
				Name: id, Desc: r.kind + " calls of " + prefix,
				URIs: []string{prefix + "*"}, Methods: r.methods,
				ServiceID: serviceID, UpstreamID: r.upstream,
				Labels: map[string]string{"managed-by": s.cfg.Name},
			}
			if err := apply(ctx, s.admin.GetRoute, s.admin.NewRoute, id, route); err != nil {
				return err
			}
		}
	}

	docsID := s.id("docs")
	docs := infragateway.Route{
		Name: docsID, Desc: "the contracts and Swagger UI, unauthenticated",
		URIs: []string{"/-/docs", "/-/openapi/*"}, Methods: []string{http.MethodGet, http.MethodHead},
		UpstreamID: readID, Labels: map[string]string{"managed-by": s.cfg.Name},
	}
	return apply(ctx, s.admin.GetRoute, s.admin.NewRoute, docsID, docs)
}

func (s *Syncer) upstream(id string, nodes []infragateway.Node) infragateway.Upstream {
	timeout := s.cfg.Timeout
	return infragateway.Upstream{
		Name: id, Type: "roundrobin", Nodes: nodes, Timeout: &timeout,
		Labels: map[string]string{"managed-by": s.cfg.Name},
		// A replica draining or still joining answers 503 on its readiness
		// path: out of rotation until it answers 200 again.
		Checks: &infragateway.HealthChecks{Active: &infragateway.ActiveCheck{
			HTTPPath:  s.cfg.ReadyPath,
			Healthy:   &infragateway.ActiveHealthy{Interval: 2, Successes: 1},
			Unhealthy: &infragateway.ActiveUnhealthy{Interval: 1, HTTPFailures: 2, TCPFailures: 2},
		}},
	}
}

func (s *Syncer) service(id string) infragateway.Service {
	return infragateway.Service{
		Name: id, Labels: map[string]string{"managed-by": s.cfg.Name},
		Plugins: map[string]any{
			// The contract's own header. The key does not reach the replicas,
			// which trust what APISIX let through and are reachable only from it.
			"key-auth": map[string]any{"header": "apikey", "hide_credentials": true},
			// One counter per consumer for the whole API: the routes share
			// this service's plugin configuration, hence its counter.
			"limit-count": map[string]any{
				"count": s.cfg.RateCount, "time_window": s.cfg.RateWindow,
				"key_type": "var", "key": "consumer_name",
				"rejected_code": http.StatusTooManyRequests, "policy": "local",
			},
			"client-control": map[string]any{"max_body_size": s.cfg.MaxBodyBytes},
			// The replicas take X-Correlation-ID as the call's id and echo it.
			"request-id": map[string]any{"header_name": "X-Correlation-ID", "include_in_response": false},
		},
	}
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
