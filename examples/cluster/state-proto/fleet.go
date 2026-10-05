// Package main holds a key-value value of its own: a protobuf message that
// implements dskit's Mergeable, which is what the gossip store requires.
package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/grafana/dskit/kv/memberlist"
)

// FleetState is what every replica publishes about itself, keyed by owner.
//
// The struct tags are what gogo/protobuf marshals from, so this hand-written
// type is wire compatible with code generated from fleet.proto. Nothing here
// needs protoc.
type FleetState struct {
	Entries map[string]Entry `protobuf:"bytes,1,rep,name=entries,proto3" json:"entries" protobuf_key:"bytes,1,opt,name=key,proto3" protobuf_val:"bytes,2,opt,name=value,proto3"`
}

// Entry is one replica's contribution. A replica only ever writes its own,
// which is what makes merging two copies well defined without a coordinator.
type Entry struct {
	Payload     string `protobuf:"bytes,1,opt,name=payload,proto3" json:"payload,omitempty"`
	UpdatedAtMs int64  `protobuf:"varint,2,opt,name=updated_at_ms,json=updatedAtMs,proto3" json:"updated_at_ms,omitempty"`
	Deleted     bool   `protobuf:"varint,3,opt,name=deleted,proto3" json:"deleted,omitempty"`
}

// NewFleetState returns an empty state, which is what the codec's factory hands
// to the decoder.
func NewFleetState() *FleetState {
	return &FleetState{Entries: map[string]Entry{}}
}

// ---- proto.Message, the three methods gogo/protobuf asks for.

// Reset implements proto.Message.
func (s *FleetState) Reset() { *s = FleetState{} }

// ProtoMessage implements proto.Message.
func (s *FleetState) ProtoMessage() {}

// String implements proto.Message.
func (s *FleetState) String() string {
	owners := s.MergeContent()
	sort.Strings(owners)
	return fmt.Sprintf("FleetState{%s}", strings.Join(owners, ","))
}

// Reset implements proto.Message.
func (e *Entry) Reset() { *e = Entry{} }

// ProtoMessage implements proto.Message.
func (e *Entry) ProtoMessage() {}

// String implements proto.Message.
func (e *Entry) String() string {
	if e.Deleted {
		return fmt.Sprintf("Entry{deleted at %d}", e.UpdatedAtMs)
	}
	return fmt.Sprintf("Entry{%q at %d}", e.Payload, e.UpdatedAtMs)
}

// ---- memberlist.Mergeable, which is where the thinking is.

// Merge folds other into this state in place, and returns what changed.
//
// The rule is last write wins per entry, decided by UpdatedAtMs. That is enough
// to make this a CRDT, which is what dskit requires of a value it gossips:
//
//   - idempotent, because re-applying an entry with the same timestamp is not
//     an update, so the second Merge returns no change;
//   - commutative and associative, because taking the newer of two timestamps
//     is both — the order updates arrive in cannot change where replicas land.
//
// Those properties are not decoration. Gossip delivers updates out of order and
// more than once, to every replica, and nothing reconciles afterwards: if Merge
// is not a CRDT, two replicas simply end up in different states for good.
//
// localCAS is the one exception, and it is deliberately not commutative. See
// below.
func (s *FleetState) Merge(other memberlist.Mergeable, localCAS bool) (memberlist.Mergeable, error) {
	return s.mergeWithTime(other, localCAS, time.Now())
}

// mergeWithTime is Merge with the clock injected, so the tests can pin it.
func (s *FleetState) mergeWithTime(mergeable memberlist.Mergeable, localCAS bool, now time.Time) (memberlist.Mergeable, error) {
	if mergeable == nil {
		return nil, nil
	}

	other, ok := mergeable.(*FleetState)
	if !ok {
		return nil, fmt.Errorf("expected *FleetState, got %T", mergeable)
	}
	if other == nil {
		return nil, nil
	}

	if s.Entries == nil {
		s.Entries = map[string]Entry{}
	}

	var updated []string

	for owner, incoming := range other.Entries {
		// A missing entry has a zero timestamp, so the comparison covers both
		// "we have never seen this owner" and "theirs is newer".
		if mine, have := s.Entries[owner]; !have || incoming.UpdatedAtMs > mine.UpdatedAtMs {
			s.Entries[owner] = incoming
			updated = append(updated, owner)
		}
	}

	if localCAS {
		// An owner we hold and the caller did not return has been deleted by
		// the caller. It becomes a tombstone rather than disappearing, or a
		// replica that has not heard about the deletion would gossip the entry
		// back.
		//
		// This breaks commutativity, which is why it only ever runs on a local
		// compare-and-swap and never on a value arriving from a peer.
		for owner, mine := range s.Entries {
			if _, still := other.Entries[owner]; !still && !mine.Deleted {
				// Stamped now, not with the old timestamp: a message already
				// in flight with a newer timestamp than the original would
				// otherwise resurrect the entry.
				s.Entries[owner] = Entry{Deleted: true, UpdatedAtMs: now.UnixMilli()}
				updated = append(updated, owner)
			}
		}
	}

	if len(updated) == 0 {
		return nil, nil
	}

	// The change carries only what moved, so that a peer receiving it can tell
	// whether it invalidates something it already has.
	change := NewFleetState()
	for _, owner := range updated {
		change.Entries[owner] = s.Entries[owner]
	}

	return change, nil
}

// MergeContent names what this value covers, which is how the store decides
// whether one change supersedes another it is still holding.
func (s *FleetState) MergeContent() []string {
	owners := make([]string, 0, len(s.Entries))
	for owner := range s.Entries {
		owners = append(owners, owner)
	}
	return owners
}

// RemoveTombstones drops deleted entries, and reports how many remain.
//
// The store calls it with a zero limit every time a client reads the value, to
// hide tombstones from the application, and with a real limit to expire them
// for good once they have had time to propagate.
func (s *FleetState) RemoveTombstones(limit time.Time) (total, removed int) {
	for owner, entry := range s.Entries {
		if !entry.Deleted {
			continue
		}
		if limit.IsZero() || time.UnixMilli(entry.UpdatedAtMs).Before(limit) {
			delete(s.Entries, owner)
			removed++
		} else {
			total++
		}
	}
	return total, removed
}

// Clone returns a deep copy. The store hands the application a copy so that
// nothing it does to the value can reach the shared state.
func (s *FleetState) Clone() memberlist.Mergeable {
	clone := &FleetState{Entries: make(map[string]Entry, len(s.Entries))}
	for owner, entry := range s.Entries {
		clone.Entries[owner] = entry
	}
	return clone
}
