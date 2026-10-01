// INFRAPI_CLUSTER_* getters, the clustering configuration a service opts into.

package config

import (
	"fmt"
	"time"
)

// ClusterDefaultConfig holds the read and validation rules of every
// INFRAPI_CLUSTER_* key. A service may tighten or relax any of them before
// reading its configuration.
//
// Unlike the INFRAPI_APP_* keys, these are reached through one struct rather
// than one getter each: there are enough of them that a getter per key would be
// forty more exported functions nobody calls individually. GetClusterConfig
// resolves them all and stops on the first one that is missing or invalid.
type ClusterDefaultConfig struct {
	// Core
	Enabled                *VariableOpts
	KvStore                *VariableOpts
	KvPrefix               *VariableOpts
	Roles                  *VariableOpts
	InstanceID             *VariableOpts
	InstanceAddr           *VariableOpts
	InstanceInterfaceNames *VariableOpts
	InstancePort           *VariableOpts
	StartTimeout           *VariableOpts
	StopTimeout            *VariableOpts

	// Ring and lifecycler
	HeartbeatPeriod              *VariableOpts
	HeartbeatTimeout             *VariableOpts
	KeepInstanceInRingOnShutdown *VariableOpts
	AutoForgetUnhealthyPeriods   *VariableOpts
	TokensObservePeriod          *VariableOpts

	// Zones
	Zone                 *VariableOpts
	ZoneAwarenessEnabled *VariableOpts
	ExcludedZones        *VariableOpts

	// Gossip, used only when KvStore is memberlist
	GossipClusterLabel                    *VariableOpts
	GossipClusterLabelVerificationDisable *VariableOpts
	GossipBindAddrs                       *VariableOpts
	GossipBindPort                        *VariableOpts
	GossipAdvertiseAddr                   *VariableOpts
	GossipAdvertisePort                   *VariableOpts
	GossipInterval                        *VariableOpts
	GossipNodes                           *VariableOpts
	GossipToTheDeadTime                   *VariableOpts
	GossipCompressionEnabled              *VariableOpts
	PushPullInterval                      *VariableOpts
	LeaveTimeout                          *VariableOpts
	BroadcastTimeoutOnShutdown            *VariableOpts
	ObsoleteEntriesTimeout                *VariableOpts
	LeftInstancesTimeout                  *VariableOpts
	MessageHistoryBufferBytes             *VariableOpts
	JoinMembers                           *VariableOpts
	JoinMinBackoff                        *VariableOpts
	JoinMaxBackoff                        *VariableOpts
	JoinMaxRetries                        *VariableOpts
	AbortIfJoinFails                      *VariableOpts
	RejoinInterval                        *VariableOpts
	GossipTLSEnabled                      *VariableOpts
	GossipTLSCaPath                       *VariableOpts
	GossipTLSCertPath                     *VariableOpts
	GossipTLSKeyPath                      *VariableOpts
	GossipTLSServerName                   *VariableOpts
	GossipTLSInsecureSkipVerify           *VariableOpts
	GossipZoneAwareRoutingEnabled         *VariableOpts
	GossipZoneRole                        *VariableOpts

	// etcd, used only when KvStore is etcd
	EtcdEndpoints     *VariableOpts
	EtcdDialTimeout   *VariableOpts
	EtcdMaxRetries    *VariableOpts
	EtcdUsername      *VariableOpts
	EtcdPassword      *VariableOpts
	EtcdTLSEnabled    *VariableOpts
	EtcdTLSCaPath     *VariableOpts
	EtcdTLSCertPath   *VariableOpts
	EtcdTLSKeyPath    *VariableOpts
	EtcdTLSServerName *VariableOpts
	EtcdTLSSkipVerify *VariableOpts
}

