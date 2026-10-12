package cluster

import (
	"fmt"
	"net"
	"strconv"

	dstls "github.com/grafana/dskit/crypto/tls"
	"github.com/grafana/dskit/flagext"
	"github.com/grafana/dskit/kv"
	"github.com/grafana/dskit/kv/codec"
	"github.com/grafana/dskit/kv/memberlist"
	"github.com/grafana/dskit/ring"

	"github.com/infrapi/lib/pkg/config"
)

// dskitConfig is the dskit side of the configuration, built once from the
// dotenv. Keeping the translation in one file is what lets the rest of the
// package stay free of dskit's config types.
type dskitConfig struct {
	// ring is the base ring configuration. Each role copies it and sets its own
	// replication factor.
	ring ring.Config

	// kv describes the key-value backend. When the store is memberlist its
	// MemberlistKV hook is filled in at Start, once the gossip service exists.
	kv kv.Config

	// memberlist is the gossip configuration, used only when the store is
	// memberlist.
	memberlist memberlist.KVConfig

	// lifecycler is the base lifecycler configuration. Each role copies it and
	// sets its own token count.
	lifecycler ring.BasicLifecyclerConfig
}

// newDskitConfig translates the dotenv into dskit's configuration structs.
//
// instanceAddr is the already-resolved advertised address, so that this
// function performs no I/O and can be tested without a network.
func newDskitConfig(app *config.AppConfig, cfg *config.ClusterConfig, codecs []codec.Codec, instanceAddr string) (*dskitConfig, error) {
	// ring.Config is the only one of these that fills in a kv.Config for us:
	// kv.Config has no RegisterFlags of its own, so handing it to
	// flagext.DefaultValues directly would panic. Note that each of these calls
	// must be separate — DefaultValues shares one flag set across its
	// arguments, and ring.Config and etcd.Config both register "etcd.*".
	var ringCfg ring.Config
	flagext.DefaultValues(&ringCfg)

	var mlCfg memberlist.KVConfig
	flagext.DefaultValues(&mlCfg)

	// A zone of its own, falling back to where the service says it runs. dskit
	// needs it in two unrelated places, which is why it is resolved once here.
	zone := cfg.Zone
	if zone == "" {
		zone = app.AppLocation
	}

	ringCfg.HeartbeatTimeout = cfg.HeartbeatTimeout
	ringCfg.ZoneAwarenessEnabled = cfg.ZoneAwarenessEnabled
	ringCfg.ExcludedZones = flagext.StringSliceCSV(cfg.ExcludedZones)

	// The shuffle-sharding subring cache buys nothing for a leader or all-mode
	// role, which never asks for a subring.
	ringCfg.SubringCacheDisabled = true

	kvCfg := ringCfg.KVStore
	kvCfg.Store = cfg.KvStore
	kvCfg.Prefix = cfg.KvPrefix
	kvCfg.Etcd.Endpoints = cfg.EtcdEndpoints
	kvCfg.Etcd.DialTimeout = cfg.EtcdDialTimeout
	kvCfg.Etcd.MaxRetries = cfg.EtcdMaxRetries
	kvCfg.Etcd.EnableTLS = cfg.EtcdTLSEnabled
	kvCfg.Etcd.UserName = cfg.EtcdUsername
	kvCfg.Etcd.Password = flagext.SecretWithValue(cfg.EtcdPassword)
	kvCfg.Etcd.TLS = tlsConfig(kvCfg.Etcd.TLS, cfg.EtcdTLSCaPath, cfg.EtcdTLSCertPath,
		cfg.EtcdTLSKeyPath, cfg.EtcdTLSServerName, cfg.EtcdTLSSkipVerify)

	// The node name is the identity an operator greps for, so it must be the
	// configured instance ID and nothing else. dskit randomises it by default.
	mlCfg.NodeName = cfg.InstanceID
	mlCfg.RandomizeNodeName = false

	mlCfg.ClusterLabel = cfg.GossipClusterLabel
	mlCfg.ClusterLabelVerificationDisabled = cfg.GossipClusterLabelVerificationDisable
	mlCfg.AdvertiseAddr = cfg.GossipAdvertiseAddr
	mlCfg.AdvertisePort = cfg.GossipAdvertisePort
	mlCfg.GossipInterval = cfg.GossipInterval
	mlCfg.GossipNodes = cfg.GossipNodes
	mlCfg.GossipToTheDeadTime = cfg.GossipToTheDeadTime
	mlCfg.EnableCompression = cfg.GossipCompressionEnabled
	mlCfg.PushPullInterval = cfg.PushPullInterval
	mlCfg.LeaveTimeout = cfg.LeaveTimeout
	mlCfg.BroadcastTimeoutForLocalUpdatesOnShutdown = cfg.BroadcastTimeoutOnShutdown
	mlCfg.ObsoleteEntriesTimeout = cfg.ObsoleteEntriesTimeout
	// dskit's name is ring-specific; the field is the limit it passes to
	// RemoveTombstones on any gossiped value.
	mlCfg.LeftIngestersTimeout = cfg.TombstoneTimeout
	mlCfg.MessageHistoryBufferBytes = cfg.MessageHistoryBufferBytes
	mlCfg.JoinMembers = flagext.StringSliceCSVMulti(cfg.JoinMembers)
	mlCfg.MinJoinBackoff = cfg.JoinMinBackoff
	mlCfg.MaxJoinBackoff = cfg.JoinMaxBackoff
	mlCfg.MaxJoinRetries = cfg.JoinMaxRetries
	mlCfg.AbortIfJoinFails = cfg.AbortIfJoinFails
	mlCfg.RejoinInterval = cfg.RejoinInterval

	// Gossip zone-aware routing is a different thing from the ring's zone
	// awareness: it keeps gossip inside a zone instead of spreading replicas
	// across zones. See the package documentation on why it needs a bridge.
	mlCfg.ZoneAwareRouting = memberlist.ZoneAwareRoutingConfig{
		Enabled: cfg.GossipZoneAwareRoutingEnabled,
		Zone:    zone,
		Role:    cfg.GossipZoneRole,
	}

	mlCfg.TCPTransport.BindAddrs = flagext.StringSlice(cfg.GossipBindAddrs)
	mlCfg.TCPTransport.BindPort = cfg.GossipBindPort
	mlCfg.TCPTransport.TLSEnabled = cfg.GossipTLSEnabled
	mlCfg.TCPTransport.TLS = tlsConfig(mlCfg.TCPTransport.TLS, cfg.GossipTLSCaPath, cfg.GossipTLSCertPath,
		cfg.GossipTLSKeyPath, cfg.GossipTLSServerName, cfg.GossipTLSInsecureSkipVerify)

	// Codecs have to be registered before the replica joins: the gossip store
	// only learns them at construction, and a client built later for an
	// unregistered codec is refused.
	mlCfg.Codecs = append([]codec.Codec{ring.GetCodec()}, codecs...)

	lifecyclerCfg := ring.BasicLifecyclerConfig{
		ID:   cfg.InstanceID,
		Addr: net.JoinHostPort(instanceAddr, strconv.Itoa(cfg.InstancePort)),

		Zone: zone,

		HeartbeatPeriod:                 cfg.HeartbeatPeriod,
		HeartbeatTimeout:                cfg.HeartbeatTimeout,
		TokensObservePeriod:             cfg.TokensObservePeriod,
		KeepInstanceInTheRingOnShutdown: cfg.KeepInstanceInRingOnShutdown,
	}

	// Checked here rather than in pkg/config because the effective zone is only
	// known once the application configuration has had its say.
	if cfg.GossipZoneAwareRoutingEnabled && zone == "" {
		return nil, fmt.Errorf("cluster: INFRAPI_CLUSTER_GOSSIP_ZONE_AWARE_ROUTING_ENABLED is set but no zone is: set INFRAPI_CLUSTER_ZONE, or INFRAPI_APP_LOCATION")
	}

	if err := validateDskitConfig(ringCfg, mlCfg, cfg.KvStore); err != nil {
		return nil, err
	}

	return &dskitConfig{
		ring:       ringCfg,
		kv:         kvCfg,
		memberlist: mlCfg,
		lifecycler: lifecyclerCfg,
	}, nil
}

