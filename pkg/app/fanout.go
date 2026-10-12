package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/infrapi/lib/pkg/app/idempotency"
	"github.com/infrapi/lib/pkg/app/problem"
)

// BroadcastIDHeader is on every write's answer: the broadcast id, under which
// BroadcastStatusPath says how a broadcast write went.
const BroadcastIDHeader = "X-Infrapi-Broadcast-ID"

type broadcastKey struct{}

type broadcastIdentity struct {
	id string
	at time.Time
}

// BroadcastID is the broadcast id of the write being handled: the same on
// every replica that runs it, so that what each participant records carries
// the one id of the operation they all took part in. A handler derives what
// it creates from it — an id, a name — where it would otherwise draw one at
// random. With an Idempotency-Key, it is derived from the key: a retry is the
// same broadcast again. Every write has one, broadcast or not; empty outside a
// write.
func BroadcastID(ctx context.Context) string {
	w, _ := ctx.Value(broadcastKey{}).(broadcastIdentity)
	return w.id
}

// BroadcastTime is when the first replica received the write, the same on
// every replica that runs it: a handler uses it instead of time.Now for what it
// records. Zero outside a write.
func BroadcastTime(ctx context.Context) time.Time {
	w, _ := ctx.Value(broadcastKey{}).(broadcastIdentity)
	return w.at
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newBroadcastIdentity is a write's broadcast id and time: the id from its
// Idempotency-Key when it has one, at random otherwise.
func newBroadcastIdentity(r *http.Request) broadcastIdentity {
	w := broadcastIdentity{at: time.Now().UTC()}
	if key := r.Header.Get(idempotency.Header); key != "" {
		sum := sha256.Sum256([]byte(r.URL.Path + "\n" + key))
		w.id = strings.ToLower(idEncoding.EncodeToString(sum[:16]))
	} else {
		// 128 of rand.Text's random bits, in 16 bytes: a UUID's worth.
		sum := sha256.Sum256([]byte(rand.Text()))
		w.id = strings.ToLower(idEncoding.EncodeToString(sum[:16]))
	}
	return w
}

// BroadcastUUID is the broadcast id as a UUID: the same on every replica
// that runs the write, and as unpredictable — 122 random bits, or derived from
// the Idempotency-Key, which only its caller knows. The id to give what a
// write creates, when ids are UUIDs. Empty outside a write.
func BroadcastUUID(ctx context.Context) string {
	id := BroadcastID(ctx)
	if id == "" {
		return ""
	}
	var raw [16]byte
	if b, err := idEncoding.DecodeString(strings.ToUpper(id)); err == nil && len(b) == 16 {
		copy(raw[:], b)
	} else {
		sum := sha256.Sum256([]byte(id)) // an id from elsewhere: still the same on every replica
		copy(raw[:], sum[:16])
	}
	raw[6] = raw[6]&0x0f | 0x40 // version 4
	raw[8] = raw[8]&0x3f | 0x80 // RFC 9562 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

// replayed counts the broadcast writes this replica applied for another one,
// by outcome.
var replayed = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "infrapi_app_broadcast_writes_total",
	Help: "Broadcast writes this replica applied, received from another member of the all-mode role, by method, route and outcome.",
}, []string{"method", "route", "outcome"})

// DefaultBroadcastMaxBody is the largest body of a broadcast write when
// broadcast.max_body_bytes is not set: the body goes into the gossip.
const DefaultBroadcastMaxBody = 64 << 10

// BroadcastWait is how long a strict or tolerant write waits for the other
// replicas' outcomes.
const BroadcastWait = time.Minute

// fanout gives every write its identity and, for a role's broadcast writes,
// publishes each one that succeeded here for the other members to apply.
type fanout struct {
	cluster *cluster.Cluster
	role    string // empty: identity only, nothing published
	maxBody int64
	wait    time.Duration
}

func newFanout(cl *cluster.Cluster, role string, maxBody int64) *fanout {
	if maxBody <= 0 {
		maxBody = DefaultBroadcastMaxBody
	}
	return &fanout{cluster: cl, role: role, maxBody: maxBody, wait: BroadcastWait}
}

// How the caller of a broadcast write learns of the other replicas, set by
// the contract's x-infrapi-broadcast.
const (
	// BroadcastStrict waits for every replica, and answers 502 when one did
	// not take the write: the caller retries with the same Idempotency-Key.
	BroadcastStrict = "strict"
	// BroadcastTolerant waits for every replica, and answers what the first
	// one did; a replica that failed is in the status and the logs.
	BroadcastTolerant = "tolerant"
	// BroadcastAsync answers at once; the other replicas apply the write as
	// the gossip brings it.
	BroadcastAsync = "async"
)

// BroadcastExtension, at the root of a contract or on an operation, says how a
// broadcast write answers: strict (the default), tolerant or async.
const BroadcastExtension = "x-infrapi-broadcast"

