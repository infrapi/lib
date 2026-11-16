package tofu_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/tofu"
)

// Without a state, the plan is a create: nothing is written, and what only
// applying knows is unknown.
func TestProvider_Plan_Create(t *testing.T) {
	p := startLocalProvider(t, "local")
	path := filepath.Join(t.TempDir(), "greeting.txt")

	plan, err := p.Plan(context.Background(), tofu.Resource{ResourceType: "local_file"},
		map[string]any{"content": "hello", "filename": path})
	require.NoError(t, err)

	assert.Equal(t, tofu.ActionCreate, plan.Action)
	assert.Contains(t, plan.Unknown, "id")
	assert.Equal(t, "hello", plan.Planned["content"])
	assert.Nil(t, plan.Planned["id"], "unknown until applied")
	assert.Contains(t, plan.Changes, tofu.Change{Path: "content", After: "hello"})
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "a plan applies nothing")
}

// A resource that matches its configuration plans nothing.
func TestProvider_Plan_None(t *testing.T) {
	p := startLocalProvider(t, "local")
	ctx := context.Background()
	config := map[string]any{"content": "hello", "filename": filepath.Join(t.TempDir(), "greeting.txt")}

	created, err := p.Create(ctx, "local_file", config)
	require.NoError(t, err)
	read, err := p.Read(ctx, created)
	require.NoError(t, err)

	plan, err := p.Plan(ctx, read, config)
	require.NoError(t, err)
	assert.Equal(t, tofu.ActionNone, plan.Action)
	assert.Empty(t, plan.Changes)
	assert.Empty(t, plan.Unknown)
	assert.Equal(t, read.Attributes["id"], plan.Planned["id"])
}

// local_file replaces itself for any change: the plan says so, with what
// changes, where Update would fail with ErrReplaceRequired.
func TestProvider_Plan_Replace(t *testing.T) {
	p := startLocalProvider(t, "local")
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "greeting.txt")

	created, err := p.Create(ctx, "local_file", map[string]any{"content": "hello", "filename": path})
	require.NoError(t, err)

	plan, err := p.Plan(ctx, created, map[string]any{"content": "bonjour", "filename": path})
	require.NoError(t, err)
	assert.Equal(t, tofu.ActionReplace, plan.Action)
	assert.Contains(t, plan.Changes, tofu.Change{Path: "content", Before: "hello", After: "bonjour"})

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(content), "a plan applies nothing")
}

// A sensitive attribute is reported changed without its values.
func TestProvider_Plan_Sensitive(t *testing.T) {
	p := startLocalProvider(t, "local")
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "secret.txt")

	created, err := p.Create(ctx, "local_sensitive_file", map[string]any{"content": "s3cret", "filename": path})
	require.NoError(t, err)

	plan, err := p.Plan(ctx, created, map[string]any{"content": "other", "filename": path})
	require.NoError(t, err)
	assert.Equal(t, tofu.ActionReplace, plan.Action)
	assert.Contains(t, plan.Changes, tofu.Change{Path: "content", Sensitive: true}, "changed, values left out")
}

// A provider built on the plugin framework may keep its id and computed
// values in a plan that still replaces the resource (UseStateForUnknown):
// the plan then reads as an update, as Update documents.
func TestProvider_Plan_KeepsStateForUnknown(t *testing.T) {
	p, err := tofu.Start(context.Background(), "random", randomProviderExe(t))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, p.Close()) })
	ctx := context.Background()
	require.NoError(t, p.Configure(ctx, "", nil))

	created, err := p.Create(ctx, "random_password", map[string]any{"length": 12})
	require.NoError(t, err)

	plan, err := p.Plan(ctx, created, map[string]any{"length": 16})
	require.NoError(t, err)
	assert.Equal(t, tofu.ActionUpdate, plan.Action)
	assert.Equal(t, []tofu.Change{{Path: "length", Before: float64(12), After: float64(16)}}, plan.Changes)

	plan, err = p.Plan(ctx, tofu.Resource{ResourceType: "random_password"}, map[string]any{"length": 12})
	require.NoError(t, err)
	assert.Equal(t, tofu.ActionCreate, plan.Action)
	assert.Contains(t, plan.Unknown, "result", "generated when applied")
}

func TestProvider_Plan_UnknownResourceType(t *testing.T) {
	p := startLocalProvider(t, "local")
	_, err := p.Plan(context.Background(), tofu.Resource{ResourceType: "local_nope"}, nil)
	require.Error(t, err)
}
