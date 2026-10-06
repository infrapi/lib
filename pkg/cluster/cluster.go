package cluster

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/go-kit/log"
	"github.com/grafana/dskit/dns"
	"github.com/grafana/dskit/kv"
	"github.com/grafana/dskit/kv/codec"
	"github.com/grafana/dskit/kv/memberlist"
	"github.com/grafana/dskit/ring"
	"github.com/grafana/dskit/services"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"

	"github.com/infrapi/lib/pkg/config"
)

// The key-value backends this package supports. dskit also implements consul
// and a mirroring multi store; widening INFRAPI_CLUSTER_KV_STORE and adding the
// matching keys is all either would need.
const (
	storeMemberlist = "memberlist"
	storeEtcd       = "etcd"
	storeInmemory   = "inmemory"
)

// MetricsPrefix is prepended to every dskit metric this package registers.
// dskit names its collectors ring_members, kv_request_duration_seconds and
// dns_lookups_total, with no namespace of their own, so without a prefix they
// would collide with any other dskit-embedding library in the process.
const MetricsPrefix = "infrapi_cluster_"

// Options carries what cannot come from a dotenv file.
type Options struct {
	// Roles is every role this binary knows how to be. The dotenv decides which
	// of them this replica actually joins.
	Roles []RoleSpec

	// Codecs are extra key-value codecs the service wants clients for, beyond
	// the ring's own. They must all be listed here: the gossip store only
	// learns its codecs when the replica joins, and a client built later for an
	// unregistered codec is refused.
	Codecs []codec.Codec

	// Logger receives every dskit log line, adapted to go-kit. Nil uses the
	// logrus standard logger.
	Logger logrus.FieldLogger

	// Registerer collects the ring, lifecycler, key-value and DNS metrics under
	// MetricsPrefix. Nil uses prometheus.DefaultRegisterer, which the server
	// package already exposes at /-/metrics.
	Registerer prometheus.Registerer
}

// state is the lifecycle of a Cluster, which is single use: dskit services
// cannot be restarted once stopped.
type state int

const (
	stateNew state = iota
	stateRunning
	stateStopped
)

// registerersInUse records the registerers live clusters hold. Two clusters on
// one registerer would both try to register dns_lookups_total, which carries no
// distinguishing label, and promauto panics rather than returning an error.
var registerersInUse sync.Map

// Cluster owns the key-value layer of one process and the role rings this
// replica joined.
//
// It is single use: once stopped it cannot be started again, and a Start that
// fails means the process should exit rather than rebuild the Cluster, because
// the dskit collectors of the failed attempt are already registered.
type Cluster struct {
	cfg    *config.ClusterConfig
	dskit  *dskitConfig
	logger logrus.FieldLogger
	gokit  log.Logger

	// baseReg is the registerer the caller handed us, used as the key of
	// registerersInUse. reg is the prefixed one everything is registered on.
	baseReg prometheus.Registerer
	reg     prometheus.Registerer

	instanceAddr string

	// fqdn and scheme come from the application configuration, so that the
	// index page can say which replica answered and build a link to the
	// leader's own page.
	fqdn   string
	scheme string

	// order is the joined role names in declaration order, so that Start, Stop
	// and Roles are deterministic whatever order the dotenv listed them in.
	order []string
	roles map[string]*Role

	codecs    map[string]codec.Codec
	kvClients map[string]kv.Client

	// leaderChecks are INFRAPI_CLUSTER_LEADER_CHECKS, parsed once: every
	// leader-mode role runs them to decide whether this replica may lead.
	leaderChecks    []Check
	leaderEligible  *prometheus.GaugeVec
	leaderCheckFail *prometheus.CounterVec

	mlSvc *memberlist.KVInitService
	mlKV  *memberlist.KV

	watcher *services.FailureWatcher

	// pathPrefix and forgetEnabled are set by RegisterGin, so that a rendered
	// page can link to its siblings and show the forget form only where it was
	// actually mounted.
	pathPrefix    string
	forgetEnabled bool

	// memberlistTmpl is built on first use, once the prefix above is known, so
	// that the gossip page can link back to the index.
	memberlistTmplOnce sync.Once
	memberlistTmpl     *template.Template

	// serviceCtx is the lifetime of every dskit service this cluster starts.
	// It is deliberately NOT the context passed to Start: dskit's StartAsync
	// takes the context a service lives in, not a start deadline, so handing it
	// a context that Start cancels on return would terminate the gossip store
	// and every ring the moment Start succeeded.
	serviceCtx    context.Context
	serviceCancel context.CancelFunc

	// started is every dskit service that reached Running, in start order, so
	// that a partial failure unwinds exactly what it created.
	started []services.Service

	mu    sync.RWMutex
	state state
}

