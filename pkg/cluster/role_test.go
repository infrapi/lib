package cluster

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/dskit/ring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSingleNode_leader_role(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	assert.Equal(t, spec.Name, role.Name())
	assert.Equal(t, ModeLeader, role.Mode())
	assert.Equal(t, spec.Name, role.Key())
	assert.NotNil(t, role.Ring())
	assert.NotNil(t, role.Lifecycler())

	// Start waits for ACTIVE, so this is true the moment Start returns rather
	// than after a gossip round.
	state, err := role.State()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", state)

	leader, err := role.Leader()
	require.NoError(t, err)
	assert.Equal(t, c.InstanceID(), leader.ID)
	assert.Equal(t, c.InstanceAddr(), leader.Addr)
	assert.Equal(t, "dc1", leader.Zone)
	assert.Equal(t, "ACTIVE", leader.State)
	assert.Len(t, leader.Tokens, 1)
	assert.WithinDuration(t, time.Now(), leader.Heartbeat, time.Minute)
	assert.WithinDuration(t, time.Now(), leader.RegisteredAt, time.Minute)

	isLeader, err := role.IsLeader()
	require.NoError(t, err)
	assert.True(t, isLeader, "the only member of a leader ring is the leader")

	should, err := role.ShouldRun()
	require.NoError(t, err)
	assert.True(t, should)

	shouldKey, err := role.ShouldRunKey("anything")
	require.NoError(t, err)
	assert.True(t, shouldKey)

	members, err := role.Members()
	require.NoError(t, err)
	require.Len(t, members, 1)
	assert.Equal(t, c.InstanceID(), members[0].ID)
}

func TestSingleNode_broadcast_role(t *testing.T) {
	spec := uniqueRole(t, "coll", ModeBroadcast)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	should, err := role.ShouldRun()
	require.NoError(t, err)
	assert.True(t, should, "a healthy member of a broadcast ring always acts")

	// There is no leader to speak of, and saying so is better than inventing
	// one.
	_, err = role.Leader()
	assert.ErrorIs(t, err, ErrWrongMode)

	_, err = role.IsLeader()
	assert.ErrorIs(t, err, ErrWrongMode)
}

// Queries made after the cluster stopped must say so rather than read a
// half-torn-down ring.
func TestRole_queries_after_stop(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)
	require.NoError(t, c.Stop(context.Background()))

	_, err = role.Leader()
	assert.ErrorIs(t, err, ErrStopped)

	_, err = role.Members()
	assert.ErrorIs(t, err, ErrStopped)

	_, err = role.State()
	assert.ErrorIs(t, err, ErrStopped)

	_, err = role.ShouldRun()
	assert.ErrorIs(t, err, ErrStopped)
}

func TestShouldRun_rejects_an_unknown_mode(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	// Reach past the constructor, which refuses this, to prove the predicate
	// itself fails closed rather than acting on a mode it cannot interpret.
	role.spec.Mode = Mode(99)

	should, err := role.ShouldRun()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBadMode)
	assert.False(t, should, "an uninterpretable mode must never act")
}

// Two replicas on the in-memory store genuinely see each other: it is a
// process-wide map, so this exercises the real ring logic with no sockets.
func TestTwoNodes_elect_exactly_one_leader(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)

	first := startTestCluster(t, nil, spec)
	second := startTestCluster(t, nil, spec)

	firstRole, err := first.Role(spec.Name)
	require.NoError(t, err)
	secondRole, err := second.Role(spec.Name)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a, errA := firstRole.Members()
		b, errB := secondRole.Members()
		return errA == nil && errB == nil && len(a) == 2 && len(b) == 2
	}, 10*time.Second, 50*time.Millisecond, "both replicas should see two members")

	// Asserted as one condition, never as two assertions: evaluating them
	// separately would compare two different ring snapshots and could see zero
	// or two leaders for no good reason.
	require.Eventually(t, func() bool {
		a, errA := firstRole.IsLeader()
		b, errB := secondRole.IsLeader()
		return errA == nil && errB == nil && (a != b)
	}, 10*time.Second, 50*time.Millisecond, "exactly one replica should be the leader")

	// Both replicas agree on who it is.
	leaderA, err := firstRole.Leader()
	require.NoError(t, err)
	leaderB, err := secondRole.Leader()
	require.NoError(t, err)
	assert.Equal(t, leaderA.ID, leaderB.ID)
}

