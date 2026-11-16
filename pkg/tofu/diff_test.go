package tofu

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

// diffBlock has one attribute or block of each shape the diff walks.
var diffBlock = Block{
	Attributes: map[string]Attribute{
		"title":    {Usage: UsageRequired, Type: cty.String},
		"token":    {Usage: UsageOptional, Type: cty.String, Sensitive: true},
		"labels":   {Usage: UsageOptional, Type: cty.Map(cty.String)},
		"tags":     {Usage: UsageOptional, Type: cty.Set(cty.String)},
		"aliases":  {Usage: UsageOptional, Type: cty.List(cty.String)},
		"url":      {Usage: UsageComputed, Type: cty.String},
		"settings": {Usage: UsageOptional, Nested: &NestedBlock{Nesting: NestingSingle, Block: Block{Attributes: map[string]Attribute{"theme": {Usage: UsageOptional, Type: cty.String}}}}},
	},
	Blocks: map[string]NestedBlock{
		"permission": {Nesting: NestingList, Block: Block{Attributes: map[string]Attribute{
			"role": {Usage: UsageRequired, Type: cty.String},
			"key":  {Usage: UsageOptional, Type: cty.String, Sensitive: true},
		}}},
	},
}

var diffType = cty.Object(map[string]cty.Type{
	"title":      cty.String,
	"token":      cty.String,
	"labels":     cty.Map(cty.String),
	"tags":       cty.Set(cty.String),
	"aliases":    cty.List(cty.String),
	"url":        cty.String,
	"settings":   cty.Object(map[string]cty.Type{"theme": cty.String}),
	"permission": cty.List(cty.Object(map[string]cty.Type{"role": cty.String, "key": cty.String})),
})

// state builds a value of diffType from the given attributes, the others null.
func state(t *testing.T, attrs map[string]any) cty.Value {
	t.Helper()
	v, err := valueFromGo(diffType, attrs)
	require.NoError(t, err)
	return v
}

func TestDiff(t *testing.T) {
	base := map[string]any{
		"title":      "Ops",
		"token":      "s3cret",
		"labels":     map[string]any{"env": "dev", "team": "sre"},
		"tags":       []any{"a", "b"},
		"aliases":    []any{"x"},
		"url":        "https://g/ops",
		"settings":   map[string]any{"theme": "dark"},
		"permission": []any{map[string]any{"role": "Viewer", "key": "k1"}},
	}
	with := func(changes map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range changes {
			out[k] = v
		}
		return out
	}

	for name, c := range map[string]struct {
		after map[string]any
		want  []Change
	}{
		"equal":     {base, nil},
		"primitive": {with(map[string]any{"title": "Operations"}), []Change{{Path: "title", Before: "Ops", After: "Operations"}}},
		"sensitive": {with(map[string]any{"token": "other"}), []Change{{Path: "token", Sensitive: true}}},
		"map key": {with(map[string]any{"labels": map[string]any{"env": "prod", "owner": "me"}}), []Change{
			{Path: `labels["env"]`, Before: "dev", After: "prod"},
			{Path: `labels["owner"]`, After: "me"},
			{Path: `labels["team"]`, Before: "sre"},
		}},
		"set as a whole":  {with(map[string]any{"tags": []any{"a", "c"}}), []Change{{Path: "tags", Before: []any{"a", "b"}, After: []any{"a", "c"}}}},
		"list grows":      {with(map[string]any{"aliases": []any{"x", "y"}}), []Change{{Path: "aliases[1]", After: "y"}}},
		"nested object":   {with(map[string]any{"settings": map[string]any{"theme": "light"}}), []Change{{Path: "settings.theme", Before: "dark", After: "light"}}},
		"block in a list": {with(map[string]any{"permission": []any{map[string]any{"role": "Editor", "key": "k1"}}}), []Change{{Path: "permission[0].role", Before: "Viewer", After: "Editor"}}},
		"sensitive in a block": {with(map[string]any{"permission": []any{map[string]any{"role": "Viewer", "key": "k2"}}}),
			[]Change{{Path: "permission[0].key", Sensitive: true}}},
		"null is not empty": {with(map[string]any{"aliases": []any{}}), []Change{{Path: "aliases[0]", Before: "x"}}},
		"to null":           {with(map[string]any{"aliases": nil}), []Change{{Path: "aliases", Before: []any{"x"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			changes, unknown, err := diff(diffBlock, state(t, base), state(t, c.after))
			require.NoError(t, err)
			assert.Equal(t, c.want, changes)
			assert.Empty(t, unknown)
		})
	}
}

// An unknown planned value is listed, and a change when it was known.
func TestDiffUnknown(t *testing.T) {
	before := state(t, map[string]any{"title": "Ops", "url": "https://g/ops"})
	attrs := before.AsValueMap()
	attrs["url"] = cty.UnknownVal(cty.String)
	after := cty.ObjectVal(attrs)

	changes, unknown, err := diff(diffBlock, before, after)
	require.NoError(t, err)
	assert.Equal(t, []string{"url"}, unknown)
	assert.Equal(t, []Change{{Path: "url", Before: "https://g/ops", Unknown: true}}, changes)
}

// From null, as for a create, every set value is a change, and what is
// unknown is only listed.
func TestDiffCreate(t *testing.T) {
	after := state(t, map[string]any{"title": "Ops", "permission": []any{map[string]any{"role": "Viewer"}}}).AsValueMap()
	after["url"] = cty.UnknownVal(cty.String)

	changes, unknown, err := diff(diffBlock, cty.NullVal(diffType), cty.ObjectVal(after))
	require.NoError(t, err)
	assert.Equal(t, []string{"url"}, unknown)
	assert.Equal(t, []Change{
		{Path: "permission", After: []any{map[string]any{"role": "Viewer", "key": nil}}},
		{Path: "title", After: "Ops"},
	}, changes)
}
