package app

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/infrapi/lib/pkg/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A contract says only what is its own: the error format and the API key are
// added, and the result is a valid contract.
func TestCompleteAddsWhatEveryContractShares(t *testing.T) {
	out, err := complete([]byte(`openapi: 3.0.3
info: {title: Orders, version: "1"}
paths:
  /orders/v1/orders:
    get:
      operationId: listOrders
      responses: {"200": {description: listed}}
`))
	require.NoError(t, err)
	doc, err := openapi3.NewLoader().LoadFromData(out)
	require.NoError(t, err)
	require.NoError(t, doc.Validate(openapi3.NewLoader().Context))

	assert.NotNil(t, doc.Components.Schemas["Problem"])
	assert.NotNil(t, doc.Components.Responses["Problem"])
	assert.NotNil(t, doc.Components.SecuritySchemes["apikey"])
	assert.Equal(t, openapi3.SecurityRequirements{{"apikey": []string{}}}, doc.Security)
	assert.NotNil(t, doc.Paths.Value("/orders/v1/orders").Get.Responses.Default())
}

// What the contract has already is left as it is.
func TestCompleteKeepsTheContractsOwn(t *testing.T) {
	out, err := complete([]byte(`openapi: 3.0.3
info: {title: Orders, version: "1"}
security: []
paths:
  /orders/v1/orders:
    get:
      responses:
        "200": {description: listed}
        default: {description: mine}
components:
  schemas:
    Problem: {type: string}
`))
	require.NoError(t, err)
	doc, err := openapi3.NewLoader().LoadFromData(out)
	require.NoError(t, err)
	assert.Equal(t, "string", doc.Components.Schemas["Problem"].Value.Type.Slice()[0], "the contract's own Problem")
	assert.Equal(t, "mine", *doc.Paths.Value("/orders/v1/orders").Get.Responses.Default().Value.Description)
	assert.Nil(t, doc.Components.SecuritySchemes["apikey"], "a contract that says its security keeps it")
	assert.Empty(t, doc.Security)
}

// A contract written from a resource's short description is a valid one once
// completed, with its writes given to their rings.
func TestGeneratedContractsServe(t *testing.T) {
	r, err := contract.Parse([]byte(`
name: bookmarks
version: 1
path: tenants/{tenant}
resource: bookmark
fields:
  url: {type: string, required: true}
  created_at: {type: datetime, computed: true}
filters:
  tag: {type: string}
operations: [list, create, get, replace, delete]
cluster:
  writes: everybody
  rings: {delete: cache}
`))
	require.NoError(t, err)
	generated, err := contract.Generate(r)
	require.NoError(t, err)
	out, err := complete(generated)
	require.NoError(t, err)
	doc, err := openapi3.NewLoader().LoadFromData(out)
	require.NoError(t, err)
	require.NoError(t, doc.Validate(openapi3.NewLoader().Context))
	assert.Equal(t, "/bookmarks/v1/tenants/", commonPrefix(doc), "the gateway routes on it")
	assert.Equal(t, "cache", doc.Paths.Value("/bookmarks/v1/tenants/{tenant}/bookmarks/{id}").Delete.Extensions[RingExtension])
}

// With the library keeping the ids, every marked id and reference is a UUID
// in the contract served.
func TestCompleteMarksReferencesAsUUIDs(t *testing.T) {
	out, err := complete(telemetryLike)
	require.NoError(t, err)
	doc := loadDoc(t, out)
	folder := doc.Components.Schemas["Folder"].Value
	assert.Equal(t, "uuid", folder.Properties["id"].Value.Format)
	assert.Equal(t, "uuid", folder.Properties["parent_id"].Value.Format)
	assert.Equal(t, "uuid", folder.Properties["dashboard_ids"].Value.Items.Value.Format)
	assert.Equal(t, "uuid", doc.Components.Schemas["Grant"].Value.Properties["team_id"].Value.Format)
	q := doc.Paths.Value("/t/v1/{tenant}/folders").Get.Parameters[0].Value
	assert.Equal(t, "uuid", q.Schema.Value.Format)
}