// Every healthy member of a broadcast ring acts, which is the whole point of
// the mode.
func TestTwoNodes_broadcast_runs_everywhere(t *testing.T) {
	spec := uniqueRole(t, "coll", ModeBroadcast)

	first := startTestCluster(t, nil, spec)
	second := startTestCluster(t, nil, spec)

	firstRole, err := first.Role(spec.Name)
	require.NoError(t, err)
	secondRole, err := second.Role(spec.Name)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a, errA := firstRole.ShouldRun()
		b, errB := secondRole.ShouldRun()
		return errA == nil && errB == nil && a && b
	}, 10*time.Second, 50*time.Millisecond, "both replicas of a broadcast role should act")
}

// Leaving the ring on shutdown hands leadership over. Unregistering rather than
// going LEAVING is what makes a planned failover immediate.
func TestTwoNodes_leadership_moves_on_shutdown(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	overrides := map[string]string{
		"INFRAPI_CLUSTER_KEEP_INSTANCE_IN_RING_ON_SHUTDOWN": "false",
	}

	first := startTestCluster(t, overrides, spec)
	second := startTestCluster(t, overrides, spec)

	firstRole, err := first.Role(spec.Name)
	require.NoError(t, err)
	secondRole, err := second.Role(spec.Name)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a, errA := firstRole.IsLeader()
		b, errB := secondRole.IsLeader()
		return errA == nil && errB == nil && (a != b)
	}, 10*time.Second, 50*time.Millisecond)

	leaderIsFirst, err := firstRole.IsLeader()
	require.NoError(t, err)

	dying, survivor, survivorRole := second, first, firstRole
	if leaderIsFirst {
		dying, survivor, survivorRole = first, second, secondRole
	}

	require.NoError(t, dying.Stop(context.Background()))

	require.Eventually(t, func() bool {
		isLeader, err := survivorRole.IsLeader()
		return err == nil && isLeader
	}, 10*time.Second, 50*time.Millisecond, "the survivor should take over once the leader has left")

	leader, err := survivorRole.Leader()
	require.NoError(t, err)
	assert.Equal(t, survivor.InstanceID(), leader.ID)
}

// The other half of that trade: a replica that keeps its ring entry on shutdown
// stays visible as LEAVING, which is what limits leader churn during a rolling
// restart.
func TestTwoNodes_a_leaving_replica_stays_visible(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	overrides := map[string]string{
		"INFRAPI_CLUSTER_KEEP_INSTANCE_IN_RING_ON_SHUTDOWN": "true",
	}

	first := startTestCluster(t, overrides, spec)
	second := startTestCluster(t, overrides, spec)

	firstRole, err := first.Role(spec.Name)
	require.NoError(t, err)
	secondRole, err := second.Role(spec.Name)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		members, err := secondRole.Members()
		return err == nil && len(members) == 2
	}, 10*time.Second, 50*time.Millisecond)

	_ = firstRole
	require.NoError(t, first.Stop(context.Background()))

	require.Eventually(t, func() bool {
		members, err := secondRole.Members()
		if err != nil {
			return false
		}
		for _, member := range members {
			if member.ID == first.InstanceID() && member.State == "LEAVING" {
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond, "the departed replica should still be listed as LEAVING")
}

// The gossip path, with real sockets. The listening port is read back from the
// store rather than guessed: binding zero and asking afterwards has no window
// in which another process can take the port, which pre-binding and closing
// would.
func TestTwoNodes_gossip_convergence(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)

	gossip := map[string]string{
		"INFRAPI_CLUSTER_KV_STORE":                 storeMemberlist,
		"INFRAPI_CLUSTER_GOSSIP_BIND_ADDRS":        `["127.0.0.1"]`,
		"INFRAPI_CLUSTER_GOSSIP_BIND_PORT":         "0",
		"INFRAPI_CLUSTER_GOSSIP_INTERVAL":          "20ms",
		"INFRAPI_CLUSTER_PUSH_PULL_INTERVAL":       "200ms",
		"INFRAPI_CLUSTER_GOSSIP_TO_THE_DEAD_TIME":  "1s",
		"INFRAPI_CLUSTER_OBSOLETE_ENTRIES_TIMEOUT": "1s",
		"INFRAPI_CLUSTER_JOIN_MIN_BACKOFF":         "50ms",
		"INFRAPI_CLUSTER_JOIN_MAX_BACKOFF":         "200ms",
		"INFRAPI_CLUSTER_JOIN_MAX_RETRIES":         "5",
		// A failed join must fail Start loudly. Without this, two replicas that
		// never met would each form a cluster of one, elect themselves, and
		// make the assertion below flap instead of failing.
		"INFRAPI_CLUSTER_ABORT_IF_JOIN_FAILS": "true",
	}

	first := startTestCluster(t, gossip, spec)

	port, err := first.GossipPort()
	require.NoError(t, err)
	require.NotZero(t, port, "an ephemeral bind port must be reported back")

	joining := map[string]string{}
	for key, value := range gossip {
		joining[key] = value
	}
	joining["INFRAPI_CLUSTER_JOIN_MEMBERS"] = fmt.Sprintf(`["127.0.0.1:%d"]`, port)

	second := startTestCluster(t, joining, spec)

	firstRole, err := first.Role(spec.Name)
	require.NoError(t, err)
	secondRole, err := second.Role(spec.Name)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a, errA := firstRole.Members()
		b, errB := secondRole.Members()
		return errA == nil && errB == nil && len(a) == 2 && len(b) == 2
	}, 15*time.Second, 100*time.Millisecond, "the two replicas should find each other over gossip")

	require.Eventually(t, func() bool {
		a, errA := firstRole.IsLeader()
		b, errB := secondRole.IsLeader()
		return errA == nil && errB == nil && (a != b)
	}, 15*time.Second, 100*time.Millisecond, "exactly one replica should be the leader")

	kv, err := first.MemberlistKV()
	require.NoError(t, err)
	assert.NotNil(t, kv)
}

