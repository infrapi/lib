package server

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsPath is where the engine serves the Prometheus exposition format.
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

// middleware records every request once it has been handled.
func (m *metrics) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		// the route pattern, not the URL, so /users/:id stays one series
		path := c.FullPath()
		if path == "" {
			path = unmatchedPath
		}

		m.requests.WithLabelValues(c.Request.Method, path, strconv.Itoa(c.Writer.Status())).Inc()
		m.duration.WithLabelValues(c.Request.Method, path).Observe(time.Since(start).Seconds())
	}
}

// handler serves the exposition format. A collector that misbehaves is reported
// in the payload rather than turned into a failed scrape.
func (m *metrics) handler() gin.HandlerFunc {
	return gin.WrapH(promhttp.HandlerFor(m.gatherer, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	}))
}