// New validates the configuration and builds the cluster. It opens no socket
// and contacts no peer: Start does that.
//
// A configuration with INFRAPI_CLUSTER_ENABLED unset still returns a usable
// Cluster whose Start is a no-op and whose queries report ErrDisabled, so a
// service can wire clustering in unconditionally and switch it on by dotenv.
func New(app *config.AppConfig, cfg *config.ClusterConfig, opts Options) (*Cluster, error) {
	if app == nil {
		return nil, errors.New("cluster: nil *config.AppConfig")
	}
	if cfg == nil {
		return nil, errors.New("cluster: nil *config.ClusterConfig")
	}

	// Declared roles are a property of the code, so they are checked even when
	// clustering is off: a duplicate name or a missing mode is a programming
	// mistake either way.
	declared, err := declaredRoles(opts.Roles)
	if err != nil {
		return nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = logrus.StandardLogger()
	}

	c := &Cluster{
		cfg:       cfg,
		logger:    logger,
		gokit:     NewGoKitLogger(logger),
		roles:     map[string]*Role{},
		codecs:    map[string]codec.Codec{},
		kvClients: map[string]kv.Client{},
		state:     stateNew,
	}

	c.fqdn = app.AppFqdn
	c.scheme = urlScheme(app.AppUrl)

	if !cfg.Enabled {
		return c, nil
	}

	order, err := joinedRoles(opts.Roles, declared, cfg.Roles)
	if err != nil {
		return nil, err
	}
	c.order = order

	for _, codecImpl := range opts.Codecs {
		id := codecImpl.CodecID()
		if _, exists := c.codecs[id]; exists {
			return nil, fmt.Errorf("cluster: two codecs share the ID %q", id)
		}
		c.codecs[id] = codecImpl
	}

	baseReg := opts.Registerer
	if baseReg == nil {
		baseReg = prometheus.DefaultRegisterer
	}
	if _, loaded := registerersInUse.LoadOrStore(baseReg, true); loaded {
		return nil, errors.New("cluster: a cluster is already registered on this registerer, pass a dedicated Options.Registerer")
	}
	c.baseReg = baseReg
	c.reg = prometheus.WrapRegistererWithPrefix(MetricsPrefix, baseReg)

	// Resolving the advertised address is the one thing New looks outward for,
	// and it reads network interfaces rather than talking to anyone. A failure
	// is returned: a library must not exit the process.
	instanceAddr, err := ring.GetInstanceAddr(cfg.InstanceAddr, cfg.InstanceInterfaceNames, c.gokit, false)
	if err != nil {
		registerersInUse.Delete(baseReg)
		return nil, fmt.Errorf("cluster: resolve instance address: %w", err)
	}
	c.instanceAddr = instanceAddr

	dskitCfg, err := newDskitConfig(app, cfg, opts.Codecs, instanceAddr)
	if err != nil {
		registerersInUse.Delete(baseReg)
		return nil, err
	}
	c.dskit = dskitCfg

	// A check that does not parse is refused here rather than skipped: a node
	// whose condition is silently dropped is a node that leads when it should
	// not.
	if c.leaderChecks, err = ParseChecks(cfg.LeaderChecks); err != nil {
		registerersInUse.Delete(baseReg)
		return nil, err
	}
	c.leaderEligible = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "leader_eligible",
		Help: "Whether this replica may lead the role: 1 when its leader checks pass, as published in the ring.",
	}, []string{"name"})
	c.leaderCheckFail = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "leader_check_failures_total",
		Help: "Leader check evaluations that failed, by role and by the check that failed.",
	}, []string{"name", "check"})
	// A cluster stopped and built again on the same registerer finds them
	// registered by its predecessor: it takes them over.
	if c.leaderEligible, err = registerOrReuse(c.reg, c.leaderEligible); err == nil {
		c.leaderCheckFail, err = registerOrReuse(c.reg, c.leaderCheckFail)
	}
	if err != nil {
		registerersInUse.Delete(baseReg)
		return nil, fmt.Errorf("cluster: register leader check metrics: %w", err)
	}

	for _, name := range order {
		spec := declared[name]
		c.roles[name] = &Role{
			spec:    spec,
			key:     spec.ringKey(),
			mode:    modeSpecs[spec.Mode],
			cluster: c,
		}
	}

	return c, nil
}