// validateDskitConfig runs dskit's own validation over what we built.
//
// Nothing a dotenv can express should reach it: the defaults come from
// flagext.DefaultValues and the dotenv only overwrites fields this package
// validates first. It is kept as a guard against a future refactor dropping
// those defaults, which would otherwise show up as a confusing runtime failure
// rather than a configuration error.
func validateDskitConfig(ringCfg ring.Config, mlCfg memberlist.KVConfig, store string) error {
	if err := ringCfg.Validate(); err != nil {
		return fmt.Errorf("cluster: ring configuration: %w", err)
	}
	if store == storeMemberlist {
		if err := mlCfg.Validate(); err != nil {
			return fmt.Errorf("cluster: gossip configuration: %w", err)
		}
		if err := mlCfg.ZoneAwareRouting.Validate(); err != nil {
			return fmt.Errorf("cluster: gossip zone-aware routing: %w", err)
		}
	}
	return nil
}

// tlsConfig overlays the configured paths onto dskit's defaults, leaving the
// cipher suite and minimum version at whatever dskit chose.
func tlsConfig(base dstls.ClientConfig, caPath, certPath, keyPath, serverName string, skipVerify bool) dstls.ClientConfig {
	base.CAPath = caPath
	base.CertPath = certPath
	base.KeyPath = keyPath
	base.ServerName = serverName
	base.InsecureSkipVerify = skipVerify
	return base
}

// ringConfigFor returns the base ring configuration with the replication factor
// the mode asks for.
func (d *dskitConfig) ringConfigFor(spec modeSpec) ring.Config {
	cfg := d.ring
	cfg.ReplicationFactor = spec.replicationFactor
	return cfg
}

// lifecyclerConfigFor returns the base lifecycler configuration with the token
// count the mode asks for.
func (d *dskitConfig) lifecyclerConfigFor(spec modeSpec) ring.BasicLifecyclerConfig {
	cfg := d.lifecycler
	cfg.NumTokens = spec.numTokens
	return cfg
}
