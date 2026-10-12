package app

import (
	"net/url"
	"sync/atomic"

	"github.com/grafana/dskit/kv/codec"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// clusterRegisterer, when set, gives each cluster its own metrics registry:
// the tests run several replicas in one process. Unset, the default one.
var clusterRegisterer atomic.Pointer[func() prometheus.Registerer]

// newCluster builds this replica's membership in the writer ring. Disabled
// unless cluster.enabled: one replica needs no election. Its metrics go to the
// default registry, which the engine serves at /-/metrics; its zone, unless
// set, is the application's location.
func newCluster(cfg *loaded) (*cluster.Cluster, error) {
	return cluster.New(cfg.app, cfg.cluster, clusterOptions(cfg))
}

// clusterOptions declares the rings this replica may join: the service's, and
// writer when a leader is needed; none when it joins none.
func clusterOptions(cfg *loaded) cluster.Options {
	opts := cluster.Options{Roles: cfg.roles, Logger: logrus.WithField("component", "cluster"),
		// The broadcast writes and their outcomes, in one key of the store.
		Codecs: []codec.Codec{statusCodec{}}}
	if reg := clusterRegisterer.Load(); reg != nil {
		opts.Registerer = (*reg)()
	}
	return opts
}

// peerScheme is how replicas reach each other's API: the scheme of the URL
// the service publishes, http when it has none.
func peerScheme(cfg *loaded) string {
	if u, err := url.Parse(cfg.app.AppUrl); err == nil && u.Scheme != "" {
		return u.Scheme
	}
	return "http"
}