func (f *fanout) handle(c *gin.Context, policy string) {
	r := c.Request
	if !isWrite(r.Method) {
		c.Next()
		return
	}

	// A write another member published, applied here: its identity, and
	// nothing published again.
	if rp, ok := r.Context().Value(replayKey{}).(replayOf); ok {
		server.LogField(c, "broadcast_from", rp.by)
		f.run(c, rp.identity)
		return
	}

	w := newBroadcastIdentity(r)
	if f.role == "" || !f.cluster.Enabled() {
		f.run(c, w)
		return
	}

	// Kept for the others: the handler reads the body.
	body, err := io.ReadAll(io.LimitReader(r.Body, f.maxBody+1))
	if err != nil {
		problem.Abort(c, problem.Invalid("the body could not be read: %v", err))
		return
	}
	if int64(len(body)) > f.maxBody {
		problem.Abort(c, problem.TooLarge("the body of a broadcast write is over %d bytes "+
			"(broadcast.max_body_bytes): it goes to every replica through the gossip", f.maxBody))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	// Strict keeps this replica's answer until the others have answered: it
	// may have to give another one.
	var held *heldResponse
	if policy == BroadcastStrict {
		held = &heldResponse{ResponseWriter: c.Writer, status: http.StatusOK}
		c.Writer = held
	}
	// The request as the caller sent it: what follows may rewrite it — the
	// library's ids become the backend's — and the replicas take the
	// caller's.
	req := replayRequest{Route: c.FullPath(), Query: r.URL.RawQuery, Body: body,
		Correlation: problem.CorrelationID(r.Context())}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header = map[string]string{"Content-Type": ct}
	}
	method, path := r.Method, r.URL.Path
	f.run(c, w)
	if held != nil {
		c.Writer = held.ResponseWriter
	}
	status := c.Writer.Status()
	if held != nil {
		status = held.status
	}
	if status >= http.StatusBadRequest {
		held.send() // refused or failed here: the others are not asked
		return
	}

	ctx := context.WithoutCancel(r.Context())
	waitFor, down, err := f.publish(ctx, w, method, path, &req, status)
	if err != nil {
		server.LogField(c, "broadcast_failed", "("+f.role+")")
		if held != nil {
			f.incomplete(c, nil, []string{"(" + f.role + ": " + err.Error() + ")"})
		}
		return
	}
	if policy == BroadcastAsync {
		server.LogField(c, "broadcast", "async")
		return
	}
	done, failed := f.await(ctx, w.id, waitFor)
	failed = append(failed, down...)
	slices.Sort(failed)
	server.LogField(c, "broadcast_to", strings.Join(done, ","))
	if len(failed) > 0 {
		server.LogField(c, "broadcast_failed", strings.Join(failed, ","))
	}
	if held == nil {
		return
	}
	if len(failed) == 0 {
		held.send()
		return
	}
	f.incomplete(c, done, failed)
}

// incomplete answers a strict write that not every replica took. The write
// is done here and on the others: a retry with the same Idempotency-Key asks
// again those that failed, and those that were down apply it when back.
func (f *fanout) incomplete(c *gin.Context, done, failed []string) {
	for k := range c.Writer.Header() {
		if k != "X-Correlation-Id" && k != http.CanonicalHeaderKey(BroadcastIDHeader) {
			c.Writer.Header().Del(k)
		}
	}
	problem.Write(c.Writer, c.Request, &problem.Error{Status: http.StatusBadGateway, Code: "broadcast_incomplete",
		Title: "Not every replica took the write", Retryable: true,
		Detail: "done here and on " + listOr(done, "no other replica") + "; not on " + strings.Join(failed, ", ") +
			". A replica that was down applies it when back, within " + window().String() +
			"; retry with the same Idempotency-Key to ask again those that failed"})
}

func listOr(list []string, none string) string {
	if len(list) == 0 {
		return none
	}
	return strings.Join(list, ", ")
}

// heldResponse keeps a handler's answer instead of sending it, until send.
type heldResponse struct {
	gin.ResponseWriter
	status  int
	written bool
	body    bytes.Buffer
}

func (h *heldResponse) WriteHeader(code int) { h.status, h.written = code, true }
func (h *heldResponse) WriteHeaderNow()      { h.written = true }
func (h *heldResponse) Write(b []byte) (int, error) {
	h.written = true
	return h.body.Write(b)
}
func (h *heldResponse) WriteString(s string) (int, error) {
	h.written = true
	return h.body.WriteString(s)
}
func (h *heldResponse) Status() int   { return h.status }
func (h *heldResponse) Size() int     { return h.body.Len() }
func (h *heldResponse) Written() bool { return h.written }
func (h *heldResponse) Flush()        {}

// send writes the held answer; nothing when there is none to hold.
func (h *heldResponse) send() {
	if h == nil {
		return
	}
	h.ResponseWriter.WriteHeader(h.status)
	if h.body.Len() == 0 {
		h.ResponseWriter.WriteHeaderNow()
		return
	}
	if _, err := h.ResponseWriter.Write(h.body.Bytes()); err != nil {
		broadcastLog().Warn("broadcast write: the answer could not be sent", "err", err)
	}
}

// run handles the write here, with its identity in the request's context.
func (f *fanout) run(c *gin.Context, w broadcastIdentity) {
	f.identify(c, w)
	// The caller can ask how the write went, broadcast, under this id.
	c.Header(BroadcastIDHeader, w.id)
	c.Next()
}

