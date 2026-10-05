package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/grafana/dskit/kv/memberlist"
)

// Items is the whole collection the API serves, held under a single key.
//
// One key for every item, rather than a key each, and that is deliberate: in
// the gossip store every key costs a broadcast worker and a slot in the state
// each replica propagates on every push/pull. A key per item is how this turns
// into an out-of-memory incident. One key holding a map keeps the cost flat.
type Items struct {
	Items map[string]Item `protobuf:"bytes,1,rep,name=items,proto3" json:"items" protobuf_key:"bytes,1,opt,name=key,proto3" protobuf_val:"bytes,2,opt,name=value,proto3"`
}

// Item is one entry. Deleted marks a tombstone rather than removing the entry,
// so that a replica which has not heard about the deletion cannot gossip it
// back.
type Item struct {
	Value       string `protobuf:"bytes,1,opt,name=value,proto3" json:"value,omitempty"`
	UpdatedAtMs int64  `protobuf:"varint,2,opt,name=updated_at_ms,json=updatedAtMs,proto3" json:"updated_at_ms,omitempty"`
	UpdatedBy   string `protobuf:"bytes,3,opt,name=updated_by,json=updatedBy,proto3" json:"updated_by,omitempty"`
	Deleted     bool   `protobuf:"varint,4,opt,name=deleted,proto3" json:"deleted,omitempty"`
}

// NewItems returns an empty collection, which is what the codec's factory hands
// to the decoder.
func NewItems() *Items {
	return &Items{Items: map[string]Item{}}
}

// Reset implements proto.Message.
func (c *Items) Reset() { *c = Items{} }

// ProtoMessage implements proto.Message.
func (c *Items) ProtoMessage() {}

// String implements proto.Message.
func (c *Items) String() string { return fmt.Sprintf("Items{%d}", len(c.Items)) }

// Reset implements proto.Message.
func (i *Item) Reset() { *i = Item{} }

// ProtoMessage implements proto.Message.
func (i *Item) ProtoMessage() {}

// String implements proto.Message.
func (i *Item) String() string { return fmt.Sprintf("Item{%q}", i.Value) }

// IDs returns the live item ids, sorted.
func (c *Items) IDs() []string {
	ids := make([]string, 0, len(c.Items))
	for id, item := range c.Items {
		if !item.Deleted {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// Merge folds other into this collection in place, last write wins per item,
// and returns what changed.
//
// That makes it a CRDT, which the gossip store requires: idempotent because
// re-applying the same timestamp is not an update, commutative and associative
// because taking the newer of two timestamps is both.
//
// Unlike ../state-proto, where each replica owns one entry, two replicas here
// can write the same id at once. Last write wins resolves it and the other
// write is simply lost — which is the price of this merge rule, not a bug in
// it. If losing a concurrent write is unacceptable, the value needs a merge
// that combines rather than one that picks.
func (c *Items) Merge(other memberlist.Mergeable, localCAS bool) (memberlist.Mergeable, error) {
	return c.mergeWithTime(other, localCAS, time.Now())
}

// mergeWithTime is Merge with the clock injected, so the tests can pin it.
func (c *Items) mergeWithTime(mergeable memberlist.Mergeable, localCAS bool, now time.Time) (memberlist.Mergeable, error) {
	if mergeable == nil {
		return nil, nil
	}

	other, ok := mergeable.(*Items)
	if !ok {
		return nil, fmt.Errorf("expected *Items, got %T", mergeable)
	}
	if other == nil {
		return nil, nil
	}

	if c.Items == nil {
		c.Items = map[string]Item{}
	}

	var updated []string

	for id, incoming := range other.Items {
		// A missing item has a zero timestamp, so this covers both "never seen"
		// and "theirs is newer".
		if mine, have := c.Items[id]; !have || incoming.UpdatedAtMs > mine.UpdatedAtMs {
			c.Items[id] = incoming
			updated = append(updated, id)
		}
	}

	if localCAS {
		// An item we hold and the caller did not return has been deleted by the
		// caller: it becomes a tombstone. This breaks commutativity, which is
		// why it only runs on a local write and never on a peer's value.
		for id, mine := range c.Items {
			if _, still := other.Items[id]; !still && !mine.Deleted {
				// Stamped now, not with the old timestamp: a message already in
				// flight would otherwise outrank it and resurrect the item.
				c.Items[id] = Item{Deleted: true, UpdatedAtMs: now.UnixMilli(), UpdatedBy: mine.UpdatedBy}
				updated = append(updated, id)
			}
		}
	}

	if len(updated) == 0 {
		return nil, nil
	}

	change := NewItems()
	for _, id := range updated {
		change.Items[id] = c.Items[id]
	}
	return change, nil
}

// MergeContent names what this value covers, which is how the store decides
// whether one change supersedes another it still holds.
func (c *Items) MergeContent() []string {
	ids := make([]string, 0, len(c.Items))
	for id := range c.Items {
		ids = append(ids, id)
	}
	return ids
}

// RemoveTombstones drops deleted items, and reports how many remain. The store
// calls it with a zero limit on every read, to hide tombstones from the API.
func (c *Items) RemoveTombstones(limit time.Time) (total, removed int) {
	for id, item := range c.Items {
		if !item.Deleted {
			continue
		}
		if limit.IsZero() || time.UnixMilli(item.UpdatedAtMs).Before(limit) {
			delete(c.Items, id)
			removed++
		} else {
			total++
		}
	}
	return total, removed
}

// Clone returns a deep copy, so that nothing the API does to a value it was
// handed can reach the shared state.
func (c *Items) Clone() memberlist.Mergeable {
	clone := &Items{Items: make(map[string]Item, len(c.Items))}
	for id, item := range c.Items {
		clone.Items[id] = item
	}
	return clone
}