// newClusterDefaultConfig returns the rules applied to the INFRAPI_CLUSTER_*
// keys. hostname is the fallback instance identity, resolved once by the
// caller the same way AppFqdn is.
//
// Numeric keys carry no validator rule: go-playground's min and max validate
// the length of a string, not its value, so a port of 99999 would pass. Their
// ranges are checked by ClusterConfig.Validate instead. Duration keys are
// likewise validated by time.ParseDuration in GetDuration.
func newClusterDefaultConfig(hostname string) *ClusterDefaultConfig {
	return &ClusterDefaultConfig{
		Enabled: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_ENABLED",
			Validator: "oneof=true false",
			Default:   "false",
		},
		KvStore: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_KV_STORE",
			Validator: "oneof=memberlist etcd inmemory",
			Default:   "memberlist",
		},
		KvPrefix: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_KV_PREFIX",
			Validator: "required,endswith=/",
			Default:   "collectors/",
		},
		Roles: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_ROLES",
			Default: "[]",
		},
		InstanceID: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_INSTANCE_ID",
			Validator: "required,hostname_rfc1123",
			Default:   hostname,
		},
		InstanceAddr: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_INSTANCE_ADDR",
			Validator: "ip",
			Optional:  true,
		},
		InstanceInterfaceNames: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_INSTANCE_INTERFACE_NAMES",
			Default: "[\"eth0\", \"en0\"]",
		},
		InstancePort: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_INSTANCE_PORT",
			Default: "7946",
		},
		StartTimeout: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_START_TIMEOUT",
			Default: "30s",
		},
		StopTimeout: &VariableOpts{
			Key: "INFRAPI_CLUSTER_STOP_TIMEOUT",
			// Covers the gossip defaults: a 20s leave plus a 10s broadcast
			// drain, plus room for each role's lifecycler. It is a ceiling, not
			// a wait: a shutdown with nothing left to say returns at once.
			Default: "45s",
		},

		HeartbeatPeriod: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_HEARTBEAT_PERIOD",
			Default: "15s",
		},
		HeartbeatTimeout: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT",
			Default: "1m",
		},
		KeepInstanceInRingOnShutdown: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_KEEP_INSTANCE_IN_RING_ON_SHUTDOWN",
			Validator: "oneof=true false",
			Default:   "true",
		},
		AutoForgetUnhealthyPeriods: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_AUTO_FORGET_UNHEALTHY_PERIODS",
			Default: "10",
		},
		TokensObservePeriod: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_TOKENS_OBSERVE_PERIOD",
			Default: "0s",
		},

		Zone: &VariableOpts{
			Key:      "INFRAPI_CLUSTER_ZONE",
			Optional: true,
		},
		ZoneAwarenessEnabled: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_ZONE_AWARENESS_ENABLED",
			Validator: "oneof=true false",
			Default:   "false",
		},
		ExcludedZones: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_EXCLUDED_ZONES",
			Default: "[]",
		},

		GossipClusterLabel: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL",
			Validator: "required",
			Default:   "infrapi",
		},
		GossipClusterLabelVerificationDisable: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_GOSSIP_CLUSTER_LABEL_VERIFICATION_DISABLED",
			Validator: "oneof=true false",
			Default:   "false",
		},
		GossipBindAddrs: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_GOSSIP_BIND_ADDRS",
			Default: "[\"0.0.0.0\"]",
		},
		GossipBindPort: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_GOSSIP_BIND_PORT",
			Default: "7946",
		},
		GossipAdvertiseAddr: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_GOSSIP_ADVERTISE_ADDR",
			Validator: "ip",
			Optional:  true,
		},
		GossipAdvertisePort: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_GOSSIP_ADVERTISE_PORT",
			Default: "0",
		},
		GossipInterval: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_GOSSIP_INTERVAL",
			Default: "200ms",
		},
		GossipNodes: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_GOSSIP_NODES",
			Default: "3",
		},
		GossipToTheDeadTime: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_GOSSIP_TO_THE_DEAD_TIME",
			Default: "30s",
		},
		GossipCompressionEnabled: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_GOSSIP_COMPRESSION_ENABLED",
			Validator: "oneof=true false",
			Default:   "true",
		},
		PushPullInterval: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_PUSH_PULL_INTERVAL",
			Default: "30s",
		},
		LeaveTimeout: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_LEAVE_TIMEOUT",
			Default: "20s",
		},
		BroadcastTimeoutOnShutdown: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_BROADCAST_TIMEOUT_ON_SHUTDOWN",
			Default: "10s",
		},
		ObsoleteEntriesTimeout: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_OBSOLETE_ENTRIES_TIMEOUT",
			Default: "30s",
		},
		LeftInstancesTimeout: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_LEFT_INSTANCES_TIMEOUT",
			Default: "5m",
		},
		MessageHistoryBufferBytes: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_MESSAGE_HISTORY_BUFFER_BYTES",
			Default: "0",
		},
		JoinMembers: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_JOIN_MEMBERS",
			Default: "[]",
		},
		JoinMinBackoff: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_JOIN_MIN_BACKOFF",
			Default: "1s",
		},
		JoinMaxBackoff: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_JOIN_MAX_BACKOFF",
			Default: "1m",
		},
		JoinMaxRetries: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_JOIN_MAX_RETRIES",
			Default: "10",
		},
		AbortIfJoinFails: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_ABORT_IF_JOIN_FAILS",
			Validator: "oneof=true false",
			Default:   "false",
		},
		RejoinInterval: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_REJOIN_INTERVAL",
			Default: "0s",
		},
		GossipTLSEnabled: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_GOSSIP_TLS_ENABLED",
			Validator: "oneof=true false",
			Default:   "false",
		},
		GossipTLSCaPath: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_GOSSIP_TLS_CA_PATH",
			Validator: "filepath",
			Optional:  true,
		},
		GossipTLSCertPath: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_GOSSIP_TLS_CERT_PATH",
			Validator: "filepath",
			Optional:  true,
		},
		GossipTLSKeyPath: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_GOSSIP_TLS_KEY_PATH",
			Validator: "filepath",
			Optional:  true,
		},
		GossipTLSServerName: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_GOSSIP_TLS_SERVER_NAME",
			Validator: "hostname",
			Optional:  true,
		},
		GossipZoneAwareRoutingEnabled: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_GOSSIP_ZONE_AWARE_ROUTING_ENABLED",
			Validator: "oneof=true false",
			Default:   "false",
		},
		GossipZoneRole: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_GOSSIP_ZONE_ROLE",
			Validator: "oneof=member bridge",
			Default:   "member",
		},
		GossipTLSInsecureSkipVerify: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_GOSSIP_TLS_INSECURE_SKIP_VERIFY",
			Validator: "oneof=true false",
			Default:   "false",
		},

		EtcdEndpoints: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_ETCD_ENDPOINTS",
			Default: "[]",
		},
		EtcdDialTimeout: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_ETCD_DIAL_TIMEOUT",
			Default: "10s",
		},
		EtcdMaxRetries: &VariableOpts{
			Key:     "INFRAPI_CLUSTER_ETCD_MAX_RETRIES",
			Default: "10",
		},
		EtcdUsername: &VariableOpts{
			Key:      "INFRAPI_CLUSTER_ETCD_USERNAME",
			Optional: true,
		},
		EtcdPassword: &VariableOpts{
			Key:      "INFRAPI_CLUSTER_ETCD_PASSWORD",
			Optional: true,
			Sensible: true,
		},
		EtcdTLSEnabled: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_ETCD_TLS_ENABLED",
			Validator: "oneof=true false",
			Default:   "false",
		},
		EtcdTLSCaPath: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_ETCD_TLS_CA_PATH",
			Validator: "filepath",
			Optional:  true,
		},
		EtcdTLSCertPath: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_ETCD_TLS_CERT_PATH",
			Validator: "filepath",
			Optional:  true,
		},
		EtcdTLSKeyPath: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_ETCD_TLS_KEY_PATH",
			Validator: "filepath",
			Optional:  true,
		},
		EtcdTLSServerName: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_ETCD_TLS_SERVER_NAME",
			Validator: "hostname",
			Optional:  true,
		},
		EtcdTLSSkipVerify: &VariableOpts{
			Key:       "INFRAPI_CLUSTER_ETCD_TLS_INSECURE_SKIP_VERIFY",
			Validator: "oneof=true false",
			Default:   "false",
		},
	}
}

