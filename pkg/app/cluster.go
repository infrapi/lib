package app

import (
	"net/url"

	"github.com/infrapi/lib/pkg/cluster"
	"github.com/sirupsen/logrus"
)

// newCluster builds this replica's membership in the writer ring. Disabled
// unless cluster.enabled: one replica needs no election. Its metrics go to the
// default registry, which the engine serves at /-/metrics; its zone, unless
// set, is the application's location.
func newCluster(cfg *loaded) (*cluster.Cluster, error) {
	return cluster.New(cfg.app, cfg.cluster, cluster.Options{
		Roles:  []cluster.RoleSpec{{Name: writerRole, Mode: cluster.ModeLeader}},
		Logger: logrus.WithField("component", "cluster"),
	})
}

// peerScheme is how replicas reach each other's API: the scheme of the URL
// the service publishes, http when it has none.
func peerScheme(cfg *loaded) string {
	if u, err := url.Parse(cfg.app.AppUrl); err == nil && u.Scheme != "" {
		return u.Scheme
	}
	return "http"
}