// declaredRoles indexes the roles a service declared, rejecting the mistakes
// that would otherwise surface as a panic from inside promauto.
func declaredRoles(specs []RoleSpec) (map[string]RoleSpec, error) {
	declared := make(map[string]RoleSpec, len(specs))
	keys := make(map[string]string, len(specs))

	for _, spec := range specs {
		if spec.Name == "" {
			return nil, fmt.Errorf("%w: a role has no name", ErrDuplicateRole)
		}
		if _, exists := declared[spec.Name]; exists {
			return nil, fmt.Errorf("%w: role %q is declared twice, and both would register the same ring metrics", ErrDuplicateRole, spec.Name)
		}
		if _, known := modeSpecs[spec.Mode]; !known {
			return nil, fmt.Errorf("%w: role %q has mode %d", ErrBadMode, spec.Name, spec.Mode)
		}

		key := spec.ringKey()
		if other, exists := keys[key]; exists {
			return nil, fmt.Errorf("%w: roles %q and %q both store their ring under key %q", ErrDuplicateRole, other, spec.Name, key)
		}

		declared[spec.Name] = spec
		keys[key] = spec.Name
	}

	return declared, nil
}

// joinedRoles reconciles the roles the dotenv selected against the roles the
// code declared, and returns the selection in declaration order.
//
// A name in the dotenv that the code does not declare is an error: it is always
// a typo or a bad rollout, and a silent no-op would mean a scheduler that never
// schedules with nothing in the logs. A declared role the dotenv does not select
// is not an error: a binary legitimately knows more roles than a deployment
// runs.
func joinedRoles(specs []RoleSpec, declared map[string]RoleSpec, selected []string) ([]string, error) {
	if len(declared) == 0 {
		// A key-value-only cluster, with no ring. Legitimate: a service may
		// want the shared store and no coordination.
		if len(selected) > 0 {
			return nil, fmt.Errorf("%w: INFRAPI_CLUSTER_ROLES selects %s but the service declared no roles",
				ErrRoleUndeclared, strings.Join(selected, ", "))
		}
		return nil, nil
	}

	if len(selected) == 0 {
		return nil, fmt.Errorf("%w: the service declared roles but INFRAPI_CLUSTER_ROLES is empty", ErrNoRoles)
	}

	wanted := make(map[string]bool, len(selected))
	for _, name := range selected {
		if _, ok := declared[name]; !ok {
			return nil, fmt.Errorf("%w: INFRAPI_CLUSTER_ROLES names %q, declared roles are %s",
				ErrRoleUndeclared, name, strings.Join(sortedNames(declared), ", "))
		}
		wanted[name] = true
	}

	order := make([]string, 0, len(wanted))
	for _, spec := range specs {
		if wanted[spec.Name] {
			order = append(order, spec.Name)
		}
	}
	return order, nil
}

