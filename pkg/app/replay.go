package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/infrapi/lib/pkg/app/problem"
	"github.com/infrapi/lib/pkg/cluster"
)

// replayKey marks, in a request's context, a broadcast write another member
// published, applied here. Only the library sets it, in process: no header a
// caller could send stands for it.
type replayKey struct{}

type replayOf struct {
	identity broadcastIdentity
	by       string // the member it came in on
}

// replaying says whether the request is a broadcast write applied for
// another member.
func replaying(ctx context.Context) bool {
	_, ok := ctx.Value(replayKey{}).(replayOf)
	return ok
}

// applier runs, on this replica, the broadcast writes the others published
// for it: as the gossip brings them, or when it comes back from being down,
// within the replay window. One at a time, oldest first, through the engine,
// as if they had come in here — the same handlers, the same validation.
type applier struct {
	cluster *cluster.Cluster
	engine  http.Handler
	wake    chan struct{}
}

func newApplier(cl *cluster.Cluster, engine http.Handler) *applier {
	return &applier{cluster: cl, engine: engine, wake: make(chan struct{}, 1)}
}

// run applies until ctx ends, once ready says the replica has joined.
func (a *applier) run(ctx context.Context, ready func() bool) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
	client, err := a.cluster.KVClient(statusCodecID)
	if err != nil {
		broadcastLog().ErrorContext(ctx, "broadcast writes: not applied here", "err", err)
		return
	}
	go client.WatchKey(ctx, statusKey, func(interface{}) bool {
		select {
		case a.wake <- struct{}{}:
		default: // already woken: the next pass sees this change too
		}
		return true
	})

	// A pass at once — what the replica missed while it was down — then on
	// each change the watch signals: one that comes while a pass runs leaves
	// its signal in wake, for the next. Every 5s too, should the watch fall
	// silent: a write is late then, never forgotten.
	tick.Reset(5 * time.Second)
	for {
		a.pass(ctx, client)
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		case <-tick.C:
		}
	}
}

// kvGetter is what a pass reads the writes from.
type kvGetter interface {
	Get(ctx context.Context, key string) (interface{}, error)
}

// pass applies every write pending here, oldest first.
func (a *applier) pass(ctx context.Context, client kvGetter) {
	v, err := client.Get(ctx, statusKey)
	if err != nil {
		if ctx.Err() == nil {
			broadcastLog().WarnContext(ctx, "broadcast writes: not read", "err", err)
		}
		return
	}
	s, _ := v.(*broadcastStatus)
	if s == nil {
		return
	}
	self := a.cluster.InstanceID()
	type pending struct {
		id      string
		w       *writeRecord
		attempt int
	}
	var todo []pending
	for id, w := range s.Writes {
		if r, ok := w.Replicas[self]; ok && r.State == statePending && w.Request != nil {
			todo = append(todo, pending{id: id, w: w, attempt: r.Attempt})
		}
	}
	sort.Slice(todo, func(i, j int) bool {
		if !todo[i].w.At.Equal(todo[j].w.At) {
			return todo[i].w.At.Before(todo[j].w.At)
		}
		return todo[i].id < todo[j].id
	})
	for _, p := range todo {
		if ctx.Err() != nil {
			return
		}
		a.apply(ctx, p.id, p.w, p.attempt)
	}
}

// apply runs one write here and records how it went.
func (a *applier) apply(ctx context.Context, id string, w *writeRecord, attempt int) {
	req := w.Request
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	ctx = context.WithValue(ctx, replayKey{}, replayOf{identity: broadcastIdentity{id: id, at: w.At}, by: w.By})
	ctx = problem.WithCorrelationID(ctx, req.Correlation)

	target := w.Path
	if req.Query != "" {
		target += "?" + req.Query
	}
	start := time.Now()
	r, err := http.NewRequestWithContext(ctx, w.Method, target, bytes.NewReader(req.Body))
	out := replicaRecord{Attempt: attempt, State: stateDone}
	if err == nil {
		for k, v := range req.Header {
			r.Header.Set(k, v)
		}
		if req.Correlation != "" {
			r.Header.Set("X-Correlation-ID", req.Correlation)
		}
		r.RemoteAddr = "127.0.0.1:0" // in process
		answer := &captured{header: http.Header{}}
		a.engine.ServeHTTP(answer, r)
		out.Status = answer.status()
		if out.Status >= http.StatusBadRequest {
			err = fmt.Errorf("answered %d: %s", out.Status, bytes.TrimSpace(answer.body.Bytes()))
		}
	}
	attrs := []any{"from", w.By, "method", w.Method, "path", w.Path, "status", out.Status,
		"duration", time.Since(start).String(), "broadcast_id", id, "correlation_id", req.Correlation}
	outcome := "ok"
	if err != nil {
		outcome, out.State, out.Error = "failed", stateFailed, err.Error()
		broadcastLog().ErrorContext(ctx, "broadcast write: not applied here", append(attrs, "err", err)...)
	} else {
		broadcastLog().InfoContext(ctx, "broadcast write: applied", attrs...)
	}
	replayed.WithLabelValues(w.Method, req.Route, outcome).Inc()
	out.Updated = time.Now().UTC()
	record(ctx, a.cluster, id, writeRecord{At: w.At, By: w.By, Method: w.Method, Path: w.Path,
		Replicas: map[string]replicaRecord{a.cluster.InstanceID(): out}})
}

// captured is a write's answer, applied in process: its status, and the start
// of its body for the status page.
type captured struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (c *captured) Header() http.Header { return c.header }
func (c *captured) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *captured) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	if room := 4096 - c.body.Len(); room > 0 {
		c.body.Write(b[:min(room, len(b))])
	}
	return len(b), nil
}

func (c *captured) status() int {
	if c.code == 0 {
		return http.StatusOK
	}
	return c.code
}
