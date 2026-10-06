package cluster

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/grafana/dskit/kv/memberlist"
)

// Where the status pages are mounted, next to the /-/metadata and /-/metrics
// the server package already serves.
const (
	// DefaultPathPrefix is where RegisterGin mounts the status pages. The /-/
	// prefix is this library's convention for the endpoints a service gets
	// without asking, and it cannot collide with an application route.
	DefaultPathPrefix = "/-/cluster"

	// StatusPath serves this package's own JSON summary of the cluster.
	StatusPath = DefaultPathPrefix

	// MemberlistPath serves dskit's gossip store page. It answers 404 unless
	// the store is memberlist.
	MemberlistPath = DefaultPathPrefix + "/memberlist"

	// RingPath serves dskit's ring page for the role named by the :role path
	// parameter. It honours an Accept: application/json request header.
	RingPath = DefaultPathPrefix + "/ring/:role"
)

// HandlerOptions tunes what RegisterGin mounts.
type HandlerOptions struct {
	// ForgetEnabled also mounts the POST route of the ring page, which evicts
	// a replica from the ring. That is an unauthenticated write to cluster
	// state, so it is off unless a deployment asks for it.
	ForgetEnabled bool

	// PathPrefix relocates the whole set. Empty means DefaultPathPrefix. It
	// must start with a slash and must not end with one.
	//
	// To reproduce another service's layout exactly, mount the handlers
	// yourself instead: Cluster.MemberlistHandler and Role.RingHandler take no
	// path parameter and go wherever a route is wanted.
	PathPrefix string
}

// prefix returns the path prefix to mount under, and whether it is usable.
func (o HandlerOptions) prefix() (string, error) {
	if o.PathPrefix == "" {
		return DefaultPathPrefix, nil
	}
	if !strings.HasPrefix(o.PathPrefix, "/") || strings.HasSuffix(o.PathPrefix, "/") {
		return "", fmt.Errorf("cluster: HandlerOptions.PathPrefix %q must start with a slash and must not end with one", o.PathPrefix)
	}
	return o.PathPrefix, nil
}

// RegisterGin mounts the cluster status pages on the engine the server package
// built.
//
// It is safe to call on a cluster that is disabled or not yet started: the
// pages then report that rather than failing to mount, so a service can wire
// its routes once at startup.
func (c *Cluster) RegisterGin(engine *gin.Engine, opts HandlerOptions) error {
	if engine == nil {
		return errors.New("cluster: nil *gin.Engine")
	}

	prefix, err := opts.prefix()
	if err != nil {
		return err
	}

	ringPath := prefix + "/ring/:role"

	// Remembered so that a page can link to its siblings: a ring page shows a
	// way back to the index, and the index links to each ring.
	c.mu.Lock()
	c.pathPrefix = prefix
	c.forgetEnabled = opts.ForgetEnabled
	c.mu.Unlock()

	engine.GET(prefix, c.StatusHandler())
	engine.GET(prefix+"/memberlist", c.MemberlistHandler())
	engine.GET(ringPath, c.RingHandler())

	if opts.ForgetEnabled {
		engine.POST(ringPath, c.RingHandler())
	}

	return nil
}

// StatusHandler serves the cluster index: this replica's identity, the store,
// and every joined role with its mode, members, leader and verdict.
//
// It renders a page, or the same model as JSON when the request asks for it
// with an Accept header, which is the negotiation dskit's own pages perform.
//
// A role that cannot be read reports its error in its own card rather than
// failing the whole page: during a partition, which roles are readable is the
// useful part.
func (c *Cluster) StatusHandler() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		view := c.indexView()

		if wantsJSON(ctx.Request) {
			ctx.JSON(http.StatusOK, view)
			return
		}
		render(ctx.Writer, indexTemplate, view)
	}
}

// indexView collects everything the index page shows.
func (c *Cluster) indexView() indexView {
	c.mu.RLock()
	prefix := c.pathPrefix
	c.mu.RUnlock()

	view := indexView{
		page:         page{Title: "Cluster", Now: time.Now()},
		Prefix:       prefix,
		Enabled:      c.Enabled(),
		Store:        c.Store(),
		InstanceID:   c.InstanceID(),
		InstanceAddr: c.InstanceAddr(),
		InstanceHost: hostOnly(c.InstanceAddr()),
		Fqdn:         c.Fqdn(),
		Roles:        []roleView{},
	}

	if !c.Enabled() {
		view.Error = ErrDisabled.Error()
		return view
	}

	view.ClusterLabel = c.cfg.GossipClusterLabel
	if port, err := c.GossipPort(); err == nil {
		view.GossipPort = port
		view.HasGossip = true
	}

	for _, name := range c.Roles() {
		view.Roles = append(view.Roles, c.roleView(name, prefix))
	}

	return view
}

