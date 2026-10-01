package cluster

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/go-kit/log"
	"github.com/grafana/dskit/kv"
	"github.com/grafana/dskit/ring"
	"github.com/sirupsen/logrus"
)

// RoleSpec declares one coordination ring: what it is called, how its members
// divide work, and the ring parameters the mode does not already imply.
//
// A role is declared in Go rather than in the dotenv because the mode decides
// what the application code does. The dotenv only lists which of the declared
// roles this replica joins.
type RoleSpec struct {
	// Name is the role identity. It is the dskit ring name, the value of the
	// "name" label on every ring metric, and the default ring key.
	Name string

	// Mode is the coordination mode, and so decides what ShouldRun means.
	Mode Mode

	// Key overrides the key the ring is stored under, below
	// INFRAPI_CLUSTER_KV_PREFIX. Empty means Name. Set it only to keep
	// compatibility with a ring an earlier deployment already wrote.
	Key string

	// NumTokens overrides how many ring tokens this replica registers. Zero
	// takes the mode's own count, which is what a leader or broadcast role
	// wants.
	NumTokens int
}

// ringKey returns the key this role's ring is stored under.
func (s RoleSpec) ringKey() string {
	if s.Key != "" {
		return s.Key
	}
	return s.Name
}

// Role is one coordination ring this replica registered in.
type Role struct {
	spec RoleSpec
	key  string
	mode modeSpec

	cluster    *Cluster
	ring       *ring.Ring
	lifecycler *ring.BasicLifecycler
}

// start builds and starts this role's lifecycler and ring, then waits until
// this replica is ACTIVE in the ring.
//
// The lifecycler starts before the ring on purpose: the ring takes a snapshot
// of the key-value store when it starts, and with the lifecycler already
// registered that first snapshot contains this replica. ShouldRun is therefore
// meaningful the moment Start returns, instead of reporting an empty ring for a
// gossip round.
func (r *Role) start(ctx context.Context) error {
	c := r.cluster
	name := r.spec.Name

	logger := log.With(c.gokit, "role", name)
	lifecyclerLogger := log.With(logger, "component", "lifecycler")
	ringLogger := log.With(logger, "component", "ring")

	// Each key-value client needs its own kv_name label: the request duration
	// histogram is labelled by backend only, so two clients on one registerer
	// would be the same collector registered twice.
	lifecyclerStore, err := kv.NewClient(c.dskit.kv, ring.GetCodec(),
		kv.RegistererWithKVName(c.reg, name+"-lifecycler"), lifecyclerLogger)
	if err != nil {
		return fmt.Errorf("lifecycler key-value client: %w", err)
	}

	ringStore, err := kv.NewClient(c.dskit.kv, ring.GetCodec(),
		kv.RegistererWithKVName(c.reg, name+"-ring"), ringLogger)
	if err != nil {
		return fmt.Errorf("ring key-value client: %w", err)
	}

	lifecyclerCfg := c.dskit.lifecyclerConfigFor(r.mode)
	if r.spec.NumTokens > 0 {
		lifecyclerCfg.NumTokens = r.spec.NumTokens
	}

	lifecycler, err := ring.NewBasicLifecycler(lifecyclerCfg, name, r.key, lifecyclerStore,
		r.delegate(lifecyclerCfg, lifecyclerLogger), lifecyclerLogger, c.reg)
	if err != nil {
		return fmt.Errorf("lifecycler: %w", err)
	}

	ringSvc, err := ring.NewWithStoreClientAndStrategy(c.dskit.ringConfigFor(r.mode), name, r.key,
		ringStore, ring.NewDefaultReplicationStrategy(), c.reg, ringLogger)
	if err != nil {
		return fmt.Errorf("ring: %w", err)
	}

	if err := c.startService(ctx, lifecycler); err != nil {
		return fmt.Errorf("start lifecycler: %w", err)
	}
	c.started = append(c.started, lifecycler)
	c.watcher.WatchService(lifecycler)

	if err := c.startService(ctx, ringSvc); err != nil {
		return fmt.Errorf("start ring: %w", err)
	}
	c.started = append(c.started, ringSvc)
	c.watcher.WatchService(ringSvc)

	r.lifecycler = lifecycler
	r.ring = ringSvc

	// Turns "Start returned" into a readiness guarantee. This retries
	// indefinitely, which is why the caller's context must carry a deadline.
	if err := ring.WaitInstanceState(ctx, ringSvc, c.cfg.InstanceID, ring.ACTIVE); err != nil {
		return fmt.Errorf("wait for ACTIVE: %w", err)
	}

	return nil
}

