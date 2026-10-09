package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/infrapi/lib/pkg/app/idempotency"
	"github.com/infrapi/lib/pkg/app/problem"
)

// Headers a replayed write carries: the broadcast id and time the first
// replica gave it.
const (
	BroadcastIDHeader   = "X-Infrapi-Broadcast-ID"
	BroadcastTimeHeader = "X-Infrapi-Broadcast-Time"
)

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

// replayed counts the writes a replica replayed on the others, by outcome.
var replayed = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "infrapi_app_broadcast_writes_total",
	Help: "Writes this replica replayed on another member of the broadcast role, by method, route and outcome.",
}, []string{"method", "route", "outcome"})

// fanout gives every write its identity and, with cluster.broadcast_writes,
// replays it on every other member of the role once it succeeded here.
type fanout struct {
	cluster *cluster.Cluster
	role    string // empty: identity only, no replay
	scheme  string
	maxBody int64
	client  *http.Client
}

func newFanout(cl *cluster.Cluster, role, scheme string, maxBody int64) *fanout {
	if maxBody <= 0 {
		maxBody = idempotency.DefaultMaxBody
	}
	// Sent from the address this replica advertises in the ring: the
	// receiver recognises a member by it, whatever route the system picks.
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if cl.Enabled() {
		if host, _, err := net.SplitHostPort(cl.InstanceAddr()); err == nil {
			if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
				dialer.LocalAddr = &net.TCPAddr{IP: ip}
			}
		}
	}
	return &fanout{cluster: cl, role: role, scheme: scheme, maxBody: maxBody, client: &http.Client{Transport: &http.Transport{
		Proxy:                 nil, // replicas talk directly, never through an egress proxy
		DialContext:           dialer.DialContext,
		ResponseHeaderTimeout: 2 * time.Minute,
		MaxIdleConnsPerHost:   4,
	}}}
}

