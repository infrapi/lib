package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/infrapi/lib/pkg/app/problem"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// notesV1 gives its writes to an all-mode ring: every replica takes them.
var notesV1 = []byte(`openapi: 3.0.3
info: {title: Notes, version: "1"}
x-infrapi-writes: everybody
paths:
  /notes/v1/notes:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema: {type: object, required: [text], properties: {text: {type: string}}}
      responses: {"201": {description: created}}
  /notes/v1/later:
    post:
      x-infrapi-broadcast: async
      requestBody:
        required: true
        content:
          application/json:
            schema: {type: object, required: [text], properties: {text: {type: string}}}
      responses: {"201": {description: created}}
`)

// replica is one of the test's replicas: what its handler saw.
type replica struct {
	mu       sync.Mutex
	notes    []string // text@broadcast id
	failOnce string   // the text whose first write fails
	stop     context.CancelFunc
	done     chan error
	base     string
}

func (r *replica) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.notes...)
}

// startReplica runs one replica of the notes service on the in-memory store,
// which the replicas of a test binary share.
func startReplica(t *testing.T, id, addr string, r *replica) {
	t.Helper()
	path := writeFile(t, required+"  listen_address: "+addr+`
cluster:
  enabled: true
  kv_store: inmemory
  instance_id: `+id+`
  heartbeat_period: 100ms
  heartbeat_timeout: 1s
  keep_instance_in_ring_on_shutdown: true
shutdown: {drain_delay: 0s}
`)
	note := func(c *Context) {
		var in struct {
			Text string `json:"text"`
		}
		if err := c.Bind(&in); err != nil {
			c.Error(err)
			return
		}
		r.mu.Lock()
		if in.Text == r.failOnce {
			r.failOnce = ""
			r.mu.Unlock()
			c.Error(problem.Unavailable("the backend is down"))
			return
		}
		r.notes = append(r.notes, in.Text+"@"+BroadcastID(c.Request().Context()))
		r.mu.Unlock()
		c.JSON(http.StatusCreated, map[string]string{"text": in.Text})
	}
	spec := Spec{
		Name:  "notes-api",
		Roles: []cluster.RoleSpec{{Name: "everybody", Mode: cluster.ModeAll}},
		API: func(context.Context, *Runtime) ([]Version, error) {
			return []Version{{
				OpenAPI: server.OpenAPISpec{Name: "notes v1", Path: "notes/v1.yaml", Content: notesV1},
				Routes: func(rt Router) {
					rt.POST("/notes/v1/notes", note)
					rt.POST("/notes/v1/later", note)
				},
			}}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.stop, r.done, r.base = cancel, make(chan error, 1), "http://"+addr
	go func() { r.done <- Run(ctx, spec, path) }()
	require.Eventually(t, func() bool {
		select {
		case err := <-r.done:
			require.NoError(t, err, "the replica stopped")
			t.Fatal("the replica stopped")
		default:
		}
		resp, err := http.Get(r.base + ReadyPath)
		if err != nil {
			return false
		}
		require.NoError(t, resp.Body.Close())
		return resp.StatusCode == http.StatusOK
	}, 15*time.Second, 50*time.Millisecond)
}

func (r *replica) shutdown(t *testing.T) {
	t.Helper()
	r.stop()
	select {
	case err := <-r.done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the replica did not stop")
	}
}

func post(t *testing.T, base, path, text string) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Post(base+path, "application/json", strings.NewReader(`{"text":"`+text+`"}`))
	require.NoError(t, err)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp.StatusCode, string(b), resp.Header
}

func broadcastStatusOf(t *testing.T, base, id string) map[string]any {
	t.Helper()
	resp, err := http.Get(base + BroadcastStatusPath + "/" + id)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NoError(t, resp.Body.Close())
	return out
}

// A broadcast write goes to the other replica through the gossip, and is run
// there by the same handler, with the same broadcast id; a replica that was
// down when a write was made applies it when back.
func TestBroadcastThroughTheGossip(t *testing.T) {
	own := func() prometheus.Registerer { return prometheus.NewRegistry() }
	clusterRegisterer.Store(&own)
	t.Cleanup(func() { clusterRegisterer.Store(nil) })
	suffix := fmt.Sprint(time.Now().UnixNano())
	a, b := &replica{}, &replica{}
	addrB := freePort(t)
	startReplica(t, "a-"+suffix, freePort(t), a)
	t.Cleanup(func() { a.shutdown(t) })
	startReplica(t, "b-"+suffix, addrB, b)

	// Strict: answered once both took it.
	code, body, hdr := post(t, a.base, "/notes/v1/notes", "one")
	require.Equal(t, http.StatusCreated, code, body)
	id := hdr.Get(BroadcastIDHeader)
	require.NotEmpty(t, id)
	assert.Equal(t, []string{"one@" + id}, a.seen())
	assert.Equal(t, []string{"one@" + id}, b.seen(), "applied on b before a answered")
	st := broadcastStatusOf(t, b.base, id)
	assert.Equal(t, "complete", st["status"])
	assert.NotContains(t, st, "request", "the request is not shown")

	// Failed on b: strict answers 502; the retry, with the same key, asks b
	// again, and only b.
	b.mu.Lock()
	b.failOnce = "flaky"
	b.mu.Unlock()
	req := func() (int, string, http.Header) {
		r, err := http.NewRequest(http.MethodPost, a.base+"/notes/v1/notes", strings.NewReader(`{"text":"flaky"}`))
		require.NoError(t, err)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "k-"+suffix)
		resp, err := http.DefaultClient.Do(r)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		return resp.StatusCode, string(body), resp.Header
	}
	code, body, hdr = req()
	require.Equal(t, http.StatusBadGateway, code, body)
	assert.Contains(t, body, `"code":"broadcast_incomplete"`)
	flaky := hdr.Get(BroadcastIDHeader)
	assert.Equal(t, "partial", broadcastStatusOf(t, a.base, flaky)["status"])
	code, body, hdr = req()
	require.Equal(t, http.StatusCreated, code, body)
	assert.Equal(t, flaky, hdr.Get(BroadcastIDHeader), "the same write")
	assert.Equal(t, []string{"one@" + id, "flaky@" + flaky}, b.seen())
	st = broadcastStatusOf(t, a.base, flaky)
	assert.Equal(t, "complete", st["status"])
	assert.EqualValues(t, 1, st["replicas"].(map[string]any)["b-"+suffix].(map[string]any)["attempt"])

	// Down, then back: b applies what it missed.
	b.shutdown(t)
	code, body, hdr = post(t, a.base, "/notes/v1/later", "two")
	require.Equal(t, http.StatusCreated, code, body)
	later := hdr.Get(BroadcastIDHeader)
	assert.Equal(t, "pending", broadcastStatusOf(t, a.base, later)["status"])

	b2 := &replica{}
	startReplica(t, "b-"+suffix, addrB, b2)
	t.Cleanup(func() { b2.shutdown(t) })
	require.Eventually(t, func() bool { return len(b2.seen()) == 1 }, 10*time.Second, 50*time.Millisecond)
	assert.Equal(t, []string{"two@" + later}, b2.seen(), "only what it missed")
	require.Eventually(t, func() bool {
		return broadcastStatusOf(t, a.base, later)["status"] == "complete"
	}, 5*time.Second, 50*time.Millisecond)
}
