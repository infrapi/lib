package tofu_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zclconf/go-cty/cty"

	infraerrors "github.com/infrapi/lib/pkg/errors"
	"github.com/infrapi/lib/pkg/tofu"
)

func startLocalProvider(t *testing.T, name string) *tofu.Provider {
	t.Helper()

	p, err := tofu.Start(context.Background(), name, localProviderExe(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	require.NoError(t, p.Configure(context.Background(), "1.15.5", nil))
	return p
}

func TestProvider_StartAndSchema(t *testing.T) {
	p := startLocalProvider(t, "local")

	assert.Equal(t, "local", p.Name())
	assert.Contains(t, p.ResourceTypes(), "local_file")
}

func TestProvider_CreateReadUpdateDelete(t *testing.T) {
	p := startLocalProvider(t, "local")
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "greeting.txt")
	config := map[string]any{"content": "hello", "filename": path}

	created, err := p.Create(ctx, "local_file", config)
	require.NoError(t, err)
	assert.Equal(t, "local_file", created.ResourceType)
	assert.Equal(t, "hello", created.Attributes["content"])

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(content))

	read, err := p.Read(ctx, created)
	require.NoError(t, err)
	assert.Equal(t, created.Attributes["content"], read.Attributes["content"])
	assert.Equal(t, created.Attributes["id"], read.Attributes["id"])

	// local_file requires replacement for every settable attribute (see
	// TestProvider_Update_RequiresReplacement), so the only config Update
	// can apply in place to it is the one already in state: this exercises
	// the validate/plan/apply round trip without tripping that limitation.
	updated, err := p.Update(ctx, read, config)
	require.NoError(t, err)
	assert.Equal(t, read.Attributes, updated.Attributes)

	require.NoError(t, p.Delete(ctx, updated))
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "file should be gone after Delete")
}

func TestProvider_Update_RequiresReplacement(t *testing.T) {
	p := startLocalProvider(t, "local")
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "greeting.txt")

	created, err := p.Create(ctx, "local_file", map[string]any{"content": "hello", "filename": path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Delete(ctx, created) })

	// local_file marks every settable attribute RequiresReplace, so
	// changing "content" needs a delete and a create, not an in-place
	// update: Update says so and applies nothing.
	_, err = p.Update(ctx, created, map[string]any{"content": "goodbye", "filename": path})
	require.ErrorIs(t, err, tofu.ErrReplaceRequired)
	assert.Equal(t, http.StatusConflict, infraerrors.ErrorCode(err))
	assert.Contains(t, err.Error(), "content")

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(content), "nothing applied")
}

func TestProvider_Create_InvalidConfig(t *testing.T) {
	p := startLocalProvider(t, "local")

	// "filename" is a required attribute of local_file: omitting it must
	// fail validation before anything is planned or applied, as the
	// caller's mistake.
	_, err := p.Create(context.Background(), "local_file", map[string]any{
		"content": "hello",
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, infraerrors.ErrorCode(err))

	var de *tofu.DiagnosticsError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, tofu.PhaseValidate, de.Phase)
	require.NotEmpty(t, de.Diagnostics)
	assert.NotEmpty(t, de.Diagnostics[0].Summary)
}

func TestProvider_ResourceSchema(t *testing.T) {
	p := startLocalProvider(t, "local")

	s, err := p.ResourceSchema("local_file")
	require.NoError(t, err)
	assert.NotEmpty(t, s.Description)
	assert.Equal(t, tofu.UsageRequired, s.Attributes["filename"].Usage)
	assert.Equal(t, cty.String, s.Attributes["filename"].Type)
	assert.NotEmpty(t, s.Attributes["filename"].Description)
	assert.Equal(t, tofu.UsageComputed, s.Attributes["content_md5"].Usage)
	assert.True(t, s.Attributes["sensitive_content"].Sensitive)
	assert.True(t, s.Attributes["sensitive_content"].Deprecated)

	// A copy each time: changing one leaves the next alone
	delete(s.Attributes, "filename")
	again, err := p.ResourceSchema("local_file")
	require.NoError(t, err)
	assert.Contains(t, again.Attributes, "filename")

	_, err = p.ResourceSchema("local_nope")
	assert.Equal(t, http.StatusNotFound, infraerrors.ErrorCode(err))
}

