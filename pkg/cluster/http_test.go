package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/grafana/dskit/ring"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// asJSON is the Accept header that makes the status pages answer with their
// model instead of a rendered page.
var asJSON = map[string]string{"Accept": "application/json"}

// mount wires the status pages onto a bare engine, the way a service wires them
// onto the one the server package builds.
func mount(t *testing.T, c *Cluster, opts HandlerOptions) *gin.Engine {
	t.Helper()

	engine := gin.New()
	require.NoError(t, c.RegisterGin(engine, opts))
	return engine
}

// get performs a request against the engine and returns the recorder.
func get(engine *gin.Engine, method, path string, header map[string]string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	for key, value := range header {
		request.Header.Set(key, value)
	}
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestRegisterGin_rejects_a_nil_engine(t *testing.T) {
	c := newTestCluster(t, nil, uniqueRole(t, "a", ModeLeader))
	require.Error(t, c.RegisterGin(nil, HandlerOptions{}))
}

func TestStatusHandler(t *testing.T) {
	leader := uniqueRole(t, "sched", ModeLeader)
	broadcast := uniqueRole(t, "coll", ModeBroadcast)
	c := startTestCluster(t, nil, leader, broadcast)

	recorder := get(mount(t, c, HandlerOptions{}), http.MethodGet, StatusPath, asJSON)
	require.Equal(t, http.StatusOK, recorder.Code)

	var status indexView
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &status))

	assert.True(t, status.Enabled)
	assert.Equal(t, storeInmemory, status.Store)
	assert.Equal(t, c.InstanceID(), status.InstanceID)
	assert.Equal(t, c.InstanceAddr(), status.InstanceAddr)
	assert.Empty(t, status.Error)

	// The gossip port belongs to the gossip store, and so is absent here.
	assert.Zero(t, status.GossipPort)

	require.Len(t, status.Roles, 2)

	byName := map[string]roleView{}
	for _, role := range status.Roles {
		byName[role.Name] = role
	}

	// The payload is camelCase throughout, like /-/metadata.
	assert.Contains(t, recorder.Body.String(), `"instanceId"`)
	assert.Contains(t, recorder.Body.String(), `"shouldRun"`)
	assert.Contains(t, recorder.Body.String(), `"memberCount"`)
	assert.NotContains(t, recorder.Body.String(), `"ID"`)

	schedStatus := byName[leader.Name]
	assert.Equal(t, "leader", schedStatus.Mode)
	assert.Equal(t, leader.Name, schedStatus.Key)
	assert.Equal(t, "ACTIVE", schedStatus.State)
	assert.True(t, schedStatus.ShouldRun)
	assert.Equal(t, c.InstanceID(), schedStatus.Leader)
	assert.Equal(t, 1, schedStatus.MemberCount)
	assert.Empty(t, schedStatus.Error)

	collStatus := byName[broadcast.Name]
	assert.Equal(t, "broadcast", collStatus.Mode)
	assert.True(t, collStatus.ShouldRun)

	// A broadcast role has no leader, and the page says nothing rather than
	// inventing one.
	assert.Empty(t, collStatus.Leader)
}

// Clustering off must still render, so an operator can tell "off" from "broken".
func TestStatusHandler_disabled(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ENABLED": "false"})
	c, err := New(testAppConfig(), cfg, Options{Logger: testLogger()})
	require.NoError(t, err)

	recorder := get(mount(t, c, HandlerOptions{}), http.MethodGet, StatusPath, asJSON)
	require.Equal(t, http.StatusOK, recorder.Code)

	var status indexView
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &status))

	assert.False(t, status.Enabled)
	assert.Equal(t, ErrDisabled.Error(), status.Error)
	assert.Empty(t, status.Roles)
}

// A role that cannot be read reports its own error rather than failing the
// whole page: during a partition, which roles are readable is the useful part.
func TestStatusHandler_reports_a_role_error(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)
	engine := mount(t, c, HandlerOptions{})

	require.NoError(t, c.Stop(context.Background()))

	recorder := get(engine, http.MethodGet, StatusPath, asJSON)
	require.Equal(t, http.StatusOK, recorder.Code)

	var status indexView
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &status))
	require.Len(t, status.Roles, 1)
	assert.Contains(t, status.Roles[0].Error, "stopped")
}

