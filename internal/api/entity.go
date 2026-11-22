package api

// The pages of one network (AS) and one domain across the house, which every
// network and domain name in the console leads to.

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"netryx/internal/rollup"
)

const (
	// entityClients and entityApps bound how many of the house's busiest
	// clients and apps a network or domain page looks through, so a page
	// reads the history a bounded number of times.
	entityClients = 64
	entityApps    = 30
)

// domainName is a name as a resolver or a handshake gives it.
var domainName = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?(\.[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?)*\.?$`)

type entityClientJSON struct {
	MAC  string `json:"mac"`
	Name string `json:"name"`
	Down uint64 `json:"down"`
	Up   uint64 `json:"up"`
}

type networkPageJSON struct {
	ASN       string             `json:"asn"`
	Org       string             `json:"org"`
	Down      uint64             `json:"down"`
	Up        uint64             `json:"up"`
	Countries []countryBytesJSON `json:"countries"`
	Clients   []entityClientJSON `json:"clients"`
	Apps      []countryAppJSON   `json:"apps"`
}

type countryBytesJSON struct {
	CC   string `json:"cc"`
	Down uint64 `json:"down"`
	Up   uint64 `json:"up"`
}

type domainAppJSON struct {
	App  appJSON `json:"app"`
	Src  string  `json:"src"`
	Down uint64  `json:"down"`
	Up   uint64  `json:"up"`
}

type domainPageJSON struct {
	Name    string             `json:"name"`
	Down    uint64             `json:"down"`
	Up      uint64             `json:"up"`
	Apps    []domainAppJSON    `json:"apps"`
	Clients []entityClientJSON `json:"clients"`
}

// busiestClients are the house's busiest clients over the period, at most n.
func (s *server) busiestClients(p period, n int) []string {
	var out []string
	for _, r := range s.h.Top(rollup.Filter{}, "mac", p.from, p.now, 0) {
		if isClient(r.Key) {
			out = append(out, r.Key)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// sumWhere adds the rows whose key is key.
func sumWhere(rows []rollup.Row, key string) (rollup.Bytes, bool) {
	var b rollup.Bytes
	found := false
	for _, r := range rows {
		if strings.EqualFold(r.Key, key) {
			b.Down += r.Bytes.Down
			b.Up += r.Bytes.Up
			found = true
		}
	}
	return b, found
}

func (s *server) clientsWhere(p period, dim, key string) []entityClientJSON {
	names := s.names()
	out := []entityClientJSON{}
	for _, mac := range s.busiestClients(p, entityClients) {
		if b, ok := sumWhere(s.h.Top(rollup.Filter{MAC: mac}, dim, p.from, p.now, 0), key); ok {
			n := names[mac]
			if n == "" {
				n = mac
			}
			out = append(out, entityClientJSON{MAC: mac, Name: n, Down: b.Down, Up: b.Up})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Down+out[i].Up > out[j].Down+out[j].Up })
	return out
}

func (s *server) network1(r *http.Request) (any, error) {
	v := r.PathValue("asn")
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil || n == 0 {
		return nil, httpError{http.StatusBadRequest, "a network is its AS number"}
	}
	asn := strconv.FormatUint(n, 10)
	p, err := s.period(r)
	if err != nil {
		return nil, err
	}
	out := networkPageJSON{ASN: asn, Countries: []countryBytesJSON{}, Apps: []countryAppJSON{}}
	for _, pr := range s.h.Top(rollup.Filter{}, "place", p.from, p.now, 0) {
		if pr.Extra["asn"] != asn {
			continue
		}
		if out.Org == "" {
			out.Org = pr.Extra["org"]
		}
		out.Countries = append(out.Countries, countryBytesJSON{CC: pr.Extra["cc"], Down: pr.Bytes.Down, Up: pr.Bytes.Up})
		out.Down += pr.Bytes.Down
		out.Up += pr.Bytes.Up
	}
	out.Clients = s.clientsWhere(p, "asn", asn)
	cat := s.catalog()
	for i, ar := range s.h.Top(rollup.Filter{}, "app", p.from, p.now, 0) {
		if i == entityApps {
			break
		}
		if b, ok := sumWhere(s.h.Top(rollup.Filter{App: ar.Key}, "asn", p.from, p.now, 0), asn); ok {
			out.Apps = append(out.Apps, countryAppJSON{App: cat.value(ar.Key), Down: b.Down, Up: b.Up, Other: ar.Key == serviceKey})
		}
	}
	sort.SliceStable(out.Apps, func(i, j int) bool { return out.Apps[i].Down+out.Apps[i].Up > out.Apps[j].Down+out.Apps[j].Up })
	return out, nil
}

func (s *server) domain1(r *http.Request) (any, error) {
	name := r.PathValue("name")
	if len(name) > 253 || !domainName.MatchString(name) {
		return nil, httpError{http.StatusBadRequest, "malformed domain name"}
	}
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	p, err := s.period(r)
	if err != nil {
		return nil, err
	}
	cat := s.catalog()
	out := domainPageJSON{Name: name, Apps: []domainAppJSON{}}
	for _, dr := range s.h.Top(rollup.Filter{}, "domain", p.from, p.now, 0) {
		if !strings.EqualFold(dr.Key, name) {
			continue
		}
		out.Apps = append(out.Apps, domainAppJSON{App: cat.value(dr.Extra["app"]), Src: dr.Extra["src"], Down: dr.Bytes.Down, Up: dr.Bytes.Up})
		out.Down += dr.Bytes.Down
		out.Up += dr.Bytes.Up
	}
	out.Clients = s.clientsWhere(p, "domain", name)
	return out, nil
}