// delegate builds the lifecycler delegate chain, innermost first: register as
// ACTIVE, set LEAVING when stopping, and forget a replica whose heartbeat has
// been stale for long enough.
//
// The forget window is a multiple of the heartbeat timeout, not of the
// heartbeat period. A multiple of the period can be shorter than the window in
// which a replica merely looks unhealthy, which turns one long garbage
// collection pause into a leader change.
func (r *Role) delegate(cfg ring.BasicLifecyclerConfig, logger log.Logger) ring.BasicLifecyclerDelegate {
	var delegate ring.BasicLifecyclerDelegate = ring.NewInstanceRegisterDelegate(ring.ACTIVE, cfg.NumTokens)
	delegate = ring.NewLeaveOnStoppingDelegate(delegate, logger)

	if periods := r.cluster.cfg.AutoForgetUnhealthyPeriods; periods > 0 {
		forgetPeriod := time.Duration(periods) * cfg.HeartbeatTimeout
		delegate = ring.NewAutoForgetDelegate(forgetPeriod, delegate, logger)
	}

	return delegate
}

// Name returns the role name.
func (r *Role) Name() string { return r.spec.Name }

// Mode returns the coordination mode the service declared for this role.
func (r *Role) Mode() Mode { return r.spec.Mode }

// Key returns the key this role's ring is stored under, below the configured
// key-value prefix.
func (r *Role) Key() string { return r.key }

// Ring returns the underlying dskit ring, for shuffle sharding, replication
// sets and everything else this package does not wrap.
func (r *Role) Ring() *ring.Ring { return r.ring }

// Lifecycler returns the underlying dskit lifecycler, for draining this replica
// with ChangeState and for token introspection.
func (r *Role) Lifecycler() *ring.BasicLifecycler { return r.lifecycler }

// ShouldRun reports whether this replica must act right now.
//
// In ModeLeader that means owning the leader token; in ModeBroadcast it means
// being a healthy member of the role ring. A non-nil error must be read as "do
// not act": it means the ring view is unusable, not that this replica was
// excluded.
func (r *Role) ShouldRun() (bool, error) {
	switch r.spec.Mode {
	case ModeLeader:
		return r.IsLeader()
	case ModeBroadcast:
		members, err := r.Members()
		if err != nil {
			return false, err
		}
		for _, member := range members {
			if member.ID == r.cluster.cfg.InstanceID {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("%w: %s", ErrBadMode, r.spec.Mode)
	}
}

// ShouldRunKey reports whether this replica must act on one particular key.
//
// The leader and broadcast modes ignore the key and answer like ShouldRun. It
// exists so that a sharded mode, where the answer does depend on the key, can
// be added without changing any of these signatures.
func (r *Role) ShouldRunKey(_ string) (bool, error) {
	return r.ShouldRun()
}

// IsLeader reports whether this replica owns the leader token. It reports
// ErrWrongMode unless the role is in ModeLeader.
func (r *Role) IsLeader() (bool, error) {
	leader, err := r.Leader()
	if err != nil {
		return false, err
	}
	return leader.ID == r.cluster.cfg.InstanceID, nil
}

// Leader returns the member that owns the leader token. It reports ErrWrongMode
// unless the role is in ModeLeader, and ErrNoLeader when the ring has no single
// healthy owner, which is the normal state of an empty or partitioned ring.
func (r *Role) Leader() (Member, error) {
	if r.spec.Mode != ModeLeader {
		return Member{}, fmt.Errorf("%w: role %q is in %s mode, there is no leader", ErrWrongMode, r.spec.Name, r.spec.Mode)
	}
	if err := r.usable(); err != nil {
		return Member{}, err
	}

	set, err := r.ring.Get(leaderToken, r.mode.op, nil, nil, nil)
	if err != nil {
		return Member{}, fmt.Errorf("%w: no healthy owner for token %d in role %q: %w", ErrNoLeader, leaderToken, r.spec.Name, err)
	}
	if len(set.Instances) != 1 {
		return Member{}, fmt.Errorf("%w: %d owners for token %d in role %q, expected exactly one",
			ErrNoLeader, len(set.Instances), leaderToken, r.spec.Name)
	}

	return newMember(set.Instances[0], true), nil
}

// Members returns the replicas the role's coordination mode considers eligible
// right now, sorted by ID.
//
// It is the membership an action should reason about, not the full ring: a
// replica whose heartbeat has gone stale is deliberately absent. For the
// complete picture, unhealthy and leaving replicas included, read the ring
// status page this package mounts.
func (r *Role) Members() ([]Member, error) {
	if err := r.usable(); err != nil {
		return nil, err
	}

	set, err := r.ring.GetAllHealthy(r.mode.op)
	if err != nil {
		return nil, fmt.Errorf("cluster: read role %q membership: %w", r.spec.Name, err)
	}

	members := make([]Member, 0, len(set.Instances))
	for _, instance := range set.Instances {
		members = append(members, newMember(instance, true))
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })

	return members, nil
}

// AllMembers returns every replica registered in the role ring, eligible or
// not, sorted by ID, with Member.Healthy saying which is which.
//
// It is what a status page needs: a ring whose unhealthy member is invisible
// hides the one thing worth looking at. Application logic wants Members
// instead, which answers with the set an action may reason about.
//
// Unlike Members, this reads the key-value store rather than the snapshot the
// ring already watches, so it costs a round trip and sees the store as it is
// right now.
func (r *Role) AllMembers(ctx context.Context) ([]Member, error) {
	if err := r.usable(); err != nil {
		return nil, err
	}

	value, err := r.ring.KVClient.Get(ctx, r.key)
	if err != nil {
		return nil, fmt.Errorf("cluster: read role %q ring: %w", r.spec.Name, err)
	}
	if value == nil {
		return []Member{}, nil
	}

	desc, ok := value.(*ring.Desc)
	if !ok {
		return nil, fmt.Errorf("cluster: role %q ring holds %T, expected a ring description", r.spec.Name, value)
	}

	timeout := r.cluster.cfg.HeartbeatTimeout
	now := time.Now()

	members := make([]Member, 0, len(desc.Ingesters))
	for id, instance := range desc.Ingesters {
		// The map key is the identity; the embedded one can be empty on an
		// entry written by an older version.
		instance.Id = id
		members = append(members, newMember(instance, instance.IsHealthy(r.mode.op, timeout, now)))
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })

	return members, nil
}

