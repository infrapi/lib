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
