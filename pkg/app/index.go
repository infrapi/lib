package app

import (
	"html/template"
	"log/slog"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/server"

	"github.com/infrapi/lib/pkg/app/internal/relay"
)

// indexCSP: the page is one document with its styles inline, and loads nothing.
const indexCSP = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

type link struct{ Href, Name, What string }

type indexView struct {
	Name, Instance, Zone, Leader, LeaderErr string
	Clustered, IsLeader                     bool
	Contracts                               []link
	Ops                                     []link
}

// indexHandler is the landing page: which replica answered, who leads, and a
// link to everything this binary serves besides the API itself.
func indexHandler(name, zone string, cl *cluster.Cluster, specs []server.OpenAPISpec) gin.HandlerFunc {
	hostname, err := os.Hostname()
	if err != nil {
		slog.Warn("no host name for the landing page", "err", err)
		hostname = "unknown"
	}
	return func(c *gin.Context) {
		w, r := c.Writer, c.Request
		v := indexView{Name: name, Instance: hostname, Zone: zone, Clustered: cl.Enabled()}
		if v.Clustered {
			v.Instance = cl.InstanceID()
			if id, _, self, err := (relay.Cluster{C: cl, Role: writerRole}).Leader(); err != nil {
				v.LeaderErr = err.Error()
			} else {
				v.Leader, v.IsLeader = id, self
			}
		}
		for _, s := range specs {
			v.Contracts = append(v.Contracts, link{Href: server.OpenAPIPrefix + s.Path, Name: s.Name,
				What: server.OpenAPIPrefix + s.Path})
		}
		v.Ops = []link{
			{server.DocsPath, "API documentation", "Swagger UI, every contract served"},
			{"/-/cluster", "Cluster", "replicas, their state, the leader of the writer ring"},
			{"/-/cluster/ring/" + writerRole, "Writer ring", "the ring the leader is elected in"},
			{"/-/cluster/memberlist", "Gossip store", "memberlist members and the keys they share"},
			{"/-/metrics", "Metrics", "Prometheus: API calls per route, writes relayed, ring, gossip, key-value store, Go runtime"},
			{"/-/metadata", "Metadata", "the service's name, platform, region, location"},
			{ReadyPath, "Readiness", "200 once joined, 503 while joining or draining"},
			{HealthPath, "Liveness", "200 while the process runs"},
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", indexCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if err := indexPage.Execute(w, v); err != nil {
			slog.WarnContext(r.Context(), "index page", "err", err)
		}
	}
}

var indexPage = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}} · {{.Instance}}</title>
<style>
:root { --bg:#f6f7f9; --fg:#1b1f24; --muted:#5b6470; --line:#dde1e6; --card:#fff; --accent:#2457c5; --ok:#1d7a46; --warn:#a35a00; }
@media (prefers-color-scheme: dark) { :root { --bg:#14171b; --fg:#e6e9ed; --muted:#9aa4b1; --line:#2a3038; --card:#1b1f25; --accent:#7fa6ff; --ok:#5fcf8f; --warn:#f0a94b; } }
* { box-sizing: border-box; }
body { margin:0; background:var(--bg); color:var(--fg); font:15px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
main { max-width: 52rem; margin: 0 auto; padding: 2rem 1rem 3rem; }
h1 { font-size: 1.5rem; margin: 0 0 .25rem; }
h2 { font-size: .8rem; text-transform: uppercase; letter-spacing: .06em; color: var(--muted); margin: 2rem 0 .5rem; }
.who { color: var(--muted); margin: 0; }
.who b { color: var(--fg); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-weight: 600; }
.badge { display:inline-block; padding: 0 .5rem; border-radius: 1rem; font-size: .8rem; border: 1px solid currentColor; }
.lead { color: var(--ok); } .follow { color: var(--muted); } .none { color: var(--warn); }
ul { list-style: none; margin: 0; padding: 0; background: var(--card); border: 1px solid var(--line); border-radius: 8px; }
li { padding: .65rem 1rem; border-top: 1px solid var(--line); display: flex; flex-wrap: wrap; gap: .25rem 1rem; align-items: baseline; }
li:first-child { border-top: 0; }
a { color: var(--accent); font-weight: 600; text-decoration: none; min-width: 11rem; }
a:hover, a:focus-visible { text-decoration: underline; }
code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .85rem; color: var(--muted); }
span.what { color: var(--muted); }
</style>
</head>
<body>
<main>
  <h1>{{.Name}}</h1>
  <p class="who">This page was served by <b>{{.Instance}}</b>{{if .Zone}} in <b>{{.Zone}}</b>{{end}}.
  {{if not .Clustered}}<span class="badge follow">single replica</span> it performs every write itself.
  {{else if .IsLeader}}<span class="badge lead">leader</span> it performs every write.
  {{else if .Leader}}<span class="badge follow">follower</span> its writes are relayed to <b>{{.Leader}}</b>.
  {{else}}<span class="badge none">no leader</span> writes are refused as retryable: {{.LeaderErr}}{{end}}</p>

  <h2>API</h2>
  <ul>
  {{range .Contracts}}<li><a href="{{.Href}}">{{.Name}}</a><code>{{.What}}</code></li>{{end}}
  </ul>

  <h2>Operations</h2>
  <ul>
  {{range .Ops}}<li><a href="{{.Href}}">{{.Name}}</a><span class="what">{{.What}}</span></li>{{end}}
  </ul>
</main>
</body>
</html>
`))