// State returns the ring state this replica currently advertises.
func (r *Role) State() (string, error) {
	if err := r.usable(); err != nil {
		return "", err
	}
	return r.lifecycler.GetState().String(), nil
}

// Run calls fn every interval for as long as ShouldRun says this replica must
// act, and returns when ctx is done. Two runs never overlap.
//
// fn receives a context that is cancelled as soon as this replica stops being
// eligible, so a long action can abandon work it is no longer entitled to
// finish. That cancellation is cooperative and nothing more: the ring issues no
// fencing token, so during a partition a deposed leader whose fn ignores its
// context can still be acting while the new one starts. The overlap is bounded
// by INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT plus one watchdog interval, and nothing
// closes it. fn must therefore be idempotent, or fenced by whatever it writes
// to: a compare-and-swap, a conditional write, a unique constraint.
//
// An error from fn is logged and the loop continues, and so is a ShouldRun that
// cannot be answered: a transient empty ring during a gossip partition must not
// kill a service's control loop.
func (r *Role) Run(ctx context.Context, interval time.Duration, fn func(context.Context) error) error {
	if interval <= 0 {
		return fmt.Errorf("cluster: role %q: Run interval must be positive, got %s", r.spec.Name, interval)
	}
	if fn == nil {
		return fmt.Errorf("cluster: role %q: Run needs a function to call", r.spec.Name)
	}

	logger := r.cluster.logger.WithField("role", r.spec.Name)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			should, err := r.ShouldRun()
			if err != nil {
				logger.WithError(err).Warn("cluster: cannot tell whether this instance should act, skipping")
				continue
			}
			if !should {
				continue
			}
			r.runOnce(ctx, logger, fn)
		}
	}
}

// runOnce calls fn with a context that is cancelled if this replica stops being
// eligible while fn is running.
func (r *Role) runOnce(ctx context.Context, logger logrus.FieldLogger, fn func(context.Context) error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	watchdog := make(chan struct{})
	go func() {
		defer close(watchdog)
		r.watchEligibility(runCtx, cancel)
	}()

	if err := fn(runCtx); err != nil {
		logger.WithError(err).Error("cluster: gated action failed")
	}

	cancel()
	<-watchdog
}

// watchEligibility cancels the running action the first time this replica stops
// being eligible. It polls at the heartbeat period, which is the fastest rate
// at which the answer can change.
func (r *Role) watchEligibility(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(r.cluster.cfg.HeartbeatPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			should, err := r.ShouldRun()
			if err == nil && !should {
				cancel()
				return
			}
		}
	}
}

// usable reports why this role cannot answer a query, if it cannot.
func (r *Role) usable() error {
	r.cluster.mu.RLock()
	defer r.cluster.mu.RUnlock()

	return r.cluster.usable()
}
