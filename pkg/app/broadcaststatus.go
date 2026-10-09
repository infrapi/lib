package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/grafana/dskit/kv/memberlist"
	"github.com/infrapi/lib/pkg/cluster"

	"github.com/infrapi/lib/pkg/app/problem"
)

// BroadcastStatusPath answers, from any replica, how a broadcast write went on
// each replica: GET {BroadcastStatusPath}/{write id}.
const BroadcastStatusPath = "/-/broadcast"

// BroadcastStatusRetention is how long a broadcast write's outcome is kept:
// its TTL inside the one key that holds them all.
const BroadcastStatusRetention = 24 * time.Hour

// BroadcastStatusMax caps the outcomes kept, the oldest dropped first: the key
// is one value, sent whole on every full gossip exchange, so its size is
// bounded whatever the write rate.
const BroadcastStatusMax = 10000

// Where the outcomes live: one key of the cluster's store, every write in it,
// the old ones dropped from the value itself.
const (
	statusCodecID = "infrapi-broadcast-status"
	statusKey     = "broadcast-status"
)

// The states of one replica's part in a broadcast write.
const (
	statePending = "pending"
	stateDone    = "done"
	stateFailed  = "failed"
	stateMissed  = "missed" // registered in the ring, not healthy: not sent
)

// broadcastStatus is every recent broadcast write's outcome, by write id.
type broadcastStatus struct {
	Writes map[string]*writeRecord `json:"writes"`
}

// writeRecord is one broadcast write: when, where it came in, and how each
// replica took it.
type writeRecord struct {
	At       time.Time                `json:"at"`
	By       string                   `json:"by"`
	Method   string                   `json:"method"`
	Path     string                   `json:"path"`
	Replicas map[string]replicaRecord `json:"replicas"`
	// Error is why the write could not be sent to the others at all.
	Error string `json:"error,omitempty"`
}

// The outcome of a broadcast write as a whole.
const (
	outcomePending  = "pending"  // a replica has not answered yet
	outcomeComplete = "complete" // every replica took it
	outcomePartial  = "partial"  // a replica did not
	outcomeError    = "error"    // it could not be sent to the others at all
)

// outcome sums the replicas up.
func (w *writeRecord) outcome() string {
	if w.Error != "" {
		return outcomeError
	}
	failed := false
	for _, r := range w.Replicas {
		switch r.State {
		case statePending:
			return outcomePending
		case stateFailed, stateMissed:
			failed = true
		}
	}
	if failed {
		return outcomePartial
	}
	return outcomeComplete
}

type replicaRecord struct {
	State   string    `json:"state"`
	Status  int       `json:"status,omitempty"`
	Error   string    `json:"error,omitempty"`
	Updated time.Time `json:"updated"`
}

// after says whether r supersedes o: a settled state over a pending one,
// then the later, then a fixed order — so that every replica keeps the same.
func (r replicaRecord) after(o replicaRecord) bool {
	settled := func(s string) bool { return s != statePending }
	if settled(r.State) != settled(o.State) {
		return settled(r.State)
	}
	if !r.Updated.Equal(o.Updated) {
		return r.Updated.After(o.Updated)
	}
	if r.State != o.State {
		return r.State > o.State
	}
	if r.Status != o.Status {
		return r.Status > o.Status
	}
	return r.Error > o.Error
}

func newBroadcastStatus() *broadcastStatus {
	return &broadcastStatus{Writes: map[string]*writeRecord{}}
}

// expire drops the writes older than the retention, from now, then the
// oldest beyond BroadcastStatusMax.
func (s *broadcastStatus) expire(now time.Time) int {
	removed := 0
	for id, w := range s.Writes {
		if now.Sub(w.At) > BroadcastStatusRetention {
			delete(s.Writes, id)
			removed++
		}
	}
	if over := len(s.Writes) - BroadcastStatusMax; over > 0 {
		ids := make([]string, 0, len(s.Writes))
		for id := range s.Writes {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			a, b := s.Writes[ids[i]].At, s.Writes[ids[j]].At
			if !a.Equal(b) {
				return a.Before(b)
			}
			return ids[i] < ids[j]
		})
		for _, id := range ids[:over] {
			delete(s.Writes, id)
		}
		removed += over
	}
	return removed
}