func TestRun_rejects_bad_arguments(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	err = role.Run(context.Background(), 0, func(context.Context) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "interval must be positive")

	err = role.Run(context.Background(), time.Second, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs a function to call")
}

// The leader runs the gated action; nobody else does.
func TestRun_calls_the_action_on_the_leader(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- role.Run(ctx, 20*time.Millisecond, func(context.Context) error {
			calls.Add(1)
			return nil
		})
	}()

	require.Eventually(t, func() bool { return calls.Load() >= 2 }, 5*time.Second, 20*time.Millisecond)

	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
}

// An error from the action is logged and the loop carries on: a transient
// failure must not kill a service's control loop.
func TestRun_survives_an_action_error(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- role.Run(ctx, 20*time.Millisecond, func(context.Context) error {
			calls.Add(1)
			return assert.AnError
		})
	}()

	require.Eventually(t, func() bool { return calls.Load() >= 3 }, 5*time.Second, 20*time.Millisecond)

	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
}

// A replica that is not eligible must stay passive, and a ring view that cannot
// be read must be treated as "do not act" rather than as permission.
func TestRun_stays_passive_when_not_eligible(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	// Stopping the cluster makes every query report ErrStopped, which is the
	// unreadable-view case.
	require.NoError(t, c.Stop(context.Background()))

	var calls atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err = role.Run(ctx, 20*time.Millisecond, func(context.Context) error {
		calls.Add(1)
		return nil
	})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Zero(t, calls.Load(), "an unreadable ring view must never be read as permission to act")
}

// The action's context is cancelled once this replica stops being eligible, so
// a long action can abandon work it is no longer entitled to finish.
func TestRun_cancels_the_action_when_eligibility_is_lost(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- role.Run(ctx, 20*time.Millisecond, func(runCtx context.Context) error {
			select {
			case <-started:
			default:
				close(started)
			}
			<-runCtx.Done()
			return runCtx.Err()
		})
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the action never started")
	}

	// Losing the ring is how a real replica loses eligibility mid-action.
	require.NoError(t, c.Stop(context.Background()))

	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
}

// A ring with members but none the mode accepts has no leader. That is the
// normal state of a partitioned or draining ring, and the right answer is
// ErrNoLeader rather than an arbitrary member.
func TestLeader_reports_no_leader_when_nobody_is_eligible(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	// JOINING is outside the states a leader role accepts, so this replica
	// leaves the eligible set without leaving the ring.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, role.Lifecycler().ChangeState(ctx, ring.JOINING))

	require.Eventually(t, func() bool {
		_, err := role.Leader()
		return errors.Is(err, ErrNoLeader)
	}, 10*time.Second, 50*time.Millisecond, "a ring with no eligible member has no leader")

	isLeader, err := role.IsLeader()
	assert.ErrorIs(t, err, ErrNoLeader)
	assert.False(t, isLeader, "an unreadable leader must never be read as being us")

	// And the predicate refuses to act rather than guessing.
	should, err := role.ShouldRun()
	assert.ErrorIs(t, err, ErrNoLeader)
	assert.False(t, should)
}

// A broadcast role whose membership cannot be read must also refuse to act.
func TestShouldRun_broadcast_refuses_on_an_unreadable_ring(t *testing.T) {
	spec := uniqueRole(t, "coll", ModeBroadcast)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)
	require.NoError(t, c.Stop(context.Background()))

	should, err := role.ShouldRun()
	assert.ErrorIs(t, err, ErrStopped)
	assert.False(t, should)
}