func TestProvider_ResourceSchemaTypes(t *testing.T) {
	p, err := tofu.Start(context.Background(), "random", randomProviderExe(t))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, p.Close()) })

	s, err := p.ResourceSchema("random_password")
	require.NoError(t, err)
	assert.Equal(t, cty.Number, s.Attributes["length"].Type)
	assert.Equal(t, cty.Map(cty.String), s.Attributes["keepers"].Type)
	assert.Equal(t, tofu.UsageOptional, s.Attributes["keepers"].Usage)
	assert.Equal(t, tofu.UsageComputed, s.Attributes["result"].Usage)
	assert.True(t, s.Attributes["result"].Sensitive)
	assert.Equal(t, tofu.UsageOptionalComputed, s.Attributes["special"].Usage)
}

func TestProvider_Import(t *testing.T) {
	p, err := tofu.Start(context.Background(), "random", randomProviderExe(t))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, p.Close()) })
	require.NoError(t, p.Configure(context.Background(), "", nil))

	// random_string imports the string itself as its id.
	res, err := p.Import(context.Background(), "random_string", "s3cret-value")
	require.NoError(t, err)
	assert.Equal(t, "random_string", res.ResourceType)
	assert.Equal(t, "s3cret-value", res.Attributes["result"])
	assert.Equal(t, float64(len("s3cret-value")), res.Attributes["length"])
}

func TestProvider_Import_Unsupported(t *testing.T) {
	p := startLocalProvider(t, "local")

	_, err := p.Import(context.Background(), "local_file", "anything")
	require.Error(t, err)
	var de *tofu.DiagnosticsError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, tofu.PhaseImport, de.Phase)
	assert.Equal(t, http.StatusBadGateway, infraerrors.ErrorCode(err))
}

func TestStart_BadExecutable(t *testing.T) {
	_, err := tofu.Start(context.Background(), "bogus", filepath.Join(t.TempDir(), "does-not-exist"))
	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, infraerrors.ErrorCode(err))
}

func TestProvider_Configure_InvalidConfig(t *testing.T) {
	p, err := tofu.Start(context.Background(), "local", localProviderExe(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	// local_file's provider block takes no arguments at all, so any key
	// fails to convert against its (empty) config schema.
	err = p.Configure(context.Background(), "1.15.5", map[string]any{"bogus": "value"})
	require.Error(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, infraerrors.ErrorCode(err))
}

func TestProvider_Create_UnknownResourceType(t *testing.T) {
	p := startLocalProvider(t, "local")

	_, err := p.Create(context.Background(), "local_does_not_exist", nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, infraerrors.ErrorCode(err))
}

func TestRegistry(t *testing.T) {
	ctx := context.Background()
	exe := localProviderExe(t)

	registry := tofu.NewRegistry()
	t.Cleanup(func() { _ = registry.Close() })

	primary, err := registry.Start(ctx, "primary", exe)
	require.NoError(t, err)
	secondary, err := registry.Start(ctx, "secondary", exe)
	require.NoError(t, err)

	require.NoError(t, primary.Configure(ctx, "1.15.5", nil))
	require.NoError(t, secondary.Configure(ctx, "1.15.5", nil))

	assert.ElementsMatch(t, []string{"primary", "secondary"}, registry.Names())

	got, ok := registry.Provider("primary")
	assert.True(t, ok)
	assert.Same(t, primary, got)

	_, ok = registry.Provider("missing")
	assert.False(t, ok)

	_, err = registry.Start(ctx, "primary", exe)
	require.Error(t, err)
	assert.Equal(t, http.StatusConflict, infraerrors.ErrorCode(err))

	primaryPath := filepath.Join(t.TempDir(), "primary.txt")
	secondaryPath := filepath.Join(t.TempDir(), "secondary.txt")

	primaryFile, err := primary.Create(ctx, "local_file", map[string]any{"content": "from primary", "filename": primaryPath})
	require.NoError(t, err)
	secondaryFile, err := secondary.Create(ctx, "local_file", map[string]any{"content": "from secondary", "filename": secondaryPath})
	require.NoError(t, err)

	require.NoError(t, primary.Delete(ctx, primaryFile))
	require.NoError(t, secondary.Delete(ctx, secondaryFile))

	require.NoError(t, registry.Close())
}
