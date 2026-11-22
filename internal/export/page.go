package export

import (
	"fmt"
	"html/template"
	"io"
	"sort"
	"strings"
	"time"
)

// Page is what a person sees. Everything is inline and nothing is fetched
// from anywhere: a monitor that phones out to a font service to draw itself
// would be reporting the household to a third party on every page load, which
// is the opposite of the promise this package makes.
//
// The table answers one question per column, in the order somebody actually
// asks them: who is this, where did it go, how much, and what could not be
// named.
func Page(w io.Writer, s Snapshot) error {
	type row struct {
		Name, Addr, MAC  string
		Up, Down         string
		Flows            int
		Sites            []string
		More             int
		Handshakes       int
		Hidden           string
		NATed, Offloaded bool
	}
	devs := sorted(s.Devices)
	rows := make([]row, 0, len(devs))
	for _, d := range devs {
		r := row{
			Name: d.Name, Addr: d.Addr, MAC: d.MAC,
			// What a person wants is "how much has this device moved", which
			// is what finished plus what is still open. Neither half alone
			// answers it: the open figure falls as connections close, and the
			// finished one ignores a download in progress.
			Up: bytesHuman(d.TotalUpBytes + d.UpBytes), Down: bytesHuman(d.TotalDownBytes + d.DownBytes),
			Flows: d.Flows, Handshakes: d.Handshakes,
			NATed: d.NATed, Offloaded: d.Offloaded,
		}
		sites := append([]string{}, d.Sites...)
		sort.Strings(sites)
		if len(sites) > 6 {
			r.More = len(sites) - 6
			sites = sites[:6]
		}
		r.Sites = sites
		if d.Hidden > 0 {
			// Said as a share rather than a count: "12 hidden" means nothing
			// without knowing that there were 400 handshakes.
			pct := 0
			if d.Handshakes > 0 {
				pct = d.Hidden * 100 / d.Handshakes
			}
			r.Hidden = fmt.Sprintf("%d (%d%%)", d.Hidden, pct)
		}
		rows = append(rows, r)
	}
	return pageTmpl.Execute(w, struct {
		Rows    []row
		Taken   string
		Flows   int
		AcctOff bool
		Lost    int
	}{rows, s.Taken.Format(time.RFC1123), s.Flows, s.AcctOff, s.LostCopies})
}

func bytesHuman(n int) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	case n > 0:
		return fmt.Sprintf("%d B", n)
	}
	return "-"
}

// html/template escapes everything it interpolates, which matters here more
// than it looks: a device's name comes from the name that device gave the
// DHCP server, and that is a string somebody else chose.
var pageTmpl = template.Must(template.New("page").Funcs(template.FuncMap{
	"join": strings.Join,
}).Parse(`<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>netryx</title>
<style>
 body { font: 15px/1.45 -apple-system, system-ui, sans-serif; margin: 0; padding: 1.2rem; color: #1b1b1b; background: #fbfbfa; }
 h1 { font-size: 1.1rem; margin: 0 0 .2rem; }
 .when { color: #6b6b6b; font-size: .85rem; margin-bottom: 1rem; }
 table { border-collapse: collapse; width: 100%; font-size: .9rem; }
 th { text-align: left; font-weight: 600; border-bottom: 2px solid #ddd; padding: .4rem .5rem; white-space: nowrap; }
 td { border-bottom: 1px solid #eee; padding: .45rem .5rem; vertical-align: top; }
 td.n { text-align: right; white-space: nowrap; font-variant-numeric: tabular-nums; }
 .who { font-weight: 600; }
 .sub { color: #7a7a7a; font-size: .8rem; }
 .site { display: inline-block; background: #eef2f7; border-radius: .6rem; padding: .05rem .5rem; margin: .1rem .15rem .1rem 0; }
 .warn { background: #fff4e5; padding: .5rem .7rem; border-radius: .4rem; margin-bottom: 1rem; }
 .none { color: #8a8a8a; }
 @media (prefers-color-scheme: dark) {
  body { background: #17181a; color: #e8e8e8; } th { border-color: #333; } td { border-color: #262626; }
  .site { background: #23282f; } .warn { background: #3a2f1c; } .sub, .when, .none { color: #9a9a9a; }
 }
</style>
<h1>netryx</h1>
<div class="when">{{.Taken}} &middot; {{.Flows}} connections in the kernel</div>
{{if .AcctOff}}<div class="warn">The kernel does not count bytes (nf_conntrack_acct is off), so volumes show as zero.</div>{{end}}
{{if .Lost}}<div class="warn">The kernel could not hand over {{.Lost}} copies, so some handshakes were not read.</div>{{end}}
{{if .Rows}}
<table>
 <tr><th>device</th><th>where it went</th><th class="n">up</th><th class="n">down</th><th class="n">connections</th><th class="n">handshakes</th><th class="n">name hidden</th></tr>
 {{range .Rows}}
 <tr>
  <td><span class="who">{{.Name}}</span><br><span class="sub">{{.Addr}}{{if .MAC}} &middot; {{.MAC}}{{end}}</span></td>
  <td>{{range .Sites}}<span class="site">{{.}}</span>{{end}}{{if .More}}<span class="sub"> {{.More}} more</span>{{end}}{{if not .Sites}}<span class="none">-</span>{{end}}</td>
  <td class="n">{{.Up}}</td><td class="n">{{.Down}}</td><td class="n">{{.Flows}}</td>
  <td class="n">{{if .Handshakes}}{{.Handshakes}}{{else}}-{{end}}</td>
  <td class="n">{{if .Hidden}}{{.Hidden}}{{else}}-{{end}}</td>
 </tr>
 {{end}}
</table>
{{else}}<p class="none">Nothing to see yet. If this was just installed, give it a minute.</p>{{end}}
<script>setTimeout(function(){location.reload()}, 10000)</script>
`))