// ClusterConfig is the resolved INFRAPI_CLUSTER_* configuration of a service.
// The gossip fields apply only when KvStore is memberlist, and the etcd fields
// only when it is etcd; the others are ignored rather than rejected, so one
// shared dotenv can serve services that made different choices.
type ClusterConfig struct {
	// Core
	Enabled                bool
	KvStore                string
	KvPrefix               string
	Roles                  []string
	InstanceID             string
	InstanceAddr           string
	InstanceInterfaceNames []string
	InstancePort           int
	StartTimeout           time.Duration
	StopTimeout            time.Duration

	// Ring and lifecycler
	HeartbeatPeriod              time.Duration
	HeartbeatTimeout             time.Duration
	KeepInstanceInRingOnShutdown bool
	AutoForgetUnhealthyPeriods   int
	TokensObservePeriod          time.Duration

	// Zones
	Zone                 string
	ZoneAwarenessEnabled bool
	ExcludedZones        []string

	// Gossip
	GossipClusterLabel                    string
	GossipClusterLabelVerificationDisable bool
	GossipBindAddrs                       []string
	GossipBindPort                        int
	GossipAdvertiseAddr                   string
	GossipAdvertisePort                   int
	GossipInterval                        time.Duration
	GossipNodes                           int
	GossipToTheDeadTime                   time.Duration
	GossipCompressionEnabled              bool
	PushPullInterval                      time.Duration
	LeaveTimeout                          time.Duration
	BroadcastTimeoutOnShutdown            time.Duration
	ObsoleteEntriesTimeout                time.Duration
	LeftInstancesTimeout                  time.Duration
	MessageHistoryBufferBytes             int
	JoinMembers                           []string
	JoinMinBackoff                        time.Duration
	JoinMaxBackoff                        time.Duration
	JoinMaxRetries                        int
	AbortIfJoinFails                      bool
	RejoinInterval                        time.Duration
	GossipTLSEnabled                      bool
	GossipTLSCaPath                       string
	GossipTLSCertPath                     string
	GossipTLSKeyPath                      string
	GossipTLSServerName                   string
	GossipTLSInsecureSkipVerify           bool
	GossipZoneAwareRoutingEnabled         bool
	GossipZoneRole                        string

	// etcd
	EtcdEndpoints     []string
	EtcdDialTimeout   time.Duration
	EtcdMaxRetries    int
	EtcdUsername      string
	EtcdPassword      string
	EtcdTLSEnabled    bool
	EtcdTLSCaPath     string
	EtcdTLSCertPath   string
	EtcdTLSKeyPath    string
	EtcdTLSServerName string
	EtcdTLSSkipVerify bool

	// path is the dotenv the values came from, so Validate can name it the way
	// the getters do.
	path string
}

