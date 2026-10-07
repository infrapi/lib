package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/infrapi/lib/pkg/config"
	"gopkg.in/yaml.v3"
)

// The configuration file. Its app and cluster sections are the library's
// INFRAPI_APP_* and INFRAPI_CLUSTER_* keys, lowercase and without the prefix —
// the same names, defaults and validation as a dotenv file:
//
//	app:
//	  platform: sandbox
//	  listen_address: 0.0.0.0:8080
//	cluster:
//	  enabled: true
//	  join_members: [orders-1:7946, orders-2:7946]
//	auth:
//	  apikey_sha256: …     # or INFRAPI_AUTH_APIKEY_SHA256
//	database:
//	  dsn: …               # or INFRAPI_DATABASE_DSN
//	gateway:
//	  admin_url: http://apisix:9180
//	  admin_key: …         # or INFRAPI_GATEWAY_ADMIN_KEY
//	shutdown:
//	  drain_delay: 3s
//	service:
//	  …                    # the service's own, decoded into Spec.Service
//
// The environment wins over the file: any INFRAPI_APP_* or INFRAPI_CLUSTER_*
// variable, and the three secrets above, which are better kept out of it.
type fileConfig struct {
	App      map[string]any `yaml:"app"`
	Cluster  map[string]any `yaml:"cluster"`
	Auth     AuthConfig     `yaml:"auth"`
	Database DatabaseConfig `yaml:"database"`
	Gateway  GatewayConfig  `yaml:"gateway"`
	Shutdown ShutdownConfig `yaml:"shutdown"`
	Service  yaml.Node      `yaml:"service"`
}

// ShutdownConfig is how a replica stops. Nil fields keep their default.
type ShutdownConfig struct {
	// DrainDelay is how long a stopping replica keeps serving after /-/readyz
	// turns 503 and it has left the ring, so that the gateway and the other
	// replicas stop sending it calls first. Default 3s: longer than the
	// gateway's unhealthy check interval.
	DrainDelay *time.Duration `yaml:"drain_delay"`
}

// AuthConfig is the API key the service checks when it is reached without the
// gateway. Empty: it checks none, and must only be reachable through it.
type AuthConfig struct {
	APIKeySHA256 string `yaml:"apikey_sha256"`
}

// DatabaseConfig is the Postgres the Idempotency-Key records go to. Empty:
// keys are ignored, and a retried write runs twice.
type DatabaseConfig struct {
	DSN string `yaml:"dsn"`
}

// GatewayConfig is the APISIX in front of the replicas. Empty admin_url: no
// gateway. Needs the cluster, whose members are its upstream nodes.
//
// Everything the leader writes is here; what is left out keeps the default
// gatewaysync.DefaultConfig gives. "{name}" in an id is gateway.name.
type GatewayConfig struct {
	AdminURL string        `yaml:"admin_url"`
	AdminKey string        `yaml:"admin_key"`
	Interval time.Duration `yaml:"interval"`
	// Name prefixes every APISIX object the leader writes. Default Spec.Name.
	Name string `yaml:"name"`
	// Labels go on every object. Default managed-by: <name>.
	Labels map[string]string `yaml:"labels"`

	// Read and Write are the two upstreams and the methods routed to each:
	// every replica reads, the leader alone writes.
	Read  GatewayRoutes `yaml:"read"`
	Write GatewayRoutes `yaml:"write"`
	// Service is the id of the service carrying the plugins. Default {name}-api.
	Service string `yaml:"service"`
	// RouteID names a contract's routes from {version} ("orders-v1") and
	// {kind} (read, write). Default {name}-{version}-{kind}.
	RouteID string `yaml:"route_id"`

	Upstream GatewayUpstream `yaml:"upstream"`

	// Plugins are APISIX plugin configurations for the service, by plugin
	// name, over the defaults (key-auth, limit-count, client-control,
	// request-id): one named here replaces the default whole; one set to null
	// is removed; any other APISIX plugin is added.
	Plugins map[string]any `yaml:"plugins"`

	// RateLimit and MaxBodyBytes tune the default limit-count and
	// client-control without restating them. MaxBodyBytes (default 1 MiB) is
	// also the largest body the idempotency middleware reads: the same limit
	// whether the gateway is in front or not.
	RateLimit struct {
		Count  int `yaml:"count"`
		Window int `yaml:"window_seconds"`
	} `yaml:"rate_limit"`
	MaxBodyBytes int `yaml:"max_body_bytes"`

	// Docs is the unauthenticated route to Swagger UI and the contracts.
	Docs struct {
		Enabled *bool    `yaml:"enabled"` // default true
		Route   string   `yaml:"route"`   // default {name}-docs
		URIs    []string `yaml:"uris"`    // default /-/docs and /-/openapi/*
	} `yaml:"docs"`
}

