// Package api serves the JSON the console UI polls. It reads the flow table,
// the rollup store, the inventory and the WAN status through the small
// interfaces below, so tests run against fakes.
package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"netryx/internal/enrich"
	"netryx/internal/flows"
	"netryx/internal/inventory"
	"netryx/internal/rollup"
	"netryx/internal/wan"
)

// Flows is the part of the flow table the API reads.
type Flows interface {
	Live() []flows.Flow
	Rates() map[string]flows.Rate
	Query(flows.Query) ([]flows.Flow, string)
	LiveSeries(mac string) []flows.LivePoint
	LiveOf(mac string) []flows.Flow
}

// History is the part of the rollup store the API reads.
type History interface {
	Series(rollup.Filter, time.Time, time.Time, time.Duration, string) []rollup.Point
	Top(rollup.Filter, string, time.Time, time.Time, int) []rollup.Row
	Pairs(rollup.Filter, string, string, time.Time, time.Time) map[string][]rollup.Row
	Recent(rollup.Filter, string, time.Duration) ([]rollup.Row, time.Duration)
}

// Apps names an app id from the catalog of rules, so a name does not depend
// on a live flow still carrying it. enrich.AppMap satisfies it.
type Apps interface {
	ByID(string) (enrich.App, bool)
}

// Inventory lists the devices the house knows about.
type Inventory interface{ Devices() []inventory.Device }

// WAN reports the internet line.
type WAN interface{ Status() wan.Status }

// Service reports on the running service itself: when it started, and how
// many connection events the kernel numbered and could not deliver.
type Service interface {
	Started() time.Time
	LostEvents() uint64
}

const (
	// A device counts as online when it was seen this recently: five minutes
	// rides out a few missed sightings without showing a device that left an
	// hour ago as online.
	onlineWindow = 5 * time.Minute
	newWindow    = 24 * time.Hour
	// maxLimit caps one page of flows so a poll cannot ask for the journal.
	maxLimit = 500
	// maxDeviceApps bounds the apps list of one device.
	maxDeviceApps = 50
	// maxDestinations bounds each destination list of a device.
	maxDestinations = 20
	// overviewTop bounds the top lists on the overview.
	overviewTop = 5
	// maxLiveConns bounds the connections of a live view: a device with
	// hundreds of short flows still answers in a few kilobytes, every 2 s.
	maxLiveConns = 50
	// sparkPoints is how many of the ring's newest points an app's
	// sparkline draws.
	sparkPoints = 60
	// serviceKey and serviceCC are the rollup's catch-all names: the traffic
	// it could not attribute to a device or app, or to a country. Rows with
	// them are marked "other" so the UI neither links nor counts them.
	serviceKey = "other"
	serviceCC  = "--"
	// lanCC is the country of a flow with both ends at home.
	lanCC = flows.LANCountry
)

// isClient says whether a history key is a client's hardware address. The
// catch-all, the router's own traffic ("router") and an address nobody could
// name are not: they are shown as rows of their own, never linked.
func isClient(key string) bool {
	_, err := net.ParseMAC(key)
	return err == nil
}

// ranges maps a range name to its span and the step of its series. The steps
// keep each series between 30 and 170 points.
var ranges = map[string]struct{ span, step time.Duration }{
	"1h":  {time.Hour, time.Minute},
	"24h": {24 * time.Hour, 10 * time.Minute},
	"7d":  {7 * 24 * time.Hour, time.Hour},
	"30d": {30 * 24 * time.Hour, 24 * time.Hour},
}

// httpError is an answer the client can act on: its status and message go
// back as they are. Any other error becomes a plain 500 with no detail, so
// internals do not leak.
type httpError struct {
	code int
	msg  string
}

func (e httpError) Error() string { return e.msg }

type server struct {
	f     Flows
	h     History
	inv   Inventory
	w     WAN
	rules Apps
	svc   Service
	now   func() time.Time
	// onLivePoll is told of every live poll, so the service can sync at the
	// live resolution only while somebody watches.
	onLivePoll func()
	// wr is the rule engine's side, set by WithRules; nil serves no rules
	// and no writes. wmu serialises every write: read the config, validate
	// the would-be result, write it, reload.
	wr  Rules
	wmu sync.Mutex
	// net reads the router's wiring for the Network screen; nil answers 503.
	net Network
}

