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

func TestApplyAndDestroy(t *testing.T) {
	exe := localProviderExe(t)
	dir := t.TempDir()

	plan := tofu.Plan{
		Providers: []tofu.ProviderSpec{
			{Name: "local", Executable: exe, TerraformVersion: "1.15.5"},
		},
		Resources: []tofu.ResourceSpec{
			{
				Provider: "local", Address: "local_file.a", ResourceType: "local_file",
				Config: map[string]any{"content": "a", "filename": filepath.Join(dir, "a.txt")},
			},
			{
				Provider: "local", Address: "local_file.b", ResourceType: "local_file",
				Config: map[string]any{"content": "b", "filename": filepath.Join(dir, "b.txt")},
			},
		},
	}

	result, err := tofu.Apply(context.Background(), plan, nil)
	require.NoError(t, err)
	require.Len(t, result.Resources, 2)
	assert.Equal(t, "a", result.Resources["local_file.a"].Attributes["content"])
	assert.Equal(t, "b", result.Resources["local_file.b"].Attributes["content"])

	for _, name := range []string{"a.txt", "b.txt"} {
		_, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err)
	}

	// Re-applying the same plan against the prior result updates rather
	// than recreates every resource: nothing changed, so the state does not.
	result2, err := tofu.Apply(context.Background(), plan, result.Resources)
	require.NoError(t, err)
	assert.Equal(t, result.Resources["local_file.a"].Attributes, result2.Resources["local_file.a"].Attributes)

	// A changed local_file requires replacement: Apply deletes and creates
	// it, where Update alone refuses.
	plan.Resources[0].Config["content"] = "a2"
	result2, err = tofu.Apply(context.Background(), plan, result2.Resources)
	require.NoError(t, err)
	assert.Equal(t, "a2", result2.Resources["local_file.a"].Attributes["content"])
	content, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "a2", string(content))

	require.NoError(t, tofu.Destroy(context.Background(), plan, result2.Resources))

	for _, name := range []string{"a.txt", "b.txt"} {
		_, err := os.Stat(filepath.Join(dir, name))
		assert.True(t, os.IsNotExist(err), "%s should be gone after Destroy", name)
	}
}

func TestApply_UnknownProvider(t *testing.T) {
	plan := tofu.Plan{
		Resources: []tofu.ResourceSpec{
			{Provider: "missing", Address: "x", ResourceType: "local_file"},
		},
	}

	_, err := tofu.Apply(context.Background(), plan, nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, infraerrors.ErrorCode(err))
}

func TestApply_ProviderStartFailure(t *testing.T) {
	plan := tofu.Plan{
		Providers: []tofu.ProviderSpec{
			{Name: "broken", Executable: filepath.Join(t.TempDir(), "does-not-exist")},
		},
	}

	_, err := tofu.Apply(context.Background(), plan, nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, infraerrors.ErrorCode(err))
}

func TestDestroy_UnknownAddress(t *testing.T) {
	err := tofu.Destroy(context.Background(), tofu.Plan{}, map[string]tofu.Resource{
		"local_file.orphan": {ResourceType: "local_file"},
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, infraerrors.ErrorCode(err))
}
