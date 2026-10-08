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

// Headers a replayed write carries: the identity the first replica gave it.
const (
	WriteIDHeader   = "X-Infrapi-Write-ID"
	WriteTimeHeader = "X-Infrapi-Write-Time"
)

type writeKey struct{}

type writeIdentity struct {
	id string
	at time.Time
}

// WriteID is the identity of the write being handled, the same on every
// replica that runs it: a handler derives what it creates from it — an id, a
// name — where it would otherwise draw one at random, so that every replica
// creates the same thing. With an Idempotency-Key, it is derived from the key:
// a retry creates the same thing again. Empty outside a write.
func WriteID(ctx context.Context) string {
	w, _ := ctx.Value(writeKey{}).(writeIdentity)
	return w.id
}

// WriteTime is when the write was received by the first replica, the same on
// every replica that runs it: a handler uses it instead of time.Now for what it
// records. Zero outside a write.
func WriteTime(ctx context.Context) time.Time {
	w, _ := ctx.Value(writeKey{}).(writeIdentity)
	return w.at
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newWriteIdentity is a write's identity: from its Idempotency-Key when it has
// one, at random otherwise.
func newWriteIdentity(r *http.Request) writeIdentity {
	w := writeIdentity{at: time.Now().UTC()}
	if key := r.Header.Get(idempotency.Header); key != "" {
		sum := sha256.Sum256([]byte(r.URL.Path + "\n" + key))
		w.id = strings.ToLower(idEncoding.EncodeToString(sum[:16]))
	} else {
		w.id = strings.ToLower(rand.Text())
	}
	return w
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

func (f *fanout) handle(c *gin.Context) {
	r := c.Request
	if !isWrite(r.Method) {
		c.Next()
		return
	}

	// A replay from another member: its identity, and no replay of our own.
	if by := r.Header.Get(ForwardedHeader); by != "" && r.Header.Get(WriteIDHeader) != "" && f.role != "" {
		at, err := time.Parse(time.RFC3339Nano, r.Header.Get(WriteTimeHeader))
		if err != nil {
			problem.Abort(c, problem.Invalid("%s: %v", WriteTimeHeader, err))
			return
		}
		if !f.fromMember(r) {
			problem.Abort(c, &problem.Error{Status: http.StatusForbidden, Code: "not_a_member", Title: "Not a member",
				Detail: "replayed writes are taken from the members of the " + f.role + " ring only"})
			return
		}
		server.LogField(c, "broadcast_from", by)
		f.run(c, writeIdentity{id: r.Header.Get(WriteIDHeader), at: at})
		return
	}

	w := newWriteIdentity(r)
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

	f.run(c, w)
	if c.Writer.Status() >= http.StatusBadRequest {
		return // refused or failed here: the others are not asked
	}
	f.replay(c, w, body)
}

// run handles the write here, with its identity in the request's context.
func (f *fanout) run(c *gin.Context, w writeIdentity) {
	f.identify(c, w)
	c.Next()
}

// identify puts the write's identity in the request's context and its log line.
func (f *fanout) identify(c *gin.Context, w writeIdentity) {
	server.LogField(c, "write_id", w.id)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), writeKey{}, w))
}

// replay sends the write to every other member of the role, at the same time,
// and waits for them: the caller's call ends when every replica has it.
func (f *fanout) replay(c *gin.Context, w writeIdentity, body []byte) {
	r := c.Request
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()

	role, err := f.cluster.Role(f.role)
	if err != nil {
		broadcastLog().ErrorContext(ctx, "broadcast write: no role to replay on", "role", f.role, "err", err)
		return
	}
	members, err := role.Members()
	if err != nil {
		broadcastLog().ErrorContext(ctx, "broadcast write: no members to replay on", "role", f.role, "err", err)
		return
	}

	self := f.cluster.InstanceID()
	var (
		wg           sync.WaitGroup
		mu           sync.Mutex
		done, failed []string
	)
	for _, m := range members {
		if m.ID == self {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			status, err := f.send(ctx, r, m.Addr, w, body)
			attrs := []any{"instance", m.ID, "addr", m.Addr, "method", r.Method, "path", r.URL.Path,
				"status", status, "duration", time.Since(start), "write_id", w.id,
				"correlation_id", problem.CorrelationID(r.Context())}
			outcome := "ok"
			if err != nil {
				outcome = "failed"
				broadcastLog().ErrorContext(ctx, "broadcast write: a replica did not take it", append(attrs, "err", err)...)
			} else {
				broadcastLog().InfoContext(ctx, "broadcast write: replayed", attrs...)
			}
			replayed.WithLabelValues(r.Method, c.FullPath(), outcome).Inc()
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
	// The caller's access line says where the write went.
	slices.Sort(done)
	slices.Sort(failed)
	server.LogField(c, "broadcast_to", strings.Join(done, ","))
	if len(failed) > 0 {
		server.LogField(c, "broadcast_failed", strings.Join(failed, ","))
	}
}

// send replays the write on one replica: same method, path, query, headers
// and body, plus its identity.
// It returns the replica's status, 0 when it did not answer.
func (f *fanout) send(ctx context.Context, in *http.Request, addr string, w writeIdentity, body []byte) (int, error) {
	target := url.URL{Scheme: f.scheme, Host: addr, Path: in.URL.Path, RawQuery: in.URL.RawQuery}
	req, err := http.NewRequestWithContext(ctx, in.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header = in.Header.Clone()
	req.Host = in.Host
	// The idempotency store would answer the replay with this replica's
	// response instead of running it: the write's identity does that job.
	req.Header.Del(idempotency.Header)
	req.Header.Set(ForwardedHeader, f.cluster.InstanceID())
	req.Header.Set(WriteIDHeader, w.id)
	req.Header.Set(WriteTimeHeader, w.at.Format(time.RFC3339Nano))
	req.Header.Set("X-Correlation-ID", problem.CorrelationID(in.Context()))

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
