package server

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsPath is where the engine serves the Prometheus exposition format,
// relative to a root: the requests of that root's routes.
const MetricsPath = "/-/metrics"

// unmatchedPath keeps the path label bounded: labelling with the raw URL would
// create a time series per request that matched no route.
const unmatchedPath = "unmatched"

type metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec

	// gatherer exposes both these collectors and whatever the service itself
	// registered on the default registry, so promauto keeps working.
	gatherer prometheus.Gatherer
}

// newMetrics builds the HTTP collectors on a registry of their own, labelled
// with the service FQDN. A private registry means several engines can live in
// the same process without fighting over collector names.
func newMetrics(fqdn string) *metrics {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Requests handled, by method, route and status code.",
		}, []string{"method", "path", "status"}),

		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Request duration in seconds, by method and route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "path"}),
	}

	registry := prometheus.NewRegistry()
	prometheus.WrapRegistererWith(prometheus.Labels{"name": fqdn}, registry).
		MustRegister(m.requests, m.duration)
	m.gatherer = prometheus.Gatherers{prometheus.DefaultGatherer, registry}

	return m
}

// record counts one handled request, by its route pattern.
func (m *metrics) record(method, path string, status int, took time.Duration) {
	m.requests.WithLabelValues(method, path, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(method, path).Observe(took.Seconds())
}

// rootMetrics keeps a metrics per root: a root's <root>/-/metrics counts the
// requests of its routes, those under no root counting as the primary's.
type rootMetrics struct {
	roots  roots
	byRoot map[string]*metrics
}

func newRootMetrics(fqdn string, rs roots) *rootMetrics {
	m := &rootMetrics{roots: rs, byRoot: map[string]*metrics{}}
	for _, root := range rs.list {
		m.byRoot[root] = newMetrics(fqdn)
	}
	return m
}

// middleware records every request in its root's metrics, the route's
// pattern telling which, or the path for a request that matched none.
func (m *rootMetrics) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		path := c.FullPath()
		owner := m.roots.owner(c.Request.URL.Path)
		if path == "" {
			path = unmatchedPath
		} else {
			owner = m.roots.owner(path)
		}
		m.byRoot[owner].record(c.Request.Method, path, c.Writer.Status(), time.Since(start))
	}
}

// handler serves root's metrics.
func (m *rootMetrics) handler(root string) gin.HandlerFunc {
	return m.byRoot[root].handler()
}

// handler serves the exposition format. A collector that misbehaves is reported
// in the payload rather than turned into a failed scrape.
func (m *metrics) handler() gin.HandlerFunc {
	return gin.WrapH(promhttp.HandlerFor(m.gatherer, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	}))
}
