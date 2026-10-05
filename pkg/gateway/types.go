package gateway

// Node is one upstream backend: where traffic goes and how much of it, relative
// to the other nodes in the same Upstream.
type Node struct {
	// Host is the backend's address, a hostname or an IP.
	Host string `json:"host"`
	// Port the backend listens on.
	Port int `json:"port"`
	// Weight the backend gets relative to the upstream's other nodes.
	Weight int `json:"weight"`
}

// Upstream is a named group of backend Nodes and the policy for picking one.
type Upstream struct {
	// Name identifies the upstream in the dashboard and logs.
	Name string `json:"name,omitempty"`
	// Desc is a human-readable description.
	Desc string `json:"desc,omitempty"`
	// Type is the load-balancing algorithm: roundrobin, chash, ewma or least_conn.
	Type string `json:"type,omitempty"`
	// Nodes are the backends traffic is sent to.
	Nodes []Node `json:"nodes,omitempty"`
	// Scheme is the protocol used to reach the nodes: http, https, grpc, grpcs...
	Scheme string `json:"scheme,omitempty"`
	// PassHost controls the Host header forwarded to the nodes: pass, node or rewrite.
	PassHost string `json:"pass_host,omitempty"`
	// Retries is the number of times a failed request is retried against another node.
	Retries int `json:"retries,omitempty"`
	// Labels are key/value tags, used for filtering in the dashboard and plugins.
	Labels map[string]string `json:"labels,omitempty"`
	// RetryTimeout bounds, in seconds, how long retries may go on; 0 disables
	// the bound. Nil leaves APISIX's default.
	RetryTimeout *float64 `json:"retry_timeout,omitempty"`
	// Timeout overrides the connect, send and read timeouts to the nodes.
	Timeout *Timeout `json:"timeout,omitempty"`
	// KeepalivePool sizes the pool of connections kept open to the nodes.
	KeepalivePool *KeepalivePool `json:"keepalive_pool,omitempty"`
	// Checks are the health checks that take a node out of rotation, and put
	// it back, without waiting for its Upstream to be rewritten.
	Checks *HealthChecks `json:"checks,omitempty"`
}

// Timeout is the time, in seconds, APISIX allows for each step of a request to
// a node. APISIX requires all three, each greater than zero.
type Timeout struct {
	Connect float64 `json:"connect"`
	Send    float64 `json:"send"`
	Read    float64 `json:"read"`
}

// KeepalivePool sizes the connections kept open to an upstream's nodes. A zero
// field leaves APISIX's default: 320 connections, 60 seconds idle, 1000
// requests each.
type KeepalivePool struct {
	// Size is how many idle connections are kept.
	Size int `json:"size,omitempty"`
	// IdleTimeout, in seconds, closes a connection idle for that long; 0 keeps
	// it open. Nil leaves APISIX's default.
	IdleTimeout *float64 `json:"idle_timeout,omitempty"`
	// Requests is how many requests a connection serves before it is closed.
	Requests int `json:"requests,omitempty"`
}

// HealthChecks are an upstream's active and passive checks. APISIX requires an
// active check whenever a passive one is set: a node a passive check took out
// only comes back through the active one.
type HealthChecks struct {
	Active  *ActiveCheck  `json:"active,omitempty"`
	Passive *PassiveCheck `json:"passive,omitempty"`
}

// ActiveCheck probes every node on its own schedule. A zero field leaves
// APISIX's default.
type ActiveCheck struct {
	// Type is http (the default), https or tcp.
	Type string `json:"type,omitempty"`
	// Timeout, in seconds, of one probe. Default 1.
	Timeout float64 `json:"timeout,omitempty"`
	// Concurrency is how many nodes are probed at once. Default 10.
	Concurrency int `json:"concurrency,omitempty"`
	// Host is the Host header of an http or https probe.
	Host string `json:"host,omitempty"`
	// Port probes another port than the one traffic goes to.
	Port int `json:"port,omitempty"`
	// HTTPMethod of the probe. Default GET.
	HTTPMethod string `json:"http_method,omitempty"`
	// HTTPPath of the probe. Default /.
	HTTPPath string `json:"http_path,omitempty"`
	// HTTPReqBody is the body the probe sends.
	HTTPReqBody string `json:"http_req_body,omitempty"`
	// HTTPSVerifyCertificate, when false, accepts any certificate on an https
	// probe. Nil leaves APISIX's default, true.
	HTTPSVerifyCertificate *bool `json:"https_verify_certificate,omitempty"`
	// ReqHeaders are extra probe headers, each "Name: value".
	ReqHeaders []string `json:"req_headers,omitempty"`
	// Healthy says when a node in rotation is probed and when one out of it
	// comes back.
	Healthy *ActiveHealthy `json:"healthy,omitempty"`
	// Unhealthy says when a node out of rotation is probed and when one in it
	// is taken out.
	Unhealthy *ActiveUnhealthy `json:"unhealthy,omitempty"`
}

// ActiveHealthy is the active check's rule for a healthy node. A zero field
// leaves APISIX's default.
type ActiveHealthy struct {
	// Interval, in seconds, between probes of a healthy node. Default 1.
	Interval int `json:"interval,omitempty"`
	// HTTPStatuses a probe counts as a success. Default 200 and 302.
	HTTPStatuses []int `json:"http_statuses,omitempty"`
	// Successes in a row that bring a node back. Default 2.
	Successes int `json:"successes,omitempty"`
}

