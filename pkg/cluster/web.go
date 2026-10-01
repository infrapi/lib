package cluster

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/grafana/dskit/kv/memberlist"
)

// web holds the stylesheet and the page templates. They are compiled into the
// binary so that a status page needs no file on disk and no network: the one
// moment an operator reaches for it is the moment the network is suspect.
//
//go:embed web/cluster.css web/layout.gohtml web/index.gohtml web/ring.gohtml web/memberlist.gohtml
var web embed.FS

// stylesheet is the embedded CSS, kept only so a test can assert it was
// embedded at all. The pages do not use it: the layout pulls the stylesheet in
// as template text instead.
var stylesheet = func() string {
	content, err := web.ReadFile("web/cluster.css")
	if err != nil {
		panic("cluster: embedded stylesheet missing: " + err.Error())
	}
	return string(content)
}()

// templateFuncs are the few helpers the pages need. Anything more would be
// logic in a template, which belongs in the view structs instead.
var templateFuncs = template.FuncMap{
	"lower": strings.ToLower,
	"ago":   ago,
}

// indexTemplate and ringTemplate are separate sets: both pages define a
// "content" block, so they cannot share one.
var (
	indexTemplate = mustParse("web/index.gohtml")
	ringTemplate  = mustParse("web/ring.gohtml")
)

// newMemberlistTemplate builds the page that replaces the one dskit would
// render for its gossip store.
//
// The contents are dskit's: the key-value store, the gossip membership and the
// message history are introspection this package could not reproduce anyway,
// because the data behind them is unexported. What is ours is the rendering —
// dskit's status handler takes a template, so the page is laid out here, in the
// same design as the rest of them, instead of dskit's unstyled default.
//
// It is built per cluster rather than once per process because dskit executes
// it with its own StatusPageData, which leaves no room for ours: the path the
// pages were mounted under therefore arrives through a function closed over
// here. A relative link cannot stand in for it — the page lives one level below
// the index, so "../" resolves above it, not to it. An empty prefix means the
// handler was mounted by hand, and the page then shows no link back rather than
// a broken one.
//
// The three functions dskit's own template relies on are provided too, since
// what is replaced is the template and not the handler.
func newMemberlistTemplate(prefix string) *template.Template {
	content, err := web.ReadFile("web/memberlist.gohtml")
	if err != nil {
		panic("cluster: embedded gossip page missing: " + err.Error())
	}

	// Parse rather than ParseFS: ParseFS names the template after the file,
	// and dskit executes the root one, which would then be empty.
	tmpl := template.Must(template.New("memberlist").Funcs(template.FuncMap{
		"StringsJoin":     strings.Join,
		"GetZoneFromMeta": func(meta []byte) string { return memberlist.EncodedNodeMetadata(meta).Zone() },
		"GetRoleFromMeta": func(meta []byte) string { return memberlist.EncodedNodeMetadata(meta).Role().String() },

		// nodeState turns memberlist's numeric node state into something
		// readable. dskit's own page prints the number and a legend.
		"nodeState":      nodeState,
		"nodeStateClass": nodeStateClass,

		// messages passes a title and a slice to the shared message table.
		"messages": func(title string, msgs []memberlist.Message) map[string]any {
			return map[string]any{"Title": title, "Messages": msgs}
		},

		"clusterPath": func() string { return prefix },
	}).Parse(string(content)))

	// The stylesheet joins the set as an associated template, so the page pulls
	// it in the same way the others do.
	template.Must(tmpl.New("cluster.css").Parse(stylesheet))

	return tmpl
}

// nodeStates maps memberlist's node state to a label and a badge colour.
var nodeStates = map[string]struct {
	label string
	class string
}{
	"0": {"alive", "badge-active"},
	"1": {"suspect", "badge-leaving"},
	"2": {"dead", "badge-unhealthy"},
	"3": {"left", "badge-passive"},
}

// nodeState names a node state. The argument is memberlist's own numeric type,
// taken as any so that this package does not depend on it directly.
func nodeState(state any) string {
	if known, ok := nodeStates[fmt.Sprint(state)]; ok {
		return known.label
	}
	return fmt.Sprintf("state %v", state)
}

// nodeStateClass is the badge colour for a node state.
func nodeStateClass(state any) string {
	if known, ok := nodeStates[fmt.Sprint(state)]; ok {
		return known.class
	}
	return "badge-mode"
}

// mustParse compiles the layout together with the stylesheet and one page.
// A failure here is a broken build, not a runtime condition.
//
// The stylesheet is parsed as a template rather than handed over as a value,
// which keeps it out of the data path: there is no unescaped conversion to
// make, and nothing for gosec to flag. html/template does run its CSS escaper
// over the result and drops the comments; every declaration survives, which
// TestStylesheet_reaches_the_page_intact pins down.
func mustParse(page string) *template.Template {
	tmpl, err := template.New("layout").Funcs(templateFuncs).ParseFS(web, "web/layout.gohtml", "web/cluster.css", page)
	if err != nil {
		panic("cluster: parsing " + page + ": " + err.Error())
	}
	return tmpl
}

// ago renders how long ago an instant was, in the shortest form that is still
// unambiguous. The zero time means the ring never recorded it.
func ago(t time.Time) string {
	if t.IsZero() || t.Unix() <= 0 {
		return "unknown"
	}

	elapsed := time.Since(t)
	if elapsed < 0 {
		return "just now"
	}

	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds ago", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm%ds ago", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh%dm ago", int(elapsed.Hours()), int(elapsed.Minutes())%60)
	default:
		return fmt.Sprintf("%dd ago", int(elapsed.Hours())/24)
	}
}

