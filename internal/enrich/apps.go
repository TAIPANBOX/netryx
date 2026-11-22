package enrich

import (
	"bufio"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// MaxAppRules bounds the application table. The generated file has a few
// tens of thousands of rules; the limit only stops a corrupt file from taking
// the router's memory.
const MaxAppRules = 500_000

// maxAppRules is MaxAppRules as a variable so a test can lower it.
var maxAppRules = MaxAppRules

// App is what a remote name or network belongs to, as the UI shows it.
type App struct {
	ID       string
	Name     string
	Category string
}

// Unknown is the answer when no rule covers a connection.
var Unknown = App{ID: "unknown", Name: "Unknown", Category: "unknown"}

type orgRule struct {
	sub string
	app App
}

// AppMap classifies remote names and ASN organisations. It is built once from
// the generated appmap.tsv and only read afterwards, so it needs no locking.
//
// Lookups are a map probe per label of the name, not a regular expression, so
// they stay cheap on the path of every new connection.
type AppMap struct {
	exact  map[string]App            // full names, from f rules
	suffix map[string]App            // domain suffixes, from d rules
	orgs   []orgRule                 // substrings of ASN organisation names, from o rules
	byID   map[string]App            // the first rule that names each id, for ByID
	addrs  map[string][]netip.Prefix // static ranges that are the app's, from a rules
	ports  map[string][]Port         // destination ports that are the app's, from p rules
}

// Port is a destination port an app is reached on (a VPN's default port).
// Proto is the IP protocol number: 6 for TCP, 17 for UDP.
type Port struct {
	Proto uint8
	Num   uint16
}

// LoadAppMap reads the tab separated format written by tools/appmap:
//
//	d <domain> <id> <name> <category>   a suffix rule
//	f <fqdn>   <id> <name> <category>   an exact rule
//	o <org>    <id> <name> <category>   a lower case organisation substring
//	a <cidr>   <id> <name> <category>   a static range that is the app's
//	p <proto>/<port> <id> <name> <category>   a destination port (tcp or udp)
//
// Blank lines and lines starting with # are skipped. Unlike the IP table, a
// malformed line is an error: this file is ours, built from a pinned list, so
// a bad line means a broken build and should not be quietly dropped.
func LoadAppMap(r io.Reader) (*AppMap, error) {
	m := &AppMap{
		exact:  make(map[string]App),
		suffix: make(map[string]App),
		byID:   make(map[string]App),
		addrs:  make(map[string][]netip.Prefix),
		ports:  make(map[string][]Port),
	}
	sc := bufio.NewScanner(r)
	rules := 0
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 5 {
			return nil, fmt.Errorf("enrich: appmap line %d: want 5 tab separated fields, got %d", lineNo, len(f))
		}
		if rules >= maxAppRules {
			return nil, fmt.Errorf("enrich: the app map has more than %d rules, the limit", maxAppRules)
		}
		rules++
		app := App{ID: f[2], Name: f[3], Category: f[4]}
		if _, dup := m.byID[app.ID]; !dup {
			m.byID[app.ID] = app
		}
		key := strings.ToLower(strings.TrimSuffix(f[1], "."))
		switch f[0] {
		case "d":
			if _, dup := m.suffix[key]; !dup {
				m.suffix[key] = app
			}
		case "f":
			if _, dup := m.exact[key]; !dup {
				m.exact[key] = app
			}
		case "o":
			// An empty substring is contained in every organisation, so it
			// would label all traffic with this app. Refuse it, as for any
			// other malformed line.
			if f[1] == "" {
				return nil, fmt.Errorf("enrich: appmap line %d: empty organisation substring", lineNo)
			}
			m.orgs = append(m.orgs, orgRule{sub: strings.ToLower(f[1]), app: app})
		case "a":
			pfx, err := netip.ParsePrefix(f[1])
			if err != nil {
				return nil, fmt.Errorf("enrich: appmap line %d: %v", lineNo, err)
			}
			m.addrs[app.ID] = append(m.addrs[app.ID], pfx.Masked())
		case "p":
			port, err := ParsePort(f[1])
			if err != nil {
				return nil, fmt.Errorf("enrich: appmap line %d: %v", lineNo, err)
			}
			m.ports[app.ID] = append(m.ports[app.ID], port)
		default:
			return nil, fmt.Errorf("enrich: appmap line %d: unknown rule kind %q", lineNo, f[0])
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return m, nil
}

// ParsePort reads "tcp/<n>" or "udp/<n>" with n from 1 to 65535.
func ParsePort(s string) (Port, error) {
	proto, num, ok := strings.Cut(s, "/")
	if !ok {
		return Port{}, fmt.Errorf("port %q: want tcp/<n> or udp/<n>", s)
	}
	var p Port
	switch proto {
	case "tcp":
		p.Proto = 6
	case "udp":
		p.Proto = 17
	default:
		return Port{}, fmt.Errorf("port %q: protocol must be tcp or udp", s)
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 || n > 65535 {
		return Port{}, fmt.Errorf("port %q: number must be 1 to 65535", s)
	}
	p.Num = uint16(n)
	return p, nil
}

// Addrs lists the static ranges that are the app's, in file order. A nil map
// or an app without such rules has none.
func (m *AppMap) Addrs(id string) []netip.Prefix {
	if m == nil {
		return nil
	}
	return m.addrs[id]
}

// Ports lists the destination ports that are the app's, in file order.
func (m *AppMap) Ports(id string) []Port {
	if m == nil {
		return nil
	}
	return m.ports[id]
}

// ByID returns the name and category of an app id, taken from the first rule
// that names that id. The map is read-only after loading, so no lock is
// needed. A nil map knows no app.
func (m *AppMap) ByID(id string) (App, bool) {
	if m == nil {
		return App{}, false
	}
	app, ok := m.byID[id]
	return app, ok
}

// Apps lists every app id the map knows once, with the name and category
// ByID gives it, in id order. A nil map knows none.
func (m *AppMap) Apps() []App {
	if m == nil {
		return nil
	}
	out := make([]App, 0, len(m.byID))
	for _, a := range m.byID {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Classify finds the app for a host name. The exact rule wins; otherwise the
// longest domain suffix that ends on a label boundary wins, so
// "evilgooglevideo.com" does not match "googlevideo.com". Case and a trailing
// dot are ignored.
func (m *AppMap) Classify(name string) (App, bool) {
	if m == nil {
		return App{}, false
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == "" {
		return App{}, false
	}
	if app, ok := m.exact[name]; ok {
		return app, true
	}
	// Walk from the full name towards the registered domain, dropping one
	// label at a time, so the first hit is the longest suffix.
	for s := name; ; {
		if app, ok := m.suffix[s]; ok {
			return app, true
		}
		i := strings.IndexByte(s, '.')
		if i < 0 {
			break
		}
		s = s[i+1:]
	}
	return App{}, false
}

// ClassifyOrg finds the app whose organisation substring occurs in the ASN
// organisation name. When several match, the longest substring wins, so a
// specific rule is not hidden by a general one.
func (m *AppMap) ClassifyOrg(org string) (App, bool) {
	if m == nil || org == "" {
		return App{}, false
	}
	lower := strings.ToLower(org)
	best := -1
	for i, r := range m.orgs {
		if strings.Contains(lower, r.sub) && (best < 0 || len(r.sub) > len(m.orgs[best].sub)) {
			best = i
		}
	}
	if best < 0 {
		return App{}, false
	}
	return m.orgs[best].app, true
}

// Resolve gives the answer shown for a connection: the name rule if one
// covers it, else the organisation rule, else Unknown. When only the
// organisation is known, the name carries it, so the user can see who the
// traffic goes to even without an app.
func (m *AppMap) Resolve(name, org string) App {
	if app, ok := m.Classify(name); ok {
		return app
	}
	if app, ok := m.ClassifyOrg(org); ok {
		return app
	}
	if org == "" {
		return Unknown
	}
	return App{ID: "unknown", Name: "Unknown · " + org, Category: "unknown"}
}
