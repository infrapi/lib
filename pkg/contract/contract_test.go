package contract

import (
	"slices"
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
cluster:
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
		"a ring on no operation": {strings.Replace(bookmarks, "delete: cache", "patch: cache", 1), `cluster.rings: "patch"`},
		"an unknown cluster key": {strings.Replace(bookmarks, "  writes: everybody", "  wirtes: everybody", 1), `cluster: "wirtes"`},
		"filters without list":   {strings.Replace(bookmarks, "[list, ", "[", 1), "no list to filter"},
		"a param not in path":    {strings.Replace(bookmarks, "  tenant: {", "  team: {", 1), "params.team"},
	} {
		_, err := Parse([]byte(c.src))
		assert.ErrorContains(t, err, c.want, name)
	}
}

// Each field says who sets it and when, as a Terraform schema does.
func TestFieldBehaviours(t *testing.T) {
	doc := generate(t, `
name: users
version: 1
resource: user
fields:
  login:    {type: string, required: true, immutable: true}
  name:     {type: string}
  password: {type: string, required: true, sensitive: true}
  role:     {type: string, optional: true, computed: true}
  created:  {type: datetime, computed: true}
operations: [create, get, replace]
`)
	props := func(schema string) map[string]any {
		return get(t, doc, "components", "schemas", schema, "properties").(map[string]any)
	}
	keys := func(m map[string]any) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	assert.Equal(t, []string{"login", "name", "password", "role"}, keys(props("UserSpec")),
		"created is the service's alone; role the caller's or the service's")
	assert.Equal(t, []string{"name", "password", "role"}, keys(props("UserUpdate")), "login is set on creation only")
	assert.Equal(t, []string{"created", "id", "login", "name", "role"}, keys(props("User")), "password is never answered")
	assert.Equal(t, "#/components/schemas/UserSpec",
		get(t, doc, "paths", "/users/v1/users", "post", "requestBody", "content", "application/json", "schema", "$ref"))
	assert.Equal(t, "#/components/schemas/UserUpdate",
		get(t, doc, "paths", "/users/v1/users/{id}", "put", "requestBody", "content", "application/json", "schema", "$ref"))

	// Without an immutable field, creating and replacing send the same.
	plain := generate(t, bookmarks)
	assert.NotContains(t, get(t, plain, "components", "schemas"), "BookmarkUpdate")
}

func TestFieldBehavioursThatContradict(t *testing.T) {
	for opts, want := range map[string]string{
		"required: true, optional: true":  "required and optional",
		"required: true, computed: true":  "required and computed",
		"sensitive: true, computed: true": "sensitive and computed",
		"required: true, default: x":      "required with a default",
		"immutable: true, computed: true": "immutable and computed",
	} {
		_, err := Parse([]byte("name: users\nversion: 1\nresource: user\noperations: [get]\nfields:\n  f: {type: string, " + opts + "}\n"))
		assert.ErrorContains(t, err, want, opts)
	}
}

// With id: infrapi, the library keeps the ids: the contract says so, and its
// file keeps the backend's ids as plain strings for the generated code.
func TestLibraryKeptIDs(t *testing.T) {
	doc := generate(t, strings.Replace(bookmarks, "resource: bookmark", "resource: bookmark\nid: infrapi", 1))
	assert.Equal(t, "bookmarks", doc["x-infrapi-ids"])
	assert.Nil(t, get(t, doc, "components", "schemas", "Bookmark", "properties", "id", "format"))
	assert.NotContains(t, get(t, doc, "components", "schemas", "BookmarkSpec", "properties"), "id")
}

// A service of several resources: shared path and errors, a type, references
// between resources, a paged and summarised list, an operation's own
// parameter, and its documentation.
const shop = `
name: shop
version: 2
path: tenants/{tenant}
id: infrapi
errors:
  503: The backend could not be reached. Retryable.
problem:
  codes: [not_found, already_exists]
  fields:
    current: {type: object}
types:
  line:
    product_id: {type: string, required: true, ref: product}
    quantity:   {type: integer, required: true, min: 1}
resources:
  - name: products
    resource: product
    description: What is sold.
    fields:
      name:  {type: string, required: true}
      price: {type: number, required: true}
      notes: {type: string, omit_empty: true}
      url:   {type: string, computed: true}
    list: {paging: true, fields: [name, url]}
    operations: [list, create, get, delete]
    query:
      delete:
        force: {type: boolean, default: false}
    docs:
      create:
        summary: Add a product
        description: Its name is unique.
        errors:
          409: Already sold under that name.
  - name: orders
    resource: order
    fields:
      lines:      {type: list, of: line, required: true}
      product_id: {type: string, ref: product}
      labels:     {type: map, of: string}
      extra:      {type: object}
    filters:
      product_id: {type: string, ref: product}
    list: {}
    operations: [list, create, get]
`

