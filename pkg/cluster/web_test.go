package cluster

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgo(t *testing.T) {
	for name, tc := range map[string]struct {
		at   time.Time
		want string
	}{
		"zero time is unknown":      {time.Time{}, "unknown"},
		"the unix epoch is unknown": {time.Unix(0, 0), "unknown"},
		"the future is now":         {time.Now().Add(time.Hour), "just now"},
		"seconds":                   {time.Now().Add(-5 * time.Second), "5s ago"},
		"minutes":                   {time.Now().Add(-(2*time.Minute + 14*time.Second)), "2m14s ago"},
		"hours":                     {time.Now().Add(-(3*time.Hour + 7*time.Minute)), "3h7m ago"},
		"days":                      {time.Now().Add(-50 * time.Hour), "2d ago"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, ago(tc.at))
		})
	}
}

// The stylesheet is embedded, so a missing or empty file is a broken build
// rather than a page that renders unstyled in production.
func TestStylesheet_is_embedded(t *testing.T) {
	assert.Contains(t, stylesheet, "--accent")
	assert.Greater(t, len(stylesheet), 1000)
}

var cssComments = regexp.MustCompile(`(?s)/\*.*?\*/`)
var cssSpace = regexp.MustCompile(`\s+`)

func normalise(css string) string {
	return strings.TrimSpace(cssSpace.ReplaceAllString(cssComments.ReplaceAllString(css, " "), " "))
}

func TestStylesheet_reaches_the_page_intact(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	body := get(mount(t, c, HandlerOptions{}), "GET", StatusPath, nil).Body.String()

	block := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(body)
	require.Len(t, block, 2)

	assert.Equal(t, normalise(stylesheet), normalise(block[1]),
		"every declaration must survive; only comments may be dropped")
}

// The gossip page is dskit's data in our design: the same sections dskit shows,
// rendered with this package's own stylesheet rather than Bootstrap or nothing.
func TestMemberlistPage_renders_in_our_design(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, map[string]string{
		"INFRAPI_CLUSTER_KV_STORE":                     storeMemberlist,
		"INFRAPI_CLUSTER_GOSSIP_BIND_ADDRS":            `["127.0.0.1"]`,
		"INFRAPI_CLUSTER_GOSSIP_BIND_PORT":             "0",
		"INFRAPI_CLUSTER_MESSAGE_HISTORY_BUFFER_BYTES": "1048576",
	}, spec)

	page := get(mount(t, c, HandlerOptions{}), http.MethodGet, MemberlistPath, nil)
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()

	// Our design, not dskit's bare HTML and not Bootstrap.
	assert.Contains(t, body, "--accent", "the stylesheet is inlined, as on the other pages")
	assert.NotContains(t, body, "cdn.jsdelivr.net")
	assert.NotContains(t, body, "text-bg-")
	assert.NotContains(t, body, `border="1"`, "dskit's table attribute is gone with its template")

	// Every section dskit's own page shows.
	assert.Contains(t, body, "<h1>Gossip store</h1>")
	assert.Contains(t, body, ">Key-Value Store<")
	assert.Contains(t, body, ">Cluster Members<")
	assert.Contains(t, body, ">Message History<")
	assert.Contains(t, body, ">Received<")
	assert.Contains(t, body, ">Sent<")

	// The store table carries five columns and no more. dskit only timestamps an
	// entry when it is deleted, so an "updated" column would read as the zero
	// time on every live row; the deleted flag itself is rare enough that it
	// lives in the struct view rather than in a column of its own.
	assert.NotContains(t, body, ">Updated<")
	assert.NotContains(t, body, ">Tombstoned<")
	assert.NotContains(t, body, ">Deleted<")
	assert.NotContains(t, body, "0001-01-01")

	// The data itself: the ring key this replica registered under, and its
	// gossip node name with a state badge.
	assert.Contains(t, body, "collectors/"+spec.Name)
	assert.Contains(t, body, c.InstanceID())
	assert.Contains(t, body, ">alive<")
	assert.Contains(t, body, "badge-active")

	// dskit's own actions survive, because the handler is still dskit's.
	assert.Contains(t, body, "?viewKey=")
	assert.Contains(t, body, "?downloadKey=")
	assert.Contains(t, body, "?viewMsg=")

	// The health score on its own means nothing to a reader, so it is spelled
	// out as a sentence, the way Grafana's own services word it.
	assert.Contains(t, body, "member(s) and it is")
	assert.Contains(t, body, "healthy")
	assert.Contains(t, body, "notice-ok")

	// Zone-aware routing is never enabled by this package, so a row saying
	// "disabled" for ever is noise: the page mentions it only when it is on.
	assert.NotContains(t, body, "Zone-aware routing")

	// A way back to the index, as an absolute path. A relative one cannot work:
	// the page sits one level below the index, so "../" resolves above it.
	assert.Contains(t, body, `href="`+StatusPath+`"`)
	assert.NotContains(t, body, `href=".."`)
}

// With the history buffer at its default of zero there is nothing to show, and
// the page says which key turns it on rather than rendering empty tables.
func TestMemberlistPage_without_message_history(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, map[string]string{
		"INFRAPI_CLUSTER_KV_STORE":          storeMemberlist,
		"INFRAPI_CLUSTER_GOSSIP_BIND_ADDRS": `["127.0.0.1"]`,
		"INFRAPI_CLUSTER_GOSSIP_BIND_PORT":  "0",
	}, spec)

	body := get(mount(t, c, HandlerOptions{}), http.MethodGet, MemberlistPath, nil).Body.String()

	assert.Contains(t, body, "Message history buffer is disabled.")
	assert.Contains(t, body, "INFRAPI_CLUSTER_MESSAGE_HISTORY_BUFFER_BYTES")

	// The dskit flag is named for recognition, not as an instruction: a service
	// built on this library has no command line to put it on.
	assert.Contains(t, body, "-memberlist.message-history-buffer-bytes")

	assert.NotContains(t, body, ">Received<")
}

