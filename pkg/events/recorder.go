package events

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/infrapi/lib/pkg/logger"
)

// Options configure a Recorder. Every duration and size has a default.
type Options struct {
	// Controller is the events' reporting_controller: the service's name.
	Controller string
	// Sender sends them: Kayra(...). The log when nil.
	Sender Sender
	// Fallback keeps what Sender cannot send: the default logger's log.
	Fallback Sender
	// Queue is how many events wait to be sent (1000): a full queue drops.
	Queue int
	// Batch is how many go at once (100), or after BatchWait (1s).
	Batch     int
	BatchWait time.Duration
	// SeriesWindow merges the same event seen again within it (10m).
	SeriesWindow time.Duration
	// SeriesFlush is how often a series' count is sent (30s).
	SeriesFlush time.Duration
	// SpamBurst events per object (25), then one every SpamRefill (5m).
	SpamBurst  int
	SpamRefill time.Duration
	// Now is the clock: time.Now when nil.
	Now func() time.Time
}

var eventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "infrapi_events_total", Help: "Events recorded, by result: sent, aggregated, dropped, failed.",
}, []string{"result"})

func init() { prometheus.MustRegister(eventsTotal) }

// Recorder records events: Event queues one and returns at once. The same
// event seen again within SeriesWindow is counted in a series, its count
// sent every SeriesFlush; an object over its spam limit gets no new event,
// only its series counted. Close sends what is left.
type Recorder struct {
	o Options

	queue chan Event
	done  chan struct{}
	once  sync.Once

	mu     sync.Mutex
	series map[string]*series
	spam   map[string]*bucket
}

