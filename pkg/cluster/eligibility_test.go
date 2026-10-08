package cluster

import (
	"context"
	"errors"
	"github.com/prometheus/client_golang/prometheus"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastChecks evaluates often and flips on the first result, so a test can
// watch the election follow a check. A new map each time: the helpers add the
// role names to it.
func fastChecks() map[string]string {
	return map[string]string{
		"INFRAPI_CLUSTER_LEADER_CHECK_INTERVAL":  "100ms",
		"INFRAPI_CLUSTER_LEADER_CHECK_TIMEOUT":   "1s",
		"INFRAPI_CLUSTER_LEADER_CHECK_THRESHOLD": "1",
	}
}

// replicaWithSwitch is a replica whose LeaderCheck is an on/off switch.
func replicaWithSwitch(t *testing.T, spec RoleSpec, overrides map[string]string) (*Role, *atomic.Bool) {
	t.Helper()
	var may atomic.Bool
	may.Store(true)
	spec.LeaderCheck = func(context.Context) error {
		if may.Load() {
			return nil
		}
		return errors.New("switched off")
	}
	c := startTestCluster(t, overrides, spec)
	role, err := c.Role(spec.Name)
	require.NoError(t, err)
	return role, &may
}

// leaderSeenByAll waits until every role agrees on a leader, and returns it —
// or "" when they agree there is none.
func leaderSeenByAll(t *testing.T, roles ...*Role) string {
	t.Helper()
	var agreed string
	require.Eventually(t, func() bool {
		seen := map[string]bool{}
		for _, r := range roles {
			m, err := r.Leader()
			if errors.Is(err, ErrNoLeader) {
				seen[""] = true
				continue
			}
			if err != nil {
				return false
			}
			seen[m.ID] = true
		}
		if len(seen) != 1 {
			return false
		}
		for id := range seen {
			agreed = id
		}
		return true
	}, 10*time.Second, 50*time.Millisecond)
	return agreed
}

// waitForLeader waits until every role sees want as the leader ("" for none).
func waitForLeader(t *testing.T, want string, roles ...*Role) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, r := range roles {
			m, err := r.Leader()
			switch {
			case want == "" && !errors.Is(err, ErrNoLeader):
				return false
			case want != "" && (err != nil || m.ID != want):
				return false
			}
		}
		return true
	}, 10*time.Second, 50*time.Millisecond, "every replica sees %q as the leader", want)
}

func TestLeaderChecks_move_the_leadership(t *testing.T) {
	spec := uniqueRole(t, "writer", ModeLeader)
	a, mayA := replicaWithSwitch(t, spec, fastChecks())
	b, mayB := replicaWithSwitch(t, spec, fastChecks())
	switches := map[string]*atomic.Bool{a.cluster.InstanceID(): mayA, b.cluster.InstanceID(): mayB}

	// Both may lead: one does, the same for both.
	first := leaderSeenByAll(t, a, b)
	require.NotEmpty(t, first)

	// The leader's check fails: the other leads, and the ring says why.
	second := a.cluster.InstanceID()
	if second == first {
		second = b.cluster.InstanceID()
	}
	switches[first].Store(false)
	waitForLeader(t, second, a, b)
	members, err := a.Members()
	require.NoError(t, err)
	for _, m := range members {
		assert.Equal(t, m.ID != first, m.MayLead, "member %s", m.ID)
	}

	// Nobody may lead: no leader, never a fallback.
	switches[second].Store(false)
	waitForLeader(t, "", a, b)
	should, err := a.ShouldRun()
	assert.ErrorIs(t, err, ErrNoLeader, "no leader is an error, read as \"do not act\"")
	assert.False(t, should)

	// One comes back: it leads.
	switches[first].Store(true)
	waitForLeader(t, first, a, b)

	// This replica's own standing, as the status pages show it.
	for _, r := range []*Role{a, b} {
		may, reason, checked := r.LeaderEligibility()
		assert.Equal(t, r.cluster.InstanceID() == first, may)
		assert.False(t, checked.IsZero())
		if !may {
			assert.Contains(t, reason, "switched off")
		}
	}
}