// A member the mode does not accept is absent from the membership a broadcast
// role reasons about, so a draining replica stops working.
func TestShouldRun_broadcast_excludes_an_ineligible_member(t *testing.T) {
	spec := uniqueRole(t, "coll", ModeBroadcast)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, role.Lifecycler().ChangeState(ctx, ring.LEAVING))

	require.Eventually(t, func() bool {
		should, err := role.ShouldRun()
		return err == nil && !should
	}, 10*time.Second, 50*time.Millisecond, "a leaving replica of a broadcast role must stop acting")
}

// AllMembers is the status page's view: it lists a replica the mode excludes,
// which is precisely the one worth seeing. Members omits it.
func TestAllMembers_lists_excluded_replicas(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	all, err := role.AllMembers(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.True(t, all[0].Healthy)
	assert.Equal(t, c.InstanceID(), all[0].ID)

	// JOINING is outside the states a leader role accepts.
	require.NoError(t, role.Lifecycler().ChangeState(ctx, ring.JOINING))

	require.Eventually(t, func() bool {
		all, err := role.AllMembers(ctx)
		return err == nil && len(all) == 1 && !all[0].Healthy
	}, 10*time.Second, 50*time.Millisecond, "the excluded replica must still be listed")

	// The eligible view drops it, which is what application logic must see.
	// Either it empties, or it reports the ring as unusable: both mean "do not
	// act", and which one arrives depends on the snapshot the ring has.
	require.Eventually(t, func() bool {
		members, err := role.Members()
		return err != nil || len(members) == 0
	}, 10*time.Second, 50*time.Millisecond, "an excluded replica must not appear in the eligible view")
}

func TestAllMembers_after_stop(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)
	require.NoError(t, c.Stop(context.Background()))

	_, err = role.AllMembers(context.Background())
	assert.ErrorIs(t, err, ErrStopped)
}

// One mesh, one ring per data centre: a role named after the zone gives each
// zone its own leader election while every replica stays in one cluster.
func TestRoles_give_one_leader_per_zone(t *testing.T) {
	dc1Role := RoleSpec{Name: t.Name() + "-scheduler-dc1", Mode: ModeLeader}
	dc2Role := RoleSpec{Name: t.Name() + "-scheduler-dc2", Mode: ModeLeader}

	// Two replicas in dc1, one in dc2. Each declares both roles, as a single
	// binary would, and joins only the one for its own zone.
	both := []RoleSpec{dc1Role, dc2Role}

	a := startTestCluster(t, map[string]string{"INFRAPI_CLUSTER_ROLES": `["` + dc1Role.Name + `"]`}, both...)
	b := startTestCluster(t, map[string]string{"INFRAPI_CLUSTER_ROLES": `["` + dc1Role.Name + `"]`}, both...)
	c := startTestCluster(t, map[string]string{"INFRAPI_CLUSTER_ROLES": `["` + dc2Role.Name + `"]`}, both...)

	dc1a, err := a.Role(dc1Role.Name)
	require.NoError(t, err)
	dc1b, err := b.Role(dc1Role.Name)
	require.NoError(t, err)
	dc2c, err := c.Role(dc2Role.Name)
	require.NoError(t, err)

	// dc1 elects exactly one leader among its two replicas, once each sees
	// the other: until then each is alone in its view, and its own leader.
	require.Eventually(t, func() bool {
		x, errX := dc1a.IsLeader()
		y, errY := dc1b.IsLeader()
		ma, errMA := dc1a.Members()
		mb, errMB := dc1b.Members()
		return errX == nil && errY == nil && errMA == nil && errMB == nil &&
			len(ma) == 2 && len(mb) == 2 && x != y
	}, 10*time.Second, 50*time.Millisecond, "dc1 should elect one of its own two replicas")

	// dc2 elects its own, independently, and it is the only member of its ring.
	require.Eventually(t, func() bool {
		isLeader, err := dc2c.IsLeader()
		members, mErr := dc2c.Members()
		return err == nil && isLeader && mErr == nil && len(members) == 1
	}, 10*time.Second, 50*time.Millisecond, "dc2 should elect its own leader, alone in its ring")

	// The dc1 ring never contains the dc2 replica, and vice versa.
	dc1Members, err := dc1a.Members()
	require.NoError(t, err)
	assert.Len(t, dc1Members, 2)
	for _, m := range dc1Members {
		assert.NotEqual(t, c.InstanceID(), m.ID, "the dc2 replica must not be in the dc1 ring")
	}

	// And a replica can report on a ring it does not belong to? No: it only
	// joined its own, so the other role is simply not available to it.
	_, err = c.Role(dc1Role.Name)
	assert.ErrorIs(t, err, ErrRoleNotJoined)
}
