// Package gateway is a client for the Apache APISIX Admin API: routes,
// services, upstreams and consumers as explicit CRUD methods on [Client], on
// top of [github.com/infrapi/lib/pkg/http], which supplies the TLS/mTLS,
// retry and timeout behaviour. Admin API failures come back as
// [github.com/infrapi/lib/pkg/errors].Error, so callers already handling that
// type elsewhere handle these too.
//
// Build a Client, then call the methods for the collection you want to
// manage - New<Type> creates or replaces an item at a chosen id, Create<Type>
// lets APISIX generate one, and Get/List/Update/Delete<Type> round out each
// collection:
//
//	client, err := gateway.NewClient(&gateway.ClientOptions{
//		Server: "127.0.0.1",
//		APIKey: apiKey,
//	})
//	upstream, err := client.NewUpstream(ctx, "1", gateway.Upstream{
//		Type:  "roundrobin",
//		Nodes: []gateway.Node{{Host: "127.0.0.1", Port: 1980, Weight: 1}},
//	})
//	route, err := client.NewRoute(ctx, "1", gateway.Route{
//		URI:      "/hello",
//		Upstream: upstream,
//	})
package gateway

import (
	"fmt"
	"time"

	infrahttp "github.com/infrapi/lib/pkg/http"
)

// adminApplication is passed to infrahttp.ClientOptions.Application, which
// NewClient requires but this package's URL scheme (/apisix/admin/...)
// never uses.
const adminApplication = "apisix-admin"

// Client is an APISIX Admin API client. It wraps an [infrahttp.Client]: that
// client's TLS, retry and timeout configuration carries over unchanged.
type Client struct {
	// APIKey is the Admin API key sent as the X-API-KEY header on every request.
	APIKey string

	// HTTP is the InfraPI HTTP client this package configures and wraps.
	// Reach into HTTP.RestyClient for anything this package does not expose.
	HTTP *infrahttp.Client
}

// ClientOptions configures a Client. Only Server and APIKey are mandatory;
// the defaults match a stock APISIX deployment.
type ClientOptions struct {
	// Scheme of the Admin API endpoint (default: http)
	Protocol string
	// Host of the Admin API endpoint
	Server string
	// Port of the Admin API endpoint (default: 9180)
	Port int

	// APISIX Admin API key, sent as the X-API-KEY header on every request
	APIKey string

	// Custom user agent (default: infrapi-lib-gateway)
	UserAgent string

	// Server CA to trust, could be byte or filesystem path, CACertContent win over CACertPath
	CACertContent []byte
	CACertPath    string

	// In case of mTLS, same than CA, content win over path
	ClientCertContent []byte
	ClientCertPath    string
	ClientKeyContent  []byte
	ClientKeyPath     string

	// Retry options, passed through to the underlying infrahttp.Client
	GlobalTimeout    int
	RetryCount       int           // Number of retries (default: 3)
	RetryWaitTime    time.Duration // Initial wait time between retries (default: 1s)
	RetryMaxWaitTime time.Duration // Maximum wait time between retries (default: 30s)
}

// Defaults applied when the options leave them unset.
const (
	DefaultGlobalTimeout = 30
	DefaultProtocol      = "http"
	DefaultPort          = 9180
	DefaultUserAgent     = "infrapi-lib-gateway"
)

// NewClient returns a Client configured with opts. Server and APIKey are
// mandatory.
func NewClient(opts *ClientOptions) (*Client, error) {
	c := new(Client)
	return c.Update(opts)
}

// Update rebuilds the client in place from opts, replacing the underlying
// infrahttp.Client and its TLS, header, timeout and retry configuration.
func (c *Client) Update(opts *ClientOptions) (*Client, error) {
	if opts == nil {
		return nil, fmt.Errorf("NewClient(opts *ClientOptions) must set ClientOptions")
	}

	if opts.Server == "" {
		return nil, fmt.Errorf("missing mandatory attribute Server")
	}

	if opts.APIKey == "" {
		return nil, fmt.Errorf("missing mandatory attribute APIKey")
	}

	protocol := opts.Protocol
	if protocol == "" {
		protocol = DefaultProtocol
	}

	port := opts.Port
	if port == 0 {
		port = DefaultPort
	}

	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}

	globalTimeout := opts.GlobalTimeout
	if globalTimeout == 0 {
		globalTimeout = DefaultGlobalTimeout
	}

	hc, err := infrahttp.NewClient(&infrahttp.ClientOptions{
		Protocol:    protocol,
		Server:      opts.Server,
		Port:        port,
		Application: adminApplication,
		UserAgent:   userAgent,
		Headers:     map[string]string{"X-API-KEY": opts.APIKey},

		CACertContent: opts.CACertContent,
		CACertPath:    opts.CACertPath,

		ClientCertContent: opts.ClientCertContent,
		ClientCertPath:    opts.ClientCertPath,
		ClientKeyContent:  opts.ClientKeyContent,
		ClientKeyPath:     opts.ClientKeyPath,

		GlobalTimeout:    globalTimeout,
		RetryCount:       opts.RetryCount,
		RetryWaitTime:    opts.RetryWaitTime,
		RetryMaxWaitTime: opts.RetryMaxWaitTime,
	})
	if err != nil {
		return nil, err
	}

	c.HTTP = hc
	c.APIKey = opts.APIKey

	return c, nil
}

// baseURL returns the Admin API root, {Protocol}://{Server}:{Port}/apisix/admin.
func (c *Client) baseURL() string {
	return fmt.Sprintf("%s://%s:%d/apisix/admin", c.HTTP.Protocol, c.HTTP.Server, c.HTTP.Port)
}