// Option configures the API beyond what every caller passes.
type Option func(*server)

// OnLivePoll has fn called on every answered live poll of a known device.
func OnLivePoll(fn func()) Option { return func(s *server) { s.onLivePoll = fn } }

// New returns the API. Every route answers GET with JSON, and anything else
// under /api/ answers with the same error shape.
func New(f Flows, h History, inv Inventory, w WAN, apps Apps, now func() time.Time, svc Service, opts ...Option) http.Handler {
	s := &server{f: f, h: h, inv: inv, w: w, rules: apps, now: now, svc: svc}
	for _, o := range opts {
		o(s)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/overview", s.route(s.overview))
	mux.HandleFunc("/api/devices", s.route(s.devices))
	if s.wr != nil {
		s.writeRoutes(mux)
	} else {
		mux.HandleFunc("/api/devices/{mac}", s.route(s.device))
	}
	mux.HandleFunc("/api/devices/{mac}/traffic", s.route(s.traffic))
	mux.HandleFunc("/api/devices/{mac}/apps", s.route(s.apps))
	mux.HandleFunc("/api/devices/{mac}/destinations", s.route(s.destinations))
	mux.HandleFunc("/api/devices/{mac}/live", s.route(s.live))
	mux.HandleFunc("/api/flows", s.route(s.flows))
	s.houseRoutes(mux)
	mux.HandleFunc("/api/networks/{asn}", s.route(s.network1))
	mux.HandleFunc("/api/domains/{name}", s.route(s.domain1))
	mux.HandleFunc("/api/network", s.route(s.network))
	// Without this the mux would answer an unknown path in plain text.
	mux.HandleFunc("/api/", s.route(func(*http.Request) (any, error) {
		return nil, httpError{http.StatusNotFound, "no such route"}
	}))
	return mux
}

type handlerFunc func(*http.Request) (any, error)

// route applies the rules every handler shares: its methods only, JSON, and headers
// that stop a browser or a proxy from caching or sniffing the answer.
func (s *server) route(fn handlerFunc) http.HandlerFunc {
	return s.routeM(methods{http.MethodGet: fn})
}

// methods maps the methods one path answers to their handlers; any other
// method is a 405 that lists them.
type methods map[string]handlerFunc

func (m methods) allow() string {
	var names []string
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (s *server) routeM(m methods) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Type", "application/json; charset=utf-8")
		fn, ok := m[r.Method]
		if !ok {
			h.Set("Allow", m.allow())
			writeError(w, httpError{http.StatusMethodNotAllowed, "method not allowed"})
			return
		}
		v, err := fn(r)
		if err != nil {
			writeError(w, err)
			return
		}
		code := http.StatusOK
		if a, ok := v.(accepted); ok {
			code, v = http.StatusAccepted, a.v
		}
		// Marshal before writing the status, so an encoding failure is still a 500.
		body, err := json.Marshal(v)
		if err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(code)
		w.Write(body)
	}
}

// accepted answers 202: the change is taken and finishes in the background.
type accepted struct{ v any }

func writeError(w http.ResponseWriter, err error) {
	code, msg := http.StatusInternalServerError, "internal error"
	var he httpError
	if errors.As(err, &he) {
		code, msg = he.code, he.msg
	}
	body, _ := json.Marshal(map[string]string{"error": msg})
	w.WriteHeader(code)
	w.Write(body)
}

// period is the window a request reads: from now back over its span, and the
// step its series uses.
type period struct {
	now, from time.Time
	step      time.Duration
}

func (s *server) period(r *http.Request) (period, error) {
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "24h"
	}
	spec, ok := ranges[name]
	if !ok {
		return period{}, httpError{http.StatusBadRequest, "range must be 1h, 24h, 7d or 30d"}
	}
	now := s.now()
	return period{now: now, from: now.Add(-spec.span), step: spec.step}, nil
}

// day is the fixed 24 hour window the device list shows.
func (s *server) day() period {
	now := s.now()
	return period{now: now, from: now.Add(-24 * time.Hour), step: ranges["24h"].step}
}

// splitParam reads the split query. Unset means "none".
func splitParam(r *http.Request, allowed ...string) (string, error) {
	v := r.URL.Query().Get("split")
	if v == "" {
		v = "none"
	}
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", httpError{http.StatusBadRequest, "split must be " + strings.Join(allowed, ", ")}
}