type series struct {
	namespace string
	id        string // Kayra's, once sent
	first     time.Time
	count     int
	last      time.Time
	sent      int // the count Kayra has
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRecorder starts a recorder.
func NewRecorder(o Options) *Recorder {
	if o.Fallback == nil {
		o.Fallback = Log(nil)
	}
	if o.Sender == nil {
		o.Sender = o.Fallback
	}
	o.Queue = cmpOr(o.Queue, 1000)
	o.Batch = cmpOr(o.Batch, 100)
	o.BatchWait = cmpOr(o.BatchWait, time.Second)
	o.SeriesWindow = cmpOr(o.SeriesWindow, 10*time.Minute)
	o.SeriesFlush = cmpOr(o.SeriesFlush, 30*time.Second)
	o.SpamBurst = cmpOr(o.SpamBurst, 25)
	o.SpamRefill = cmpOr(o.SpamRefill, 5*time.Minute)
	if o.Now == nil {
		o.Now = time.Now
	}
	r := &Recorder{o: o, queue: make(chan Event, o.Queue), done: make(chan struct{}),
		series: map[string]*series{}, spam: map[string]*bucket{}}
	go r.run()
	return r
}

func cmpOr[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

// Event records an event of type (Normal, Warning) about regarding.
func (r *Recorder) Event(regarding Regarding, typ, reason, note string) {
	r.Emit(Event{Regarding: regarding, Type: typ, Reason: reason, Note: note})
}

// Emit records e, its controller and time set when empty.
func (r *Recorder) Emit(e Event) {
	if r == nil {
		return
	}
	now := r.o.Now()
	if e.ReportingController == "" {
		e.ReportingController = r.o.Controller
	}
	if e.Time.IsZero() {
		e.Time = now
	}
	key := e.key()

	r.mu.Lock()
	s := r.series[key]
	if s != nil && now.Sub(s.first) < r.o.SeriesWindow {
		s.count++
		s.last = now
		r.mu.Unlock()
		eventsTotal.WithLabelValues("aggregated").Inc()
		return
	}
	if !r.allow(e.object(), now) {
		if s != nil { // over the limit: counted in its series still
			s.count++
			s.last = now
			r.mu.Unlock()
			eventsTotal.WithLabelValues("aggregated").Inc()
			return
		}
		r.mu.Unlock()
		eventsTotal.WithLabelValues("dropped").Inc()
		return
	}
	r.series[key] = &series{namespace: e.Regarding.Namespace, first: now, count: 1, last: now, sent: 1}
	r.mu.Unlock()

	select {
	case r.queue <- e:
	default:
		eventsTotal.WithLabelValues("dropped").Inc()
	}
}

// allow takes a token of object's bucket. Caller holds r.mu.
func (r *Recorder) allow(object string, now time.Time) bool {
	b := r.spam[object]
	if b == nil {
		b = &bucket{tokens: float64(r.o.SpamBurst), last: now}
		r.spam[object] = b
	}
	b.tokens = math.Min(float64(r.o.SpamBurst), b.tokens+float64(now.Sub(b.last))/float64(r.o.SpamRefill))
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Close sends what is queued and every series' count, within ctx.
func (r *Recorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.once.Do(func() { close(r.queue) })
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.flushSeries(ctx, true)
	return nil
}

// run sends the queue in batches, and the series every SeriesFlush.
func (r *Recorder) run() {
	defer close(r.done)
	var batch []Event
	wait := time.NewTimer(r.o.BatchWait)
	wait.Stop()
	tick := time.NewTicker(r.o.SeriesFlush)
	defer tick.Stop()
	send := func() {
		if len(batch) > 0 {
			r.send(batch)
			batch = nil
		}
		wait.Stop()
	}
	for {
		select {
		case e, ok := <-r.queue:
			if !ok {
				send()
				return
			}
			if len(batch) == 0 {
				wait.Reset(r.o.BatchWait)
			}
			batch = append(batch, e)
			if len(batch) >= r.o.Batch {
				send()
			}
		case <-wait.C:
			send()
		case <-tick.C:
			r.flushSeries(context.Background(), false)
		}
	}
}

// send sends batch, a request per namespace; on a 429 it waits as asked
// (a minute at most) and tries once more; what cannot be sent goes to the
// fallback.
func (r *Recorder) send(batch []Event) {
	byNS := map[string][]Event{}
	var order []string
	for _, e := range batch {
		ns := e.Regarding.Namespace
		if _, ok := byNS[ns]; !ok {
			order = append(order, ns)
		}
		byNS[ns] = append(byNS[ns], e)
	}
	for _, ns := range order {
		evs := byNS[ns]
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		ids, err := r.o.Sender.Create(ctx, ns, evs)
		var ra RetryAfter
		if errors.As(err, &ra) {
			time.Sleep(min(ra.Wait, time.Minute))
			ids, err = r.o.Sender.Create(ctx, ns, evs)
		}
		cancel()
		if err != nil {
			eventsTotal.WithLabelValues("failed").Add(float64(len(evs)))
			logger.L().Warn("events: sent to the log instead", logger.String("error", err.Error()), logger.Int("events", len(evs)))
			_, _ = r.o.Fallback.Create(context.Background(), ns, evs)
			continue
		}
		eventsTotal.WithLabelValues("sent").Add(float64(len(evs)))
		r.mu.Lock()
		for i, e := range evs {
			if s := r.series[e.key()]; s != nil && i < len(ids) {
				s.id = ids[i]
			}
		}
		r.mu.Unlock()
	}
}

// flushSeries sends each series whose count Kayra does not have, and
// forgets those past their window; all, at Close.
func (r *Recorder) flushSeries(ctx context.Context, final bool) {
	now := r.o.Now()
	type update struct {
		ns, id string
		s      Series
	}
	var updates []update
	r.mu.Lock()
	for key, s := range r.series {
		if s.id != "" && s.count > s.sent {
			updates = append(updates, update{s.namespace, s.id, Series{Count: s.count, LastObserved: s.last}})
			s.sent = s.count
		}
		if final || now.Sub(s.last) > r.o.SeriesWindow+r.o.SeriesFlush {
			delete(r.series, key)
		}
	}
	for obj, b := range r.spam {
		if now.Sub(b.last) > time.Duration(r.o.SpamBurst)*r.o.SpamRefill {
			delete(r.spam, obj)
		}
	}
	r.mu.Unlock()
	for _, u := range updates {
		if err := r.o.Sender.UpdateSeries(ctx, u.ns, u.id, u.s); err != nil {
			eventsTotal.WithLabelValues("failed").Inc()
		}
	}
}
