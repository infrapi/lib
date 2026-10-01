package cluster

import "errors"

var (
	// ErrNotStarted is returned by a query made before Start.
	ErrNotStarted = errors.New("cluster: not started")

	// ErrStopped is returned by any call made after Stop.
	ErrStopped = errors.New("cluster: stopped")

	// ErrAlreadyStarted is returned by a second call to Start.
	ErrAlreadyStarted = errors.New("cluster: already started")

	// ErrDisabled is returned by a query on a cluster that the configuration
	// turned off, so that a service can tell "no leader yet" from "clustering
	// is not enabled here".
	ErrDisabled = errors.New("cluster: disabled by configuration")

	// ErrRoleNotJoined is returned for a role this replica does not run.
	ErrRoleNotJoined = errors.New("cluster: role not joined by this instance")

	// ErrRoleUndeclared is returned when the dotenv names a role that the
	// service did not declare in Options.Roles.
	ErrRoleUndeclared = errors.New("cluster: role listed in configuration but not declared in code")

	// ErrNoRoles is returned when a service declares roles but the dotenv
	// selects none of them, which would start a cluster that coordinates
	// nothing.
	ErrNoRoles = errors.New("cluster: no role selected by configuration")

	// ErrDuplicateRole is returned when two declared roles share a name or a
	// ring key. Both would register the same Prometheus collectors.
	ErrDuplicateRole = errors.New("cluster: duplicate role")

	// ErrBadMode is returned for a RoleSpec with no coordination mode, or one
	// this version does not know.
	ErrBadMode = errors.New("cluster: unknown coordination mode")

	// ErrWrongMode is returned by a query that does not apply to the role's
	// coordination mode, such as Leader on a broadcast role.
	ErrWrongMode = errors.New("cluster: operation does not apply to this coordination mode")

	// ErrWrongStore is returned by an accessor specific to another key-value
	// store, such as GossipPort when the store is etcd.
	ErrWrongStore = errors.New("cluster: operation does not apply to this key-value store")

	// ErrNoLeader is returned when the role ring has no single healthy owner of
	// the leader token, which is the normal state of an empty or partitioned
	// ring rather than a failure of this replica.
	ErrNoLeader = errors.New("cluster: no leader")

	// ErrCodecUnknown is returned by KVClient for a codec that was not passed
	// in Options.Codecs. Codecs must be registered before the replica joins.
	ErrCodecUnknown = errors.New("cluster: codec not registered")
)
