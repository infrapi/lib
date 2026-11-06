package tofu_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	infraerrors "github.com/infrapi/lib/pkg/errors"
	"github.com/infrapi/lib/pkg/tofu"
)

func TestProvider_DataSourceTypesAndSchema(t *testing.T) {
	p := startLocalProvider(t, "local")

	assert.Contains(t, p.DataSourceTypes(), "local_file")

	b, err := p.DataSourceSchema("local_file")
	require.NoError(t, err)
	assert.Equal(t, tofu.UsageRequired, b.Attributes["filename"].Usage, "an argument")
	assert.Equal(t, tofu.UsageComputed, b.Attributes["content"].Usage, "what it reads")

	_, err = p.DataSourceSchema("local_does_not_exist")
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, infraerrors.ErrorCode(err))
}

// A data source is read from its arguments alone: local_file reads the
// file it names.
func TestProvider_ReadDataSource(t *testing.T) {
	p := startLocalProvider(t, "local")
	path := filepath.Join(t.TempDir(), "greeting.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o600))

	attrs, err := p.ReadDataSource(context.Background(), "local_file", map[string]any{"filename": path})
	require.NoError(t, err)
	assert.Equal(t, "hello", attrs["content"])
	assert.Equal(t, path, attrs["filename"])
	assert.NotEmpty(t, attrs["id"])
}

// What the provider refuses, a missing file here, is its error, as a
// resource's would be.
func TestProvider_ReadDataSource_ProviderError(t *testing.T) {
	p := startLocalProvider(t, "local")

	_, err := p.ReadDataSource(context.Background(), "local_file",
		map[string]any{"filename": filepath.Join(t.TempDir(), "missing.txt")})
	require.Error(t, err)
}

func TestProvider_ReadDataSource_Unknown(t *testing.T) {
	p := startLocalProvider(t, "local")

	_, err := p.ReadDataSource(context.Background(), "local_does_not_exist", nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, infraerrors.ErrorCode(err))
}

// An argument of the wrong type is the caller's error, before the provider
// is asked.
func TestProvider_ReadDataSource_InvalidConfig(t *testing.T) {
	p := startLocalProvider(t, "local")

	_, err := p.ReadDataSource(context.Background(), "local_file", map[string]any{"filename": []any{"a"}})
	require.Error(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, infraerrors.ErrorCode(err))
}