// clusterLoader reads a run of keys and keeps the first error, so that
// GetClusterConfig reads like a list of assignments and still stops on the
// first key that is missing or fails its validator.
type clusterLoader struct {
	v   *Variables
	err error
}

func (l *clusterLoader) str(opts *VariableOpts) string {
	if l.err != nil {
		return ""
	}
	value, err := l.v.GetString(opts)
	if err != nil {
		l.err = err
	}
	return value
}

func (l *clusterLoader) boolean(opts *VariableOpts) bool {
	if l.err != nil {
		return false
	}
	value, err := l.v.GetBool(opts)
	if err != nil {
		l.err = err
	}
	return value
}

func (l *clusterLoader) integer(opts *VariableOpts) int {
	if l.err != nil {
		return 0
	}
	value, err := l.v.GetInt(opts)
	if err != nil {
		l.err = err
	}
	return value
}

func (l *clusterLoader) duration(opts *VariableOpts) time.Duration {
	if l.err != nil {
		return 0
	}
	value, err := l.v.GetDuration(opts)
	if err != nil {
		l.err = err
	}
	return value
}

func (l *clusterLoader) slice(opts *VariableOpts) []string {
	if l.err != nil {
		return nil
	}
	value, err := l.v.GetSliceString(opts)
	if err != nil {
		l.err = err
	}
	return value
}

