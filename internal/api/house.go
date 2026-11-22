package api

// The house-wide views: every app the house used and every place it reached,
// over a period, with the clients behind each. Device views live in api.go.

import (
	"net/http"
	"regexp"
	"strings"

	"netryx/internal/enrich"
	"netryx/internal/rollup"
)

const (
	// maxHouseApps bounds the house's app list, and maxHouseCountries its
	// countries: a house uses tens of each, a flood of names folds away.
	maxHouseApps      = 100
	maxHouseCountries = 100
	// maxHouseNetworks and maxHouseDomains bound the other destination lists.
	maxHouseNetworks = 50
	maxHouseDomains  = 50
	// houseTopClients is how many clients a row of a house list names.
	houseTopClients = 3
	// maxDetailClients bounds the clients of one app or country.
	maxDetailClients = 50
)

var (
	// appID is what a catalog id looks like: lower case, digits and . _ + -,
	// starting with a letter or digit.
	appID = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,63}$`)
	// ccCode is a country code as the address table writes it.
	ccCode = regexp.MustCompile(`^[A-Za-z]{2}$`)
)

type clientRefJSON struct {
	MAC   string `json:"mac"`
	Name  string `json:"name"`
	Down  uint64 `json:"down"`
	Up    uint64 `json:"up"`
	Other bool   `json:"other"`
}

type houseAppJSON struct {
	App     appJSON         `json:"app"`
	Down    uint64          `json:"down"`
	Up      uint64          `json:"up"`
	Flows   uint64          `json:"flows"`
	Share   float64         `json:"share"`
	Clients int             `json:"clients"`
	Top     []clientRefJSON `json:"top"`
	Other   bool            `json:"other"`
}

type networkJSON struct {
	CC    string `json:"cc"`
	ASN   string `json:"asn"`
	Org   string `json:"org"`
	Down  uint64 `json:"down"`
	Up    uint64 `json:"up"`
	Other bool   `json:"other"`
}

type appDetailJSON struct {
	App   appJSON `json:"app"`
	Down  uint64  `json:"down"`
	Up    uint64  `json:"up"`
	Flows uint64  `json:"flows"`
	Share float64 `json:"share"`
	// ClientCount counts every client; Clients lists the busiest.
	ClientCount int             `json:"clientCount"`
	Clients     []clientRefJSON `json:"clients"`
	Places      []networkJSON   `json:"places"`
	Domains     []domainJSON    `json:"domains"`
	Series      []seriesPoint   `json:"series"`
}

type houseCountryJSON struct {
	CC      string          `json:"cc"`
	Down    uint64          `json:"down"`
	Up      uint64          `json:"up"`
	Share   float64         `json:"share"`
	Clients int             `json:"clients"`
	Top     []clientRefJSON `json:"top"`
	Other   bool            `json:"other"`
}

type houseDestinationsJSON struct {
	Countries []houseCountryJSON `json:"countries"`
	Networks  []networkJSON      `json:"networks"`
	Domains   []destDomainJSON   `json:"domains"`
}

type countryAppJSON struct {
	App   appJSON `json:"app"`
	Down  uint64  `json:"down"`
	Up    uint64  `json:"up"`
	Other bool    `json:"other"`
}

type countryDetailJSON struct {
	CC          string           `json:"cc"`
	Down        uint64           `json:"down"`
	Up          uint64           `json:"up"`
	Share       float64          `json:"share"`
	ClientCount int              `json:"clientCount"`
	Clients     []clientRefJSON  `json:"clients"`
	Apps        []countryAppJSON `json:"apps"`
	Networks    []networkJSON    `json:"networks"`
	Series      []seriesPoint    `json:"series"`
}

func (s *server) houseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/apps", s.route(s.houseApps))
	mux.HandleFunc("/api/apps/{id}", s.route(s.houseApp))
	mux.HandleFunc("/api/destinations", s.route(s.houseDestinations))
	mux.HandleFunc("/api/destinations/{cc}", s.route(s.houseCountry))
}

// names maps each known MAC to the name the console shows.
func (s *server) names() map[string]string {
	m := map[string]string{}
	for _, d := range s.inv.Devices() {
		m[d.MAC] = d.Name
	}
	return m
}

// clientRefs turns device rows into named rows, at most n of them (all if n
// is zero or less).
func clientRefs(rows []rollup.Row, names map[string]string, n int) []clientRefJSON {
	out := []clientRefJSON{}
	for _, r := range cut(rows, n) {
		c := clientRefJSON{MAC: r.Key, Name: names[r.Key], Down: r.Bytes.Down, Up: r.Bytes.Up, Other: !isClient(r.Key)}
		if c.Name == "" {
			c.Name = r.Key
		}
		out = append(out, c)
	}
	return out
}

// topRefs names the busiest few clients of a row; a row with no client
// behind it names its first rows instead (the router, addresses without a
// hardware address), so the page can say whose traffic it is.
func topRefs(rows []rollup.Row, names map[string]string) []clientRefJSON {
	var cl []rollup.Row
	for _, r := range rows {
		if isClient(r.Key) {
			cl = append(cl, r)
			if len(cl) == houseTopClients {
				break
			}
		}
	}
	if len(cl) == 0 {
		cl = rows
	}
	return clientRefs(cl, names, houseTopClients)
}

// clientCount counts the clients among device rows: the router, the
// catch-all and an address nobody could name are not clients.
func clientCount(rows []rollup.Row) int {
	n := 0
	for _, r := range rows {
		if isClient(r.Key) {
			n++
		}
	}
	return n
}

func networkView(r rollup.Row) networkJSON {
	return networkJSON{
		CC: r.Extra["cc"], ASN: r.Extra["asn"], Org: r.Extra["org"], Down: r.Bytes.Down, Up: r.Bytes.Up,
		Other: r.Key == serviceKey || r.Extra["cc"] == serviceCC,
	}
}

// wanOnly leaves the house's own traffic out of a list of countries or
// places: it is not a destination.
func wanOnly(rows []rollup.Row, cc func(rollup.Row) string) []rollup.Row {
	out := rows[:0:0]
	for _, r := range rows {
		if cc(r) != lanCC {
			out = append(out, r)
		}
	}
	return out
}

func (s *server) houseApps(r *http.Request) (any, error) {
	p, err := s.period(r)
	if err != nil {
		return nil, err
	}
	cat, names := s.catalog(), s.names()
	all := s.h.Top(rollup.Filter{}, "app", p.from, p.now, 0)
	total := sumBytes(all)
	clients := s.h.Pairs(rollup.Filter{}, "app", "mac", p.from, p.now)
	out := []houseAppJSON{}
	for _, ar := range cut(all, maxHouseApps) {
		cl := clients[ar.Key]
		out = append(out, houseAppJSON{
			App: cat.value(ar.Key), Down: ar.Bytes.Down, Up: ar.Bytes.Up, Flows: ar.Bytes.Flows,
			Share: share(rowBytes(ar), total), Clients: clientCount(cl),
			Top: topRefs(cl, names), Other: ar.Key == serviceKey,
		})
	}
	return out, nil
}

func (s *server) houseApp(r *http.Request) (any, error) {
	id := r.PathValue("id")
	if !appID.MatchString(id) {
		return nil, httpError{http.StatusBadRequest, "malformed app id"}
	}
	p, err := s.period(r)
	if err != nil {
		return nil, err
	}
	all := s.h.Top(rollup.Filter{}, "app", p.from, p.now, 0)
	var mine rollup.Row
	seen := false
	for _, ar := range all {
		if ar.Key == id {
			mine, seen = ar, true
			break
		}
	}
	if !seen && id != enrich.Unknown.ID && id != serviceKey {
		known := false
		if s.rules != nil {
			_, known = s.rules.ByID(id)
		}
		if !known {
			return nil, httpError{http.StatusNotFound, "no such app"}
		}
	}
	cat := s.catalog()
	f := rollup.Filter{App: id}
	macs := s.h.Top(f, "mac", p.from, p.now, 0)
	out := appDetailJSON{
		App: cat.value(id), Down: mine.Bytes.Down, Up: mine.Bytes.Up, Flows: mine.Bytes.Flows,
		Share: share(rowBytes(mine), sumBytes(all)), ClientCount: clientCount(macs),
		Clients: clientRefs(macs, s.names(), maxDetailClients),
		Places:  []networkJSON{}, Domains: []domainJSON{},
		Series: s.seriesOf(f, p, ""),
	}
	for _, pr := range s.h.Top(f, "place", p.from, p.now, maxDestinations) {
		out.Places = append(out.Places, networkView(pr))
	}
	for _, dr := range s.h.Top(f, "domain", p.from, p.now, maxDestinations) {
		out.Domains = append(out.Domains, domainJSON{
			Name: dr.Key, Src: dr.Extra["src"], Down: dr.Bytes.Down, Up: dr.Bytes.Up, Other: dr.Key == serviceKey,
		})
	}
	return out, nil
}

func (s *server) houseDestinations(r *http.Request) (any, error) {
	p, err := s.period(r)
	if err != nil {
		return nil, err
	}
	cat, names := s.catalog(), s.names()
	ccs := wanOnly(s.h.Top(rollup.Filter{}, "cc", p.from, p.now, 0), func(r rollup.Row) string { return r.Key })
	total := sumBytes(ccs)
	clients := s.h.Pairs(rollup.Filter{NotCC: lanCC}, "cc", "mac", p.from, p.now)
	out := houseDestinationsJSON{Countries: []houseCountryJSON{}, Networks: []networkJSON{}, Domains: []destDomainJSON{}}
	for _, cr := range cut(ccs, maxHouseCountries) {
		cl := clients[cr.Key]
		out.Countries = append(out.Countries, houseCountryJSON{
			CC: cr.Key, Down: cr.Bytes.Down, Up: cr.Bytes.Up, Share: share(rowBytes(cr), total),
			Clients: clientCount(cl), Top: topRefs(cl, names), Other: cr.Key == serviceCC,
		})
	}
	places := wanOnly(s.h.Top(rollup.Filter{}, "place", p.from, p.now, 0), func(r rollup.Row) string { return r.Extra["cc"] })
	for _, pr := range cut(places, maxHouseNetworks) {
		out.Networks = append(out.Networks, networkView(pr))
	}
	for _, dr := range s.h.Top(rollup.Filter{}, "domain", p.from, p.now, maxHouseDomains) {
		out.Domains = append(out.Domains, destDomainJSON{
			Name: dr.Key, App: cat.ref(dr.Extra["app"]), Down: dr.Bytes.Down, Up: dr.Bytes.Up, Other: dr.Key == serviceKey,
		})
	}
	return out, nil
}

func (s *server) houseCountry(r *http.Request) (any, error) {
	cc := r.PathValue("cc")
	if !ccCode.MatchString(cc) {
		return nil, httpError{http.StatusBadRequest, "a country is two letters"}
	}
	cc = strings.ToUpper(cc)
	p, err := s.period(r)
	if err != nil {
		return nil, err
	}
	ccs := wanOnly(s.h.Top(rollup.Filter{}, "cc", p.from, p.now, 0), func(r rollup.Row) string { return r.Key })
	var mine rollup.Row
	for _, cr := range ccs {
		if cr.Key == cc {
			mine = cr
			break
		}
	}
	cat := s.catalog()
	f := rollup.Filter{CC: cc}
	macs := s.h.Top(f, "mac", p.from, p.now, 0)
	out := countryDetailJSON{
		CC: cc, Down: mine.Bytes.Down, Up: mine.Bytes.Up, Share: share(rowBytes(mine), sumBytes(ccs)),
		ClientCount: clientCount(macs), Clients: clientRefs(macs, s.names(), maxDetailClients),
		Apps: []countryAppJSON{}, Networks: []networkJSON{},
		Series: s.seriesOf(f, p, ""),
	}
	for _, ar := range s.h.Top(f, "app", p.from, p.now, maxDestinations) {
		out.Apps = append(out.Apps, countryAppJSON{App: cat.value(ar.Key), Down: ar.Bytes.Down, Up: ar.Bytes.Up, Other: ar.Key == serviceKey})
	}
	for _, pr := range s.h.Top(f, "place", p.from, p.now, maxDestinations) {
		out.Networks = append(out.Networks, networkView(pr))
	}
	return out, nil
}