// A handler mounted by hand has no prefix to link to, so the page shows no link
// back rather than one that goes somewhere wrong.
func TestMemberlistPage_omits_the_crumb_when_unmounted(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, map[string]string{
		"INFRAPI_CLUSTER_KV_STORE":          storeMemberlist,
		"INFRAPI_CLUSTER_GOSSIP_BIND_ADDRS": `["127.0.0.1"]`,
		"INFRAPI_CLUSTER_GOSSIP_BIND_PORT":  "0",
	}, spec)

	engine := gin.New()
	engine.GET("/memberlist", c.MemberlistHandler())

	body := get(engine, http.MethodGet, "/memberlist", nil).Body.String()
	assert.Contains(t, body, "<h1>Gossip store</h1>")
	assert.NotContains(t, body, "&larr; cluster")
}

// Replacing the template does not replace the handler, so everything dskit
// answers with that is not a page still works.
func TestMemberlistPage_non_page_responses(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, map[string]string{
		"INFRAPI_CLUSTER_KV_STORE":          storeMemberlist,
		"INFRAPI_CLUSTER_GOSSIP_BIND_ADDRS": `["127.0.0.1"]`,
		"INFRAPI_CLUSTER_GOSSIP_BIND_PORT":  "0",
	}, spec)

	engine := mount(t, c, HandlerOptions{})

	page := get(engine, http.MethodGet, MemberlistPath, nil).Body.String()
	match := regexp.MustCompile(`\?viewKey=([^&"]+)`).FindStringSubmatch(page)
	require.Len(t, match, 2, "the page should link to at least one stored key")
	key := match[1]

	view := get(engine, http.MethodGet, MemberlistPath+"?viewKey="+key+"&format=json-pretty", nil)
	require.Equal(t, http.StatusOK, view.Code)
	assert.NotContains(t, view.Header().Get("Content-Type"), "text/html")
	assert.NotContains(t, view.Body.String(), "--accent", "a non-page response carries no stylesheet")

	download := get(engine, http.MethodGet, MemberlistPath+"?downloadKey="+key, nil)
	require.Equal(t, http.StatusOK, download.Code)
	assert.NotContains(t, download.Body.String(), "--accent")
}

// The lifecycler page is the one page still rendered by dskit, so it is the one
// the stylesheet is injected into.
func TestDskitPage_injects_the_stylesheet(t *testing.T) {
	spec := uniqueRole(t, "sched", ModeLeader)
	c := startTestCluster(t, nil, spec)

	role, err := c.Role(spec.Name)
	require.NoError(t, err)

	engine := gin.New()
	engine.GET("/ring/lifecycler", role.LifecyclerHandler())

	page := get(engine, http.MethodGet, "/ring/lifecycler", nil)
	require.Equal(t, http.StatusOK, page.Code)

	body := page.Body.String()
	assert.Contains(t, body, "<h1>Ring Status</h1>", "it is still dskit's page")
	assert.Contains(t, body, "--accent", "with our stylesheet in it")
	assert.Less(t, strings.Index(body, "<style>"), strings.Index(body, "</head>"),
		"the style element belongs in the head")

	// The length changed, so a stale Content-Length would truncate the page.
	assert.Empty(t, page.Header().Get("Content-Length"))
}

// memberlist reports a node's state as a number. dskit's own page prints it
// with a legend; this one names it, so the mapping is worth pinning.
func TestNodeState(t *testing.T) {
	for state, want := range map[any]struct {
		label string
		class string
	}{
		0:   {"alive", "badge-active"},
		1:   {"suspect", "badge-leaving"},
		2:   {"dead", "badge-unhealthy"},
		3:   {"left", "badge-passive"},
		9:   {"state 9", "badge-mode"},
		"x": {"state x", "badge-mode"},
	} {
		assert.Equal(t, want.label, nodeState(state))
		assert.Equal(t, want.class, nodeStateClass(state))
	}
}

// The table treatment is part of what makes these pages readable, so the rules
// that carry it are pinned: banded rows, a hover tone of its own, and a header
// that stands apart from the body.
func TestStylesheet_table_treatment(t *testing.T) {
	for _, rule := range []string{
		"thead th {",
		"tbody tr:nth-child(even) td {",
		"tbody tr:hover td { background: var(--hover); }",
		"--hover:",
	} {
		assert.Contains(t, stylesheet, rule)
	}

	// The hover tone must not be the banding tone, or hovering a banded row
	// would show nothing at all.
	assert.NotContains(t, stylesheet, "tbody tr:hover td { background: var(--raised); }")
}

func TestHostOnly(t *testing.T) {
	assert.Equal(t, "127.0.0.1", hostOnly("127.0.0.1:8080"))
	assert.Equal(t, "node-1.example.com", hostOnly("node-1.example.com:8080"))
	assert.Equal(t, "::1", hostOnly("[::1]:8080"))

	// Nothing to drop, so nothing is dropped rather than the value being lost.
	assert.Equal(t, "127.0.0.1", hostOnly("127.0.0.1"))
	assert.Empty(t, hostOnly(""))
}
