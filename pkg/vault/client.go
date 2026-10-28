// Package vault wraps github.com/hashicorp/vault-client-go with InfraPI
// defaults: TLS/mTLS, retry, and an opinionated surface for the KV v2 and
// PKI secrets engines. Callers whose use case does not fit that surface
// use [Client.APIClient] directly, which is the vanilla Vault client.
package vault

import (
	"fmt"
	"time"

	api "github.com/hashicorp/vault-client-go"
)

// Defaults applied to every client when ClientOptions leaves them unset -
// the same values github.com/hashicorp/vault-client-go documents as its
// own, made explicit here rather than left to the dependency to pick.
const (
	DefaultTimeout          = 60 * time.Second
	DefaultRetryCount       = 2
	DefaultRetryWaitTime    = time.Second
	DefaultRetryMaxWaitTime = 1500 * time.Millisecond
)

// ClientOptions configures a Client. Address is mandatory.
type ClientOptions struct {
	// Address of the Vault server, e.g. "https://vault.example.com:8200".
	Address string

	// Namespace selects a Vault Enterprise namespace for every request
	// this client makes. Ignored against Vault Community Edition.
	Namespace string

	// Timeout per request (default: DefaultTimeout).
	Timeout time.Duration
	// RetryCount is the number of times a 5xx or 412 response is retried
	// (default: DefaultRetryCount).
	RetryCount int
	// RetryWaitTime is the initial wait between retries (default:
	// DefaultRetryWaitTime).
	RetryWaitTime time.Duration
	// RetryMaxWaitTime is the maximum wait between retries (default:
	// DefaultRetryMaxWaitTime).
	RetryMaxWaitTime time.Duration

	// CA certificate trusted to verify the Vault server. Content wins
	// over path when both are set.
	CACertContent []byte
	CACertPath    string

	// Client certificate and key, for mTLS. Content wins over path when
	// both are set; a keypair needs both halves from the same source.
	ClientCertContent []byte
	ClientCertPath    string
	ClientKeyContent  []byte
	ClientKeyPath     string
}

// Client calls Vault's KV v2 and PKI secrets engines, over a
// github.com/hashicorp/vault-client-go client configured from
// ClientOptions.
type Client struct {
	ClientOptions

	// APIClient is the underlying vault-client-go client. Anything this
	// wrapper does not cover - other secrets engines, system endpoints,
	// a different auth method - is reachable there.
	APIClient *api.Client
}

// NewClient returns a Client calling opts.Address.
func NewClient(opts ClientOptions) (*Client, error) {
	if opts.Address == "" {
		return nil, fmt.Errorf("vault: NewClient requires Address")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	retryCount := opts.RetryCount
	if retryCount == 0 {
		retryCount = DefaultRetryCount
	}
	retryWaitTime := opts.RetryWaitTime
	if retryWaitTime <= 0 {
		retryWaitTime = DefaultRetryWaitTime
	}
	retryMaxWaitTime := opts.RetryMaxWaitTime
	if retryMaxWaitTime <= 0 {
		retryMaxWaitTime = DefaultRetryMaxWaitTime
	}

	raw, err := api.New(
		api.WithAddress(opts.Address),
		api.WithRequestTimeout(timeout),
		api.WithRetryConfiguration(api.RetryConfiguration{
			RetryMax:     retryCount,
			RetryWaitMin: retryWaitTime,
			RetryWaitMax: retryMaxWaitTime,
		}),
		api.WithTLS(tlsConfiguration(opts)),
	)
	if err != nil {
		return nil, fmt.Errorf("vault: NewClient: %w", err)
	}

	if opts.Namespace != "" {
		if err := raw.SetNamespace(opts.Namespace); err != nil {
			return nil, fmt.Errorf("vault: set namespace %q: %w", opts.Namespace, err)
		}
	}

	return &Client{ClientOptions: opts, APIClient: raw}, nil
}

// tlsConfiguration builds vault-client-go's TLS settings from opts, with
// each *Content field taking precedence over its *Path counterpart.
func tlsConfiguration(opts ClientOptions) api.TLSConfiguration {
	var tls api.TLSConfiguration

	switch {
	case len(opts.CACertContent) > 0:
		tls.ServerCertificate.FromBytes = opts.CACertContent
	case opts.CACertPath != "":
		tls.ServerCertificate.FromFile = opts.CACertPath
	}

	switch {
	case len(opts.ClientCertContent) > 0:
		tls.ClientCertificate.FromBytes = opts.ClientCertContent
	case opts.ClientCertPath != "":
		tls.ClientCertificate.FromFile = opts.ClientCertPath
	}

	switch {
	case len(opts.ClientKeyContent) > 0:
		tls.ClientCertificateKey.FromBytes = opts.ClientKeyContent
	case opts.ClientKeyPath != "":
		tls.ClientCertificateKey.FromFile = opts.ClientKeyPath
	}

	return tls
}