// identify puts the broadcast id in the request's context and its log line.
func (f *fanout) identify(c *gin.Context, w broadcastIdentity) {
	server.LogField(c, "broadcast_id", w.id)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), broadcastKey{}, w))
}

// publish puts the write, done here, in the shared status, for every other
// member of the role to apply: those registered in the ring, a replica that
// is down too, which applies it when back. It returns the members that are
// up, whose outcome a strict or tolerant write waits for, and those that are
// not. The same write published again — a retry, with the same
// Idempotency-Key — asks again the replicas that failed it.
func (f *fanout) publish(ctx context.Context, w broadcastIdentity, method, path string, req *replayRequest,
	status int) (up, down []string, err error) {
	self := f.cluster.InstanceID()
	role, err := f.cluster.Role(f.role)
	var registered []cluster.Member
	if err == nil {
		// Every replica the ring holds, those it excludes too: one that
		// stopped but stays registered (keep_instance_in_ring_on_shutdown),
		// or whose heartbeat is stale, is still one of the role's.
		registered, err = role.AllMembers(ctx)
	}
	if err != nil {
		broadcastLog().ErrorContext(ctx, "broadcast write: not published", "role", f.role, "err", err)
		record(ctx, f.cluster, w.id, writeRecord{At: w.at, By: self, Method: method, Path: path, Error: err.Error(),
			Replicas: map[string]replicaRecord{self: {State: stateDone, Status: status, Updated: time.Now().UTC()}}})
		return nil, nil, err
	}

	client, err := f.cluster.KVClient(statusCodecID)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	err = client.CAS(ctx, statusKey, func(in interface{}) (interface{}, bool, error) {
		current, ok := in.(*broadcastStatus)
		if !ok || current == nil {
			current = newBroadcastStatus()
		}
		before := current.Writes[w.id]
		rec := &writeRecord{At: w.at, By: self, Method: method, Path: path, Request: req,
			Replicas: map[string]replicaRecord{}}
		if before != nil {
			rec.At, rec.By = before.At, before.By
		}
		for _, m := range registered {
			prev, seen := replicaRecord{}, false
			if before != nil {
				prev, seen = before.Replicas[m.ID]
			}
			switch {
			case m.ID == self:
				attempt := prev.Attempt
				if seen && prev.State != stateDone {
					attempt++
				}
				rec.Replicas[self] = replicaRecord{Attempt: attempt, State: stateDone, Status: status, Updated: now}
			case !seen:
				rec.Replicas[m.ID] = replicaRecord{State: statePending, Updated: now}
			case prev.State == stateFailed || prev.State == stateMissed:
				rec.Replicas[m.ID] = replicaRecord{Attempt: prev.Attempt + 1, State: statePending, Updated: now}
			}
		}
		if _, err := current.Merge(&broadcastStatus{Writes: map[string]*writeRecord{w.id: rec}}, true); err != nil {
			return nil, false, err
		}
		return current, true, nil
	})
	if err != nil {
		broadcastLog().ErrorContext(ctx, "broadcast write: not published", "broadcast_id", w.id, "err", err)
		return nil, nil, err
	}
	for _, m := range registered {
		switch {
		case m.ID == self:
		case m.Healthy:
			up = append(up, m.ID)
		default:
			down = append(down, m.ID)
			broadcastLog().WarnContext(ctx, "broadcast write: a replica is registered but not healthy: "+
				"it applies the write when back", "instance", m.ID, "state", m.State, "broadcast_id", w.id,
				"window", window().String())
		}
	}
	return up, down, nil
}

// await waits until each of the replicas has applied the write, or failed
// it, or the wait is over, and says which took it and which did not.
func (f *fanout) await(ctx context.Context, id string, replicas []string) (done, failed []string) {
	if len(replicas) == 0 {
		return nil, nil
	}
	client, err := f.cluster.KVClient(statusCodecID)
	if err != nil {
		return nil, replicas
	}
	ctx, cancel := context.WithTimeout(ctx, f.wait)
	defer cancel()
	settled := func() (done, failed []string, all bool) {
		v, err := client.Get(ctx, statusKey)
		s, _ := v.(*broadcastStatus)
		var w *writeRecord
		if err == nil && s != nil {
			w = s.Writes[id]
		}
		all = true
		for _, node := range replicas {
			var r replicaRecord
			if w != nil {
				r = w.Replicas[node]
			}
			switch r.State {
			case stateDone:
				done = append(done, node)
			case stateFailed, stateMissed:
				failed = append(failed, node)
			default:
				all = false
				failed = append(failed, node) // so far
			}
		}
		return done, failed, all
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		done, failed, all := settled()
		if all || ctx.Err() != nil {
			slices.Sort(done)
			slices.Sort(failed)
			return done, failed
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}

// broadcastLog is the logger of the broadcast writes, tagged like the cluster's own
// lines. Taken at each call: the default logger is the service's only once it
// has started.
func broadcastLog() *slog.Logger { return slog.Default().With("component", "broadcast") }