// The configured checks: a maintenance flag takes a replica out of the
// election, and the leader is still the smallest token among the others.
func TestLeaderChecks_from_the_configuration(t *testing.T) {
	spec := uniqueRole(t, "writer", ModeLeader)
	flag := filepath.Join(t.TempDir(), "maintenance")
	withFlag := fastChecks()
	withFlag["INFRAPI_CLUSTER_LEADER_CHECKS"] = `["!file://` + flag + `"]`

	a := startTestCluster(t, withFlag, spec)
	b := startTestCluster(t, nil, spec) // no checks: may always lead
	ra, err := a.Role(spec.Name)
	require.NoError(t, err)
	rb, err := b.Role(spec.Name)
	require.NoError(t, err)

	// Without the flag, the leader is the owner of the smallest token.
	leader := leaderSeenByAll(t, ra, rb)
	members, err := ra.Members()
	require.NoError(t, err)
	slices.SortFunc(members, func(x, y Member) int { return int(slices.Min(x.Tokens)) - int(slices.Min(y.Tokens)) })
	assert.Equal(t, members[0].ID, leader)

	require.NoError(t, os.WriteFile(flag, nil, 0o600))
	require.Eventually(t, func() bool {
		may, _, _ := ra.LeaderEligibility()
		return !may
	}, 10*time.Second, 50*time.Millisecond, "a sees its maintenance flag")
	waitForLeader(t, b.InstanceID(), ra, rb)
	require.Eventually(t, func() bool {
		members, err := rb.Members()
		if err != nil {
			return false
		}
		for _, m := range members {
			if m.MayLead != (m.ID == b.InstanceID()) {
				return false
			}
		}
		return true
	}, 10*time.Second, 50*time.Millisecond, "b sees a as unable to lead")
	_, reason, _ := ra.LeaderEligibility()
	assert.Contains(t, reason, "!file://"+flag)

	// The status page says so too.
	view := a.roleView(spec.Name, "")
	assert.False(t, view.MayLead)
	assert.Contains(t, view.LeaderCheckFail, "!file://"+flag)
}

func TestLeaderChecks_refuse_a_check_that_does_not_parse(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_LEADER_CHECKS": `["ftp://x:21"]`})
	_, err := New(testAppConfig(), cfg, Options{Logger: testLogger()})
	assert.ErrorContains(t, err, "ftp://x:21")
}

// A role's own checks apply to it alone: one replica in maintenance for one
// role still leads, or may lead, the other.
func TestLeaderChecks_of_one_role(t *testing.T) {
	purger, writer := uniqueRole(t, "purger", ModeLeader), uniqueRole(t, "writer", ModeLeader)
	flag := filepath.Join(t.TempDir(), "maintenance")
	require.NoError(t, os.WriteFile(flag, nil, 0o600))
	overrides := fastChecks()
	overrides["INFRAPI_CLUSTER_ROLE_LEADER_CHECKS"] = `{"` + purger.Name + `": ["!file://` + flag + `"]}`

	c := startTestCluster(t, overrides, purger, writer)
	rp, err := c.Role(purger.Name)
	require.NoError(t, err)
	rw, err := c.Role(writer.Name)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		may, _, _ := rp.LeaderEligibility()
		return !may
	}, 10*time.Second, 50*time.Millisecond, "the purger sees its flag")
	_, reason, _ := rp.LeaderEligibility()
	assert.Contains(t, reason, "!file://"+flag)
	may, _, _ := rw.LeaderEligibility()
	assert.True(t, may, "the writer has no check of its own")
	waitForLeader(t, c.InstanceID(), rw)
}

func TestLeaderChecks_of_one_role_name_a_leader_role_it_joins(t *testing.T) {
	leader, broadcast := RoleSpec{Name: "purger", Mode: ModeLeader}, RoleSpec{Name: "cache", Mode: ModeBroadcast}
	for checks, want := range map[string]string{
		`{"compactor": ["tcp://db:5432"]}`: `"compactor"`,
		`{"cache": ["tcp://db:5432"]}`:     "nobody leads it",
		`{"purger": ["ftp://x:21"]}`:       "ftp://x:21",
	} {
		cfg := testClusterConfig(t, map[string]string{
			"INFRAPI_CLUSTER_ROLES":              `["purger", "cache"]`,
			"INFRAPI_CLUSTER_ROLE_LEADER_CHECKS": checks,
		})
		_, err := New(testAppConfig(), cfg, Options{Roles: []RoleSpec{leader, broadcast}, Logger: testLogger(),
			Registerer: prometheus.NewRegistry()})
		assert.ErrorContains(t, err, want, checks)
	}
}