// Start brings up the key-value backend and then every joined role, and returns
// once this replica is ACTIVE in all of them. On failure it stops whatever it
// had already started and returns the original error.
//
// ctx bounds the whole sequence, with INFRAPI_CLUSTER_START_TIMEOUT applied on
// top: waiting for a ring state retries indefinitely, so a deadline is not
// optional.
func (c *Cluster) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.state {
	case stateRunning:
		return ErrAlreadyStarted
	case stateStopped:
		return ErrStopped
	}

	if !c.cfg.Enabled {
		c.state = stateRunning
		return nil
	}

	startCtx, cancel := context.WithTimeout(ctx, c.cfg.StartTimeout)
	defer cancel()

	c.watcher = services.NewFailureWatcher()
	c.serviceCtx, c.serviceCancel = context.WithCancel(context.WithoutCancel(ctx))

	if err := c.start(startCtx); err != nil {
		// A startup failure is almost always the store being unreachable, and
		// the error coming back from dskit says nothing about where the store
		// is. Naming it here is the difference between "context deadline
		// exceeded" and knowing which address to go and look at.
		err = fmt.Errorf("%w (%s)", err, c.storeLocation())

		// Unwind on a context of its own: the failure may well be that
		// startCtx expired, and the services already running still deserve a
		// clean leave.
		stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.StopTimeout)
		defer stopCancel()

		if unwindErr := c.stopStarted(stopCtx); unwindErr != nil {
			err = errors.Join(err, fmt.Errorf("cluster: unwinding a failed start: %w", unwindErr))
		}
		c.watcher.Close()
		c.serviceCancel()
		c.state = stateStopped
		registerersInUse.Delete(c.baseReg)
		return err
	}

	c.state = stateRunning
	return nil
}

// start is Start without the locking and the unwinding.
func (c *Cluster) start(ctx context.Context) error {
	if err := c.startStore(ctx); err != nil {
		return err
	}

	// Codec clients are built here rather than in New because with the gossip
	// store the key-value configuration only becomes usable once the store is
	// running.
	for id, codecImpl := range c.codecs {
		client, err := kv.NewClient(c.dskit.kv, codecImpl, kv.RegistererWithKVName(c.reg, "codec-"+id), c.gokit)
		if err != nil {
			return fmt.Errorf("cluster: key-value client for codec %q: %w", id, err)
		}
		c.kvClients[id] = client
	}

	for _, name := range c.order {
		if err := c.roles[name].start(ctx); err != nil {
			return fmt.Errorf("cluster: start role %q: %w", name, err)
		}
	}

	return nil
}

// startStore brings up the key-value backend. Only the gossip store has
// anything to start; etcd and the in-memory store are created on demand by
// kv.NewClient.
func (c *Cluster) startStore(ctx context.Context) error {
	if c.cfg.KvStore != storeMemberlist {
		return nil
	}

	// One DNS provider per registerer: dns_lookups_total carries no label that
	// would tell two of them apart.
	provider := dns.NewProvider(dns.GolangResolverType, 0, log.With(c.gokit, "component", "dns"), c.reg)

	c.mlSvc = memberlist.NewKVInitService(&c.dskit.memberlist, log.With(c.gokit, "component", "memberlist"), provider, c.reg)
	if err := c.startService(ctx, c.mlSvc); err != nil {
		return fmt.Errorf("cluster: start gossip service: %w", err)
	}
	c.started = append(c.started, c.mlSvc)

	// The init service only watches: it creates the store lazily on the first
	// GetMemberlistKV and merely starts it asynchronously. Without awaiting it
	// here, "Start returned" would not mean the gossip layer is up, a failed
	// join would surface much later, and GossipPort would have nothing to
	// report.
	mlKV, err := c.mlSvc.GetMemberlistKV()
	if err != nil {
		return fmt.Errorf("cluster: build gossip store: %w", err)
	}
	if err := mlKV.AwaitRunning(ctx); err != nil {
		return fmt.Errorf("cluster: await gossip store: %w", err)
	}
	c.mlKV = mlKV

	// The deferred hook kv.NewClient calls for the memberlist backend.
	c.dskit.kv.MemberlistKV = c.mlSvc.GetMemberlistKV

	return nil
}

