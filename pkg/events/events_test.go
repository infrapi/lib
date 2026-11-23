package events

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memory is a Sender keeping what it is sent.
type memory struct {
	mu      sync.Mutex
	created map[string][]Event
	series  map[string]Series
	n       int
	fail    error
}

func newMemory() *memory { return &memory{created: map[string][]Event{}, series: map[string]Series{}} }

func (m *memory) Create(_ context.Context, ns string, evs []Event) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return nil, m.fail
	}
	ids := make([]string, len(evs))
	for i := range evs {
		m.n++
		ids[i] = "id-" + strconv.Itoa(m.n)
	}
	m.created[ns] = append(m.created[ns], evs...)
	return ids, nil
}

func (m *memory) UpdateSeries(_ context.Context, _, id string, s Series) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.series[id] = s
	return nil
}

var vm = Regarding{API: "compute.infrapi.io", Namespace: "core", Resource: "vms", Name: "web-1"}

// The same event 30 times within the window: one event, its series 30.
func TestSeries(t *testing.T) {
	m := newMemory()
	r := NewRecorder(Options{Controller: "compute", Sender: m, BatchWait: 10 * time.Millisecond})
	for range 30 {
		r.Event(vm, Warning, "VMCreateFailed", "no capacity")
	}
	r.Event(vm, Normal, "VMCreated", "") // another reason: another event
	require.NoError(t, r.Close(context.Background()))
	m.mu.Lock()
	defer m.mu.Unlock()
	require.Len(t, m.created["core"], 2)
	assert.Equal(t, "compute", m.created["core"][0].ReportingController)
	assert.Equal(t, 30, m.series["id-1"].Count)
	_, other := m.series["id-2"]
	assert.False(t, other, "a series of one is not sent")
}

// Past the window, the same event is a new one.
func TestSeriesWindow(t *testing.T) {
	m := newMemory()
	now := time.Now()
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	r := NewRecorder(Options{Sender: m, BatchWait: time.Millisecond, SeriesWindow: time.Minute, Now: clock})
	r.Event(vm, Normal, "VMCreated", "")
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	r.Event(vm, Normal, "VMCreated", "")
	require.NoError(t, r.Close(context.Background()))
	assert.Len(t, m.created["core"], 2)
}

// An object over its spam limit gets no new event: counted in a series
// when it has one, dropped otherwise; another object is not affected.
func TestSpam(t *testing.T) {
	m := newMemory()
	dropped := testutil.ToFloat64(eventsTotal.WithLabelValues("dropped"))
	r := NewRecorder(Options{Sender: m, BatchWait: time.Millisecond, SpamBurst: 3, SpamRefill: time.Hour})
	for i := range 10 {
		r.Event(vm, Warning, "Reason"+strconv.Itoa(i), "") // ten different events
	}
	other := vm
	other.Name = "web-2"
	r.Event(other, Normal, "VMCreated", "")
	require.NoError(t, r.Close(context.Background()))
	n := 0
	for _, e := range m.created["core"] {
		if e.Regarding.Name == "web-1" {
			n++
		}
	}
	assert.Equal(t, 3, n, "the burst, then nothing")
	assert.Len(t, m.created["core"], 4)
	assert.Equal(t, dropped+7, testutil.ToFloat64(eventsTotal.WithLabelValues("dropped")))
}

// A batch per namespace, the cluster's apart.
func TestNamespaces(t *testing.T) {
	m := newMemory()
	r := NewRecorder(Options{Sender: m, BatchWait: 20 * time.Millisecond})
	r.Event(vm, Normal, "A", "")
	r.Event(Regarding{API: "compute.infrapi.io", Namespace: "acme", Resource: "vms", Name: "x"}, Normal, "A", "")
	r.Event(Regarding{API: "compute.infrapi.io", Resource: "hosts", Name: "h-1"}, Normal, "HostDrained", "")
	require.NoError(t, r.Close(context.Background()))
	assert.Len(t, m.created["core"], 1)
	assert.Len(t, m.created["acme"], 1)
	assert.Len(t, m.created[""], 1)
}

// Kayra unreachable: the fallback has them.
func TestFallback(t *testing.T) {
	m, log := newMemory(), newMemory()
	m.fail = errors.New("connection refused")
	r := NewRecorder(Options{Sender: m, Fallback: log, BatchWait: time.Millisecond})
	r.Event(vm, Normal, "VMCreated", "")
	require.NoError(t, r.Close(context.Background()))
	assert.Len(t, log.created["core"], 1)
}

// The Kayra sender: its events group from kayra.json, the token, a batch,
// a series' PATCH, and a 429 waited out.
func TestKayra(t *testing.T) {
	var posts, patches atomic.Int32
	var limited atomic.Bool
	limited.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/.well-known/kayra.json":
			_, _ = w.Write([]byte(`{"apis":{"events":"/apis/events.infrapi.io/v1"}}`))
			return
		case r.Header.Get("Authorization") != "Bearer tok":
			w.WriteHeader(http.StatusUnauthorized)
			return
		case r.Method == http.MethodPost && r.URL.Path == "/apis/events.infrapi.io/v1/namespaces/core/events":
			if limited.CompareAndSwap(true, false) {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			posts.Add(1)
			var body struct{ Items []Event }
			_ = json.NewDecoder(r.Body).Decode(&body)
			out := []map[string]string{}
			for range body.Items {
				out = append(out, map[string]string{"id": "e" + strconv.Itoa(int(posts.Load()))})
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/events.infrapi.io/v1/namespaces/core/events/e1":
			var body map[string]Series
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["series"].Count == 5 {
				patches.Add(1)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	k := Kayra(srv.URL, func(context.Context) (string, error) { return "tok", nil }, nil)
	r := NewRecorder(Options{Controller: "compute", Sender: k, BatchWait: 10 * time.Millisecond})
	start := time.Now()
	for range 5 {
		r.Event(vm, Warning, "VMCreateFailed", "no capacity")
	}
	require.NoError(t, r.Close(context.Background()))
	assert.Equal(t, int32(1), posts.Load(), "one event, after the 429")
	assert.Equal(t, int32(1), patches.Load(), "its series of 5")
	assert.GreaterOrEqual(t, time.Since(start), time.Second, "Retry-After waited")
}