// seriesSplit turns the API's "none" into the store's empty split.
func seriesSplit(v string) string {
	if v == "none" {
		return ""
	}
	return v
}

// seriesOf reads a series and labels its unsplit key as "total".
func (s *server) seriesOf(f rollup.Filter, p period, split string) []seriesPoint {
	points := s.h.Series(f, p.from, p.now.Truncate(p.step), p.step, split)
	out := make([]seriesPoint, 0, len(points))
	for _, pt := range points {
		step := pt.Step
		if step <= 0 {
			step = p.step
		}
		sp := seriesPoint{T: pt.At.UTC().Format(time.RFC3339), StepSec: step.Seconds(), Down: map[string]uint64{}, Up: map[string]uint64{}}
		for k, b := range pt.By {
			if k == "" && split == "" {
				k = "total"
			}
			sp.Down[k] = b.Down
			sp.Up[k] = b.Up
		}
		out = append(out, sp)
	}
	return out
}

// totals is every device's bytes over the period, keyed by MAC.
func (s *server) totals(p period) map[string]rollup.Bytes {
	m := map[string]rollup.Bytes{}
	for _, r := range s.h.Top(rollup.Filter{}, "mac", p.from, p.now, 0) {
		m[r.Key] = r.Bytes
	}
	return m
}

// appCatalog names an app id. The rule catalog is asked first, since it knows
// every app the rules name. A live flow comes next, and the id itself last.
// Unknown is always named, even when no rule set is loaded.
type appCatalog struct {
	rules Apps
	live  map[string]enrich.App
}

func (s *server) catalog() appCatalog {
	c := appCatalog{rules: s.rules, live: map[string]enrich.App{}}
	for _, f := range s.f.Live() {
		c.live[f.App.ID] = f.App
	}
	return c
}

func appView(a enrich.App) appJSON {
	return appJSON{ID: a.ID, Name: a.Name, Category: a.Category}
}

func (c appCatalog) value(id string) appJSON {
	// A live flow's Unknown carries that flow's organisation in its name;
	// across the house it is plain Unknown.
	if id == enrich.Unknown.ID {
		return appView(enrich.Unknown)
	}
	if c.rules != nil {
		if a, ok := c.rules.ByID(id); ok {
			return appView(a)
		}
	}
	if a, ok := c.live[id]; ok {
		return appView(a)
	}
	return appJSON{ID: id, Name: id}
}

// ref is nil for an empty id, so JSON gets null rather than an empty app.
func (c appCatalog) ref(id string) *appJSON {
	if id == "" {
		return nil
	}
	v := c.value(id)
	return &v
}

// topApp is the app a device moved the most bytes with, or nil if none.
func (s *server) topApp(mac string, p period, cat appCatalog) *appJSON {
	rows := s.h.Top(rollup.Filter{MAC: mac}, "app", p.from, p.now, 1)
	if len(rows) == 0 {
		return nil
	}
	return cat.ref(rows[0].Key)
}

func rowBytes(r rollup.Row) uint64 { return r.Bytes.Down + r.Bytes.Up }

func sumBytes(rows []rollup.Row) uint64 {
	var t uint64
	for _, r := range rows {
		t += rowBytes(r)
	}
	return t
}

// share is part over total, or 0 when nothing moved.
func share(part, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total)
}