// Stop leaves every role ring and then the key-value backend, in reverse order,
// and returns the joined errors of every step it attempted. It is safe on a
// cluster that never started, and it is idempotent.
//
// Cancellation of ctx is deliberately ignored: a shutdown is normally triggered
// because the caller's context was cancelled, and honouring it here would
// abandon the gossip leave and leave this replica in every ring until its
// heartbeat went stale.
func (c *Cluster) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state == stateStopped {
		return nil
	}

	wasRunning := c.state == stateRunning
	c.state = stateStopped

	if !c.cfg.Enabled || !wasRunning {
		return nil
	}

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.StopTimeout)
	defer cancel()

	err := c.stopStarted(stopCtx)

	if c.watcher != nil {
		c.watcher.Close()
	}
	if c.serviceCancel != nil {
		c.serviceCancel()
	}
	registerersInUse.Delete(c.baseReg)

	return err
}

// stopStarted stops every service that reached Running, in reverse start order,
// attempting them all even if one fails: a ring that will not stop must not
// skip the gossip leave.
//
// Reverse order matters twice. A role stops its ring before its lifecycler, so
// that queries start erroring immediately rather than reading a stale view. And
// the key-value store stops last, because leaving a ring is a write to it.
func (c *Cluster) stopStarted(ctx context.Context) error {
	var errs []error

	for i := len(c.started) - 1; i >= 0; i-- {
		if err := services.StopAndAwaitTerminated(ctx, c.started[i]); err != nil {
			errs = append(errs, err)
		}
	}
	c.started = nil

	return errors.Join(errs...)
}

// memberlistPage returns this cluster's gossip page template, built once.
func (c *Cluster) memberlistPage() *template.Template {
	c.memberlistTmplOnce.Do(func() {
		c.mu.RLock()
		prefix := c.pathPrefix
		c.mu.RUnlock()

		c.memberlistTmpl = newMemberlistTemplate(prefix)
	})
	return c.memberlistTmpl
}

// startService starts s for as long as this cluster lives, and waits for it to
// be running within the caller's deadline.
//
// Those are two different contexts on purpose. dskit's StartAsync takes the
// context the service runs in: cancelling it stops the service. The deadline
// belongs on AwaitRunning alone.
func (c *Cluster) startService(ctx context.Context, service services.Service) error {
	if err := service.StartAsync(c.serviceCtx); err != nil {
		return err
	}
	return service.AwaitRunning(ctx)
}

// Role returns the named role.
//
// It reports ErrDisabled when clustering is off, ErrNotStarted before Start,
// ErrStopped after Stop, and ErrRoleNotJoined for a role this replica does not
// run — four distinct answers, because a service reacts differently to each.
func (c *Cluster) Role(name string) (*Role, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.usable(); err != nil {
		return nil, err
	}
	role, ok := c.roles[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrRoleNotJoined, name)
	}
	return role, nil
}

// Roles returns the names of the roles this replica joined, in declaration
// order. It returns nothing when clustering is off.
func (c *Cluster) Roles() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return append([]string(nil), c.order...)
}

// InstanceID returns the identity this replica registers under.
func (c *Cluster) InstanceID() string {
	return c.cfg.InstanceID
}

// InstanceAddr returns the host:port this replica advertises in its rings. It
// is empty when clustering is off, because nothing was resolved.
func (c *Cluster) InstanceAddr() string {
	if c.instanceAddr == "" {
		return ""
	}
	return c.dskit.lifecycler.Addr
}

// Store returns the configured key-value backend: memberlist, etcd or inmemory.
func (c *Cluster) Store() string {
	return c.cfg.KvStore
}