// GetClusterConfig resolves every INFRAPI_CLUSTER_* key and returns them as a
// single struct, stopping on the first key that is missing or fails its
// validator, then checks the rules that span several keys. It may be called on
// a nil *Variables, in which case NewConfig is used to load the dotenv file
// first.
//
// A service that does not cluster still gets a usable value: Enabled defaults
// to false and every other key has a default, so the call succeeds and the
// cluster package turns into a no-op.
func (v *Variables) GetClusterConfig() (*ClusterConfig, error) {
	if v == nil {
		var err error
		v, err = NewConfig()
		if err != nil {
			return nil, err
		}
	}

	d := v.DefaultConfig.Cluster
	l := &clusterLoader{v: v}

	cfg := &ClusterConfig{
		path: v.Path,

		Enabled:                l.boolean(d.Enabled),
		KvStore:                l.str(d.KvStore),
		KvPrefix:               l.str(d.KvPrefix),
		Roles:                  l.slice(d.Roles),
		InstanceID:             l.str(d.InstanceID),
		InstanceAddr:           l.str(d.InstanceAddr),
		InstanceInterfaceNames: l.slice(d.InstanceInterfaceNames),
		InstancePort:           l.integer(d.InstancePort),
		StartTimeout:           l.duration(d.StartTimeout),
		StopTimeout:            l.duration(d.StopTimeout),

		HeartbeatPeriod:              l.duration(d.HeartbeatPeriod),
		HeartbeatTimeout:             l.duration(d.HeartbeatTimeout),
		KeepInstanceInRingOnShutdown: l.boolean(d.KeepInstanceInRingOnShutdown),
		AutoForgetUnhealthyPeriods:   l.integer(d.AutoForgetUnhealthyPeriods),
		TokensObservePeriod:          l.duration(d.TokensObservePeriod),

		Zone:                 l.str(d.Zone),
		ZoneAwarenessEnabled: l.boolean(d.ZoneAwarenessEnabled),
		ExcludedZones:        l.slice(d.ExcludedZones),

		GossipClusterLabel:                    l.str(d.GossipClusterLabel),
		GossipClusterLabelVerificationDisable: l.boolean(d.GossipClusterLabelVerificationDisable),
		GossipBindAddrs:                       l.slice(d.GossipBindAddrs),
		GossipBindPort:                        l.integer(d.GossipBindPort),
		GossipAdvertiseAddr:                   l.str(d.GossipAdvertiseAddr),
		GossipAdvertisePort:                   l.integer(d.GossipAdvertisePort),
		GossipInterval:                        l.duration(d.GossipInterval),
		GossipNodes:                           l.integer(d.GossipNodes),
		GossipToTheDeadTime:                   l.duration(d.GossipToTheDeadTime),
		GossipCompressionEnabled:              l.boolean(d.GossipCompressionEnabled),
		PushPullInterval:                      l.duration(d.PushPullInterval),
		LeaveTimeout:                          l.duration(d.LeaveTimeout),
		BroadcastTimeoutOnShutdown:            l.duration(d.BroadcastTimeoutOnShutdown),
		ObsoleteEntriesTimeout:                l.duration(d.ObsoleteEntriesTimeout),
		LeftInstancesTimeout:                  l.duration(d.LeftInstancesTimeout),
		MessageHistoryBufferBytes:             l.integer(d.MessageHistoryBufferBytes),
		JoinMembers:                           l.slice(d.JoinMembers),
		JoinMinBackoff:                        l.duration(d.JoinMinBackoff),
		JoinMaxBackoff:                        l.duration(d.JoinMaxBackoff),
		JoinMaxRetries:                        l.integer(d.JoinMaxRetries),
		AbortIfJoinFails:                      l.boolean(d.AbortIfJoinFails),
		RejoinInterval:                        l.duration(d.RejoinInterval),
		GossipTLSEnabled:                      l.boolean(d.GossipTLSEnabled),
		GossipTLSCaPath:                       l.str(d.GossipTLSCaPath),
		GossipTLSCertPath:                     l.str(d.GossipTLSCertPath),
		GossipTLSKeyPath:                      l.str(d.GossipTLSKeyPath),
		GossipTLSServerName:                   l.str(d.GossipTLSServerName),
		GossipTLSInsecureSkipVerify:           l.boolean(d.GossipTLSInsecureSkipVerify),
		GossipZoneAwareRoutingEnabled:         l.boolean(d.GossipZoneAwareRoutingEnabled),
		GossipZoneRole:                        l.str(d.GossipZoneRole),

		EtcdEndpoints:     l.slice(d.EtcdEndpoints),
		EtcdDialTimeout:   l.duration(d.EtcdDialTimeout),
		EtcdMaxRetries:    l.integer(d.EtcdMaxRetries),
		EtcdUsername:      l.str(d.EtcdUsername),
		EtcdPassword:      l.str(d.EtcdPassword),
		EtcdTLSEnabled:    l.boolean(d.EtcdTLSEnabled),
		EtcdTLSCaPath:     l.str(d.EtcdTLSCaPath),
		EtcdTLSCertPath:   l.str(d.EtcdTLSCertPath),
		EtcdTLSKeyPath:    l.str(d.EtcdTLSKeyPath),
		EtcdTLSServerName: l.str(d.EtcdTLSServerName),
		EtcdTLSSkipVerify: l.boolean(d.EtcdTLSSkipVerify),
	}

	if l.err != nil {
		return nil, l.err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks the rules that span several keys, which no single validator
// rule can express. Every message names the keys at fault and the dotenv they
// came from, the way the per-key errors do.
func (c *ClusterConfig) Validate() error {
	if err := c.validatePorts(); err != nil {
		return err
	}
	if err := c.validateRoles(); err != nil {
		return err
	}
	if err := c.validateTimings(); err != nil {
		return err
	}
	if err := c.validateStore(); err != nil {
		return err
	}
	if err := c.validateZones(); err != nil {
		return err
	}
	return c.validateTLS()
}

func (c *ClusterConfig) validatePorts() error {
	if c.InstancePort < 1 || c.InstancePort > 65535 {
		return c.errorf("INFRAPI_CLUSTER_INSTANCE_PORT is %d, expected 1 to 65535", c.InstancePort)
	}
	if c.GossipBindPort < 0 || c.GossipBindPort > 65535 {
		return c.errorf("INFRAPI_CLUSTER_GOSSIP_BIND_PORT is %d, expected 0 to 65535, 0 for an ephemeral port", c.GossipBindPort)
	}
	if c.GossipAdvertisePort < 0 || c.GossipAdvertisePort > 65535 {
		return c.errorf("INFRAPI_CLUSTER_GOSSIP_ADVERTISE_PORT is %d, expected 0 to 65535, 0 to advertise the bind port", c.GossipAdvertisePort)
	}
	if c.GossipNodes < 1 {
		return c.errorf("INFRAPI_CLUSTER_GOSSIP_NODES is %d, expected at least 1", c.GossipNodes)
	}
	if c.JoinMaxRetries < 0 {
		return c.errorf("INFRAPI_CLUSTER_JOIN_MAX_RETRIES is %d, expected 0 or more", c.JoinMaxRetries)
	}
	if c.EtcdMaxRetries < 0 {
		return c.errorf("INFRAPI_CLUSTER_ETCD_MAX_RETRIES is %d, expected 0 or more", c.EtcdMaxRetries)
	}
	if c.MessageHistoryBufferBytes < 0 {
		return c.errorf("INFRAPI_CLUSTER_MESSAGE_HISTORY_BUFFER_BYTES is %d, expected 0 or more", c.MessageHistoryBufferBytes)
	}
	return nil
}

func (c *ClusterConfig) validateRoles() error {
	seen := make(map[string]bool, len(c.Roles))
	for _, role := range c.Roles {
		if role == "" {
			return c.errorf("INFRAPI_CLUSTER_ROLES contains an empty role name")
		}
		if seen[role] {
			return c.errorf("INFRAPI_CLUSTER_ROLES lists role %q more than once", role)
		}
		seen[role] = true
	}
	return nil
}

func (c *ClusterConfig) validateTimings() error {
	// The ring reads an instance as unhealthy once its heartbeat is older than
	// the timeout, so a timeout at or below the period makes every instance
	// flap between healthy and unhealthy.
	if c.HeartbeatTimeout <= c.HeartbeatPeriod {
		return c.errorf("INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT (%s) must be greater than INFRAPI_CLUSTER_HEARTBEAT_PERIOD (%s)",
			c.HeartbeatTimeout, c.HeartbeatPeriod)
	}

	// Forgetting an instance after a single unhealthy period means evicting it
	// from the ring the moment it misses a heartbeat, which turns one GC pause
	// into a leader change. 0 disables the delegate entirely.
	if c.AutoForgetUnhealthyPeriods == 1 {
		return c.errorf("INFRAPI_CLUSTER_AUTO_FORGET_UNHEALTHY_PERIODS is 1, expected 0 to disable auto forget or 2 or more")
	}
	if c.AutoForgetUnhealthyPeriods < 0 {
		return c.errorf("INFRAPI_CLUSTER_AUTO_FORGET_UNHEALTHY_PERIODS is %d, expected 0 or more", c.AutoForgetUnhealthyPeriods)
	}

	if c.JoinMaxBackoff < c.JoinMinBackoff {
		return c.errorf("INFRAPI_CLUSTER_JOIN_MAX_BACKOFF (%s) must be greater than or equal to INFRAPI_CLUSTER_JOIN_MIN_BACKOFF (%s)",
			c.JoinMaxBackoff, c.JoinMinBackoff)
	}

	// Leaving the gossip cluster is the last thing a shutdown does, and it costs
	// the broadcast drain plus the leave itself. A stop budget below their sum
	// guarantees the leave is cut short, and this replica is then remembered as
	// dead rather than gone: every peer keeps it in the ring until its
	// heartbeat goes stale. Each role's lifecycler also takes its turn before
	// any of this, so the real figure is higher still.
	if c.KvStore == "memberlist" {
		leaving := c.LeaveTimeout + c.BroadcastTimeoutOnShutdown
		if c.StopTimeout <= leaving {
			return c.errorf("INFRAPI_CLUSTER_STOP_TIMEOUT (%s) must be greater than INFRAPI_CLUSTER_LEAVE_TIMEOUT (%s) plus INFRAPI_CLUSTER_BROADCAST_TIMEOUT_ON_SHUTDOWN (%s), which is %s",
				c.StopTimeout, c.LeaveTimeout, c.BroadcastTimeoutOnShutdown, leaving)
		}
	}

	return nil
}

func (c *ClusterConfig) validateStore() error {
	switch c.KvStore {
	case "etcd":
		if len(c.EtcdEndpoints) == 0 {
			return c.errorf("INFRAPI_CLUSTER_KV_STORE is etcd but INFRAPI_CLUSTER_ETCD_ENDPOINTS is empty")
		}
	case "inmemory":
		// The in-memory store is a process-wide map. It coordinates nothing
		// beyond this process, so asking it to join peers is always a mistake.
		if len(c.JoinMembers) > 0 {
			return c.errorf("INFRAPI_CLUSTER_KV_STORE is inmemory but INFRAPI_CLUSTER_JOIN_MEMBERS is not empty, the in-memory store cannot reach another process")
		}
	}
	return nil
}

// maxZoneNameLength mirrors dskit's own limit: a zone name travels in every
// gossip packet's node metadata, where one byte holds its length.
const maxZoneNameLength = 16

func (c *ClusterConfig) validateZones() error {
	if len(c.Zone) > maxZoneNameLength {
		return c.errorf("INFRAPI_CLUSTER_ZONE is %d bytes, the gossip node metadata allows at most %d", len(c.Zone), maxZoneNameLength)
	}

	// Excluding our own zone would filter this replica out of its own view of
	// the ring: it would read an empty ring and never act.
	for _, zone := range c.ExcludedZones {
		if zone == "" {
			return c.errorf("INFRAPI_CLUSTER_EXCLUDED_ZONES contains an empty zone name")
		}
		if c.Zone != "" && zone == c.Zone {
			return c.errorf("INFRAPI_CLUSTER_EXCLUDED_ZONES lists %q, which is this replica's own INFRAPI_CLUSTER_ZONE", zone)
		}
	}

	return nil
}

func (c *ClusterConfig) validateTLS() error {
	if (c.GossipTLSCertPath == "") != (c.GossipTLSKeyPath == "") {
		return c.errorf("INFRAPI_CLUSTER_GOSSIP_TLS_CERT_PATH and INFRAPI_CLUSTER_GOSSIP_TLS_KEY_PATH must be set together")
	}
	if (c.EtcdTLSCertPath == "") != (c.EtcdTLSKeyPath == "") {
		return c.errorf("INFRAPI_CLUSTER_ETCD_TLS_CERT_PATH and INFRAPI_CLUSTER_ETCD_TLS_KEY_PATH must be set together")
	}
	return nil
}

// errorf formats a cross-key error, naming the dotenv the values came from.
func (c *ClusterConfig) errorf(format string, args ...any) error {
	return fmt.Errorf(format+", dotenv located at %s", append(args, c.path)...)
}