// roleView collects one role's card. prefix is where the status pages are
// mounted, which the leader link needs: the leader's own copy of this page is
// at the same path on its own address.
func (c *Cluster) roleView(name, prefix string) roleView {
	role, err := c.Role(name)
	if err != nil {
		return roleView{Name: name, Error: err.Error()}
	}

	view := roleView{
		Name: name,
		Mode: role.Mode().String(),
		Key:  role.Key(),
	}

	if state, err := role.State(); err == nil {
		view.State = state
	}

	members, err := role.Members()
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.MemberCount = len(members)

	should, err := role.ShouldRun()
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.ShouldRun = should

	if role.Mode() == ModeLeader {
		view.MayLead, view.LeaderCheckFail, _ = role.LeaderEligibility()
		if leader, err := role.Leader(); err == nil {
			view.Leader = leader.ID
			view.LeaderIsSelf = leader.ID == c.InstanceID()

			// Behind a load balancer the replica that answered is rarely the
			// one being looked for, so the leader gets a link to its own copy
			// of this page. The address is the one it advertises in the ring,
			// which is where peers reach it.
			if !view.LeaderIsSelf && leader.Addr != "" {
				view.LeaderAddr = leader.Addr
				view.LeaderURL = c.scheme + "://" + leader.Addr + prefix
			}
		}
	}

	return view
}

// MemberlistHandler serves the gossip store page, or 404 when the store is not
// memberlist.
//
// The contents are dskit's — the key-value store, the cluster members and the
// message history are introspection this package has no copy of, and could not
// build one from outside dskit anyway: the data behind them is unexported. What
// is ours is the rendering: dskit's status handler accepts a template, so the
// page is laid out here, with Bootstrap, instead of dskit's unstyled default.
func (c *Cluster) MemberlistHandler() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		c.mu.RLock()
		service := c.mlSvc
		c.mu.RUnlock()

		if service == nil {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "the gossip store is not in use"})
			return
		}
		memberlist.NewHTTPStatusHandler(service, c.memberlistPage()).ServeHTTP(ctx.Writer, ctx.Request)
	}
}

// RingHandler serves the ring page of the role named by the :role path
// parameter, or 404 for a role this replica does not run.
func (c *Cluster) RingHandler() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		name := ctx.Param("role")

		role, err := c.Role(name)
		if err != nil {
			ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		role.serveRing(ctx)
	}
}

// RingHandler serves this one role's ring page, so that it can be mounted at a
// path of the service's choosing rather than under the :role parameter
// RegisterGin uses.
//
//	engine.GET("/ring", scheduler.RingHandler())
//
// It renders the model as JSON when the request asks for it, and answers 404
// before the cluster has started.
func (r *Role) RingHandler() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		r.serveRing(ctx)
	}
}

// serveRing renders the ring page, or hands a forget request to dskit.
func (r *Role) serveRing(ctx *gin.Context) {
	if err := r.usable(); err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	ringSvc := r.ring
	if ringSvc == nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": ErrNotStarted.Error()})
		return
	}

	// Forgetting a replica is a compare-and-swap on the ring, which dskit's own
	// handler already performs and answers with a redirect back here. The form
	// on our page posts to this same path, so there is nothing to reimplement.
	if ctx.Request.Method == http.MethodPost {
		ringSvc.ServeHTTP(ctx.Writer, ctx.Request)
		return
	}

	view := r.ringView(ctx)

	if wantsJSON(ctx.Request) {
		ctx.JSON(http.StatusOK, view)
		return
	}
	render(ctx.Writer, ringTemplate, view)
}

// ringView collects everything a role's ring page shows.
func (r *Role) ringView(ctx *gin.Context) ringView {
	c := r.cluster

	c.mu.RLock()
	prefix := c.pathPrefix
	forget := c.forgetEnabled
	c.mu.RUnlock()

	view := ringView{
		page:             page{Title: r.spec.Name + " ring", Now: time.Now()},
		Prefix:           prefix,
		ForgetEnabled:    forget,
		RoleName:         r.spec.Name,
		Mode:             r.spec.Mode.String(),
		Key:              r.key,
		InstanceID:       c.cfg.InstanceID,
		HeartbeatTimeout: c.cfg.HeartbeatTimeout.String(),
		Members:          []Member{},
	}

	if r.spec.Mode == ModeLeader {
		if leader, err := r.Leader(); err == nil {
			view.Leader = leader.ID
		}
	}

	// The whole ring, excluded replicas included: a status page that hides the
	// unhealthy member hides the reason someone opened it.
	members, err := r.AllMembers(ctx.Request.Context())
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.Members = members

	return view
}

// LifecyclerHandler serves dskit's own ring page, built from this replica's
// lifecycler rather than from the ring service.
//
// It is the escape hatch: the same data as RingHandler, rendered by dskit, read
// from the key-value store on every request instead of from the snapshot the
// ring already watches. Useful when this package's page omits something, or to
// compare an uncached read against the watched view.
func (r *Role) LifecyclerHandler() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if err := r.usable(); err != nil {
			ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}

		lifecycler := r.lifecycler
		if lifecycler == nil {
			ctx.JSON(http.StatusNotFound, gin.H{"error": ErrNotStarted.Error()})
			return
		}
		dskitPage(lifecycler, ctx.Writer, ctx.Request)
	}
}
