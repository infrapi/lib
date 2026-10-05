package main

import (
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/grafana/dskit/kv/memberlist"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// at builds an entry with a fixed timestamp, so the tests never race a clock.
func at(payload string, ms int64) Entry {
	return Entry{Payload: payload, UpdatedAtMs: ms}
}

func state(entries map[string]Entry) *FleetState {
	s := NewFleetState()
	for owner, entry := range entries {
		s.Entries[owner] = entry
	}
	return s
}

// The hand-written type has to survive the codec, or none of the rest matters.
func TestFleetState_round_trips_through_protobuf(t *testing.T) {
	original := state(map[string]Entry{
		"node-1": at("ready", 1000),
		"node-2": {Deleted: true, UpdatedAtMs: 2000},
	})

	encoded, err := proto.Marshal(original)
	require.NoError(t, err)

	decoded := NewFleetState()
	require.NoError(t, proto.Unmarshal(encoded, decoded))
	assert.Equal(t, original.Entries, decoded.Entries)
}

// Last write wins, per entry, and only what moved comes back as the change.
func TestFleetState_Merge_takes_the_newer_entry(t *testing.T) {
	mine := state(map[string]Entry{
		"node-1": at("old", 1000),
		"node-2": at("mine", 5000),
	})
	theirs := state(map[string]Entry{
		"node-1": at("new", 2000),
		"node-3": at("theirs", 3000),
	})

	change, err := mine.Merge(theirs, false)
	require.NoError(t, err)

	assert.Equal(t, "new", mine.Entries["node-1"].Payload, "theirs is newer")
	assert.Equal(t, "mine", mine.Entries["node-2"].Payload, "they never saw node-2")
	assert.Equal(t, "theirs", mine.Entries["node-3"].Payload, "learned from them")

	// The change is what a peer would be told, so it carries only the moves.
	require.NotNil(t, change)
	assert.ElementsMatch(t, []string{"node-1", "node-3"}, change.MergeContent())
}

// Idempotent: dskit delivers the same update more than once, and the second
// time must not look like news.
func TestFleetState_Merge_is_idempotent(t *testing.T) {
	theirs := state(map[string]Entry{"node-1": at("value", 1000)})

	mine := NewFleetState()
	first, err := mine.Merge(theirs.Clone(), false)
	require.NoError(t, err)
	require.NotNil(t, first)

	second, err := mine.Merge(theirs.Clone(), false)
	require.NoError(t, err)
	assert.Nil(t, second, "re-applying the same state is not a change")
}

// Commutative: gossip arrives in whatever order, and the end state cannot
// depend on it.
func TestFleetState_Merge_is_commutative(t *testing.T) {
	a := map[string]Entry{"node-1": at("a", 1000), "node-2": at("a2", 4000)}
	b := map[string]Entry{"node-1": at("b", 2000), "node-3": at("b3", 3000)}

	left := state(a)
	_, err := left.Merge(state(b), false)
	require.NoError(t, err)

	right := state(b)
	_, err = right.Merge(state(a), false)
	require.NoError(t, err)

	assert.Equal(t, left.Entries, right.Entries)
}

// Associative: folding in a different grouping lands in the same place.
func TestFleetState_Merge_is_associative(t *testing.T) {
	a := map[string]Entry{"node-1": at("a", 1000)}
	b := map[string]Entry{"node-1": at("b", 3000), "node-2": at("b2", 1000)}
	c := map[string]Entry{"node-1": at("c", 2000), "node-3": at("c3", 5000)}

	// (a . b) . c
	one := state(a)
	_, err := one.Merge(state(b), false)
	require.NoError(t, err)
	_, err = one.Merge(state(c), false)
	require.NoError(t, err)

	// a . (b . c)
	bc := state(b)
	_, err = bc.Merge(state(c), false)
	require.NoError(t, err)
	two := state(a)
	_, err = two.Merge(bc, false)
	require.NoError(t, err)

	assert.Equal(t, one.Entries, two.Entries)
}

// A local compare-and-swap that drops an owner turns it into a tombstone, so
// that a peer which has not heard yet cannot gossip the entry back.
func TestFleetState_Merge_localCAS_tombstones_what_the_caller_dropped(t *testing.T) {
	now := time.UnixMilli(9000)

	mine := state(map[string]Entry{
		"node-1": at("keep", 1000),
		"node-2": at("drop", 1000),
	})

	// What the caller returned: node-2 is gone.
	wanted := state(map[string]Entry{"node-1": at("keep", 1000)})

	change, err := mine.mergeWithTime(wanted, true, now)
	require.NoError(t, err)

	dropped := mine.Entries["node-2"]
	assert.True(t, dropped.Deleted, "the entry must survive as a tombstone")
	assert.Equal(t, now.UnixMilli(), dropped.UpdatedAtMs,
		"stamped now, or a message already in flight would resurrect it")
	assert.Empty(t, dropped.Payload)

	require.NotNil(t, change)
	assert.ElementsMatch(t, []string{"node-2"}, change.MergeContent())

	// Without the flag the same merge leaves our extra entry alone: that
	// asymmetry is why it may only ever run on a local write.
	peerView := state(map[string]Entry{
		"node-1": at("keep", 1000),
		"node-2": at("drop", 1000),
	})
	_, err = peerView.mergeWithTime(wanted, false, now)
	require.NoError(t, err)
	assert.False(t, peerView.Entries["node-2"].Deleted)
}

func TestFleetState_RemoveTombstones(t *testing.T) {
	build := func() *FleetState {
		return state(map[string]Entry{
			"alive": at("here", 5000),
			"old":   {Deleted: true, UpdatedAtMs: 1000},
			"fresh": {Deleted: true, UpdatedAtMs: 8000},
		})
	}

	// A zero limit hides every tombstone, which is what the store does before
	// handing the value to the application.
	hidden := build()
	total, removed := hidden.RemoveTombstones(time.Time{})
	assert.Equal(t, 0, total)
	assert.Equal(t, 2, removed)
	assert.ElementsMatch(t, []string{"alive"}, hidden.MergeContent())

	// A real limit expires only those old enough to have propagated.
	expired := build()
	total, removed = expired.RemoveTombstones(time.UnixMilli(4000))
	assert.Equal(t, 1, total, "the fresh tombstone still has to be gossiped")
	assert.Equal(t, 1, removed)
	assert.ElementsMatch(t, []string{"alive", "fresh"}, expired.MergeContent())
}

func TestFleetState_Clone_is_deep(t *testing.T) {
	original := state(map[string]Entry{"node-1": at("value", 1000)})

	clone, ok := original.Clone().(*FleetState)
	require.True(t, ok)

	clone.Entries["node-1"] = at("changed", 2000)
	clone.Entries["node-2"] = at("added", 2000)

	assert.Equal(t, "value", original.Entries["node-1"].Payload)
	assert.NotContains(t, original.Entries, "node-2")
}

func TestFleetState_Merge_rejects_another_type(t *testing.T) {
	_, err := NewFleetState().Merge(nil, false)
	require.NoError(t, err, "a nil mergeable is no change, not an error")

	var other memberlist.Mergeable = (*FleetState)(nil)
	_, err = NewFleetState().Merge(other, false)
	assert.NoError(t, err, "a typed nil is no change either")
}
