package http

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"

	"resty.dev/v3"
)

type Client struct {
	ClientOptions

	RestyClient *resty.Client
}

// Default and opinionated client for infrapi, but the client expose resty and tls config, to allow customization.
type ClientOptions struct {
	Protocol string
	Server   string
	Port     int

	// InfraPI token, should be set
	Token string

	// InfraPI API version
	APIVersion string
	// InfraPI application name
	Application string

	// Default headers to set, if empty will add application/json as content type and accept
	Headers map[string]string

	// Custom user agent (default: infrapi-lib-http-{application})
	UserAgent string

	// Server CA to trust, could be byte or filesystem path, CACertContent win over CACertPath
	CACertContent []byte
	CACertPath    string

	// In case of mTLS, same than CA, content win over path
	ClientCertContent []byte
	ClientCertPath    string
	ClientKeyContent  []byte
	ClientKeyPath     string

	// Resty retry options
	GlobalTimeout    int
	RetryCount       int           // Number of retries (default: 3)
	RetryWaitTime    time.Duration // Initial wait time between retries (default: 1s)
	RetryMaxWaitTime time.Duration // Maximum wait time between retries (default: 30s)

	// Hedging sends concurrent staggered requests and returns the first success.
	// Only applies to read-only methods (GET, HEAD, OPTIONS, TRACE) unless NonReadOnlyAllowed is set.
	// When hedging is enabled, retry is disabled by default inside resty.
	Hedging *HedgingOptions
}

// HedgingOptions configures resty's built-in hedging transport.
type HedgingOptions struct {
	// Enabled activates hedging (default: false).
	Enabled bool

	// Delay between successive hedged requests (default: 0, resty sends immediately).
	Delay time.Duration

	// UpTo is the maximum number of concurrent hedged requests (default: 0, disabled).
	UpTo int

	// MaxPerSecond limits total hedged requests per second (0 = unlimited).
	// Fractional rates supported: 0.5 = 1 req/2s, 2.5 = 2.5 req/s.
	MaxPerSecond float64

	// NonReadOnlyAllowed enables hedging on POST/PUT/DELETE/PATCH.
	// Disabled by default to avoid duplicate side effects on the server.
	NonReadOnlyAllowed bool
}

type RequestOptions struct {
	Server      string
	Data        []byte
	Headers     map[string]string
	MinHttpCode int
	MaxHttpCode int
}

type Response struct {
	Code int
	Body []byte
}

const (
	DefaultGlobalTimeout = time.Duration(60) * time.Second
	DefaultContent       = "application/json"
	DefaultMinHttpCode   = 200
	DefaultMaxHttpCode   = 400
)

func NewClient(opts *ClientOptions) (*Client, error) {
	c := new(Client)
	return c.Update(opts)
}