// GatewayRoutes is one of the two upstreams and the methods it receives.
type GatewayRoutes struct {
	Upstream string   `yaml:"upstream"` // default {name}-read, {name}-write
	Methods  []string `yaml:"methods"`  // default GET HEAD; POST PUT PATCH DELETE
}

// GatewayUpstream is what both upstreams share.
type GatewayUpstream struct {
	Type    string `yaml:"type"` // default roundrobin
	Timeout *struct {
		Connect time.Duration `yaml:"connect"`
		Send    time.Duration `yaml:"send"`
		Read    time.Duration `yaml:"read"`
	} `yaml:"timeout"` // default 2s, 30s, 120s
	KeepalivePool *struct {
		Size        int            `yaml:"size"`
		IdleTimeout *time.Duration `yaml:"idle_timeout"`
		Requests    int            `yaml:"requests"`
	} `yaml:"keepalive_pool"` // default APISIX's
	HealthCheck GatewayHealthCheck `yaml:"health_check"`
}

// GatewayHealthCheck is APISIX's active check of every node. Intervals are
// whole seconds: APISIX counts in them.
type GatewayHealthCheck struct {
	Enabled *bool         `yaml:"enabled"` // default true
	Path    string        `yaml:"path"`    // default /-/readyz
	Timeout time.Duration `yaml:"timeout"` // default APISIX's, 1s
	Healthy struct {
		Interval     time.Duration `yaml:"interval"`  // default 2s
		Successes    int           `yaml:"successes"` // default 1
		HTTPStatuses []int         `yaml:"http_statuses"`
	} `yaml:"healthy"`
	Unhealthy struct {
		Interval     time.Duration `yaml:"interval"`      // default 1s
		HTTPFailures int           `yaml:"http_failures"` // default 2
		TCPFailures  int           `yaml:"tcp_failures"`  // default 2
		Timeouts     int           `yaml:"timeouts"`
		HTTPStatuses []int         `yaml:"http_statuses"`
	} `yaml:"unhealthy"`
}

// loaded is the configuration once resolved.
type loaded struct {
	app        *config.AppConfig
	cluster    *config.ClusterConfig
	Auth       AuthConfig
	Database   DatabaseConfig
	Gateway    GatewayConfig
	drainDelay time.Duration
}

// writerRole is the ring the replicas elect the writer in.
const writerRole = "writer"