// Enabled reports whether the configuration turned clustering on.
func (c *Cluster) Enabled() bool {
	return c.cfg.Enabled
}

// KVClient returns the key-value client for one of the codecs passed in
// Options.Codecs, looked up by its CodecID, so that a service can share state
// across replicas over the same store the rings use.
func (c *Cluster) KVClient(codecID string) (kv.Client, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.usable(); err != nil {
		return nil, err
	}
	client, ok := c.kvClients[codecID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrCodecUnknown, codecID)
	}
	return client, nil
}

// GossipPort returns the port the gossip transport is actually listening on,
// which is not the configured one when that was zero. It reports ErrWrongStore
// unless the store is memberlist.
func (c *Cluster) GossipPort() (int, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.usable(); err != nil {
		return 0, err
	}
	if c.mlKV == nil {
		return 0, fmt.Errorf("%w: the store is %s, not %s", ErrWrongStore, c.cfg.KvStore, storeMemberlist)
	}
	return c.mlKV.GetListeningPort(), nil
}

// MemberlistKV returns the raw gossip store, for a caller that needs dskit APIs
// this package does not wrap. It reports ErrWrongStore unless the store is
// memberlist.
func (c *Cluster) MemberlistKV() (*memberlist.KV, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.usable(); err != nil {
		return nil, err
	}
	if c.mlKV == nil {
		return nil, fmt.Errorf("%w: the store is %s, not %s", ErrWrongStore, c.cfg.KvStore, storeMemberlist)
	}
	return c.mlKV, nil
}

// Err delivers the first asynchronous failure of any underlying dskit service.
// A value on this channel means the cluster is no longer usable and the process
// should restart. It is nil when clustering is off or before Start.
func (c *Cluster) Err() <-chan error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.watcher == nil {
		return nil
	}
	return c.watcher.Chan()
}

// storeLocation describes where the key-value store is, for an error message.
func (c *Cluster) storeLocation() string {
	switch c.cfg.KvStore {
	case storeEtcd:
		return fmt.Sprintf("store %s at %v", storeEtcd, c.cfg.EtcdEndpoints)
	case storeMemberlist:
		if len(c.cfg.JoinMembers) > 0 {
			return fmt.Sprintf("store %s, seeding off %v", storeMemberlist, c.cfg.JoinMembers)
		}
		return fmt.Sprintf("store %s, no seed members configured", storeMemberlist)
	default:
		return "store " + c.cfg.KvStore
	}
}

// usable reports why the cluster cannot answer a query, if it cannot. Callers
// hold at least the read lock.
func (c *Cluster) usable() error {
	if !c.cfg.Enabled {
		return ErrDisabled
	}
	switch c.state {
	case stateNew:
		return ErrNotStarted
	case stateStopped:
		return ErrStopped
	}
	return nil
}

// urlScheme takes the scheme a peer is most likely reachable on: the one this
// service publishes itself under. A service behind TLS has TLS-enabled peers.
func urlScheme(appURL string) string {
	if parsed, err := url.Parse(appURL); err == nil && parsed.Scheme != "" {
		return parsed.Scheme
	}
	return "http"
}

// Fqdn returns the fully qualified name of the host this replica runs on, which
// is what tells an operator behind a load balancer which replica answered.
func (c *Cluster) Fqdn() string { return c.fqdn }

// sortedNames returns the declared role names in a stable order, so that
// errors and Roles never depend on map iteration order.
func sortedNames(declared map[string]RoleSpec) []string {
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// registerOrReuse registers c, or returns the collector of the same kind
// already registered under its name.
func registerOrReuse[T prometheus.Collector](reg prometheus.Registerer, c T) (T, error) {
	err := reg.Register(c)
	var already prometheus.AlreadyRegisteredError
	if errors.As(err, &already) {
		if existing, ok := already.ExistingCollector.(T); ok {
			return existing, nil
		}
	}
	return c, err
}