// page is what every template receives: the chrome, plus the view below it.
type page struct {
	Title string
	Now   time.Time
}

// indexView is the model of the cluster index page, and the shape of its JSON.
type indexView struct {
	page `json:"-"`

	// Prefix is where the status pages are mounted, so the page can link to
	// its siblings. Empty when a handler was mounted by hand and this package
	// cannot know.
	Prefix string `json:"-"`

	Enabled      bool   `json:"enabled"`
	Store        string `json:"store"`
	InstanceID   string `json:"instanceId"`
	InstanceAddr string `json:"instanceAddr"`

	// InstanceHost is InstanceAddr without its port, which is what the index
	// shows: the port belongs to the ring page, where each member's full
	// address is listed.
	InstanceHost string `json:"instanceHost,omitempty"`

	// Fqdn is not rendered. It is here for a caller reading the JSON, which is
	// the one place a script behind a load balancer can learn which host
	// answered.
	Fqdn         string     `json:"fqdn,omitempty"`
	ClusterLabel string     `json:"clusterLabel,omitempty"`
	GossipPort   int        `json:"gossipPort,omitempty"`
	HasGossip    bool       `json:"-"`
	Roles        []roleView `json:"roles"`
	Error        string     `json:"error,omitempty"`
}

// roleView is one role's entry on the index page.
type roleView struct {
	Name        string `json:"name"`
	Mode        string `json:"mode"`
	Key         string `json:"key"`
	State       string `json:"state,omitempty"`
	ShouldRun   bool   `json:"shouldRun"`
	Leader      string `json:"leader,omitempty"`
	MemberCount int    `json:"memberCount"`
	Error       string `json:"error,omitempty"`

	// LeaderAddr is the leader's advertised address, and LeaderURL a link to
	// its own copy of this page. Both are empty when this replica is the
	// leader, or when there is none.
	LeaderAddr   string `json:"leaderAddr,omitempty"`
	LeaderURL    string `json:"leaderUrl,omitempty"`
	LeaderIsSelf bool   `json:"leaderIsSelf"`
}

// ringView is the model of a role's ring page, and the shape of its JSON.
type ringView struct {
	page `json:"-"`

	Prefix        string `json:"-"`
	ForgetEnabled bool   `json:"-"`

	RoleName         string   `json:"role"`
	Mode             string   `json:"mode"`
	Key              string   `json:"key"`
	Leader           string   `json:"leader,omitempty"`
	InstanceID       string   `json:"instanceId"`
	HeartbeatTimeout string   `json:"heartbeatTimeout"`
	Members          []Member `json:"members"`
	Error            string   `json:"error,omitempty"`
}

// hostOnly drops the port from a host:port address, and returns the input
// unchanged when there is no port to drop.
func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// wantsJSON reports whether the caller asked for JSON rather than a page. It is
// the same negotiation dskit's own status pages perform.
func wantsJSON(req *http.Request) bool {
	return strings.Contains(req.Header.Get("Accept"), "application/json")
}

// render writes a page, or reports the failure in the response rather than
// leaving the caller with a blank one.
func render(w http.ResponseWriter, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		// The status line is long gone by the time a template fails halfway,
		// so the only honest thing left is to say so in the body.
		_, _ = fmt.Fprintf(w, "\n<!-- cluster: rendering failed: %s -->\n", template.HTMLEscapeString(err.Error()))
	}
}

// styleMarker is where the stylesheet is injected into a page this package did
// not write.
const styleMarker = "</head>"

// dskitPage wraps one of dskit's own status pages so that our stylesheet can be
// injected into it.
//
// dskit's pages carry no styling at all — they reference no stylesheet, no
// script and no font, which is why they are self-contained and also why they
// look nothing like the rest of a service. The gossip page is rendered from a
// template of ours instead; this is for the ring page dskit builds from a
// lifecycler, where buffering the response is safe because what it returns is
// one ring, and anything that is not HTML passes through untouched.
func dskitPage(target http.Handler, w gin.ResponseWriter, req *http.Request) {
	captured := &capturedResponse{header: http.Header{}, status: http.StatusOK}
	target.ServeHTTP(captured, req)
	captured.writeStyledTo(w)
}

// capturedResponse buffers a response so that it can be rewritten before it
// reaches the client.
type capturedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

// Header implements http.ResponseWriter.
func (c *capturedResponse) Header() http.Header { return c.header }

// Write implements http.ResponseWriter.
func (c *capturedResponse) Write(p []byte) (int, error) { return c.body.Write(p) }

// WriteHeader implements http.ResponseWriter.
func (c *capturedResponse) WriteHeader(status int) { c.status = status }

// writeStyledTo copies the captured response out, adding the stylesheet when
// what came back is an HTML page with a head to put it in.
func (c *capturedResponse) writeStyledTo(w gin.ResponseWriter) {
	body := c.body.String()

	styled := strings.Contains(c.header.Get("Content-Type"), "text/html") &&
		strings.Contains(body, styleMarker)

	if styled {
		body = strings.Replace(body, styleMarker, "<style>"+stylesheet+"</style>"+styleMarker, 1)
	}

	for key, values := range c.header {
		// The length changed, and Go recomputes it.
		if http.CanonicalHeaderKey(key) == "Content-Length" {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	w.WriteHeader(c.status)
	_, _ = w.WriteString(body)
}