func cut(rows []rollup.Row, n int) []rollup.Row {
	if len(rows) > n {
		return rows[:n]
	}
	return rows
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// addrText renders an address, or nothing for the zero value: a flow with no
// remote address should not say "invalid AddrPort".
func addrText(a netip.AddrPort) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// online is true when the device was seen within the online window, a flow
// of its moved bytes within it, or the neighbour table has it reachable now:
// a long download makes no new handshake and finishes no connection.
func online(d inventory.Device, now time.Time, active map[string]bool) bool {
	return !d.Last.Before(now.Add(-onlineWindow)) || active[d.MAC] || d.Reachable
}

// active is every device a live flow of which moved bytes within the online
// window.
func (s *server) active(now time.Time) map[string]bool {
	m := map[string]bool{}
	for _, f := range s.f.Live() {
		if !f.Updated.Before(now.Add(-onlineWindow)) {
			m[f.MAC] = true
		}
	}
	return m
}

func wanView(st wan.Status) wanJSON {
	j := wanJSON{
		Up: st.Up, Device: st.Device, IPv4: st.IPv4, IPv6: st.IPv6,
		UptimeSec: st.Uptime.Seconds(), RxRate: st.RxRate, TxRate: st.TxRate,
		LossPct: st.LossPct,
	}
	// Latency and jitter are measured only when a probe replied (LatencyOK).
	// Otherwise a 0 ms reading would look like a fast line.
	if st.LatencyOK {
		lat, jit := st.LatencyMs, st.JitterMs
		j.LatencyMs, j.JitterMs = &lat, &jit
	}
	return j
}

func (s *server) overview(r *http.Request) (any, error) {
	p, err := s.period(r)
	if err != nil {
		return nil, err
	}
	split, err := splitParam(r, "mac", "app", "none")
	if err != nil {
		return nil, err
	}
	cat := s.catalog()
	devs := s.inv.Devices()
	names := map[string]string{}
	for _, d := range devs {
		names[d.MAC] = d.Name
	}

	macRows := s.h.Top(rollup.Filter{}, "mac", p.from, p.now, 0)

	topClients := []topClientJSON{}
	for _, row := range cut(macRows, overviewTop) {
		c := topClientJSON{
			MAC: row.Key, Name: names[row.Key],
			Down: row.Bytes.Down, Up: row.Bytes.Up,
			TopApp: s.topApp(row.Key, p, cat), Countries: []string{},
			Other: !isClient(row.Key),
		}
		if c.Name == "" {
			c.Name = row.Key
		}
		for _, cr := range s.h.Top(rollup.Filter{MAC: row.Key, NotCC: lanCC}, "cc", p.from, p.now, overviewTop) {
			c.Countries = append(c.Countries, cr.Key)
		}
		topClients = append(topClients, c)
	}

	topApps := []topAppJSON{}
	for _, ar := range s.h.Top(rollup.Filter{}, "app", p.from, p.now, overviewTop) {
		clients := len(s.h.Top(rollup.Filter{App: ar.Key}, "mac", p.from, p.now, 0))
		topApps = append(topApps, topAppJSON{
			appJSON: cat.value(ar.Key), Clients: clients,
			Down: ar.Bytes.Down, Up: ar.Bytes.Up,
			Other: ar.Key == serviceKey,
		})
	}

	// A country's organisation is the one its dominant place reached: the
	// rows come sorted by bytes, so the first place seen for a country wins.
	orgOf := map[string]string{}
	for _, pr := range s.h.Top(rollup.Filter{}, "place", p.from, p.now, 0) {
		cc := pr.Extra["cc"]
		if _, seen := orgOf[cc]; !seen {
			orgOf[cc] = pr.Extra["org"]
		}
	}
	// Countries are places on the internet: the house's own traffic is not
	// one, and their shares are taken of what crossed the line.
	var wanRows []rollup.Row
	for _, cr := range s.h.Top(rollup.Filter{}, "cc", p.from, p.now, 0) {
		if cr.Key != lanCC {
			wanRows = append(wanRows, cr)
		}
	}
	total := sumBytes(wanRows)
	countries := []countryJSON{}
	for _, cr := range cut(wanRows, overviewTop) {
		countries = append(countries, countryJSON{
			CC: cr.Key, Org: orgOf[cr.Key], Down: cr.Bytes.Down, Up: cr.Bytes.Up,
			Share: share(rowBytes(cr), total), Other: cr.Key == serviceCC,
		})
	}

	var cl clientsJSON
	active := s.active(p.now)
	fresh := 0
	for _, d := range devs {
		if online(d, p.now, active) {
			cl.Online++
		}
		if !d.First.Before(p.now.Add(-newWindow)) {
			fresh++
		}
	}
	if s.svc == nil || !s.svc.Started().After(p.now.Add(-newWindow)) {
		cl.New24h = &fresh
	}
	var lost uint64
	if s.svc != nil {
		lost = s.svc.LostEvents()
	}

	return overviewJSON{
		WAN:        wanView(s.w.Status()),
		Clients:    cl,
		Series:     s.seriesOf(rollup.Filter{NotCC: lanCC}, p, seriesSplit(split)),
		TopClients: topClients,
		TopApps:    topApps,
		Countries:  countries,
		LiveFlows:  len(s.f.Live()),
		LostEvents: lost,
	}, nil
}

// realCountries counts the countries in a country list, leaving out the
// catch-all and the house's own traffic, which are not places the device
// talked to.
func realCountries(rows []rollup.Row) int {
	n := 0
	for _, r := range rows {
		if r.Key != serviceCC && r.Key != lanCC {
			n++
		}
	}
	return n
}

func (s *server) deviceView(d inventory.Device, p period, cat appCatalog, rates map[string]flows.Rate, totals map[string]rollup.Bytes, active map[string]bool) deviceJSON {
	rate := rates[d.MAC]
	t := totals[d.MAC]
	ipv4, ipv6 := d.IPv4, d.IPv6
	if ipv4 == nil {
		ipv4 = []string{}
	}
	if ipv6 == nil {
		ipv6 = []string{}
	}
	return deviceJSON{
		MAC: d.MAC, Name: d.Name, Hostname: d.Hostname, Vendor: d.Vendor,
		Label: d.Label, Group: d.Group, Random: d.Random,
		IPv4: ipv4, IPv6: ipv6,
		Link:  linkJSON{Kind: d.Link.Kind, Iface: d.Link.Iface, SSID: d.Link.SSID, Band: d.Link.Band, Signal: d.Link.Signal},
		First: ts(d.First), Last: ts(d.Last), Online: online(d, p.now, active),
		Rate:         rateJSON{Up: rate.Up, Down: rate.Down},
		Last24h:      bytesJSON{Down: t.Down, Up: t.Up},
		TopApp:       s.topApp(d.MAC, p, cat),
		CountryCount: realCountries(s.h.Top(rollup.Filter{MAC: d.MAC}, "cc", p.from, p.now, 0)),
	}
}

// deviceFor reads the {mac} path value and finds that device. A malformed MAC
// is a 400; a well-formed one the inventory does not know is a 404.
func (s *server) deviceFor(r *http.Request) (inventory.Device, error) {
	hw, err := net.ParseMAC(r.PathValue("mac"))
	if err != nil {
		return inventory.Device{}, httpError{http.StatusBadRequest, "mac must be a MAC address"}
	}
	for _, d := range s.inv.Devices() {
		if strings.EqualFold(d.MAC, hw.String()) {
			return d, nil
		}
	}
	return inventory.Device{}, httpError{http.StatusNotFound, "no device with that MAC"}
}

func (s *server) devices(r *http.Request) (any, error) {
	recent, recentSec, err := s.recent(r, rollup.Filter{}, "mac")
	if err != nil {
		return nil, err
	}
	p := s.day()
	cat, rates, totals, active := s.catalog(), s.f.Rates(), s.totals(p), s.active(p.now)
	out := []deviceJSON{}
	sec := s.security()
	for _, d := range s.inv.Devices() {
		j := withSecurity(s.deviceView(d, p, cat, rates, totals, active), sec)
		j.Recent, j.RecentSec = recent[d.MAC], recentSec
		out = append(out, j)
	}
	return out, nil
}

func (s *server) device(r *http.Request) (any, error) {
	d, err := s.deviceFor(r)
	if err != nil {
		return nil, err
	}
	recent, recentSec, err := s.recent(r, rollup.Filter{MAC: d.MAC}, "mac")
	if err != nil {
		return nil, err
	}
	p := s.day()
	j := withSecurity(s.deviceView(d, p, s.catalog(), s.f.Rates(), s.totals(p), s.active(p.now)), s.security())
	j.Recent, j.RecentSec = recent[d.MAC], recentSec
	return j, nil
}

// security is each device's isolation and quarantine, nil without the rules.
func (s *server) security() map[string]DeviceSecurity {
	if s.wr == nil {
		return nil
	}
	return s.wr.Security()
}

func withSecurity(j deviceJSON, sec map[string]DeviceSecurity) deviceJSON {
	d, ok := sec[j.MAC]
	if !ok {
		return j
	}
	j.Isolate, j.Quarantined, j.LikelySame = d.Isolate, d.Quarantined, d.LikelySame
	if i := d.Isolation; i != nil {
		j.Isolation = &isolationJSON{Routed: i.Routed, Bridged: i.Bridged, SSID: i.SSID, SSIDIsolated: i.SSIDIsolated,
			HardwareSwitch: i.HardwareSwitch, Text: i.Text}
	}
	return j
}

func (s *server) traffic(r *http.Request) (any, error) {
	d, err := s.deviceFor(r)
	if err != nil {
		return nil, err
	}
	p, err := s.period(r)
	if err != nil {
		return nil, err
	}
	split, err := splitParam(r, "app", "none")
	if err != nil {
		return nil, err
	}
	return s.seriesOf(rollup.Filter{MAC: d.MAC}, p, seriesSplit(split)), nil
}

func (s *server) apps(r *http.Request) (any, error) {
	d, err := s.deviceFor(r)
	if err != nil {
		return nil, err
	}
	p, err := s.period(r)
	if err != nil {
		return nil, err
	}
	cat := s.catalog()
	// Accepted decision: the share is taken over this device's total for the
	// period, not over the whole house, so one device's app shares add up to 1.
	recent, _, err := s.recent(r, rollup.Filter{MAC: d.MAC}, "app")
	if err != nil {
		return nil, err
	}
	all := s.h.Top(rollup.Filter{MAC: d.MAC}, "app", p.from, p.now, 0)
	total := sumBytes(all)
	out := []appUsageJSON{}
	for _, ar := range cut(all, maxDeviceApps) {
		f := rollup.Filter{MAC: d.MAC, App: ar.Key}
		u := appUsageJSON{
			App: cat.value(ar.Key), Down: ar.Bytes.Down, Up: ar.Bytes.Up,
			Flows: ar.Bytes.Flows, Share: share(rowBytes(ar), total),
			Domains: []domainJSON{}, Places: []placeJSON{},
			Other: ar.Key == serviceKey, Recent: recent[ar.Key],
		}
		for _, dr := range s.h.Top(f, "domain", p.from, p.now, maxDestinations) {
			u.Domains = append(u.Domains, domainJSON{
				Name: dr.Key, Src: dr.Extra["src"], Down: dr.Bytes.Down, Up: dr.Bytes.Up,
				Other: dr.Key == serviceKey,
			})
		}
		// A place is one country and one organisation the app reached.
		for _, pr := range s.h.Top(f, "place", p.from, p.now, maxDestinations) {
			u.Places = append(u.Places, placeJSON{
				CC: pr.Extra["cc"], Org: pr.Extra["org"], Down: pr.Bytes.Down, Up: pr.Bytes.Up,
				Other: pr.Key == serviceKey || pr.Extra["cc"] == serviceCC,
			})
		}
		out = append(out, u)
	}
	return out, nil
}

func (s *server) destinations(r *http.Request) (any, error) {
	d, err := s.deviceFor(r)
	if err != nil {
		return nil, err
	}
	p, err := s.period(r)
	if err != nil {
		return nil, err
	}
	cat := s.catalog()
	f := rollup.Filter{MAC: d.MAC}
	out := destinationsJSON{Countries: []destCountryJSON{}, Orgs: []destOrgJSON{}, Domains: []destDomainJSON{}}
	for _, cr := range s.h.Top(f, "cc", p.from, p.now, maxDestinations) {
		out.Countries = append(out.Countries, destCountryJSON{CC: cr.Key, Down: cr.Bytes.Down, Up: cr.Bytes.Up, Other: cr.Key == serviceCC})
	}
	for _, ar := range s.h.Top(f, "asn", p.from, p.now, maxDestinations) {
		out.Orgs = append(out.Orgs, destOrgJSON{ASN: ar.Key, Org: ar.Extra["org"], Down: ar.Bytes.Down, Up: ar.Bytes.Up, Other: ar.Key == serviceKey})
	}
	for _, dr := range s.h.Top(f, "domain", p.from, p.now, maxDestinations) {
		out.Domains = append(out.Domains, destDomainJSON{Name: dr.Key, App: cat.ref(dr.Extra["app"]), Down: dr.Bytes.Down, Up: dr.Bytes.Up, Other: dr.Key == serviceKey})
	}
	return out, nil
}

func (s *server) flows(r *http.Request) (any, error) {
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, httpError{http.StatusBadRequest, "limit must be a whole number"}
		}
		limit = min(n, maxLimit)
	}
	dir := flows.Direction(q.Get("dir"))
	switch dir {
	case "", flows.Out, flows.In, flows.Local:
	default:
		return nil, httpError{http.StatusBadRequest, "dir must be out, in or local"}
	}
	mac := ""
	if v := q.Get("mac"); v != "" {
		hw, err := net.ParseMAC(v)
		if err != nil {
			return nil, httpError{http.StatusBadRequest, "mac must be a MAC address"}
		}
		mac = hw.String()
	}
	var from, to time.Time
	for _, t := range []struct {
		name string
		at   *time.Time
	}{{"from", &from}, {"to", &to}} {
		if v := q.Get(t.name); v != "" {
			at, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return nil, httpError{http.StatusBadRequest, t.name + " must be a time like 2026-11-22T20:00:00Z"}
			}
			*t.at = at
		}
	}
	var asn uint32
	if v := q.Get("asn"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil || n == 0 {
			return nil, httpError{http.StatusBadRequest, "asn must be a network number"}
		}
		asn = uint32(n)
	}
	live := q.Get("live") == "1"
	var window time.Duration
	if live && q.Get("window") != "" {
		w, err := windowOf(r)
		if err != nil {
			return nil, err
		}
		window = w
	}
	rows, next := s.f.Query(flows.Query{
		MAC: mac, App: q.Get("app"), CC: q.Get("cc"), Text: q.Get("q"),
		Dir: dir, Live: live, Limit: limit, Cursor: q.Get("cursor"), Window: window,
		From: from, To: to, ASN: asn,
	})

	clients := map[string]string{}
	for _, d := range s.inv.Devices() {
		clients[d.MAC] = d.Name
	}
	page := flowsPage{Rows: make([]flowRow, 0, len(rows)), Next: next}
	for _, f := range rows {
		client := clients[f.MAC]
		if client == "" {
			client = f.MAC
		}
		page.Rows = append(page.Rows, flowRow{
			End: ts(f.End), Start: ts(f.Start), MAC: f.MAC, Client: client,
			Dir: string(f.Dir), Local: addrText(f.Local), Remote: addrText(f.Remote),
			Proto: f.Proto, Name: f.Name, NameSrc: f.NameSrc,
			App: appJSON{ID: f.App.ID, Name: f.App.Name, Category: f.App.Category},
			CC:  f.Geo.CC, ASN: f.Geo.ASN, Org: f.Geo.Org,
			Up: f.Up.Bytes, Down: f.Down.Bytes, Evicted: f.Evicted,
			WinUp: f.WinUp, WinDown: f.WinDown,
		})
	}
	return page, nil
}

