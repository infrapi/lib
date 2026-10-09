package contract

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const bookmarks = `
name: bookmarks
version: 1
path: tenants/{tenant}
params:
  tenant: {type: string, pattern: "^[a-z0-9-]{1,40}$"}
resource: bookmark
fields:
  url:        {type: string, required: true, max: 2000}
  title:      {type: string, required: true, min: 1, max: 200}
  tags:       {type: list, of: string, max: 10}
  created_at: {type: datetime, computed: true}
filters:
  tag: {type: string}
operations: [list, create, get, replace, delete]
example: {url: "https://grafana.com/docs/", title: Grafana documentation}
writes: everybody
rings: {delete: cache}
`

func generate(t *testing.T, src string) map[string]any {
	t.Helper()
	r, err := Parse([]byte(src))
	require.NoError(t, err)
	out, err := Generate(r)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(out, &doc))
	return doc
}

func get(t *testing.T, v any, path ...string) any {
	t.Helper()
	for _, k := range path {
		m, ok := v.(map[string]any)
		require.True(t, ok, "%v at %s", v, k)
		v = m[k]
	}
	return v
}

func TestGenerate(t *testing.T) {
	doc := generate(t, bookmarks)
	collection, item := "/bookmarks/v1/tenants/{tenant}/bookmarks", "/bookmarks/v1/tenants/{tenant}/bookmarks/{id}"

	for path, ops := range map[string]map[string]string{
		collection: {"get": "listBookmarks", "post": "createBookmark"},
		item:       {"get": "getBookmark", "put": "replaceBookmark", "delete": "deleteBookmark"},
	} {
		for method, id := range ops {
			assert.Equal(t, id, get(t, doc, "paths", path, method, "operationId"))
		}
	}
	assert.Contains(t, get(t, doc, "paths", collection, "post", "responses"), "201", "codes are strings")
	assert.Equal(t, "everybody", doc["x-infrapi-writes"])
	assert.Equal(t, "cache", get(t, doc, "paths", item, "delete", "x-infrapi-ring"))

	spec := get(t, doc, "components", "schemas", "BookmarkSpec").(map[string]any)
	assert.Equal(t, []any{"url", "title"}, spec["required"])
	assert.Equal(t, false, spec["additionalProperties"])
	assert.NotContains(t, spec["properties"], "created_at", "a computed field is not sent")
	assert.Equal(t, 2000, get(t, spec, "properties", "url", "maxLength"))

	answer := get(t, doc, "components", "schemas", "Bookmark").(map[string]any)
	assert.Equal(t, []any{"id", "url", "title", "tags", "created_at"}, answer["required"], "an answer has every field")
	assert.Equal(t, "date-time", get(t, answer, "properties", "created_at", "format"))
}

// The fields keep the order they are written in, and so does the example.
func TestGenerateKeepsTheOrder(t *testing.T) {
	r, err := Parse([]byte(bookmarks))
	require.NoError(t, err)
	out, err := Generate(r)
	require.NoError(t, err)
	s := string(out)
	assert.Less(t, strings.Index(s, "        url:"), strings.Index(s, "        title:"))
	assert.Contains(t, s, `example: {url: "https://grafana.com/docs/", title: Grafana documentation}`)
}

func TestCallerChosenID(t *testing.T) {
	doc := generate(t, strings.Replace(bookmarks, "resource: bookmark", "resource: bookmark\nid: caller", 1))
	assert.Contains(t, get(t, doc, "components", "schemas", "BookmarkSpec", "properties"), "id")
}

func TestParseRefuses(t *testing.T) {
	for name, c := range map[string]struct{ src, want string }{
		"an unknown key":         {"nmae: x\n", `"nmae" is not a key`},
		"an unknown field key":   {strings.Replace(bookmarks, "required: true, max: 2000", "requird: true", 1), `"requird" is not a key of a field`},
		"a field called id":      {strings.Replace(bookmarks, "  url: ", "  id: {type: string}\n  url: ", 1), "fields.id"},
		"a bad type":             {strings.Replace(bookmarks, "type: datetime", "type: timestamp", 1), `type "timestamp"`},
		"a list without of":      {strings.Replace(bookmarks, ", of: string", "", 1), "a list needs of"},
		"an unknown operation":   {strings.Replace(bookmarks, "delete]", "purge]", 1), `"purge" is not one of`},
		"a ring on no operation": {strings.Replace(bookmarks, "delete: cache", "patch: cache", 1), `rings: "patch"`},
		"filters without list":   {strings.Replace(bookmarks, "[list, ", "[", 1), "no list to filter"},
		"a param not in path":    {strings.Replace(bookmarks, "  tenant: {", "  team: {", 1), "params.team"},
	} {
		_, err := Parse([]byte(c.src))
		assert.ErrorContains(t, err, c.want, name)
	}
}
