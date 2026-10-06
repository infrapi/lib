package cluster

import (
	"time"

	"github.com/grafana/dskit/ring"
)

// Member is one replica registered in a role ring. It is a snapshot taken from
// the ring, which has moved on by the time the caller reads it.
type Member struct {
	// ID is the replica identity, unique within the ring.
	ID string `json:"id"`

	// Addr is the advertised address, in host:port form.
	Addr string `json:"addr"`

	// Zone is the availability zone, empty when zone awareness is unused.
	Zone string `json:"zone,omitempty"`

	// State is the ring state: ACTIVE, JOINING, LEAVING or PENDING.
	State string `json:"state"`

	// Tokens are the ring tokens this member owns.
	Tokens []uint32 `json:"tokens"`

	// Heartbeat is when the member last refreshed its ring entry.
	Heartbeat time.Time `json:"heartbeat"`

	// RegisteredAt is when the member first joined the ring, or the zero time
	// when the ring does not record it.
	RegisteredAt time.Time `json:"registeredAt"`

	// MayLead reports whether the member's leader checks pass, as it published
	// in the ring. Always true on a role without leader checks.
	MayLead bool `json:"mayLead"`

	// Healthy reports whether the role's coordination mode accepts this member
	// right now. Members always returns eligible members, so it is true there;
	// AllMembers is where it earns its keep.
	Healthy bool `json:"healthy"`
}

// newMember converts a ring entry into a Member, so that dskit's generated
// protobuf type with its mutable exported fields stays inside this package.
func newMember(desc ring.InstanceDesc, healthy bool) Member {
	return Member{
		Healthy:      healthy,
		MayLead:      !desc.ReadOnly,
		ID:           desc.Id,
		Addr:         desc.Addr,
		Zone:         desc.Zone,
		State:        desc.State.String(),
		Tokens:       desc.Tokens,
		Heartbeat:    time.Unix(desc.Timestamp, 0),
		RegisteredAt: desc.GetRegisteredAt(),
	}
}