func TestRingHandler(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)
	engine := mount(t, c, HandlerOptions{})

	path := strings.Replace(RingPath, ":role", spec.Name, 1)

	html := get(engine, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, html.Code)
	assert.Contains(t, html.Body.String(), c.InstanceID())

	fromJSON := get(engine, http.MethodGet, path, asJSON)
	require.Equal(t, http.StatusOK, fromJSON.Code)

	var payload ringView
	require.NoError(t, json.Unmarshal(fromJSON.Body.Bytes(), &payload))
	assert.Equal(t, spec.Name, payload.RoleName)
	assert.Equal(t, "leader", payload.Mode)
	assert.Equal(t, c.InstanceID(), payload.Leader)
	require.Len(t, payload.Members, 1)
	assert.True(t, payload.Members[0].Healthy)
}

func TestRingHandler_unknown_role(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	recorder := get(mount(t, c, HandlerOptions{}), http.MethodGet,
		strings.Replace(RingPath, ":role", "nope", 1), nil)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "role not joined")
}

// The ring page's POST route evicts a replica from the ring, so it is an
// unauthenticated write to cluster state and stays unmounted by default.
func TestRingHandler_forget_is_off_by_default(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)
	path := strings.Replace(RingPath, ":role", spec.Name, 1)

	assert.Equal(t, http.StatusNotFound,
		get(mount(t, c, HandlerOptions{}), http.MethodPost, path, nil).Code)

	withForget := get(mount(t, c, HandlerOptions{ForgetEnabled: true}), http.MethodPost, path, nil)
	assert.NotEqual(t, http.StatusNotFound, withForget.Code)
}

func TestMemberlistHandler_absent_without_the_gossip_store(t *testing.T) {
	c := startTestCluster(t, nil, uniqueRole(t, "a", ModeLeader))

	recorder := get(mount(t, c, HandlerOptions{}), http.MethodGet, MemberlistPath, nil)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "gossip store is not in use")
}

func TestMemberlistHandler_and_gossip_port(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, map[string]string{
		"INFRAPI_CLUSTER_KV_STORE":          storeMemberlist,
		"INFRAPI_CLUSTER_GOSSIP_BIND_ADDRS": `["127.0.0.1"]`,
		"INFRAPI_CLUSTER_GOSSIP_BIND_PORT":  "0",
	}, spec)

	recorder := get(mount(t, c, HandlerOptions{}), http.MethodGet, MemberlistPath, nil)
	require.Equal(t, http.StatusOK, recorder.Code)

	status := get(mount(t, c, HandlerOptions{}), http.MethodGet, StatusPath, asJSON)
	require.Equal(t, http.StatusOK, status.Code)

	var payload indexView
	require.NoError(t, json.Unmarshal(status.Body.Bytes(), &payload))
	assert.Equal(t, storeMemberlist, payload.Store)
	assert.Equal(t, "infrapi", payload.ClusterLabel)
	assert.NotZero(t, payload.GossipPort, "an ephemeral bind port must be reported on the status page")
}

// The pages mount before Start, so a service can wire its routes once at
// startup and have them explain themselves until the cluster is up.
func TestHandlers_before_start(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := newTestCluster(t, nil, spec)
	engine := mount(t, c, HandlerOptions{})

	status := get(engine, http.MethodGet, StatusPath, asJSON)
	require.Equal(t, http.StatusOK, status.Code)

	var payload indexView
	require.NoError(t, json.Unmarshal(status.Body.Bytes(), &payload))
	require.Len(t, payload.Roles, 1)
	assert.Contains(t, payload.Roles[0].Error, "not started")

	ring := get(engine, http.MethodGet, strings.Replace(RingPath, ":role", spec.Name, 1), nil)
	assert.Equal(t, http.StatusNotFound, ring.Code)
	assert.Contains(t, ring.Body.String(), "not started")

	memberlist := get(engine, http.MethodGet, MemberlistPath, nil)
	assert.Equal(t, http.StatusNotFound, memberlist.Code)
}