// ActiveUnhealthy is the active check's rule for an unhealthy node. A zero
// field leaves APISIX's default.
type ActiveUnhealthy struct {
	// Interval, in seconds, between probes of an unhealthy node. Default 1.
	Interval int `json:"interval,omitempty"`
	// HTTPStatuses a probe counts as a failure. Default 429, 404, 500 to 505.
	HTTPStatuses []int `json:"http_statuses,omitempty"`
	// HTTPFailures in a row that take a node out. Default 5.
	HTTPFailures int `json:"http_failures,omitempty"`
	// TCPFailures in a row that take a node out. Default 2.
	TCPFailures int `json:"tcp_failures,omitempty"`
	// Timeouts in a row that take a node out. Default 3.
	Timeouts int `json:"timeouts,omitempty"`
}

// PassiveCheck judges nodes by the traffic itself. Its counters accept 0,
// which turns that criterion off, so they are pointers: nil leaves APISIX's
// default.
type PassiveCheck struct {
	// Type is http (the default), https or tcp.
	Type string `json:"type,omitempty"`
	// Healthy says which answers count as a node doing well.
	Healthy *PassiveHealthy `json:"healthy,omitempty"`
	// Unhealthy says which answers count against a node.
	Unhealthy *PassiveUnhealthy `json:"unhealthy,omitempty"`
}

// PassiveHealthy is the passive check's rule for a healthy node.
type PassiveHealthy struct {
	// HTTPStatuses counted as successes. Default every 2xx and 3xx.
	HTTPStatuses []int `json:"http_statuses,omitempty"`
	// Successes in a row that mark a node healthy. Default 5.
	Successes *int `json:"successes,omitempty"`
}

// PassiveUnhealthy is the passive check's rule for an unhealthy node.
type PassiveUnhealthy struct {
	// HTTPStatuses counted as failures. Default 429, 500 and 503.
	HTTPStatuses []int `json:"http_statuses,omitempty"`
	// HTTPFailures in a row that take a node out. Default 5.
	HTTPFailures *int `json:"http_failures,omitempty"`
	// TCPFailures in a row that take a node out. Default 2.
	TCPFailures *int `json:"tcp_failures,omitempty"`
	// Timeouts in a row that take a node out. Default 7.
	Timeouts *int `json:"timeouts,omitempty"`
}

// Route binds a request pattern to an Upstream or a Service, with plugins
// applied along the way.
type Route struct {
	// Name identifies the route in the dashboard and logs.
	Name string `json:"name,omitempty"`
	// Desc is a human-readable description.
	Desc string `json:"desc,omitempty"`
	// URI is the single path this route matches. Use URIs for more than one.
	URI string `json:"uri,omitempty"`
	// URIs is the list of paths this route matches.
	URIs []string `json:"uris,omitempty"`
	// Host is the single Host header this route matches. Use Hosts for more than one.
	Host string `json:"host,omitempty"`
	// Hosts is the list of Host headers this route matches.
	Hosts []string `json:"hosts,omitempty"`
	// Methods restricts the route to these HTTP methods. Empty matches all of them.
	Methods []string `json:"methods,omitempty"`
	// Priority breaks ties between routes that would otherwise match the same request;
	// higher wins.
	Priority int `json:"priority,omitempty"`
	// Plugins are the plugin configurations applied to requests through this route,
	// keyed by plugin name.
	Plugins map[string]any `json:"plugins,omitempty"`
	// Upstream is an upstream defined inline, specific to this route.
	Upstream *Upstream `json:"upstream,omitempty"`
	// UpstreamID references a standalone Upstream instead of embedding one.
	UpstreamID string `json:"upstream_id,omitempty"`
	// ServiceID references a Service this route is attached to.
	ServiceID string `json:"service_id,omitempty"`
	// Labels are key/value tags, used for filtering in the dashboard and plugins.
	Labels map[string]string `json:"labels,omitempty"`
	// Status is 1 to enable the route and 0 to disable it without deleting it.
	Status int `json:"status,omitempty"`
}

// Service is a reusable group of plugins and an upstream, shared by the routes
// that reference it.
type Service struct {
	// Name identifies the service in the dashboard and logs.
	Name string `json:"name,omitempty"`
	// Desc is a human-readable description.
	Desc string `json:"desc,omitempty"`
	// Plugins are the plugin configurations applied to every route attached to
	// this service, keyed by plugin name.
	Plugins map[string]any `json:"plugins,omitempty"`
	// Upstream is an upstream defined inline, shared by every route attached to
	// this service.
	Upstream *Upstream `json:"upstream,omitempty"`
	// UpstreamID references a standalone Upstream instead of embedding one.
	UpstreamID string `json:"upstream_id,omitempty"`
	// Labels are key/value tags, used for filtering in the dashboard and plugins.
	Labels map[string]string `json:"labels,omitempty"`
}

// Consumer is a caller identity that routes and services can authenticate and
// authorize by, through plugins like key-auth or basic-auth. Consumers are
// keyed by Username rather than a generated id.
type Consumer struct {
	// Username identifies the consumer and doubles as its Admin API id.
	Username string `json:"username"`
	// Desc is a human-readable description.
	Desc string `json:"desc,omitempty"`
	// Plugins configure how this consumer authenticates, keyed by plugin name,
	// e.g. {"key-auth": {"key": "secret"}}.
	Plugins map[string]any `json:"plugins,omitempty"`
	// GroupID attaches the consumer to a consumer_groups entry it inherits
	// plugin configuration from.
	GroupID string `json:"group_id,omitempty"`
	// Labels are key/value tags, used for filtering in the dashboard and plugins.
	Labels map[string]string `json:"labels,omitempty"`
}
