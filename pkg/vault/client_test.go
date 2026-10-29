package vault

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient_requiresAddress(t *testing.T) {
	_, err := NewClient(ClientOptions{})
	assert.Error(t, err)
}

func TestNewClient_appliesOptions(t *testing.T) {
	client, err := NewClient(ClientOptions{
		Address:          "http://vault.example.com",
		Timeout:          20 * time.Second,
		RetryWaitTime:    2 * time.Second,
		RetryMaxWaitTime: 4 * time.Second,
		RetryCount:       5,
	})
	require.NoError(t, err)

	cfg := client.APIClient.Clone().Configuration()
	assert.Equal(t, "http://vault.example.com", cfg.Address)
	assert.Equal(t, 20*time.Second, cfg.RequestTimeout)
	assert.Equal(t, 2*time.Second, cfg.RetryConfiguration.RetryWaitMin)
	assert.Equal(t, 4*time.Second, cfg.RetryConfiguration.RetryWaitMax)
	assert.Equal(t, 5, cfg.RetryConfiguration.RetryMax)
}

func TestNewClient_defaults(t *testing.T) {
	client, err := NewClient(ClientOptions{Address: "http://vault.example.com"})
	require.NoError(t, err)

	cfg := client.APIClient.Clone().Configuration()
	assert.Equal(t, DefaultTimeout, cfg.RequestTimeout)
	assert.Equal(t, DefaultRetryCount, cfg.RetryConfiguration.RetryMax)
	assert.Equal(t, DefaultRetryWaitTime, cfg.RetryConfiguration.RetryWaitMin)
	assert.Equal(t, DefaultRetryMaxWaitTime, cfg.RetryConfiguration.RetryWaitMax)
}

func TestNewClient_setsNamespace(t *testing.T) {
	_, err := NewClient(ClientOptions{Address: "http://vault.example.com", Namespace: "tenant-a"})
	require.NoError(t, err)
}

func TestNewClient_noRetries(t *testing.T) {
	client, err := NewClient(ClientOptions{Address: "http://vault.example.com", RetryCount: -1})
	require.NoError(t, err)
	assert.Equal(t, 0, client.APIClient.Clone().Configuration().RetryConfiguration.RetryMax)
}
