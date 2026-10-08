// Package relay makes one replica — the leader of a ring that may span data
// centres — the only one that writes to the backends.
//
// Every replica answers; a write that reaches a replica which is not the
// leader is relayed to the leader, unchanged, and the leader's answer comes
// back unchanged. Reads are served where they arrive: they change nothing,
// and the backend they read is the same whichever replica asks.
//
// What this is not: a lock. Leadership is ownership of a ring token, kept by
// heartbeat, with no quorum. During a partition each side can elect its own
// leader until the partition heals, and both write. A service whose writes
// converge on their body — a create checks what exists, a replace sets what it
// was given, a delete of nothing succeeds — ends where one leader would. One
// whose writes do not must fence them in what it writes to.
package relay

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/infrapi/lib/pkg/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/infrapi/lib/pkg/app/problem"
)

// ForwardedHeader marks a request one replica relayed to another. It carries
// the relaying replica's id, and stops a request from being relayed twice.
const ForwardedHeader = "X-Infrapi-Forwarded-By"

// LeaderHeader is set on every write's answer: the replica that performed it.
const LeaderHeader = "X-Infrapi-Leader"

// Ring is what the forwarder needs from the cluster.
type Ring interface {
	// Leader returns the leader's id and HTTP address, and whether it is this
	// replica. An error means there is no leader to be had right now.
	Leader() (id, addr string, self bool, err error)
	// Self is this replica's id.
	Self() string
}

// relayed counts the writes this replica sent on to the leader, by the route of
// the contract they matched. The engine's own request metrics count them too,
// with the leader's status: this says which of them did not run here.
var relayed = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "infrapi_app_relayed_requests_total",
	Help: "Writes this replica relayed to the leader of the writer ring, by method and route.",
}, []string{"method", "route"})

// Forward relays writes to the leader. scheme is how replicas reach each
// other's API ("http" or "https"). Mounted on a route group, after routing:
// the route it records is the contract's.
func Forward(ring Ring, scheme string) gin.HandlerFunc {
	return forward(ring, scheme, false)
}

// ForwardAll relays every request to the leader, reads included: for what
// only the leader holds.
func ForwardAll(ring Ring, scheme string) gin.HandlerFunc {
	return forward(ring, scheme, true)
}

func forward(ring Ring, scheme string, reads bool) gin.HandlerFunc {
	transport := &http.Transport{
		Proxy:                 nil, // replicas talk directly, never through an egress proxy
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		ResponseHeaderTimeout: 2 * time.Minute, // a dashboard write with its permissions takes a few calls
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	return func(c *gin.Context) {
		r := c.Request
		if !reads && !isWrite(r.Method) {
			c.Next()
			return
		}

		id, addr, self, err := ring.Leader()
		if err != nil {
			problem.Abort(c, noLeader(err))
			return
		}
		if self {
			c.Header(LeaderHeader, id)
			server.LogField(c, "relayed", false)
			c.Next()
			return
		}

		// Relayed already, and this replica is not the leader either:
		// leadership moved during the hop. Relaying again could bounce
		// between two replicas whose views disagree; the caller retries.
		if by := r.Header.Get(ForwardedHeader); by != "" {
			problem.Abort(c, &problem.Error{Status: http.StatusServiceUnavailable, Code: "not_leader",
				Title: "Not the leader", Retryable: true,
				Detail: "relayed by " + by + " to a replica that is no longer the leader; retry"})
			return
		}

		server.LogField(c, "relayed", true)
		server.LogField(c, "leader", id)
		relayed.WithLabelValues(r.Method, c.FullPath()).Inc()

		target := &url.URL{Scheme: scheme, Host: addr}
		proxy := &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.Out.Host = pr.In.Host
				pr.Out.Header.Set(ForwardedHeader, ring.Self())
				// One id for the whole call, in both replicas' logs.
				pr.Out.Header.Set("X-Correlation-ID", problem.CorrelationID(pr.In.Context()))
			},
			ModifyResponse: func(resp *http.Response) error {
				// This replica already set it, to the same value.
				resp.Header.Del("X-Correlation-ID")
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				// The leader may or may not have received the request: a
				// write sent with an Idempotency-Key is safe to repeat.
				problem.Write(w, r, &problem.Error{Status: http.StatusServiceUnavailable,
					Code: "leader_unreachable", Title: "Leader unreachable", Retryable: true,
					Detail: "the leader " + id + " (" + addr + ") did not answer: " + err.Error()})
			},
		}
		proxy.ServeHTTP(c.Writer, r)
		c.Abort()
	}
}

func isWrite(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func noLeader(err error) *problem.Error {
	return &problem.Error{Status: http.StatusServiceUnavailable, Code: "no_leader", Title: "No leader",
		Retryable: true, Detail: "no replica is the leader right now — the cluster is starting, stopping " +
			"or electing; retry (" + err.Error() + ")"}
}
