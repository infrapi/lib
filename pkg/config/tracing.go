package config

import (
	"fmt"
	"strconv"
	"strings"
)

// TracingConfig is the resolved INFRAPI_TRACING_* configuration: where a
// service sends its traces, over OTLP. pkg/tracing sets the exporter up from
// it.
type TracingConfig struct {
	// Enabled turns the export on. Off, nothing is sent and the spans cost
	// next to nothing.
	Enabled bool
	// Endpoint is the OTLP receiver, as a URL: http://collector:4318 for
	// http, http://collector:4317 for grpc. An http:// scheme sends in clear,
	// https:// over TLS.
	Endpoint string
	// Protocol is http (OTLP/HTTP, protobuf) or grpc.
	Protocol string
	// SampleRatio is the share of new traces kept, 0 to 1. A trace the
	// caller already sampled is followed whatever it is.
	SampleRatio float64
	// Headers are sent with every export, e.g. a tenant: X-Scope-OrgID.
	Headers map[string]string
	// CACertPath is a PEM file of the CA the collector's certificate is
	// signed by, trusted besides the system's: a private CA. https only.
	CACertPath string
	// ClientCertPath and ClientKeyPath are the PEM certificate and key the
	// service presents to the collector: mTLS. Both or neither; https only.
	ClientCertPath string
	ClientKeyPath  string
}

// GetTracingEnabled returns INFRAPI_TRACING_ENABLED.
func (v *Variables) GetTracingEnabled() (bool, error) {
	return v.GetBool(v.DefaultConfig.TracingEnabled)
}

// GetTracingEndpoint returns INFRAPI_TRACING_ENDPOINT, empty when unset.
func (v *Variables) GetTracingEndpoint() (string, error) {
	return v.GetString(v.DefaultConfig.TracingEndpoint)
}

// GetTracingProtocol returns INFRAPI_TRACING_PROTOCOL.
func (v *Variables) GetTracingProtocol() (string, error) {
	return v.GetString(v.DefaultConfig.TracingProtocol)
}

// GetTracingSampleRatio returns INFRAPI_TRACING_SAMPLE_RATIO, between 0 and 1.
func (v *Variables) GetTracingSampleRatio() (float64, error) {
	s, err := v.GetString(v.DefaultConfig.TracingSampleRatio)
	if err != nil {
		return 0, err
	}
	r, err := strconv.ParseFloat(s, 64)
	if err != nil || r < 0 || r > 1 {
		return 0, fmt.Errorf("key %s value is not a ratio between 0 and 1, dotenv located at %s",
			v.DefaultConfig.TracingSampleRatio.Key, v.Path)
	}
	return r, nil
}

// GetTracingHeaders returns INFRAPI_TRACING_HEADERS, a JSON list of
// "Name=value" strings: ["X-Scope-OrgID=acme"].
func (v *Variables) GetTracingHeaders() (map[string]string, error) {
	list, err := v.GetSliceString(v.DefaultConfig.TracingHeaders)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, h := range list {
		name, value, ok := strings.Cut(h, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("key %s: an entry is not Name=value, dotenv located at %s",
				v.DefaultConfig.TracingHeaders.Key, v.Path)
		}
		out[strings.TrimSpace(name)] = value
	}
	return out, nil
}

// GetTracingConfig resolves every INFRAPI_TRACING_* key. Enabled without an
// endpoint is an error: the traces would have nowhere to go.
func (v *Variables) GetTracingConfig() (*TracingConfig, error) {
	if v == nil {
		var err error
		v, err = NewConfig()
		if err != nil {
			return nil, err
		}
	}
	var (
		c   TracingConfig
		err error
	)
	if c.Enabled, err = v.GetTracingEnabled(); err != nil {
		return nil, err
	}
	if c.Endpoint, err = v.GetTracingEndpoint(); err != nil {
		return nil, err
	}
	if c.Protocol, err = v.GetTracingProtocol(); err != nil {
		return nil, err
	}
	if c.SampleRatio, err = v.GetTracingSampleRatio(); err != nil {
		return nil, err
	}
	if c.Headers, err = v.GetTracingHeaders(); err != nil {
		return nil, err
	}
	d := v.DefaultConfig
	if c.CACertPath, err = v.GetString(d.TracingCACertPath); err != nil {
		return nil, err
	}
	if c.ClientCertPath, err = v.GetString(d.TracingClientCertPath); err != nil {
		return nil, err
	}
	if c.ClientKeyPath, err = v.GetString(d.TracingClientKeyPath); err != nil {
		return nil, err
	}
	if (c.ClientCertPath == "") != (c.ClientKeyPath == "") {
		return nil, fmt.Errorf("keys %s and %s go together: set both or neither, dotenv located at %s",
			d.TracingClientCertPath.Key, d.TracingClientKeyPath.Key, v.Path)
	}
	tls := c.CACertPath != "" || c.ClientCertPath != ""
	if tls && !strings.HasPrefix(strings.ToLower(c.Endpoint), "https://") {
		return nil, fmt.Errorf("keys %s, %s and %s are for an https:// %s, dotenv located at %s",
			d.TracingCACertPath.Key, d.TracingClientCertPath.Key, d.TracingClientKeyPath.Key,
			d.TracingEndpoint.Key, v.Path)
	}
	if c.Enabled && c.Endpoint == "" {
		return nil, fmt.Errorf("key %s is required when %s is true, dotenv located at %s",
			v.DefaultConfig.TracingEndpoint.Key, v.DefaultConfig.TracingEnabled.Key, v.Path)
	}
	return &c, nil
}