func loadConfig(spec Spec, path string) (*loaded, error) {
	var file fileConfig
	source := "the environment"
	if path != "" {
		b, err := os.ReadFile(path) // #nosec G304 G703 -- the operator's own configuration file, from -config
		if err != nil {
			return nil, fmt.Errorf("app: %w", err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true) // a misspelled key is a mistake, not a default
		if err := dec.Decode(&file); err != nil {
			return nil, fmt.Errorf("app: %s: %w", path, err)
		}
		source = path + " and the environment"
	}

	if file.Service.Kind != 0 {
		if spec.Service == nil {
			return nil, fmt.Errorf("app: %s has a service section, and the service takes none", path)
		}
		var buf bytes.Buffer
		if err := yaml.NewEncoder(&buf).Encode(&file.Service); err != nil {
			return nil, fmt.Errorf("app: %s: service: %w", path, err)
		}
		dec := yaml.NewDecoder(&buf)
		dec.KnownFields(true)
		if err := dec.Decode(spec.Service); err != nil {
			return nil, fmt.Errorf("app: %s: service: %w", path, err)
		}
	}

	defaults := config.NewDefaultConfig()
	defaults.AppName.Default = spec.Name
	defaults.AppListenAddress.Default = "0.0.0.0:8080"
	defaults.AppLogFormat.Default = "json"

	values, err := keyValues("INFRAPI_APP_", "app", file.App, appKeys(defaults))
	if err != nil {
		return nil, fmt.Errorf("app: %s: %w", path, err)
	}
	app, err := (&config.Variables{Values: values, Path: source, DefaultConfig: defaults}).GetAppConfig()
	if err != nil {
		return nil, err
	}

	// The ring address is the API's own port: the leader's address is where
	// the others relay writes. A replica that stops leaves the ring at once,
	// handing leadership over instead of holding it until its heartbeat goes
	// stale. Its own key prefix and gossip label keep it apart from other
	// services' rings on a shared network.
	_, port, err := net.SplitHostPort(app.AppListenAddress)
	if err != nil {
		return nil, fmt.Errorf("app: listen address %q: %w", app.AppListenAddress, err)
	}
	d := defaults.Cluster
	d.Roles.Default = `["` + writerRole + `"]`
	d.InstancePort.Default = port
	d.KeepInstanceInRingOnShutdown.Default = "false"
	d.KvPrefix.Default = spec.Name + "/"
	d.GossipClusterLabel.Default = spec.Name

	values, err = keyValues("INFRAPI_CLUSTER_", "cluster", file.Cluster, clusterKeys(d))
	if err != nil {
		return nil, fmt.Errorf("app: %s: %w", path, err)
	}
	cl, err := (&config.Variables{Values: values, Path: source, DefaultConfig: defaults}).GetClusterConfig()
	if err != nil {
		return nil, err
	}

	l := &loaded{app: app, cluster: cl, Auth: file.Auth, Database: file.Database, Gateway: file.Gateway,
		drainDelay: 3 * time.Second}
	if d := file.Shutdown.DrainDelay; d != nil {
		l.drainDelay = *d
	}
	override(&l.Auth.APIKeySHA256, "INFRAPI_AUTH_APIKEY_SHA256")
	override(&l.Database.DSN, "INFRAPI_DATABASE_DSN")
	override(&l.Gateway.AdminKey, "INFRAPI_GATEWAY_ADMIN_KEY")

	if g := &l.Gateway; g.AdminURL != "" {
		if g.Interval == 0 {
			g.Interval = 15 * time.Second
		}
		if g.Name == "" {
			g.Name = spec.Name
		}
		// Resolved now, so a mistake stops the service at start.
		if _, err := g.syncConfig(nil, cl.LeaderWrites); err != nil {
			return nil, fmt.Errorf("app: %s: gateway: %w", path, err)
		}
	}
	return l, nil
}

func override(field *string, env string) {
	if v := os.Getenv(env); v != "" {
		*field = v
	}
}

// keyValues turns a section of the file into the library's keys, then lets
// the environment override them. A key the library does not know is refused:
// it would otherwise be silently ignored.
func keyValues(prefix, section string, from map[string]any, known map[string]bool) (map[string]string, error) {
	values := map[string]string{}
	keys := make([]string, 0, len(from))
	for k := range from {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		key := prefix + strings.ToUpper(k)
		if !known[key] {
			return nil, fmt.Errorf("%s.%s is not a setting (%s)", section, k, key)
		}
		v, err := keyValue(from[k])
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", section, k, err)
		}
		values[key] = v
	}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, prefix) {
			values[k] = v
		}
	}
	return values, nil
}

// keyValue writes a YAML value the way a dotenv file holds it: lists as JSON
// arrays, everything else as its text.
func keyValue(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case []any:
		items := make([]string, 0, len(x))
		for _, item := range x {
			s, err := keyValue(item)
			if err != nil {
				return "", err
			}
			items = append(items, s)
		}
		b, err := json.Marshal(items)
		return string(b), err
	case map[string]any:
		return "", fmt.Errorf("a map is not a value")
	default:
		return fmt.Sprint(x), nil
	}
}

// appKeys and clusterKeys are the keys the library reads, from its own rules.
func appKeys(d *config.DefaultConfig) map[string]bool { return optsKeys(reflect.ValueOf(d).Elem()) }

func clusterKeys(d *config.ClusterDefaultConfig) map[string]bool {
	return optsKeys(reflect.ValueOf(d).Elem())
}

func optsKeys(v reflect.Value) map[string]bool {
	keys := map[string]bool{}
	for i := 0; i < v.NumField(); i++ {
		if opts, ok := v.Field(i).Interface().(*config.VariableOpts); ok && opts != nil {
			keys[opts.Key] = true
		}
	}
	return keys
}
