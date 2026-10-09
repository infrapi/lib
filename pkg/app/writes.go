package app

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"
	"github.com/infrapi/lib/pkg/cluster"

	"github.com/infrapi/lib/pkg/app/internal/relay"
)

// The contract's extensions that give writes to rings.
const (
	// WritesExtension, at the root of a contract, names the ring of every
	// write the contract does not mark otherwise.
	WritesExtension = "x-infrapi-writes"
	// RingExtension, on a write operation, names its ring.
	RingExtension = "x-infrapi-ring"
	// LocalRing, as either one's value, keeps a write on the replica that
	// received it.
	LocalRing = "local"
)

// How a write runs: given by the mode of its ring.
const (
	writeLocal     = "local"     // on the replica that received it
	writeLeader    = "leader"    // on the leader of a leader-mode ring, relayed there
	writeBroadcast = "broadcast" // here, then replayed on every member of a broadcast ring
)

// writeMode is one write's way of running: its kind, and the ring it runs on.
type writeMode struct {
	kind   string
	role   string
	policy string // a broadcast write's: strict, tolerant or async
}

func (m writeMode) String() string {
	switch {
	case m.role == "":
		return m.kind
	case m.policy != "":
		return m.kind + " on " + m.role + ", " + m.policy
	}
	return m.kind + " on " + m.role
}

// dispatcher runs each write on the ring its contract gives it.
type dispatcher struct {
	byRoute map[string]writeMode // "POST /orders/v1/…/:id" → mode
	reads   map[string]writeMode // the reads relayed to a leader
	local   *fanout
	fanouts map[string]*fanout
	leaders map[string]gin.HandlerFunc
	// readLeaders relay reads too, for the reads given to a leader's ring.
	readLeaders map[string]gin.HandlerFunc
}

// newDispatcher reads each operation's ring from the contracts. A write's: the operation's
// x-infrapi-ring, else the contract's x-infrapi-writes, else the writer ring
// with cluster.leader_writes, else local. The ring's mode, from Spec.Roles,
// says how the write runs there.
func newDispatcher(cfg *loaded, cl *cluster.Cluster, docs []*openapi3.T) (*dispatcher, error) {
	scheme, maxBody := peerScheme(cfg), int64(cfg.Gateway.MaxBodyBytes)
	d := &dispatcher{
		byRoute: map[string]writeMode{}, reads: map[string]writeMode{},
		local:   newFanout(cl, "", scheme, maxBody),
		fanouts: map[string]*fanout{}, leaders: map[string]gin.HandlerFunc{},
		readLeaders: map[string]gin.HandlerFunc{},
	}

	fallback := writeMode{kind: writeLocal}
	if cfg.cluster.LeaderWrites {
		fallback = writeMode{kind: writeLeader, role: writerRole}
	}

	for _, doc := range docs {
		docPolicy := BroadcastStrict
		if p, set, err := broadcastPolicy(doc.Extensions); err != nil {
			return nil, fmt.Errorf("app: %s: %w", doc.Info.Title, err)
		} else if set {
			docPolicy = p
		}
		docDefault := fallback
		if name, set, err := extension(doc.Extensions, WritesExtension); err != nil {
			return nil, fmt.Errorf("app: %s: %w", doc.Info.Title, err)
		} else if set {
			if cfg.cluster.LeaderWrites {
				return nil, fmt.Errorf("app: %s: %s and cluster.leader_writes both give the writes a ring: keep one",
					doc.Info.Title, WritesExtension)
			}
			if docDefault, err = cfg.ringMode(name); err != nil {
				return nil, fmt.Errorf("app: %s: %s: %w", doc.Info.Title, WritesExtension, err)
			}
		}

		for _, path := range doc.Paths.InMatchingOrder() {
			for method, op := range doc.Paths.Value(path).Operations() {
				name, set, err := extension(op.Extensions, RingExtension)
				if err != nil {
					return nil, fmt.Errorf("app: %s: %w", op.OperationID, err)
				}
				if !isWrite(method) {
					if _, own, _ := broadcastPolicy(op.Extensions); own {
						return nil, fmt.Errorf("app: %s: %s on a read", op.OperationID, BroadcastExtension)
					}
					// A read runs where it arrives, unless its ring is a
					// leader's: what only the leader holds is read there.
					if !set {
						continue
					}
					m, err := cfg.ringMode(name)
					if err != nil {
						return nil, fmt.Errorf("app: %s: %s: %w", op.OperationID, RingExtension, err)
					}
					if m.kind == writeBroadcast {
						return nil, fmt.Errorf("app: %s: %s: %q is a broadcast ring; a read has one answer, "+
							"from one replica", op.OperationID, RingExtension, name)
					}
					if cl.Enabled() && m.kind == writeLeader {
						d.reads[method+" "+ginPath(path)] = m
					}
					continue
				}
				m := docDefault
				if set {
					if m, err = cfg.ringMode(name); err != nil {
						return nil, fmt.Errorf("app: %s: %s: %w", op.OperationID, RingExtension, err)
					}
				}
				policy, own, err := broadcastPolicy(op.Extensions)
				switch {
				case err != nil:
					return nil, fmt.Errorf("app: %s: %w", op.OperationID, err)
				case own && m.kind != writeBroadcast:
					return nil, fmt.Errorf("app: %s: %s on a write that is not broadcast (%s)",
						op.OperationID, BroadcastExtension, m)
				case !own:
					policy = docPolicy
				}
				if m.kind == writeBroadcast {
					m.policy = policy
				}
				if !cl.Enabled() {
					m = writeMode{kind: writeLocal} // one replica: every write runs here
				}
				d.byRoute[method+" "+ginPath(path)] = m
			}
		}
	}

	for _, m := range d.reads {
		if d.readLeaders[m.role] == nil {
			d.readLeaders[m.role] = relay.ForwardAll(relay.Cluster{C: cl, Role: m.role}, scheme)
		}
	}
	for _, m := range d.byRoute {
		switch m.kind {
		case writeBroadcast:
			if d.fanouts[m.role] == nil {
				d.fanouts[m.role] = newFanout(cl, m.role, scheme, maxBody)
			}
		case writeLeader:
			if d.leaders[m.role] == nil {
				d.leaders[m.role] = relay.Forward(relay.Cluster{C: cl, Role: m.role}, scheme)
			}
		}
	}
	return d, nil
}

