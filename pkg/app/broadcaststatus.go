package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/grafana/dskit/kv/memberlist"
	"github.com/infrapi/lib/pkg/cluster"

	"github.com/infrapi/lib/pkg/app/problem"
)

// BroadcastStatusPath answers, from any replica, how a broadcast write went on
// each replica: GET {BroadcastStatusPath}/{broadcast id}.
const BroadcastStatusPath = "/-/broadcast"

// BroadcastStatusRetention is how long a broadcast write's outcome is kept:
// its TTL inside the one key that holds them all.
const BroadcastStatusRetention = 24 * time.Hour

// BroadcastStatusMax caps the outcomes kept, the oldest dropped first: the key
// is one value, sent whole on every full gossip exchange, so its size is
// bounded whatever the write rate.
const BroadcastStatusMax = 10000

// DefaultReplayWindow is how long a broadcast write can still be applied by a
// replica that was down when it was made: its request is kept that long, in
// the same key as the outcomes (broadcast.replay_window).
const DefaultReplayWindow = time.Hour

// BroadcastRequestsMax caps the bytes of the requests kept for the replicas
// still to apply them, the oldest dropped first: they go whole with every
// full gossip exchange.
const BroadcastRequestsMax = 8 << 20

// replayWindow is the configured window, read by the merges the gossip runs.
var replayWindow atomic.Int64

func init() { replayWindow.Store(int64(DefaultReplayWindow)) }

func window() time.Duration { return time.Duration(replayWindow.Load()) }

// Where the writes and their outcomes live: one key of the cluster's store,
// every write in it, the old ones dropped from the value itself.
const (
	statusCodecID = "infrapi-broadcast-status"
	statusKey     = "broadcast-status"
)

// The states of one replica's part in a broadcast write.
const (
	statePending = "pending"
	stateDone    = "done"
	stateFailed  = "failed"
	stateMissed  = "missed" // not applied within the replay window
)

// broadcastStatus is every recent broadcast write's outcome, by broadcast id.
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
	// Request is what the replicas replay, kept for the replay window.
	Request *replayRequest `json:"request,omitempty"`
}

// replayRequest is the write as the caller sent it: every replica runs it
// again, in process, from this.
type replayRequest struct {
	Route       string            `json:"route"` // the contract's, for the metrics
	Query       string            `json:"query,omitempty"`
	Header      map[string]string `json:"header,omitempty"`
	Body        []byte            `json:"body,omitempty"`
	Correlation string            `json:"correlation,omitempty"`
}

func (r *replayRequest) size() int {
	if r == nil {
		return 0
	}
	n := len(r.Route) + len(r.Query) + len(r.Body) + len(r.Correlation)
	for k, v := range r.Header {
		n += len(k) + len(v)
	}
	return n
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
	// Attempt counts the times the write was asked of the replica: a retry
	// asks again those that failed.
	Attempt int       `json:"attempt,omitempty"`
	State   string    `json:"state"`
	Status  int       `json:"status,omitempty"`
	Error   string    `json:"error,omitempty"`
	Updated time.Time `json:"updated"`
}

// after says whether r supersedes o: the later attempt, then the replica's
// own answer over a missed one over a pending one, then the later, then a
// fixed order — so that every replica keeps the same.
func (r replicaRecord) after(o replicaRecord) bool {
	if r.Attempt != o.Attempt {
		return r.Attempt > o.Attempt
	}
	rank := func(s string) int {
		switch s {
		case statePending:
			return 0
		case stateMissed:
			return 1
		}
		return 2
	}
	if rank(r.State) != rank(o.State) {
		return rank(r.State) > rank(o.State)
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
// oldest beyond BroadcastStatusMax. A request older than the replay window is
// dropped, and the replicas that had not applied it have missed it: every
// replica concludes the same, at the same time. Beyond BroadcastRequestsMax,
// the oldest requests are dropped too, without concluding anything.
func (s *broadcastStatus) expire(now time.Time) int {
	removed := 0
	win := window()
	for _, w := range s.Writes {
		if w.Request == nil || now.Sub(w.At) <= win {
			continue
		}
		w.Request = nil
		missed(w, win)
	}
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
	total := 0
	for _, w := range s.Writes {
		total += w.Request.size()
	}
	if total > BroadcastRequestsMax {
		ids := make([]string, 0, len(s.Writes))
		for id, w := range s.Writes {
			if w.Request != nil {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool {
			a, b := s.Writes[ids[i]].At, s.Writes[ids[j]].At
			if !a.Equal(b) {
				return a.Before(b)
			}
			return ids[i] < ids[j]
		})
		for _, id := range ids {
			if total <= BroadcastRequestsMax {
				break
			}
			total -= s.Writes[id].Request.size()
			s.Writes[id].Request = nil
		}
	}
	return removed
}

// missed settles the replicas that had not applied a write when its replay
// window closed.
func missed(w *writeRecord, win time.Duration) {
	for node, r := range w.Replicas {
		if r.State == statePending {
			w.Replicas[node] = replicaRecord{Attempt: r.Attempt, State: stateMissed, Updated: w.At.Add(win),
				Error: "not applied within the replay window (" + win.String() + ")"}
		}
	}
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
		if mine.Request == nil && ow.Request != nil && now.Sub(ow.At) <= window() {
			mine.Request = ow.Request
			change.entry(id, mine).Request = ow.Request
		}
		if ow.Error > mine.Error { // the same on every replica, whatever the order
			mine.Error = ow.Error
			change.entry(id, mine).Error = mine.Error
		}
		for node, or := range ow.Replicas {
			if m, ok := mine.Replicas[node]; ok && !or.after(m) {
				continue
			}
			mine.Replicas[node] = or
			change.entry(id, mine).Replicas[node] = or
		}
	}
	s.expire(now)
	if len(change.Writes) == 0 {
		return nil, nil
	}
	return change, nil
}

// entry is the change's record of a write, added when absent.
func (s *broadcastStatus) entry(id string, of *writeRecord) *writeRecord {
	c, ok := s.Writes[id]
	if !ok {
		c = &writeRecord{At: of.At, By: of.By, Method: of.Method, Path: of.Path, Replicas: map[string]replicaRecord{}}
		s.Writes[id] = c
	}
	return c
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
		if w.Request != nil {
			out = append(out, id+"/request")
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
		broadcastLog().WarnContext(ctx, "broadcast write: no status kept", "broadcast_id", id, "err", err)
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
		broadcastLog().WarnContext(ctx, "broadcast write: status not kept", "broadcast_id", id, "err", err)
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
		body := gin.H{"broadcast_id": id, "status": w.outcome(), "at": w.At, "by": w.By, "method": w.Method,
			"path": w.Path, "replicas": w.Replicas}
		if w.Error != "" {
			body["error"] = w.Error
		}
		c.JSON(http.StatusOK, body)
	}
}