// How the caller of a broadcast write learns of its replays, set by the
// contract's x-infrapi-broadcast.
const (
	// BroadcastStrict waits for every replica, and answers 502 when one did
	// not take the write: the caller retries with the same Idempotency-Key.
	BroadcastStrict = "strict"
	// BroadcastTolerant waits for every replica, and answers what the first
	// one did; a replica that failed is in the logs and the metrics only.
	BroadcastTolerant = "tolerant"
	// BroadcastAsync answers at once, and replays in the background.
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

	// A replay from another member: its identity, and no replay of our own.
	if by := r.Header.Get(ForwardedHeader); by != "" && r.Header.Get(BroadcastIDHeader) != "" && f.role != "" {
		at, err := time.Parse(time.RFC3339Nano, r.Header.Get(BroadcastTimeHeader))
		if err != nil {
			problem.Abort(c, problem.Invalid("%s: %v", BroadcastTimeHeader, err))
			return
		}
		if !f.fromMember(r) {
			problem.Abort(c, &problem.Error{Status: http.StatusForbidden, Code: "not_a_member", Title: "Not a member",
				Detail: "replayed writes are taken from the members of the " + f.role + " ring only"})
			return
		}
		server.LogField(c, "broadcast_from", by)
		f.run(c, broadcastIdentity{id: r.Header.Get(BroadcastIDHeader), at: at})
		return
	}

	w := newBroadcastIdentity(r)
	if f.role == "" || !f.cluster.Enabled() {
		f.run(c, w)
		return
	}

	// Kept for the replays: the handler reads the body.
	body, err := io.ReadAll(io.LimitReader(r.Body, f.maxBody+1))
	if err != nil {
		problem.Abort(c, problem.Invalid("the body could not be read: %v", err))
		return
	}
	if int64(len(body)) > f.maxBody {
		problem.Abort(c, problem.TooLarge("the body is over %d bytes", f.maxBody))
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
	path, query := c.Request.URL.Path, c.Request.URL.RawQuery
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

	out := snapshot(c, w, body, status)
	out.path, out.query = path, query
	if policy == BroadcastAsync {
		server.LogField(c, "broadcast", "async")
		go f.replay(out) // c is gin's again once this returns: the snapshot is all it reads
		return
	}
	done, failed := f.replay(out)
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
	// The write is done here and on the others: a retry with the same
	// Idempotency-Key does it again everywhere, the same, and reaches the
	// replicas that missed it.
	for k := range c.Writer.Header() {
		if k != "X-Correlation-Id" && k != http.CanonicalHeaderKey(BroadcastIDHeader) {
			c.Writer.Header().Del(k)
		}
	}
	problem.Write(c.Writer, c.Request, &problem.Error{Status: http.StatusBadGateway, Code: "broadcast_incomplete",
		Title: "Not every replica took the write", Retryable: true,
		Detail: "done here and on " + listOr(done, "no other replica") + "; not on " + strings.Join(failed, ", ") +
			". Retry with the same Idempotency-Key: the write is done again, the same, everywhere"})
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

// outgoing is what a replay needs of the request, copied: an async replay
// outlives it.
type outgoing struct {
	method, path, query, host, route, correlation string
	header                                        http.Header
	identity                                      broadcastIdentity
	body                                          []byte
	status                                        int // this replica's answer
}

func snapshot(c *gin.Context, w broadcastIdentity, body []byte, status int) outgoing {
	r := c.Request
	return outgoing{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, host: r.Host, route: c.FullPath(),
		correlation: problem.CorrelationID(r.Context()), header: r.Header.Clone(), identity: w, body: body,
		status: status}
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

// replay sends the write to every other member of the role, at the same time,
// waits for them, and says which took it and which did not.
func (f *fanout) replay(out outgoing) (done, failed []string) {
	ctx, cancel := context.WithTimeout(problem.WithCorrelationID(context.Background(), out.correlation), 2*time.Minute)
	defer cancel()

	self := f.cluster.InstanceID()
	role, err := f.cluster.Role(f.role)
	var registered []cluster.Member
	if err == nil {
		// Every replica the ring holds, those it excludes too: one that
		// stopped but stays registered (keep_instance_in_ring_on_shutdown),
		// or whose heartbeat is stale, is still one of the role's, and its
		// missing the write is said rather than hidden.
		registered, err = role.AllMembers(ctx)
	}
	var members, missed []cluster.Member
	for _, m := range registered {
		if m.Healthy || m.ID == self {
			members = append(members, m)
		} else {
			missed = append(missed, m)
		}
	}
	if err != nil {
		broadcastLog().ErrorContext(ctx, "broadcast write: not sent to the others", "role", f.role, "err", err)
		record(ctx, f.cluster, out.identity.id, writeRecord{At: out.identity.at, By: self, Method: out.method,
			Path: out.path, Error: err.Error(), Replicas: map[string]replicaRecord{
				self: {State: stateDone, Status: out.status, Updated: time.Now().UTC()}}})
		return nil, []string{"(" + f.role + ")"}
	}

	// What every replica can read back: this one done, the others pending.
	rec := writeRecord{At: out.identity.at, By: self, Method: out.method, Path: out.path,
		Replicas: map[string]replicaRecord{}}
	now := time.Now().UTC()
	for _, m := range members {
		r := replicaRecord{State: statePending, Updated: now}
		if m.ID == self {
			r.State, r.Status = stateDone, out.status
		}
		rec.Replicas[m.ID] = r
	}
	for _, m := range missed {
		rec.Replicas[m.ID] = replicaRecord{State: stateMissed, Updated: now,
			Error: "not sent: the replica is " + strings.ToLower(m.State) + " in the ring, and not healthy"}
		failed = append(failed, m.ID)
		replayed.WithLabelValues(out.method, out.route, "missed").Inc()
		broadcastLog().WarnContext(ctx, "broadcast write: a replica is registered but not healthy, not sent",
			"instance", m.ID, "state", m.State, "broadcast_id", out.identity.id, "correlation_id", out.correlation)
	}
	record(ctx, f.cluster, out.identity.id, rec)

	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for _, m := range members {
		if m.ID == self {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			status, err := f.send(ctx, out, m.Addr)
			attrs := []any{"instance", m.ID, "addr", m.Addr, "method", out.method, "path", out.path,
				"status", status, "duration", time.Since(start), "broadcast_id", out.identity.id,
				"correlation_id", out.correlation}
			outcome := "ok"
			if err != nil {
				outcome = "failed"
				broadcastLog().ErrorContext(ctx, "broadcast write: a replica did not take it", append(attrs, "err", err)...)
			} else {
				broadcastLog().InfoContext(ctx, "broadcast write: replayed", attrs...)
			}
			replayed.WithLabelValues(out.method, out.route, outcome).Inc()
			r := replicaRecord{State: stateDone, Status: status, Updated: time.Now().UTC()}
			if err != nil {
				r.State, r.Error = stateFailed, err.Error()
			}
			record(ctx, f.cluster, out.identity.id, writeRecord{At: out.identity.at, By: self, Method: out.method,
				Path: out.path, Replicas: map[string]replicaRecord{m.ID: r}})
			mu.Lock()
			if err != nil {
				failed = append(failed, m.ID)
			} else {
				done = append(done, m.ID)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	slices.Sort(done)
	slices.Sort(failed)
	return done, failed
}

// send replays the write on one replica: same method, path, query, headers
// and body, plus its identity.
// It returns the replica's status, 0 when it did not answer.
func (f *fanout) send(ctx context.Context, out outgoing, addr string) (int, error) {
	target := url.URL{Scheme: f.scheme, Host: addr, Path: out.path, RawQuery: out.query}
	req, err := http.NewRequestWithContext(ctx, out.method, target.String(), bytes.NewReader(out.body))
	if err != nil {
		return 0, err
	}
	req.Header = out.header.Clone()
	req.Host = out.host
	// The idempotency store would answer the replay with this replica's
	// response instead of running it: the broadcast id does that job.
	req.Header.Del(idempotency.Header)
	req.Header.Set(ForwardedHeader, f.cluster.InstanceID())
	req.Header.Set(BroadcastIDHeader, out.identity.id)
	req.Header.Set(BroadcastTimeHeader, out.identity.at.Format(time.RFC3339Nano))
	req.Header.Set("X-Correlation-ID", out.correlation)

	// The host is a member's address, read from the ring; the path is the
	// route the request already matched.
	resp, err := f.client.Do(req) // #nosec G704 -- not the caller's URL
	if err != nil {
		return 0, fmt.Errorf("unreachable: %w", err)
	}
	answer, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	closeErr := resp.Body.Close()
	if resp.StatusCode < http.StatusBadRequest {
		return resp.StatusCode, errors.Join(readErr, closeErr)
	}
	return resp.StatusCode, errors.Join(fmt.Errorf("answered %d: %s", resp.StatusCode, bytes.TrimSpace(answer)),
		readErr, closeErr)
}

// fromMember says whether the request comes from the host of a member of the
// role's ring: each replica sends from the address it advertises there.
func (f *fanout) fromMember(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	role, err := f.cluster.Role(f.role)
	if err != nil {
		return false
	}
	members, err := role.Members()
	if err != nil {
		return false
	}
	for _, m := range members {
		if h, _, err := net.SplitHostPort(m.Addr); err == nil && h == host {
			return true
		}
	}
	return false
}

// broadcastLog is the logger of the replays, tagged like the cluster's own
// lines. Taken at each call: the default logger is the service's only once it
// has started.
func broadcastLog() *slog.Logger { return slog.Default().With("component", "broadcast") }
