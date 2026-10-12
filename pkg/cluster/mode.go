package cluster

import "github.com/grafana/dskit/ring"

// Mode is how the members of a role ring divide work between themselves.
type Mode int

const (
	// ModeLeader elects a single acting member, the one that owns the leader
	// token. Every other member stays passive.
	ModeLeader Mode = iota + 1

	// ModeAll makes every healthy member of the role ring an acting one: a
	// group of replicas, all of which act. A write a contract gives such a
	// role is replayed on each member (infrapi/app's broadcast writes).
	ModeAll
)

// String returns the name the mode is known by in logs and on the status page.
func (m Mode) String() string {
	switch m {
	case ModeLeader:
		return "leader"
	case ModeAll:
		return "all"
	default:
		return "unknown"
	}
}

// modeSpec is the per-mode behaviour, kept in one place so that the ring
// parameters and the eligibility rule of a mode cannot drift apart. Adding a
// sharded mode is a constant and a row here.
type modeSpec struct {
	// numTokens is how many ring tokens a member registers.
	numTokens int

	// replicationFactor is the ring replication factor. It must be at least 1:
	// the ring rejects anything lower.
	replicationFactor int

	// op is the instance state filter used when reading the ring.
	op ring.Operation
}

// modeSpecs holds the parameters of every known mode.
//
// ModeLeader counts LEAVING members as eligible, which is what keeps a rolling
// restart from changing leader at every step: a draining replica keeps the
// token until its heartbeat goes stale. ModeAll deliberately does not,
// because a replica that is draining must stop working.
//
// Both register a single token. An all-mode role never looks up a token, but a
// member with none renders as an oddity on the status page and could not be
// switched to leader mode without rewriting the ring.
var modeSpecs = map[Mode]modeSpec{
	ModeLeader: {
		numTokens:         1,
		replicationFactor: 1,
		op:                ring.NewOp([]ring.InstanceState{ring.ACTIVE, ring.LEAVING}, nil),
	},
	ModeAll: {
		numTokens:         1,
		replicationFactor: 1,
		op:                ring.NewOp([]ring.InstanceState{ring.ACTIVE}, nil),
	},
}
