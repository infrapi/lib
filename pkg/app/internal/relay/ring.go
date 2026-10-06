package relay

import "github.com/infrapi/lib/pkg/cluster"

// Cluster adapts the cluster library's ring named Role. The role is looked up
// on every call: it only exists once the cluster has started, and a call
// before that is answered like any moment without a leader.
type Cluster struct {
	C    *cluster.Cluster
	Role string
}

// Leader implements Ring.
func (c Cluster) Leader() (string, string, bool, error) {
	role, err := c.C.Role(c.Role)
	if err != nil {
		return "", "", false, err
	}
	m, err := role.Leader()
	if err != nil {
		return "", "", false, err
	}
	return m.ID, m.Addr, m.ID == c.C.InstanceID(), nil
}

// Self implements Ring.
func (c Cluster) Self() string { return c.C.InstanceID() }

// Members returns the HTTP addresses of the replicas in the writer ring.
func (c Cluster) Members() ([]string, error) {
	role, err := c.C.Role(c.Role)
	if err != nil {
		return nil, err
	}
	members, err := role.Members()
	if err != nil {
		return nil, err
	}
	addrs := make([]string, 0, len(members))
	for _, m := range members {
		addrs = append(addrs, m.Addr)
	}
	return addrs, nil
}
