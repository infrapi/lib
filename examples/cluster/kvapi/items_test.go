package main

import (
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func at(value string, ms int64) Item {
	return Item{Value: value, UpdatedAtMs: ms, UpdatedBy: "test"}
}

func collection(items map[string]Item) *Items {
	c := NewItems()
	for id, item := range items {
		c.Items[id] = item
	}
	return c
}

// The hand-written type has to survive the codec, or nothing else matters.
func TestItems_round_trips_through_protobuf(t *testing.T) {
	original := collection(map[string]Item{
		"one": at("hello", 1000),
		"two": {Deleted: true, UpdatedAtMs: 2000},
	})

	encoded, err := proto.Marshal(original)
	require.NoError(t, err)

	decoded := NewItems()
	require.NoError(t, proto.Unmarshal(encoded, decoded))
	assert.Equal(t, original.Items, decoded.Items)
}

// The three properties the gossip store requires of a merge. A value whose
// merge lacks them leaves replicas in different states permanently, and nothing
// reports it — see ../state-proto for the long version.
func TestItems_Merge_is_a_crdt(t *testing.T) {
	a := map[string]Item{"one": at("a", 1000), "two": at("a2", 4000)}
	b := map[string]Item{"one": at("b", 2000), "three": at("b3", 3000)}
	c := map[string]Item{"one": at("c", 3000), "four": at("c4", 5000)}

	t.Run("idempotent", func(t *testing.T) {
		mine := NewItems()
		first, err := mine.Merge(collection(b), false)
		require.NoError(t, err)
		require.NotNil(t, first)

		second, err := mine.Merge(collection(b), false)
		require.NoError(t, err)
		assert.Nil(t, second, "re-applying the same state is not a change")
	})

	t.Run("commutative", func(t *testing.T) {
		left := collection(a)
		_, err := left.Merge(collection(b), false)
		require.NoError(t, err)

		right := collection(b)
		_, err = right.Merge(collection(a), false)
		require.NoError(t, err)

		assert.Equal(t, left.Items, right.Items)
	})

	t.Run("associative", func(t *testing.T) {
		one := collection(a)
		_, err := one.Merge(collection(b), false)
		require.NoError(t, err)
		_, err = one.Merge(collection(c), false)
		require.NoError(t, err)

		bc := collection(b)
		_, err = bc.Merge(collection(c), false)
		require.NoError(t, err)
		two := collection(a)
		_, err = two.Merge(bc, false)
		require.NoError(t, err)

		assert.Equal(t, one.Items, two.Items)
	})
}

// Last write wins, and the change carries only what moved.
func TestItems_Merge_takes_the_newer_item(t *testing.T) {
	mine := collection(map[string]Item{"one": at("old", 1000), "mine": at("kept", 9000)})
	theirs := collection(map[string]Item{"one": at("new", 2000), "theirs": at("learned", 3000)})

	change, err := mine.Merge(theirs, false)
	require.NoError(t, err)

	assert.Equal(t, "new", mine.Items["one"].Value)
	assert.Equal(t, "kept", mine.Items["mine"].Value)
	assert.Equal(t, "learned", mine.Items["theirs"].Value)

	require.NotNil(t, change)
	assert.ElementsMatch(t, []string{"one", "theirs"}, change.MergeContent())
}

// Deleting through the API drops the entry from the value the compare-and-swap
// returns, and the merge turns that into a tombstone.
func TestItems_Merge_localCAS_tombstones_a_deletion(t *testing.T) {
	now := time.UnixMilli(9000)

	mine := collection(map[string]Item{"keep": at("here", 1000), "drop": at("gone", 1000)})
	wanted := collection(map[string]Item{"keep": at("here", 1000)})

	change, err := mine.mergeWithTime(wanted, true, now)
	require.NoError(t, err)

	dropped := mine.Items["drop"]
	assert.True(t, dropped.Deleted)
	assert.Equal(t, now.UnixMilli(), dropped.UpdatedAtMs,
		"stamped now, or a message already in flight would resurrect it")
	assert.Empty(t, dropped.Value)

	require.NotNil(t, change)
	assert.ElementsMatch(t, []string{"drop"}, change.MergeContent())

	// Without the flag our extra item is left alone: that asymmetry is why the
	// branch may only run on a local write.
	peer := collection(map[string]Item{"keep": at("here", 1000), "drop": at("gone", 1000)})
	_, err = peer.mergeWithTime(wanted, false, now)
	require.NoError(t, err)
	assert.False(t, peer.Items["drop"].Deleted)
}

// IDs is what the API lists, so it must not leak tombstones.
func TestItems_IDs_hides_tombstones(t *testing.T) {
	c := collection(map[string]Item{
		"alive":   at("here", 1000),
		"deleted": {Deleted: true, UpdatedAtMs: 2000},
	})
	assert.Equal(t, []string{"alive"}, c.IDs())
}

func TestItems_RemoveTombstones(t *testing.T) {
	build := func() *Items {
		return collection(map[string]Item{
			"alive": at("here", 5000),
			"old":   {Deleted: true, UpdatedAtMs: 1000},
			"fresh": {Deleted: true, UpdatedAtMs: 8000},
		})
	}

	hidden := build()
	total, removed := hidden.RemoveTombstones(time.Time{})
	assert.Equal(t, 0, total)
	assert.Equal(t, 2, removed)

	expired := build()
	total, removed = expired.RemoveTombstones(time.UnixMilli(4000))
	assert.Equal(t, 1, total, "the fresh tombstone still has to be gossiped")
	assert.Equal(t, 1, removed)
}

func TestItems_Clone_is_deep(t *testing.T) {
	original := collection(map[string]Item{"one": at("value", 1000)})

	clone, ok := original.Clone().(*Items)
	require.True(t, ok)
	clone.Items["one"] = at("changed", 2000)
	clone.Items["two"] = at("added", 2000)

	assert.Equal(t, "value", original.Items["one"].Value)
	assert.NotContains(t, original.Items, "two")
}