// Merge keeps, for each replica of each write, the record that supersedes
// the other: idempotent, commutative and associative, as the gossip needs.
// Expired writes are not taken in.
func (s *broadcastStatus) Merge(other memberlist.Mergeable, _ bool) (memberlist.Mergeable, error) {
	o, ok := other.(*broadcastStatus)
	if !ok {
		return nil, fmt.Errorf("broadcast status: cannot merge a %T", other)
	}
	if o == nil {
		return nil, nil
	}
	if s.Writes == nil {
		s.Writes = map[string]*writeRecord{}
	}
	now := time.Now()
	change := newBroadcastStatus()
	for id, ow := range o.Writes {
		if now.Sub(ow.At) > BroadcastStatusRetention {
			continue
		}
		mine, ok := s.Writes[id]
		if !ok {
			mine = &writeRecord{At: ow.At, By: ow.By, Method: ow.Method, Path: ow.Path, Replicas: map[string]replicaRecord{}}
			s.Writes[id] = mine
		}
		if ow.Error > mine.Error { // the same on every replica, whatever the order
			mine.Error = ow.Error
			c := change.Writes[id]
			if c == nil {
				c = &writeRecord{At: mine.At, By: mine.By, Method: mine.Method, Path: mine.Path,
					Replicas: map[string]replicaRecord{}}
				change.Writes[id] = c
			}
			c.Error = mine.Error
		}
		for node, or := range ow.Replicas {
			if m, ok := mine.Replicas[node]; ok && !or.after(m) {
				continue
			}
			mine.Replicas[node] = or
			c, ok := change.Writes[id]
			if !ok {
				c = &writeRecord{At: mine.At, By: mine.By, Method: mine.Method, Path: mine.Path,
					Replicas: map[string]replicaRecord{}}
				change.Writes[id] = c
			}
			c.Replicas[node] = or
		}
	}
	s.expire(now)
	if len(change.Writes) == 0 {
		return nil, nil
	}
	return change, nil
}

// MergeContent names each replica of each write: the gossip drops a queued
// change whose content another one covers, so a change about one replica must
// not be taken to cover one about another.
func (s *broadcastStatus) MergeContent() []string {
	var out []string
	for id, w := range s.Writes {
		for node := range w.Replicas {
			out = append(out, id+"/"+node)
		}
		if w.Error != "" {
			out = append(out, id+"/error")
		}
	}
	sort.Strings(out)
	return out
}

// RemoveTombstones drops the expired writes: there are no tombstones, the
// value forgets what is old by itself.
func (s *broadcastStatus) RemoveTombstones(time.Time) (total, removed int) {
	return 0, s.expire(time.Now())
}

func (s *broadcastStatus) Clone() memberlist.Mergeable {
	out := newBroadcastStatus()
	for id, w := range s.Writes {
		c := *w
		c.Replicas = make(map[string]replicaRecord, len(w.Replicas))
		for n, r := range w.Replicas {
			c.Replicas[n] = r
		}
		out.Writes[id] = &c
	}
	return out
}

// statusCodec stores the status as JSON.
type statusCodec struct{}

func (statusCodec) CodecID() string { return statusCodecID }

func (statusCodec) Encode(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func (statusCodec) Decode(b []byte) (interface{}, error) {
	s := newBroadcastStatus()
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Writes == nil {
		s.Writes = map[string]*writeRecord{}
	}
	return s, nil
}

// record merges what one replica learnt of a write into the shared status.
// A failure to record is logged, not returned: the write itself is done.
func record(ctx context.Context, cl *cluster.Cluster, id string, w writeRecord) {
	client, err := cl.KVClient(statusCodecID)
	if err != nil {
		broadcastLog().WarnContext(ctx, "broadcast write: no status kept", "write_id", id, "err", err)
		return
	}
	update := &broadcastStatus{Writes: map[string]*writeRecord{id: &w}}
	err = client.CAS(ctx, statusKey, func(in interface{}) (interface{}, bool, error) {
		current, ok := in.(*broadcastStatus)
		if !ok || current == nil {
			current = newBroadcastStatus()
		}
		if _, err := current.Merge(update.Clone(), true); err != nil {
			return nil, false, err
		}
		return current, true, nil
	})
	if err != nil {
		broadcastLog().WarnContext(ctx, "broadcast write: status not kept", "write_id", id, "err", err)
	}
}

// statusHandler answers how a broadcast write went, from the shared status.
func statusHandler(cl *cluster.Cluster) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		client, err := cl.KVClient(statusCodecID)
		if errors.Is(err, cluster.ErrDisabled) {
			problem.Abort(c, problem.NotFound("no cluster: nothing is broadcast"))
			return
		}
		if err != nil {
			problem.Abort(c, problem.Unavailable("%v", err))
			return
		}
		v, err := client.Get(c.Request.Context(), statusKey)
		if err != nil {
			problem.Abort(c, problem.Unavailable("%v", err))
			return
		}
		s, _ := v.(*broadcastStatus)
		var w *writeRecord
		if s != nil {
			w = s.Writes[id]
		}
		if w == nil || time.Since(w.At) > BroadcastStatusRetention {
			problem.Abort(c, problem.NotFound("no broadcast write %q in the last %s", id, BroadcastStatusRetention))
			return
		}
		body := gin.H{"write_id": id, "status": w.outcome(), "at": w.At, "by": w.By, "method": w.Method,
			"path": w.Path, "replicas": w.Replicas}
		if w.Error != "" {
			body["error"] = w.Error
		}
		c.JSON(http.StatusOK, body)
	}
}