// normMAC spells a MAC the way the flow table files it.
func normMAC(s string) string {
	if hw, err := net.ParseMAC(s); err == nil {
		return hw.String()
	}
	return strings.ToLower(s)
}

// liveApp is the id a live flow is shown under: one still waiting for its
// name has none yet, and the flow table's ring files it under unknown too.
func liveApp(f flows.Flow) string {
	if f.App.ID == "" {
		return enrich.Unknown.ID
	}
	return f.App.ID
}

func flowRate(f flows.Flow) float64 { return f.RateDown + f.RateUp }

// busier orders live flows by their rate now, then by what they moved, then
// by the newest, so equal rows do not swap places between polls.
func busier(a, b flows.Flow) bool {
	if ra, rb := flowRate(a), flowRate(b); ra != rb {
		return ra > rb
	}
	if ba, bb := a.Down.Bytes+a.Up.Bytes, b.Down.Bytes+b.Up.Bytes; ba != bb {
		return ba > bb
	}
	return a.Start.After(b.Start)
}

// live answers what a device is doing now, at the resolution of the flow
// table's Sync: its live connections, their apps and places, and the ring.
// The rate is the device's own over the last interval, which also counts
// flows that ended inside it.
func (s *server) live(r *http.Request) (any, error) {
	d, err := s.deviceFor(r)
	if err != nil {
		return nil, err
	}
	if s.onLivePoll != nil {
		s.onLivePoll()
	}
	// One key for the flows, the ring and the rate: the flow table files a
	// device under its MAC in net.HardwareAddr's spelling, whatever case the
	// inventory or the URL used.
	mac := normMAC(d.MAC)
	mine := s.f.LiveOf(mac)
	cat := appCatalog{rules: s.rules, live: map[string]enrich.App{}}
	for _, f := range mine {
		cat.live[f.App.ID] = f.App
	}
	sort.SliceStable(mine, func(i, j int) bool { return busier(mine[i], mine[j]) })
	points := s.f.LiveSeries(mac)
	now := s.now()
	rate := s.f.Rates()[mac]
	out := liveJSON{
		At: ts(now), Rate: rateJSON{Up: rate.Up, Down: rate.Down}, Flows: len(mine),
		Apps: []liveAppJSON{}, Conns: []liveConnJSON{}, Places: []livePlaceJSON{},
		Series: make([]livePointJSON, 0, len(points)),
	}
	if len(points) > 0 {
		out.StepSec = points[len(points)-1].Step.Seconds()
	}
	for _, p := range points {
		lp := livePointJSON{T: ts(p.At), StepSec: p.Step.Seconds(), Down: map[string]float64{}, Up: map[string]float64{}}
		// An app that moved nothing one way is left out of that map, as the
		// stacked chart expects.
		for _, a := range p.Apps {
			if a.Down > 0 {
				lp.Down[a.App] = float64(a.Down)
			}
			if a.Up > 0 {
				lp.Up[a.App] = float64(a.Up)
			}
		}
		out.Series = append(out.Series, lp)
	}
	spark := points
	if len(spark) > sparkPoints {
		spark = spark[len(spark)-sparkPoints:]
	}

	// mine is busiest first, so the first flow met of an app is its busiest
	// connection, and the first 50 met are the connections shown. Apps and
	// places are sorted by their own totals afterwards.
	apps := map[string]*liveAppJSON{}
	var appOrder []string
	type placeKey struct{ cc, org string }
	places := map[placeKey]*livePlaceJSON{}
	var placeOrder []placeKey
	for _, f := range mine {
		id := liveApp(f)
		a := apps[id]
		if a == nil {
			a = &liveAppJSON{
				App: cat.value(id), Domain: f.Name, Src: f.NameSrc, CC: f.Geo.CC, Org: f.Geo.Org,
				Spark: make([]float64, sparkPoints),
			}
			// A ring shorter than the sparkline is drawn right-aligned, the
			// time before it as nothing moving.
			for i, p := range spark {
				for _, ar := range p.Apps {
					if ar.App == id {
						a.Spark[sparkPoints-len(spark)+i] = float64(ar.Down)
					}
				}
			}
			apps[id] = a
			appOrder = append(appOrder, id)
		}
		a.Down += f.RateDown
		a.Up += f.RateUp
		a.Flows++
		pk := placeKey{f.Geo.CC, f.Geo.Org}
		p := places[pk]
		if p == nil {
			p = &livePlaceJSON{CC: f.Geo.CC, Org: f.Geo.Org, Other: f.Geo.CC == serviceCC}
			places[pk] = p
			placeOrder = append(placeOrder, pk)
		}
		p.Down += f.RateDown
		p.Up += f.RateUp
		if len(out.Conns) < maxLiveConns {
			out.Conns = append(out.Conns, liveConnJSON{
				App: cat.value(id), Name: f.Name, Src: f.NameSrc, CC: f.Geo.CC, Org: f.Geo.Org,
				ASN: f.Geo.ASN, Proto: f.Proto, Port: f.Remote.Port(), Local: f.Local.String(), Remote: f.Remote.String(),
				Down: f.RateDown, Up: f.RateUp, BytesDown: f.Down.Bytes, BytesUp: f.Up.Bytes,
				DurSec: max(now.Sub(f.Start), 0).Seconds(),
			})
		}
	}
	for _, id := range appOrder {
		out.Apps = append(out.Apps, *apps[id])
	}
	sort.SliceStable(out.Apps, func(i, j int) bool {
		a, b := out.Apps[i], out.Apps[j]
		if ra, rb := a.Down+a.Up, b.Down+b.Up; ra != rb {
			return ra > rb
		}
		return a.Flows > b.Flows
	})
	out.Apps = out.Apps[:min(len(out.Apps), maxDeviceApps)]
	for _, pk := range placeOrder {
		out.Places = append(out.Places, *places[pk])
	}
	sort.SliceStable(out.Places, func(i, j int) bool {
		return out.Places[i].Down+out.Places[i].Up > out.Places[j].Down+out.Places[j].Up
	})
	out.Places = out.Places[:min(len(out.Places), maxDestinations)]
	return out, nil
}