// broadcastPolicy reads x-infrapi-broadcast, if set.
func broadcastPolicy(ext map[string]any) (string, bool, error) {
	v, set := ext[BroadcastExtension]
	if !set {
		return "", false, nil
	}
	switch v {
	case BroadcastStrict, BroadcastTolerant, BroadcastAsync:
		return v.(string), true, nil
	}
	return "", true, fmt.Errorf("%s: %v is not strict, tolerant or async", BroadcastExtension, v)
}

// extension reads a string extension, if set.
func extension(ext map[string]any, key string) (string, bool, error) {
	v, set := ext[key]
	if !set {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", true, fmt.Errorf("%s: %v is not a ring's name", key, v)
	}
	return s, true, nil
}

// ringMode is how a write given to the named ring runs: by the ring's mode,
// which Spec.Roles declares. The ring must be one this replica joins.
func (l *loaded) ringMode(name string) (writeMode, error) {
	if name == LocalRing {
		return writeMode{kind: writeLocal}, nil
	}
	mode, declared := l.roleModes[name]
	switch {
	case !declared:
		return writeMode{}, fmt.Errorf("%q is not one of Spec.Roles", name)
	case l.cluster.Enabled && !slices.Contains(l.cluster.Roles, name):
		return writeMode{}, fmt.Errorf("%q is not a role this replica joins (cluster.roles): its writes would have nowhere to go", name)
	case mode == cluster.ModeLeader:
		return writeMode{kind: writeLeader, role: name}, nil
	default:
		return writeMode{kind: writeBroadcast, role: name}, nil
	}
}

func (d *dispatcher) handle(c *gin.Context) {
	if !isWrite(c.Request.Method) {
		if m, ok := d.reads[c.Request.Method+" "+c.FullPath()]; ok {
			d.readLeaders[m.role](c)
			return
		}
		c.Next()
		return
	}
	m := d.byRoute[c.Request.Method+" "+c.FullPath()]
	switch m.kind {
	case writeBroadcast:
		d.fanouts[m.role].handle(c, m.policy)
	case writeLeader:
		// The identity first: the relay hands over to the leader, which
		// gives the write its own, or runs it here and goes on.
		d.local.identify(c, newBroadcastIdentity(c.Request))
		d.leaders[m.role](c)
	default:
		d.local.handle(c, "")
	}
}

// isWrite is a method that changes something: everything but a read.
func isWrite(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}