// A role whose ring is missing must 404 rather than panic. Reachable only by
// reaching past the constructor, which is the point: the handler fails closed.
func TestRingHandler_without_a_ring(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": `["` + spec.Name + `"]`})

	c, err := New(testAppConfig(), cfg, Options{
		Roles:      []RoleSpec{spec},
		Logger:     testLogger(),
		Registerer: prometheus.NewRegistry(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	require.NoError(t, c.Start(context.Background()))

	c.roles[spec.Name].ring = nil

	recorder := get(mount(t, c, HandlerOptions{}), http.MethodGet,
		strings.Replace(RingPath, ":role", spec.Name, 1), nil)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "not started")
}

// A role's own ring page goes wherever a service wants it, which is how another
// service's layout is reproduced exactly.
func TestRole_RingHandler_at_an_arbitrary_path(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	engine := gin.New()
	engine.GET("/ring", role.RingHandler())
	engine.GET("/ring/lifecycler", role.LifecyclerHandler())
	engine.GET("/memberlist", c.MemberlistHandler())

	html := get(engine, http.MethodGet, "/ring", nil)
	require.Equal(t, http.StatusOK, html.Code)
	assert.Contains(t, html.Body.String(), c.InstanceID())

	fromJSON := get(engine, http.MethodGet, "/ring", asJSON)
	require.Equal(t, http.StatusOK, fromJSON.Code)
	assert.Contains(t, fromJSON.Body.String(), `"role"`)

	// The lifecycler page is dskit's own, so it still answers in dskit's shape.
	fromLifecycler := get(engine, http.MethodGet, "/ring/lifecycler", nil)
	require.Equal(t, http.StatusOK, fromLifecycler.Code)
	assert.Contains(t, fromLifecycler.Body.String(), c.InstanceID())
	assert.Contains(t, get(engine, http.MethodGet, "/ring/lifecycler", asJSON).Body.String(), "shards")

	// The gossip page is still store-dependent wherever it is mounted.
	assert.Equal(t, http.StatusNotFound, get(engine, http.MethodGet, "/memberlist", nil).Code)
}

// Both role handlers fail closed once the cluster is gone.
func TestRole_handlers_after_stop(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	engine := gin.New()
	engine.GET("/ring", role.RingHandler())
	engine.GET("/ring/lifecycler", role.LifecyclerHandler())

	require.NoError(t, c.Stop(context.Background()))

	for _, path := range []string{"/ring", "/ring/lifecycler"} {
		recorder := get(engine, http.MethodGet, path, nil)
		assert.Equal(t, http.StatusNotFound, recorder.Code, path)
		assert.Contains(t, recorder.Body.String(), "stopped", path)
	}
}

// Before Start there is no ring to render, and the handlers say so rather than
// panicking on a nil service.
func TestRole_handlers_before_start(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": `["` + spec.Name + `"]`})

	c, err := New(testAppConfig(), cfg, Options{
		Roles:      []RoleSpec{spec},
		Logger:     testLogger(),
		Registerer: prometheus.NewRegistry(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	// Reach past Cluster.Role, which refuses before Start, to the role itself.
	role := c.roles[spec.Name]

	engine := gin.New()
	engine.GET("/ring", role.RingHandler())
	engine.GET("/ring/lifecycler", role.LifecyclerHandler())

	for _, path := range []string{"/ring", "/ring/lifecycler"} {
		recorder := get(engine, http.MethodGet, path, nil)
		assert.Equal(t, http.StatusNotFound, recorder.Code, path)
		assert.Contains(t, recorder.Body.String(), "not started", path)
	}
}

// The handlers also fail closed on a role with no ring behind it. Reaching past
// the constructor is the only way there, which is the point: a 404 beats a nil
// dereference inside a request.
func TestRole_handlers_without_a_ring(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	role.ring = nil
	role.lifecycler = nil

	engine := gin.New()
	engine.GET("/ring", role.RingHandler())
	engine.GET("/ring/lifecycler", role.LifecyclerHandler())

	for _, path := range []string{"/ring", "/ring/lifecycler"} {
		recorder := get(engine, http.MethodGet, path, nil)
		assert.Equal(t, http.StatusNotFound, recorder.Code, path)
		assert.Contains(t, recorder.Body.String(), "not started", path)
	}
}

func TestRegisterGin_path_prefix(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	engine := mount(t, c, HandlerOptions{PathPrefix: "/debug/cluster"})

	assert.Equal(t, http.StatusOK, get(engine, http.MethodGet, "/debug/cluster", asJSON).Code)
	assert.Equal(t, http.StatusOK,
		get(engine, http.MethodGet, "/debug/cluster/ring/"+spec.Name, nil).Code)
	assert.Equal(t, http.StatusNotFound,
		get(engine, http.MethodGet, "/debug/cluster/memberlist", nil).Code)

	// The default set is no longer mounted.
	assert.Equal(t, http.StatusNotFound, get(engine, http.MethodGet, StatusPath, nil).Code)
}

func TestRegisterGin_rejects_a_bad_prefix(t *testing.T) {
	c := newTestCluster(t, nil, uniqueRole(t, "a", ModeLeader))

	for _, prefix := range []string{"cluster", "/cluster/"} {
		err := c.RegisterGin(gin.New(), HandlerOptions{PathPrefix: prefix})
		require.Error(t, err, prefix)
		assert.Contains(t, err.Error(), "must start with a slash")
	}
}

func TestStatusHandler_renders_a_page(t *testing.T) {
	leader := uniqueRole(t, "sched", ModeLeader)
	broadcast := uniqueRole(t, "coll", ModeBroadcast)
	c := startTestCluster(t, nil, leader, broadcast)

	recorder := get(mount(t, c, HandlerOptions{}), http.MethodGet, StatusPath, nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Content-Type"), "text/html")

	body := recorder.Body.String()

	assert.Contains(t, body, "<!DOCTYPE html>")
	assert.Contains(t, body, "<h1>Cluster</h1>")

	// The stylesheet is inlined, so a relocated page cannot lose its styling.
	assert.Contains(t, body, "--accent:")
	assert.NotContains(t, body, "<link rel=\"stylesheet\"")

	assert.Contains(t, body, c.InstanceID())
	assert.Contains(t, body, storeInmemory)

	// Each role is a card, linking to its own ring page, with its verdict.
	for _, spec := range []RoleSpec{leader, broadcast} {
		assert.Contains(t, body, spec.Name)
		assert.Contains(t, body, StatusPath+"/ring/"+spec.Name)
	}
	assert.Contains(t, body, "acting")
	assert.Contains(t, body, "badge-mode")

	// The gossip section belongs to the gossip store only.
	assert.NotContains(t, body, StatusPath+"/memberlist")
	assert.NotContains(t, body, "ul class=\"links\"")
}

func TestStatusHandler_renders_a_disabled_cluster(t *testing.T) {
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ENABLED": "false"})
	c, err := New(testAppConfig(), cfg, Options{Logger: testLogger()})
	require.NoError(t, err)

	body := get(mount(t, c, HandlerOptions{}), http.MethodGet, StatusPath, nil).Body.String()
	assert.Contains(t, body, "INFRAPI_CLUSTER_ENABLED")
	assert.NotContains(t, body, ">acting<", "a disabled cluster has no verdict to show")
	assert.NotContains(t, body, "<table", "and no members")
}

func TestRingHandler_renders_a_page(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)
	engine := mount(t, c, HandlerOptions{})

	body := get(engine, http.MethodGet, StatusPath+"/ring/"+spec.Name, nil).Body.String()

	assert.Contains(t, body, "<h1>"+spec.Name+"</h1>")
	assert.Contains(t, body, "--accent:")

	// A way back to the index, which only this package knows the path of.
	assert.Contains(t, body, `href="`+StatusPath+`"`)

	// The member table, with this replica marked as itself and eligible.
	assert.Contains(t, body, ">Instance<")
	assert.Contains(t, body, ">Last heartbeat<")
	assert.Contains(t, body, c.InstanceID())
	assert.Contains(t, body, "badge-active")
	assert.Contains(t, body, "eligible")
	assert.Contains(t, body, `class="self"`)
	assert.Contains(t, body, "ago")

	// No forget form unless a deployment asked for one. Asserted on the markup:
	// the inlined stylesheet mentions the form's own class.
	assert.NotContains(t, body, `method="post"`)
	assert.NotContains(t, body, `name="forget"`)
}

func TestRingHandler_renders_the_forget_form(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	body := get(mount(t, c, HandlerOptions{ForgetEnabled: true}),
		http.MethodGet, StatusPath+"/ring/"+spec.Name, nil).Body.String()

	assert.Contains(t, body, `method="post"`)
	assert.Contains(t, body, `name="forget"`)
}

// A page mounted by hand has no way to know where its siblings are, so it shows
// no link rather than a broken one.
func TestRole_RingHandler_omits_the_crumb_when_unmounted(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	cfg := testClusterConfig(t, map[string]string{"INFRAPI_CLUSTER_ROLES": `["` + spec.Name + `"]`})

	c, err := New(testAppConfig(), cfg, Options{
		Roles:      []RoleSpec{spec},
		Logger:     testLogger(),
		Registerer: prometheus.NewRegistry(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	require.NoError(t, c.Start(context.Background()))

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	engine := gin.New()
	engine.GET("/ring", role.RingHandler())

	body := get(engine, http.MethodGet, "/ring", nil).Body.String()
	assert.Contains(t, body, "<h1>"+spec.Name+"</h1>")
	assert.NotContains(t, body, "&larr; cluster")
}

// The broadcast ring page has no leader to report, and says nothing rather than
// nominating one.
func TestRingHandler_broadcast_has_no_leader_row(t *testing.T) {
	spec := uniqueRole(t, "coll", ModeBroadcast)
	c := startTestCluster(t, nil, spec)

	body := get(mount(t, c, HandlerOptions{}), http.MethodGet, StatusPath+"/ring/"+spec.Name, nil).Body.String()
	assert.Contains(t, body, "broadcast")
	assert.NotContains(t, body, ">Leader<")
}

// The page's reason for existing: it shows the replica the mode excludes. That
// is the row someone opened the page to look at.
func TestRingHandler_renders_an_excluded_replica(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)
	engine := mount(t, c, HandlerOptions{})
	path := StatusPath + "/ring/" + spec.Name

	require.Contains(t, get(engine, http.MethodGet, path, nil).Body.String(), ">eligible<")

	// JOINING is outside the states a leader role accepts.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, c.roles[spec.Name].Lifecycler().ChangeState(ctx, ring.JOINING))

	require.Eventually(t, func() bool {
		body := get(engine, http.MethodGet, path, nil).Body.String()
		return strings.Contains(body, ">excluded<") &&
			strings.Contains(body, ">JOINING<") &&
			strings.Contains(body, `class="self"`)
	}, 10*time.Second, 50*time.Millisecond, "the excluded replica must still be rendered, marked excluded")

	// And the JSON view says the same thing.
	var payload ringView
	require.NoError(t, json.Unmarshal(get(engine, http.MethodGet, path, asJSON).Body.Bytes(), &payload))
	require.Len(t, payload.Members, 1)
	assert.False(t, payload.Members[0].Healthy)
	assert.Equal(t, "JOINING", payload.Members[0].State)
}

// Behind a load balancer the first question is which replica answered, and the
// second is how to reach the leader. The index page answers both.
func TestStatusHandler_identifies_the_replica_and_links_the_leader(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)

	// Two replicas on the shared in-memory store, so one of them is looking at
	// a leader that is not itself.
	first := startTestCluster(t, nil, spec)
	second := startTestCluster(t, nil, spec)

	require.Eventually(t, func() bool {
		a, errA := first.indexView(), error(nil)
		_ = errA
		return len(a.Roles) == 1 && a.Roles[0].MemberCount == 2
	}, 10*time.Second, 50*time.Millisecond)

	// Whichever of the two is not the leader must offer a link to the other.
	var follower, leader *Cluster
	view := first.indexView()
	if view.Roles[0].LeaderIsSelf {
		follower, leader = second, first
	} else {
		follower, leader = first, second
	}

	body := get(mount(t, follower, HandlerOptions{}), http.MethodGet, StatusPath, nil).Body.String()

	// Which replica answered: the first fact a reader behind a load balancer
	// needs before anything else on the page means much.
	assert.Contains(t, body, ">This replica<")
	assert.Contains(t, body, follower.InstanceID())

	// The address without its port. The port lives on the ring page, where each
	// member's full address is listed.
	assert.Contains(t, body, ">Address<")
	assert.NotContains(t, body, ">Host<")
	assert.NotContains(t, body, ">Ring address<")
	assert.Equal(t, "127.0.0.1", follower.indexView().InstanceHost)

	// Still in the JSON, where a script behind a load balancer can read it.
	assert.NotEmpty(t, follower.indexView().Fqdn)

	followerView := follower.indexView()
	require.Equal(t, leader.InstanceID(), followerView.Roles[0].Leader)
	assert.False(t, followerView.Roles[0].LeaderIsSelf)

	// The link is the leader's advertised address plus this mount path, under
	// the scheme the service publishes itself on.
	assert.Equal(t, leader.InstanceAddr(), followerView.Roles[0].LeaderAddr)
	assert.Equal(t, "http://"+leader.InstanceAddr()+StatusPath, followerView.Roles[0].LeaderURL)
	assert.Contains(t, body, `href="`+followerView.Roles[0].LeaderURL+`"`)

	// On the leader's own page there is nothing to link to.
	leaderBody := get(mount(t, leader, HandlerOptions{}), http.MethodGet, StatusPath, nil).Body.String()
	assert.Contains(t, leaderBody, "this replica")
	assert.NotContains(t, leaderBody, "http://"+follower.InstanceAddr())
}

// The scheme comes from the URL the service publishes itself under, because a
// service behind TLS has TLS-enabled peers.
func TestUrlScheme(t *testing.T) {
	assert.Equal(t, "https", urlScheme("https://svc.example.com:8443"))
	assert.Equal(t, "http", urlScheme("http://svc.example.com"))
	assert.Equal(t, "http", urlScheme(""), "an unset URL falls back rather than producing a broken link")
	assert.Equal(t, "http", urlScheme("not a url"))
}
