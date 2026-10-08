package app

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ringsContract = `openapi: 3.0.3
info: {title: Orders, version: "1"}
x-infrapi-writes: everybody
paths:
  /orders/v1/tenants/{tenant}/orders:
    post: {operationId: placeOrder, responses: {"201": {description: placed}}}
    get: {operationId: listOrders, responses: {"200": {description: listed}}}
  /orders/v1/tenants/{tenant}/orders/{id}:
    delete: {operationId: cancelOrder, x-infrapi-ring: purger, responses: {"204": {description: cancelled}}}
    put: {operationId: replaceOrder, x-infrapi-ring: local, responses: {"200": {description: replaced}}}
`

var ringsSpec = Spec{Name: "orders-api", Roles: []cluster.RoleSpec{
	{Name: "everybody", Mode: cluster.ModeBroadcast},
	{Name: "purger", Mode: cluster.ModeLeader},
}}

const ringsCluster = `
cluster:
  enabled: true
  kv_store: inmemory
  instance_addr: 127.0.0.1
`

// dispatch builds the dispatcher of contract under the configuration extra.
func dispatch(t *testing.T, contract, extra string) (*dispatcher, error) {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData([]byte(contract))
	require.NoError(t, err)
	cfg, err := loadConfig(ringsSpec, writeFile(t, required+ringsCluster+extra))
	require.NoError(t, err)
	opts := clusterOptions(cfg)
	opts.Registerer = prometheus.NewRegistry() // apart from the other tests' clusters
	cl, err := cluster.New(cfg.app, cfg.cluster, opts)
	require.NoError(t, err)
	return newDispatcher(cfg, cl, []*openapi3.T{doc})
}

// The contract gives each write its ring: the operation's own, else the
// contract's, and the ring's mode says how it runs.
func TestTheContractGivesWritesTheirRing(t *testing.T) {
	d, err := dispatch(t, ringsContract, "")
	require.NoError(t, err)
	assert.Equal(t, map[string]writeMode{
		"POST /orders/v1/tenants/:tenant/orders":       {kind: writeBroadcast, role: "everybody", policy: BroadcastStrict},
		"DELETE /orders/v1/tenants/:tenant/orders/:id": {kind: writeLeader, role: "purger"},
		"PUT /orders/v1/tenants/:tenant/orders/:id":    {kind: writeLocal},
	}, d.byRoute, "reads are not there: they run where they arrive")
	assert.Contains(t, d.fanouts, "everybody")
	assert.Contains(t, d.leaders, "purger")
	assert.Empty(t, d.reads, "a read without a ring runs where it arrives")

	// A read given to a leader's ring is relayed to it.
	d, err = dispatch(t, strings.Replace(ringsContract, "get: {operationId: listOrders,",
		"get: {operationId: listOrders, x-infrapi-ring: purger,", 1), "")
	require.NoError(t, err)
	assert.Equal(t, map[string]writeMode{
		"GET /orders/v1/tenants/:tenant/orders": {kind: writeLeader, role: "purger"},
	}, d.reads)
	assert.Contains(t, d.readLeaders, "purger")
}

// Without marks, writes are local, or on the writer's leader with
// cluster.leader_writes.
func TestUnmarkedWrites(t *testing.T) {
	const plain = `openapi: 3.0.3
info: {title: Orders, version: "1"}
paths:
  /orders/v1/tenants/{tenant}/orders:
    post: {operationId: placeOrder, responses: {"201": {description: placed}}}
`
	d, err := dispatch(t, plain, "")
	require.NoError(t, err)
	assert.Equal(t, writeMode{kind: writeLocal}, d.byRoute["POST /orders/v1/tenants/:tenant/orders"])

	d, err = dispatch(t, plain, "  leader_writes: true\n")
	require.NoError(t, err)
	assert.Equal(t, writeMode{kind: writeLeader, role: writerRole}, d.byRoute["POST /orders/v1/tenants/:tenant/orders"])
}

func TestRingMistakesStopTheService(t *testing.T) {
	for name, c := range map[string]struct{ contract, extra, want string }{
		"a ring the service has not": {
			contract: `x-infrapi-ring: compactor`, want: `"compactor" is not one of Spec.Roles`},
		"a ring this replica does not join": {
			contract: `x-infrapi-ring: purger`, extra: "  roles: [everybody]\n", want: "not a role this replica joins"},
		"a broadcast ring on a read": {
			contract: `x-infrapi-ring: everybody`, want: "a read has one answer"},
		"a contract default with leader_writes": {
			extra: "  leader_writes: true\n", want: "keep one"},
		"not a name": {
			contract: `x-infrapi-ring: 42`, want: "is not a ring's name"},
	} {
		doc := ringsContract
		if c.contract != "" {
			op := "delete: {operationId: cancelOrder, " + c.contract + ","
			if name == "a broadcast ring on a read" {
				doc = strings.Replace(doc, "get: {operationId: listOrders,", "get: {operationId: listOrders, "+c.contract+",", 1)
			} else {
				doc = strings.Replace(doc, "delete: {operationId: cancelOrder, x-infrapi-ring: purger,", op, 1)
			}
		}
		_, err := dispatch(t, doc, c.extra)
		assert.ErrorContains(t, err, c.want, name)
	}
}