func TestGenerateSeveralResources(t *testing.T) {
	doc := generate(t, shop)
	products, product := "/shop/v2/tenants/{tenant}/products", "/shop/v2/tenants/{tenant}/products/{id}"
	orders := "/shop/v2/tenants/{tenant}/orders"

	assert.Equal(t, "shop", doc["x-infrapi-ids"])
	assert.Equal(t, "createProduct", get(t, doc, "paths", products, "post", "operationId"))
	assert.Equal(t, "listOrders", get(t, doc, "paths", orders, "get", "operationId"))
	assert.Equal(t, "Add a product", get(t, doc, "paths", products, "post", "summary"))
	assert.Equal(t, []any{"products"}, get(t, doc, "paths", products, "post", "tags"))

	// Errors: the service's everywhere, written once; 400 where a body or a
	// parameter can be wrong; 404 on an item; an operation's own.
	create := get(t, doc, "paths", products, "post", "responses").(map[string]any)
	assert.Equal(t, "#/components/responses/Problem503", get(t, create, "503", "$ref"))
	assert.Equal(t, "#/components/responses/Problem400", get(t, create, "400", "$ref"))
	assert.Equal(t, "Already sold under that name.", get(t, create, "409", "description"))
	assert.NotContains(t, create, "404")
	assert.Contains(t, get(t, doc, "paths", product, "get", "responses"), "404")

	// The ids: marked as the library's, a reference too, a list's items' too.
	assert.Equal(t, "products", get(t, doc, "components", "schemas", "Product", "properties", "id", "x-infrapi-id"))
	assert.Equal(t, "products", get(t, doc, "components", "schemas", "Line", "properties", "product_id", "x-infrapi-ref"))
	assert.Equal(t, "#/components/schemas/Line", get(t, doc, "components", "schemas", "OrderSpec", "properties", "lines", "items", "$ref"))
	var filter map[string]any
	for _, p := range get(t, doc, "paths", orders, "get", "parameters").([]any) {
		if m, ok := p.(map[string]any); ok && m["name"] == "product_id" {
			filter = m
		}
	}
	require.NotNil(t, filter)
	assert.Equal(t, "products", get(t, filter, "schema", "x-infrapi-ref"))

	// Objects and maps.
	assert.Equal(t, "string", get(t, doc, "components", "schemas", "OrderSpec", "properties", "labels", "additionalProperties", "type"))
	assert.Equal(t, "object", get(t, doc, "components", "schemas", "OrderSpec", "properties", "extra", "type"))

	// The lists: paged and summarised, or the items alone.
	assert.Equal(t, []any{"items", "page", "limit"}, get(t, doc, "components", "schemas", "ProductList", "required"))
	assert.Equal(t, "#/components/schemas/ProductSummary",
		get(t, doc, "components", "schemas", "ProductList", "properties", "items", "items", "$ref"))
	assert.Equal(t, []any{"id", "name", "url"}, get(t, doc, "components", "schemas", "ProductSummary", "required"))
	assert.Equal(t, []any{"items"}, get(t, doc, "components", "schemas", "OrderList", "required"))
	assert.Equal(t, []any{"id", "name", "price", "url"}, get(t, doc, "components", "schemas", "Product", "required"),
		"omit_empty: notes may be absent")

	// An operation's own parameter, and the headers app reads.
	names := func(path, method string) []string {
		var out []string
		for _, p := range get(t, doc, "paths", path, method, "parameters").([]any) {
			m := p.(map[string]any)
			if n, ok := m["name"].(string); ok {
				out = append(out, n)
			} else {
				out = append(out, m["$ref"].(string))
			}
		}
		return out
	}
	assert.Equal(t, []string{"force", "#/components/parameters/CorrelationID", "#/components/parameters/IdempotencyKey"},
		names(product, "delete"))
	assert.NotContains(t, names(products, "get"), "#/components/parameters/IdempotencyKey", "a read is not a write")

	// The problem: the library's fields, and the service's.
	assert.Contains(t, get(t, doc, "components", "schemas", "Problem", "properties"), "current")
	assert.Contains(t, get(t, doc, "components", "schemas", "Problem", "properties", "code", "description"), "already_exists")
}

func TestParseRefusesSeveralResources(t *testing.T) {
	for name, c := range map[string]struct{ src, want string }{
		"a resource key at the top": {strings.Replace(shop, "resources:", "fields: {}\nresources:", 1), `"fields" belongs to a resource`},
		"an unknown resource key":   {strings.Replace(shop, "    description: What is sold.", "    descrption: x", 1), `"descrption" is not a key of a resource`},
		"a ref to nothing":          {strings.Replace(shop, "ref: product}\n    quantity", "ref: produce}\n    quantity", 1), `ref "produce"`},
		"a ref on a number":         {strings.Replace(shop, "price: {type: number, required: true}", "price: {type: number, required: true, ref: product}", 1), "an id is a string"},
		"an unknown type":           {strings.Replace(shop, "of: line", "of: lien", 1), "a list needs of"},
		"a summary of no field":     {strings.Replace(shop, "fields: [name, url]", "fields: [name, colour]", 1), `list.fields: "colour"`},
		"a query on no operation":   {strings.Replace(shop, "      delete:\n        force", "      replace:\n        force", 1), `query: "replace"`},
		"docs on no operation":      {strings.Replace(shop, "      create:\n        summary", "      update:\n        summary", 1), `docs: "update"`},
		"an object filter":          {strings.Replace(shop, "      product_id: {type: string, ref: product}\n    list", "      product_id: {type: object}\n    list", 1), "an object is for a body"},
		"a doc key":                 {strings.Replace(shop, "        summary: Add a product", "        sumary: Add a product", 1), `"sumary"`},
		"rings at the top":          {strings.Replace(shop, "errors:\n  503", "cluster: {rings: {get: cache}}\nerrors:\n  503", 1), `cluster: "rings"`},
	} {
		_, err := Parse([]byte(c.src))
		assert.ErrorContains(t, err, c.want, name)
	}
}
