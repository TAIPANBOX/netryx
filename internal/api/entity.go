package api

// The pages of one network (AS) and one domain across the house, which every
// network and domain name in the console leads to.

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"netryx/internal/rollup"
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

// clientsOf names the devices among holders, busiest first; the router and
// addresses without a hardware address are not clients.
func (s *server) clientsOf(rows []rollup.Row) []entityClientJSON {
	names := s.names()
	out := []entityClientJSON{}
	for _, r := range rows {
		if !isClient(r.Key) {
			continue
		}
		n := names[r.Key]
		if n == "" {
			n = r.Key
		}
		out = append(out, entityClientJSON{MAC: r.Key, Name: n, Down: r.Bytes.Down, Up: r.Bytes.Up})
	}
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
	hd := s.h.Holders("asn", asn, p.from, p.to)
	out := networkPageJSON{ASN: asn, Org: hd.Org, Countries: []countryBytesJSON{}, Apps: []countryAppJSON{}}
	for _, c := range hd.CCs {
		out.Countries = append(out.Countries, countryBytesJSON{CC: c.Key, Down: c.Bytes.Down, Up: c.Bytes.Up})
		out.Down += c.Bytes.Down
		out.Up += c.Bytes.Up
	}
	out.Clients = s.clientsOf(hd.MACs)
	cat := s.catalog()
	for _, a := range hd.Apps {
		out.Apps = append(out.Apps, countryAppJSON{App: cat.value(a.Key), Down: a.Bytes.Down, Up: a.Bytes.Up, Other: a.Key == serviceKey})
	}
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
	for _, dr := range s.h.Top(rollup.Filter{}, "domain", p.from, p.to, 0) {
		if !strings.EqualFold(dr.Key, name) {
			continue
		}
		out.Apps = append(out.Apps, domainAppJSON{App: cat.value(dr.Extra["app"]), Src: dr.Extra["src"], Down: dr.Bytes.Down, Up: dr.Bytes.Up})
		out.Down += dr.Bytes.Down
		out.Up += dr.Bytes.Up
	}
	out.Clients = s.clientsOf(s.h.Holders("domain", name, p.from, p.to).MACs)
	return out, nil
}
