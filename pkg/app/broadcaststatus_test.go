package app

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func status(id string, at time.Time, node, state string, updated time.Time) *broadcastStatus {
	return &broadcastStatus{Writes: map[string]*writeRecord{id: {At: at, By: "node-1", Method: "POST", Path: "/x",
		Replicas: map[string]replicaRecord{node: {State: state, Updated: updated}}}}}
}

func encoded(t *testing.T, s *broadcastStatus) string {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return string(b)
}

// The gossip needs a merge that ends in the same state whatever the order:
// idempotent, commutative, associative. A settled state wins over pending.
func TestBroadcastStatusMerge(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	a := status("w1", now, "node-2", statePending, now)
	b := status("w1", now, "node-2", stateDone, now.Add(time.Second))
	c := status("w1", now, "node-3", stateFailed, now.Add(2*time.Second))

	merge := func(parts ...*broadcastStatus) string {
		s := newBroadcastStatus()
		for _, p := range parts {
			_, err := s.Merge(p.Clone(), false)
			require.NoError(t, err)
		}
		return encoded(t, s)
	}
	assert.Equal(t, merge(a, b, c), merge(c, b, a), "commutative")
	assert.Equal(t, merge(a, b, c), merge(a, b, c, b, a), "idempotent")
	ab := newBroadcastStatus()
	_, err := ab.Merge(a.Clone(), false)
	require.NoError(t, err)
	_, err = ab.Merge(b.Clone(), false)
	require.NoError(t, err)
	assert.Equal(t, merge(a, b, c), merge(ab, c), "associative")

	s := newBroadcastStatus()
	_, err = s.Merge(merge3(t, a, b, c), false)
	require.NoError(t, err)
	assert.Equal(t, stateDone, s.Writes["w1"].Replicas["node-2"].State, "settled over pending")
	assert.Equal(t, stateFailed, s.Writes["w1"].Replicas["node-3"].State)

	change, err := s.Merge(b.Clone(), false)
	require.NoError(t, err)
	assert.Nil(t, change, "what is known already is no change")
}

func merge3(t *testing.T, parts ...*broadcastStatus) *broadcastStatus {
	t.Helper()
	s := newBroadcastStatus()
	for _, p := range parts {
		_, err := s.Merge(p.Clone(), false)
		require.NoError(t, err)
	}
	return s
}

// Old outcomes leave by themselves: past the TTL, then the oldest past the cap.
func TestBroadcastStatusExpiry(t *testing.T) {
	now := time.Now()
	s := newBroadcastStatus()
	_, err := s.Merge(status("old", now.Add(-BroadcastStatusRetention-time.Minute), "n", stateDone, now), false)
	require.NoError(t, err)
	assert.Empty(t, s.Writes, "an expired write is not taken in")

	for i := range BroadcastStatusMax + 5 {
		s.Writes[fmt.Sprintf("w%05d", i)] = &writeRecord{At: now.Add(time.Duration(i) * time.Millisecond)}
	}
	_, removed := s.RemoveTombstones(time.Time{})
	assert.Equal(t, 5, removed)
	assert.Len(t, s.Writes, BroadcastStatusMax)
	assert.NotContains(t, s.Writes, "w00000", "the oldest go first")
	assert.Contains(t, s.Writes, fmt.Sprintf("w%05d", BroadcastStatusMax+4))
}

// A change about one replica does not cover one about another: the gossip
// would drop the first otherwise.
func TestBroadcastStatusContentIsPerReplica(t *testing.T) {
	now := time.Now()
	first := &broadcastStatus{Writes: map[string]*writeRecord{"w1": {At: now, Replicas: map[string]replicaRecord{
		"node-1": {State: stateDone}, "node-2": {State: statePending}}}}}
	second := status("w1", now, "node-2", stateDone, now)
	assert.Equal(t, []string{"w1/node-1", "w1/node-2"}, first.MergeContent())
	assert.Equal(t, []string{"w1/node-2"}, second.MergeContent(), "not a superset of the first")
}

func TestBroadcastOutcome(t *testing.T) {
	rec := func(states ...string) *writeRecord {
		w := &writeRecord{Replicas: map[string]replicaRecord{}}
		for i, st := range states {
			w.Replicas[fmt.Sprintf("node-%d", i)] = replicaRecord{State: st}
		}
		return w
	}
	assert.Equal(t, outcomeComplete, rec(stateDone, stateDone).outcome())
	assert.Equal(t, outcomePartial, rec(stateDone, stateFailed).outcome())
	assert.Equal(t, outcomePending, rec(stateDone, stateFailed, statePending).outcome(), "not over yet")
	assert.Equal(t, outcomePartial, rec(stateDone, stateMissed).outcome(), "a registered replica that did not get it")
	w := rec(stateDone)
	w.Error = "no members"
	assert.Equal(t, outcomeError, w.outcome())

	// The error travels like the replicas do.
	s := newBroadcastStatus()
	_, err := s.Merge(&broadcastStatus{Writes: map[string]*writeRecord{"w1": {At: time.Now(), Error: "no members",
		Replicas: map[string]replicaRecord{}}}}, false)
	require.NoError(t, err)
	assert.Equal(t, "no members", s.Writes["w1"].Error)
	assert.Contains(t, s.MergeContent(), "w1/error")
}