func (c *Client) Update(opts *ClientOptions) (*Client, error) {
	if opts == nil {
		return nil, fmt.Errorf("NewClient(opts *ClientOptions) must set ClientOptions")
	}

	if opts.Server == "" {
		return nil, fmt.Errorf("missing mandatory attribute Server")
	}

	if opts.Application == "" {
		return nil, fmt.Errorf("missing mandatory attribute Application")
	}

	c.RestyClient = resty.New()

	// TLS Configuration
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Server CA Certificate
	if len(opts.CACertContent) > 0 || opts.CACertPath != "" {
		tlsConfig.RootCAs = x509.NewCertPool()

		if len(opts.CACertContent) > 0 {
			tlsConfig.RootCAs.AppendCertsFromPEM(opts.CACertContent)
		} else if opts.CACertPath != "" {
			caCert, err := os.ReadFile(opts.CACertPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read CA certificate: %w", err)
			}
			tlsConfig.RootCAs.AppendCertsFromPEM(caCert)
		}
	}

	// mTLS: Client certificate and key
	if len(opts.ClientCertContent) > 0 && len(opts.ClientKeyContent) > 0 {
		// Content takes precedence over path
		cert, err := tls.X509KeyPair(opts.ClientCertContent, opts.ClientKeyContent)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate from content: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	} else if opts.ClientCertPath != "" && opts.ClientKeyPath != "" {
		cert, err := tls.LoadX509KeyPair(opts.ClientCertPath, opts.ClientKeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate from path: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	c.RestyClient.SetTLSClientConfig(tlsConfig)

	// Set timeout
	if opts.GlobalTimeout > 0 {
		c.RestyClient.SetTimeout(time.Duration(opts.GlobalTimeout) * time.Second)
	} else {
		c.RestyClient.SetTimeout(DefaultGlobalTimeout)
	}

	// Set user agent
	userAgent := fmt.Sprintf("infrapi-lib-http-%s", opts.Application)
	if opts.UserAgent != "" {
		userAgent = opts.UserAgent
	}
	c.RestyClient.SetHeader("User-Agent", userAgent)

	// Set default headers
	if opts.Token != "" {
		c.RestyClient.SetAuthToken(opts.Token)
	}

	c.RestyClient.SetHeader("Content-Type", "application/json")
	c.RestyClient.SetHeader("Accept", "application/json")
	if len(opts.Headers) > 0 {
		c.RestyClient.SetHeaders(opts.Headers)
	}

	// Configure retry mechanism
	retryCount := 3
	if opts.RetryCount > 0 {
		retryCount = opts.RetryCount
	}

	retryWaitTime := 1 * time.Second
	if opts.RetryWaitTime > 0 {
		retryWaitTime = opts.RetryWaitTime
	}

	retryMaxWaitTime := 30 * time.Second
	if opts.RetryMaxWaitTime > 0 {
		retryMaxWaitTime = opts.RetryMaxWaitTime
	}

	c.RestyClient.
		SetRetryCount(retryCount).
		SetRetryWaitTime(retryWaitTime).
		SetRetryMaxWaitTime(retryMaxWaitTime).
		AddRetryConditions(
			func(r *resty.Response, err error) bool {
				if err != nil {
					return true
				}
				return r.StatusCode() == http.StatusTooManyRequests || r.StatusCode() >= 500
			},
		)

	// Configure hedging
	if opts.Hedging != nil && opts.Hedging.Enabled {
		c.RestyClient.EnableHedging(opts.Hedging.Delay, opts.Hedging.UpTo, opts.Hedging.MaxPerSecond)
		if opts.Hedging.NonReadOnlyAllowed {
			c.RestyClient.SetHedgingAllowNonReadOnly(true)
		}
	}

	// Set default values
	if opts.Port == 0 {
		c.Port = 443
	} else {
		c.Port = opts.Port
	}

	if opts.Protocol == "" {
		c.Protocol = "https"
	} else {
		c.Protocol = opts.Protocol
	}

	if opts.APIVersion == "" {
		c.APIVersion = "v1"
	} else {
		c.APIVersion = opts.APIVersion
	}

	c.Server = opts.Server
	c.Application = opts.Application

	return c, nil
}

func (c *Client) Get(path string, opts *RequestOptions) (*Response, error) {
	return c.Do("GET", path, opts)
}

func (c *Client) Post(path string, opts *RequestOptions) (*Response, error) {
	return c.Do("POST", path, opts)
}

func (c *Client) Put(path string, opts *RequestOptions) (*Response, error) {
	return c.Do("PUT", path, opts)
}

func (c *Client) Delete(path string, opts *RequestOptions) (*Response, error) {
	return c.Do("DELETE", path, opts)
}

func (c *Client) Patch(path string, opts *RequestOptions) (*Response, error) {
	return c.Do("PATCH", path, opts)
}

func (c *Client) Head(path string, opts *RequestOptions) (*Response, error) {
	return c.Do("HEAD", path, opts)
}

func (c *Client) Do(method string, path string, opts *RequestOptions) (*Response, error) {
	r := &Response{
		Code: http.StatusInternalServerError,
	}
	if opts == nil {
		opts = &RequestOptions{}
	}

	if opts.MinHttpCode == 0 {
		opts.MinHttpCode = DefaultMinHttpCode
	}

	if opts.MaxHttpCode == 0 {
		opts.MaxHttpCode = DefaultMaxHttpCode
	}

	if len(opts.Headers) < 1 {
		opts.Headers = map[string]string{
			"Content-Type": DefaultContent,
			"Accept":       DefaultContent,
		}
	}

	if opts.Server == "" {
		opts.Server = c.Server
	}

	uri := fmt.Sprintf(
		"%s://%s:%d/api/%s/%s/%s",
		c.Protocol,
		opts.Server,
		c.Port,
		c.APIVersion,
		c.Application,
		path,
	)

	// Create request
	req := c.RestyClient.R()

	// Set request body if provided
	if opts.Data != nil {
		req.SetBody(opts.Data)
	}

	// Set additional headers from opts
	req.SetHeaders(opts.Headers)

	// Execute request
	var resp *resty.Response
	var err error

	switch method {
	case "GET":
		resp, err = req.Get(uri)
	case "POST":
		resp, err = req.Post(uri)
	case "PUT":
		resp, err = req.Put(uri)
	case "DELETE":
		resp, err = req.Delete(uri)
	case "PATCH":
		resp, err = req.Patch(uri)
	case "HEAD":
		resp, err = req.Head(uri)
	default:
		return r, fmt.Errorf("unsupported HTTP method: %s", method)
	}

	if err != nil {
		return r, err
	}

	r.Code = resp.StatusCode()
	r.Body = resp.Bytes()

	if resp.StatusCode() < opts.MinHttpCode || resp.StatusCode() > opts.MaxHttpCode {
		return r, fmt.Errorf("%s", http.StatusText(r.Code))
	}

	return r, nil
}
