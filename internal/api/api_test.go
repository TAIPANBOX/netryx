package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"netryx/internal/ct"
	"netryx/internal/enrich"
	"netryx/internal/flows"
	"netryx/internal/inventory"
	"netryx/internal/rollup"
	"netryx/internal/wan"
)

var now0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

const (
	laptop = "aa:bb:cc:dd:ee:01"
	phone  = "aa:bb:cc:dd:ee:02"
	tv     = "aa:bb:cc:dd:ee:03"
)

func clock() time.Time { return now0 }

type fakeFlows struct {
	live   []flows.Flow
	rates  map[string]flows.Rate
	series map[string][]flows.LivePoint
	rows   []flows.Flow
	next   string
	got    flows.Query
	// liveCalls counts reads of the whole live table.
	liveCalls int
}

func (f *fakeFlows) Live() []flows.Flow { f.liveCalls++; return f.live }

// LiveOf matches the MAC exactly, as the flow table does: the API must ask
// in the table's spelling.
func (f *fakeFlows) LiveOf(mac string) []flows.Flow {
	var out []flows.Flow
	for _, fl := range f.live {
		if fl.MAC == mac {
			out = append(out, fl)
		}
	}
	return out
}

func (f *fakeFlows) Rates() map[string]flows.Rate            { return f.rates }
func (f *fakeFlows) LiveSeries(mac string) []flows.LivePoint { return f.series[mac] }
func (f *fakeFlows) Query(q flows.Query) ([]flows.Flow, string) {
	f.got = q
	return f.rows, f.next
}

// fakeHistory answers Top from a table keyed by dimension and filter, so each
// test names exactly the queries the API is expected to make. Like the real
// store, Top cuts to n when n is positive.
type fakeHistory struct {
	points []rollup.Point
	top    map[string][]rollup.Row
	series []seriesCall
	// recent answers Recent by topKey; covered is the span it says it read,
	// spans the spans it was asked for.
	recent  map[string][]rollup.Row
	covered time.Duration
	spans   []time.Duration
	// sparks answers Sparks by dimension.
	sparks map[string]rollup.SparksOf
	// tops records every Top asked for.
	tops []topCall
	// newKeys answers NewKeys by dimension; newAsked records each call.
	newKeys  map[string][]rollup.NewRow
	newAsked []newCall
	// firstSeen answers FirstSeen by MAC.
	firstSeen map[string]time.Time
}

type newCall struct {
	mac, dim         string
	split, from, now time.Time
}

func (h *fakeHistory) NewKeys(mac, dim string, split, from, now time.Time) []rollup.NewRow {
	h.newAsked = append(h.newAsked, newCall{mac, dim, split, from, now})
	return h.newKeys[dim]
}

type topCall struct {
	dim      string
	from, to time.Time
}

func (h *fakeHistory) Sparks(f rollup.Filter, dim string, span, step time.Duration) rollup.SparksOf {
	return h.sparks[dim]
}

func (h *fakeHistory) Recent(f rollup.Filter, dim string, span time.Duration) ([]rollup.Row, time.Duration) {
	h.spans = append(h.spans, span)
	return h.recent[topKey(dim, f)], h.covered
}

type seriesCall struct {
	f        rollup.Filter
	from, to time.Time
	step     time.Duration
	split    string
}

func topKey(dim string, f rollup.Filter) string {
	return dim + "|" + f.MAC + "|" + f.App + "|" + f.CC
}

func (h *fakeHistory) Series(f rollup.Filter, from, to time.Time, step time.Duration, split string) []rollup.Point {
	h.series = append(h.series, seriesCall{f: f, from: from, to: to, step: step, split: split})
	return h.points
}

func (h *fakeHistory) FirstSeen(mac string, from time.Time) time.Time { return h.firstSeen[mac] }

func (h *fakeHistory) Top(f rollup.Filter, dim string, from, to time.Time, n int) []rollup.Row {
	h.tops = append(h.tops, topCall{dim: dim, from: from, to: to})
	rows := h.top[topKey(dim, f)]
	if n > 0 && len(rows) > n {
		rows = rows[:n]
	}
	return rows
}

type fakeApps map[string]enrich.App

func (f fakeApps) ByID(id string) (enrich.App, bool) {
	a, ok := f[id]
	return a, ok
}

type fakeInv struct{ devs []inventory.Device }

func (f fakeInv) Devices() []inventory.Device { return f.devs }

type fakeWAN struct{ st wan.Status }

func (f fakeWAN) Status() wan.Status { return f.st }

func row(key string, down, up uint64, flows uint64) rollup.Row {
	return rollup.Row{Key: key, Bytes: rollup.Bytes{Down: down, Up: up, Flows: flows}}
}

func withApp(r rollup.Row, app, src string) rollup.Row {
	r.Extra = map[string]string{"app": app, "src": src}
	return r
}

func withPlace(r rollup.Row, cc, asn, org string) rollup.Row {
	r.Extra = map[string]string{"cc": cc, "asn": asn, "org": org}
	return r
}

func withOrg(r rollup.Row, org string) rollup.Row {
	r.Extra = map[string]string{"org": org}
	return r
}

// fixture is one house: a laptop, a phone and a TV, two apps that the
// laptop and the phone share, and traffic that adds up to 6000 bytes down
// plus up across the whole period.
func fixture() (*fakeFlows, *fakeHistory, fakeInv, fakeWAN, fakeApps) {
	netflix := enrich.App{ID: "netflix", Name: "Netflix", Category: "video"}
	github := enrich.App{ID: "github", Name: "GitHub", Category: "dev"}

	fl := &fakeFlows{
		live: []flows.Flow{
			{
				MAC: laptop, Local: netip.MustParseAddrPort("192.168.1.10:51000"),
				Remote: netip.MustParseAddrPort("203.0.113.5:443"), Proto: 6, Dir: flows.Out,
				Name: "video.example", NameSrc: "sni", App: netflix,
				Geo:   enrich.ASInfo{CC: "DE", ASN: 2906, Org: "Netflix"},
				Start: now0.Add(-time.Minute), End: now0, Count: 1,
				Up: ct.Counters{Bytes: 500}, Down: ct.Counters{Bytes: 2000},
			},
			{
				MAC: laptop, Local: netip.MustParseAddrPort("192.168.1.10:51001"),
				Remote: netip.MustParseAddrPort("198.51.100.9:443"), Proto: 6, Dir: flows.Out,
				Name: "git.example", NameSrc: "sni", App: github,
				Geo:   enrich.ASInfo{CC: "DE", ASN: 16509, Org: "Amazon"},
				Start: now0.Add(-time.Minute), End: now0, Count: 1,
				Up: ct.Counters{Bytes: 500}, Down: ct.Counters{Bytes: 1000},
			},
		},
		rates: map[string]flows.Rate{laptop: {Up: 100, Down: 2000}},
	}

	h := &fakeHistory{
		points: []rollup.Point{{
			At: now0.Truncate(10 * time.Minute),
			By: map[string]rollup.Bytes{"": {Up: 100, Down: 2000}},
		}},
		top: map[string][]rollup.Row{
			// The whole house, by device, by app and by country.
			"mac|||": {
				row(laptop, 3000, 1000, 4),
				row(phone, 1000, 500, 3),
				row(tv, 500, 0, 1),
			},
			"app|||": {
				row("netflix", 2500, 500, 3),
				row("github", 1000, 500, 2),
				row("unknown", 1000, 500, 4),
			},
			"cc|||": {
				row("DE", 3000, 1000, 5),
				row("US", 1500, 500, 2),
			},
			// Clients of each app.
			"mac||netflix|": {row(laptop, 2000, 500, 3), row(phone, 500, 0, 1)},
			"mac||github|":  {row(laptop, 1000, 500, 2)},
			"mac||unknown|": {row(phone, 500, 500, 2), row(tv, 500, 0, 1)},
			// What each device runs and where it goes.
			"app|" + laptop + "||": {
				row("netflix", 2000, 500, 3),
				row("github", 1000, 500, 2),
			},
			"app|" + phone + "||": {
				row("unknown", 500, 500, 2),
				row("netflix", 500, 0, 1),
			},
			"app|" + tv + "||":    {row("unknown", 500, 0, 1)},
			"cc|" + laptop + "||": {row("DE", 3000, 1000, 5)},
			// Places of the whole house, dominant first: DE is Netflix's AS
			// (2500 bytes) ahead of Amazon's (1500).
			"place|||": {
				withPlace(row("DE/2906", 2000, 500, 3), "DE", "2906", "Netflix"),
				withPlace(row("DE/16509", 1000, 500, 2), "DE", "16509", "Amazon"),
				withPlace(row("US/15169", 1000, 500, 2), "US", "15169", "Google"),
			},
			"cc|" + phone + "||":   {row("US", 1000, 500, 2)},
			"cc|" + tv + "||":      {row("US", 500, 0, 1)},
			"asn|" + laptop + "||": {withOrg(row("2906", 2000, 500, 3), "Netflix"), withOrg(row("16509", 1000, 500, 2), "Amazon")},
			// Domains and places under one app of one device.
			"domain|" + laptop + "|netflix|": {withApp(row("video.example", 2000, 500, 3), "netflix", "sni")},
			"place|" + laptop + "|netflix|":  {withPlace(row("DE/2906", 2000, 500, 3), "DE", "2906", "Netflix")},
			"domain|" + laptop + "|github|":  {withApp(row("git.example", 1000, 500, 2), "github", "quic")},
			"place|" + laptop + "|github|":   {withPlace(row("DE/16509", 1000, 500, 2), "DE", "16509", "Amazon")},
			"domain|" + laptop + "||":        {withApp(row("video.example", 2000, 500, 3), "netflix", "sni"), withApp(row("git.example", 1000, 500, 2), "github", "quic")},
		},
	}

	inv := fakeInv{devs: []inventory.Device{
		{
			MAC: laptop, Name: "Laptop", Hostname: "laptop.lan", Vendor: "Acme", Group: "work",
			IPv4:  []string{"192.168.1.10"},
			Link:  inventory.Link{Kind: "wifi", Iface: "wl0", SSID: "home", Band: "5g", Signal: -50},
			First: now0.Add(-48 * time.Hour), Last: now0.Add(-time.Minute),
		},
		{MAC: phone, Name: "Phone", First: now0.Add(-2 * time.Hour), Last: now0.Add(-time.Hour)},
		{MAC: tv, Name: "TV", First: now0.Add(-30 * time.Hour), Last: now0.Add(-10 * time.Minute)},
	}}

	w := fakeWAN{st: wan.Status{
		Up: true, Device: "eth1", IPv4: "198.51.100.7", Uptime: 90 * time.Second,
		RxRate: 1000, TxRate: 200, LatencyMs: 12.5, JitterMs: 1.5, LossPct: 0,
	}}
	ap := fakeApps{netflix.ID: netflix, github.ID: github}
	return fl, h, inv, w, ap
}

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body.String())
	}
	return v
}

// field walks nested JSON objects; it returns nil when a key is missing, so a
// missing field fails the assertion rather than panicking.
func field(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func arr(v any) []any { a, _ := v.([]any); return a }

func at(v any, i int) any {
	a := arr(v)
	if i >= len(a) {
		return nil
	}
	return a[i]
}

func num(v any) float64 { f, _ := v.(float64); return f }
func str(v any) string  { s, _ := v.(string); return s }

func TestOverviewShape(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/overview?range=24h&split=none")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)

	for _, k := range []string{"wan", "clients", "series", "topClients", "topApps", "countries", "liveFlows"} {
		if _, ok := body.(map[string]any)[k]; !ok {
			t.Errorf("overview has no %q field", k)
		}
	}
	if got := num(field(body, "clients", "online")); got != 1 {
		t.Errorf("clients.online = %v, want 1 (only the laptop was seen in the last 5 minutes)", got)
	}
	if got := num(field(body, "clients", "new24h")); got != 1 {
		t.Errorf("clients.new24h = %v, want 1 (only the phone first appeared within 24 h)", got)
	}
	if got := num(field(body, "liveFlows")); got != 2 {
		t.Errorf("liveFlows = %v, want 2", got)
	}
	if got := str(field(body, "wan", "device")); got != "eth1" {
		t.Errorf("wan.device = %q, want eth1", got)
	}

	point := at(field(body, "series"), 0)
	if got := num(field(point, "down", "total")); got != 2000 {
		t.Errorf("series[0].down.total = %v, want 2000 (unsplit series is keyed total)", got)
	}
	if got := num(field(point, "up", "total")); got != 100 {
		t.Errorf("series[0].up.total = %v, want 100", got)
	}
	if _, err := time.Parse(time.RFC3339, str(field(point, "t"))); err != nil {
		t.Errorf("series[0].t is not RFC 3339: %v", err)
	}

	client := at(field(body, "topClients"), 0)
	if got := str(field(client, "mac")); got != laptop {
		t.Errorf("topClients[0].mac = %q, want %q", got, laptop)
	}
	if got := str(field(client, "name")); got != "Laptop" {
		t.Errorf("topClients[0].name = %q, want Laptop", got)
	}
	if got := num(field(client, "down")); got != 3000 {
		t.Errorf("topClients[0].down = %v, want 3000", got)
	}
	if got := str(field(client, "topApp", "id")); got != "netflix" {
		t.Errorf("topClients[0].topApp.id = %q, want netflix", got)
	}
	if got := field(client, "countries"); len(arr(got)) != 1 || str(at(got, 0)) != "DE" {
		t.Errorf("topClients[0].countries = %v, want [DE]", got)
	}

	app := at(field(body, "topApps"), 0)
	if got := str(field(app, "id")); got != "netflix" {
		t.Errorf("topApps[0].id = %q, want netflix", got)
	}
	if got := str(field(app, "name")); got != "Netflix" {
		t.Errorf("topApps[0].name = %q, want Netflix", got)
	}
	if got := str(field(app, "category")); got != "video" {
		t.Errorf("topApps[0].category = %q, want video", got)
	}
	if got := num(field(app, "clients")); got != 2 {
		t.Errorf("topApps[0].clients = %v, want 2", got)
	}

	country := at(field(body, "countries"), 0)
	if got := str(field(country, "cc")); got != "DE" {
		t.Errorf("countries[0].cc = %q, want DE", got)
	}
	if got := str(field(country, "org")); got != "Netflix" {
		t.Errorf("countries[0].org = %q, want Netflix (the dominant organisation of DE)", got)
	}
	// The organisation is a link to its network's page, so it carries its AS.
	if got := str(field(country, "asn")); got != "2906" {
		t.Errorf("countries[0].asn = %q, want 2906 (the network behind the organisation)", got)
	}
	if got := num(field(country, "share")); got < 0.66 || got > 0.67 {
		t.Errorf("countries[0].share = %v, want about 4000/6000", got)
	}
}

func TestDeviceAppsCarryDomainsAndPlaces(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/apps?range=24h")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	apps := decode(t, rec)

	first := at(apps, 0)
	if got := str(field(first, "app", "id")); got != "netflix" {
		t.Fatalf("apps[0].app.id = %q, want netflix", got)
	}
	if got := num(field(first, "down")); got != 2000 {
		t.Errorf("apps[0].down = %v, want 2000", got)
	}
	if got := num(field(first, "flows")); got != 3 {
		t.Errorf("apps[0].flows = %v, want 3", got)
	}
	if got := num(field(first, "share")); got < 0.6 || got > 0.7 {
		t.Errorf("apps[0].share = %v, want about 2500/4000", got)
	}
	domain := at(field(first, "domains"), 0)
	if got := str(field(domain, "name")); got != "video.example" {
		t.Errorf("apps[0].domains[0].name = %q, want video.example", got)
	}
	if got := num(field(domain, "up")); got != 500 {
		t.Errorf("apps[0].domains[0].up = %v, want 500", got)
	}
	if got := str(field(domain, "src")); got != "sni" {
		t.Errorf("apps[0].domains[0].src = %q, want sni (how the domain was named)", got)
	}
	place := at(field(first, "places"), 0)
	if got := str(field(place, "cc")); got != "DE" {
		t.Errorf("apps[0].places[0].cc = %q, want DE", got)
	}
	if got := str(field(place, "org")); got != "Netflix" {
		t.Errorf("apps[0].places[0].org = %q, want Netflix", got)
	}
}

func TestFlowsQueryPassesFiltersAndCapsLimit(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	fl.rows = fl.live
	fl.next = "c2"
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet,
		"/api/flows?q=video&mac=AA:BB:CC:DD:EE:01&app=netflix&cc=DE&dir=out&live=1&limit=9999&cursor=c1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := fl.got
	if got.Text != "video" || got.MAC != laptop || got.App != "netflix" || got.CC != "DE" {
		t.Errorf("query filters = text %q mac %q app %q cc %q, want video, %s, netflix, DE", got.Text, got.MAC, got.App, got.CC, laptop)
	}
	if got.Dir != flows.Out || !got.Live {
		t.Errorf("query dir %q live %v, want out and live", got.Dir, got.Live)
	}
	if got.Limit != 500 {
		t.Errorf("query limit = %d, want 500 (9999 is capped)", got.Limit)
	}
	if got.Cursor != "c1" {
		t.Errorf("query cursor = %q, want c1", got.Cursor)
	}
	if next := str(field(decode(t, rec), "next")); next != "c2" {
		t.Errorf("next = %q, want c2", next)
	}
	if row := at(field(decode(t, rec), "rows"), 0); str(field(row, "name")) != "video.example" {
		t.Errorf("rows[0].name = %v, want video.example", field(row, "name"))
	}
}

func TestUnknownRangeIs400(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	handler := New(fl, h, inv, w, ap, clock, nil)
	for _, target := range []string{
		"/api/overview?range=5y",
		"/api/devices/" + laptop + "/traffic?range=5y",
		"/api/devices/" + laptop + "/apps?range=5y",
		"/api/devices/" + laptop + "/destinations?range=5y",
	} {
		rec := get(t, handler, http.MethodGet, target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, want 400", target, rec.Code)
		}
		if str(field(decode(t, rec), "error")) == "" {
			t.Errorf("GET %s: body has no error text", target)
		}
	}
}

func TestUnknownDeviceIs404(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	handler := New(fl, h, inv, w, ap, clock, nil)
	for _, target := range []string{
		"/api/devices/00:00:00:00:00:99",
		"/api/devices/00:00:00:00:00:99/traffic",
		"/api/devices/00:00:00:00:00:99/apps",
		"/api/devices/00:00:00:00:00:99/destinations",
	} {
		rec := get(t, handler, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", target, rec.Code)
		}
		if str(field(decode(t, rec), "error")) == "" {
			t.Errorf("GET %s: body has no error text", target)
		}
	}
}

func TestAPIReturnsNamesVerbatim(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	const hostile = `<script>alert(1)</script>`
	inv.devs[0].Name = hostile
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if got := str(field(decode(t, rec), "name")); got != hostile {
		t.Errorf("name = %q, want the device name back verbatim", got)
	}
}

func TestEveryRouteSetsSafeHeaders(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	handler := New(fl, h, inv, w, ap, clock, nil)
	for _, target := range []string{
		"/api/overview",
		"/api/devices",
		"/api/devices/" + laptop,
		"/api/devices/" + laptop + "/traffic",
		"/api/devices/" + laptop + "/apps",
		"/api/devices/" + laptop + "/destinations",
		"/api/flows",
	} {
		rec := get(t, handler, http.MethodGet, target)
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", target, got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options = %q, want nosniff", target, got)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("GET %s: Content-Type = %q, want application/json; charset=utf-8", target, got)
		}
	}
}

func TestRejectsNonGETWithJSONError(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodPost, "/api/overview")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET" {
		t.Errorf("Allow = %q, want GET", got)
	}
	if str(field(decode(t, rec), "error")) == "" {
		t.Errorf("405 body has no error text")
	}
}

func TestLimitMustBeANumber(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	handler := New(fl, h, inv, w, ap, clock, nil)
	for _, target := range []string{"/api/flows?limit=abc", "/api/flows?limit=-3"} {
		rec := get(t, handler, http.MethodGet, target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, want 400", target, rec.Code)
		}
	}
}

func TestSplitValuesAreChecked(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	handler := New(fl, h, inv, w, ap, clock, nil)
	if rec := get(t, handler, http.MethodGet, "/api/overview?split=bogus"); rec.Code != http.StatusBadRequest {
		t.Errorf("overview split=bogus: status %d, want 400", rec.Code)
	}
	// A device's traffic cannot be split by device: it is one device already.
	if rec := get(t, handler, http.MethodGet, "/api/devices/"+laptop+"/traffic?split=mac"); rec.Code != http.StatusBadRequest {
		t.Errorf("device traffic split=mac: status %d, want 400", rec.Code)
	}
}

func TestSeriesStepPerRange(t *testing.T) {
	cases := []struct {
		rng  string
		step time.Duration
	}{
		{"1h", time.Minute},
		{"24h", 10 * time.Minute},
		{"7d", time.Hour},
		{"30d", 24 * time.Hour},
	}
	for _, c := range cases {
		fl, h, inv, w, ap := fixture()
		get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/overview?range="+c.rng)
		if len(h.series) != 1 {
			t.Fatalf("range %s: %d series calls, want 1", c.rng, len(h.series))
		}
		if got := h.series[0].step; got != c.step {
			t.Errorf("range %s: step %v, want %v", c.rng, got, c.step)
		}
		if got := h.series[0].to; !got.Equal(now0.Truncate(c.step)) {
			t.Errorf("range %s: to %v, want now truncated to the step", c.rng, got)
		}
	}
}

func TestDeviceListCarriesRateAndTotals(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	list := decode(t, rec)
	if n := len(arr(list)); n != 3 {
		t.Fatalf("devices = %d, want 3", n)
	}
	laptopRow := at(list, 0)
	if got := str(field(laptopRow, "mac")); got != laptop {
		t.Fatalf("devices[0].mac = %q, want %q", got, laptop)
	}
	if got := num(field(laptopRow, "rate", "down")); got != 2000 {
		t.Errorf("rate.down = %v, want 2000", got)
	}
	if got := num(field(laptopRow, "last24h", "down")); got != 3000 {
		t.Errorf("last24h.down = %v, want 3000", got)
	}
	if got := str(field(laptopRow, "topApp", "id")); got != "netflix" {
		t.Errorf("topApp.id = %q, want netflix", got)
	}
	if got := num(field(laptopRow, "countryCount")); got != 1 {
		t.Errorf("countryCount = %v, want 1", got)
	}
}

func TestDeviceTrafficSeriesKeysTotalWhenUnsplit(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/traffic?range=1h")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(h.series) != 1 {
		t.Fatalf("%d series calls, want 1", len(h.series))
	}
	if h.series[0].f.MAC != laptop || h.series[0].split != "" {
		t.Errorf("series call filter %q split %q, want %s and unsplit", h.series[0].f.MAC, h.series[0].split, laptop)
	}
	if got := num(field(at(decode(t, rec), 0), "down", "total")); got != 2000 {
		t.Errorf("down.total = %v, want 2000", got)
	}
}

func TestDestinationsListCountriesOrgsDomains(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/destinations")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if got := str(field(at(field(body, "countries"), 0), "cc")); got != "DE" {
		t.Errorf("countries[0].cc = %q, want DE", got)
	}
	if got := str(field(at(field(body, "orgs"), 0), "org")); got != "Netflix" {
		t.Errorf("orgs[0].org = %q, want Netflix", got)
	}
	if got := str(field(at(field(body, "domains"), 0), "name")); got != "video.example" {
		t.Errorf("domains[0].name = %q, want video.example", got)
	}
}

func TestMACIsNormalisedAndMalformedIs400(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	handler := New(fl, h, inv, w, ap, clock, nil)
	rec := get(t, handler, http.MethodGet, "/api/devices/AA-BB-CC-DD-EE-01")
	if rec.Code != http.StatusOK {
		t.Fatalf("upper-case dashed MAC: status %d, want 200", rec.Code)
	}
	if got := str(field(decode(t, rec), "mac")); got != laptop {
		t.Errorf("mac = %q, want %q", got, laptop)
	}
	if rec := get(t, handler, http.MethodGet, "/api/devices/zz"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed MAC: status %d, want 400", rec.Code)
	}
}

func TestWANLatencyIsNullWhenNoProbeReplied(t *testing.T) {
	cases := []struct {
		name     string
		st       wan.Status
		wantNull bool
		latency  float64
		jitter   float64
	}{
		{name: "an empty probe window", st: wan.Status{LatencyOK: false, LatencyMs: 0, LossPct: 0}, wantNull: true},
		{name: "a dead line", st: wan.Status{Up: false, LatencyOK: false, LatencyMs: 0, LossPct: 100}, wantNull: true},
		{name: "a line that answers", st: wan.Status{Up: true, LatencyOK: true, LatencyMs: 9.8, JitterMs: 1.2}, latency: 9.8, jitter: 1.2},
	}
	for _, c := range cases {
		fl, h, inv, _, ap := fixture()
		body := decode(t, get(t, New(fl, h, inv, fakeWAN{st: c.st}, ap, clock, nil), http.MethodGet, "/api/overview"))
		wanObj, _ := field(body, "wan").(map[string]any)
		for _, k := range []string{"latencyMs", "jitterMs"} {
			v, ok := wanObj[k]
			if !ok {
				t.Errorf("%s: wan.%s is missing", c.name, k)
				continue
			}
			if c.wantNull && v != nil {
				t.Errorf("%s: wan.%s = %v, want null", c.name, k, v)
			}
		}
		if !c.wantNull {
			if got := num(field(body, "wan", "latencyMs")); got != c.latency {
				t.Errorf("%s: wan.latencyMs = %v, want %v", c.name, got, c.latency)
			}
			if got := num(field(body, "wan", "jitterMs")); got != c.jitter {
				t.Errorf("%s: wan.jitterMs = %v, want %v", c.name, got, c.jitter)
			}
		}
	}
}

// byKey finds the row of a list whose string field key equals want.
func byKey(list any, key, want string) map[string]any {
	for _, v := range arr(list) {
		m, _ := v.(map[string]any)
		if s, _ := m[key].(string); s == want {
			return m
		}
	}
	return nil
}

func TestServiceRowsAreMarked(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	// The rollup's catch-all rows: a device, an app, a country and a domain,
	// each named "other" or "--", added to the lists the API reads.
	h.top["mac|||"] = append(h.top["mac|||"], row("other", 100, 0, 1))
	h.top["app|||"] = append(h.top["app|||"], row("other", 100, 0, 1))
	h.top["cc|||"] = append(h.top["cc|||"], row("--", 100, 0, 1))
	h.top["app|"+laptop+"||"] = append(h.top["app|"+laptop+"||"], row("other", 100, 0, 1))
	h.top["cc|"+laptop+"||"] = append(h.top["cc|"+laptop+"||"], row("--", 100, 0, 1))
	h.top["domain|"+laptop+"||"] = append(h.top["domain|"+laptop+"||"], withApp(row("other", 100, 0, 1), "", ""))
	h.top["asn|"+laptop+"||"] = append(h.top["asn|"+laptop+"||"], withOrg(row("other", 100, 0, 1), ""))
	h.top["place|"+laptop+"|netflix|"] = append(h.top["place|"+laptop+"|netflix|"], row("other", 100, 0, 1))
	h.top["app|||"] = append(h.top["app|||"], row("other", 100, 0, 1))

	overview := decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/overview"))
	if got, ok := byKey(field(overview, "topClients"), "mac", "other")["other"].(bool); !ok || !got {
		t.Errorf("topClients row for the catch-all device is not marked other")
	}
	if got := byKey(field(overview, "topClients"), "mac", laptop); got == nil || got["other"] != false {
		t.Errorf("topClients row for the laptop should carry other:false, got %v", got)
	}
	if got := byKey(field(overview, "topApps"), "id", "other"); got == nil || got["other"] != true {
		t.Errorf("topApps row for app other is not marked other: %v", got)
	}
	if got := byKey(field(overview, "countries"), "cc", "--"); got == nil || got["other"] != true {
		t.Errorf("countries row for -- is not marked other: %v", got)
	}

	apps := decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/apps"))
	for _, a := range arr(apps) {
		m, _ := a.(map[string]any)
		id := str(field(m, "app", "id"))
		if id == "other" && m["other"] != true {
			t.Errorf("device app other is not marked other")
		}
		if id == "netflix" && m["other"] != false {
			t.Errorf("device app netflix should carry other:false")
		}
		if id == "netflix" {
			place := byKey(m["places"], "cc", "")
			if place == nil || place["other"] != true {
				t.Errorf("place of the catch-all in netflix is not marked other: %v", place)
			}
		}
	}

	dests := decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/destinations"))
	if got := byKey(field(dests, "countries"), "cc", "--"); got == nil || got["other"] != true {
		t.Errorf("destination country -- is not marked other: %v", got)
	}
	if got := byKey(field(dests, "domains"), "name", "other"); got == nil || got["other"] != true {
		t.Errorf("destination domain other is not marked other: %v", got)
	}
	if got := byKey(field(dests, "orgs"), "asn", "other"); got == nil || got["other"] != true {
		t.Errorf("destination organisation other is not marked other: %v", got)
	}

	// The laptop talks to one real country; the -- row is not a country.
	dev := decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop))
	if got := num(field(dev, "countryCount")); got != 1 {
		t.Errorf("countryCount = %v, want 1: the -- row is not a country", got)
	}
}

func TestZeroAddressRendersEmpty(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	fl.rows = []flows.Flow{{MAC: laptop, Dir: flows.Out}}
	rows := field(decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/flows")), "rows")
	if got := str(field(at(rows, 0), "local")); got != "" {
		t.Errorf("local = %q for a zero address, want empty", got)
	}
	if got := str(field(at(rows, 0), "remote")); got != "" {
		t.Errorf("remote = %q for a zero address, want empty", got)
	}
}

// Errors and unknown routes keep the same headers and JSON body as answers.
func TestErrorsCarrySafeHeadersAndJSON(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	handler := New(fl, h, inv, w, ap, clock, nil)
	cases := []struct {
		method, target string
		code           int
	}{
		{http.MethodGet, "/api/overview?range=5y", http.StatusBadRequest},
		{http.MethodGet, "/api/devices/00:00:00:00:00:99", http.StatusNotFound},
		{http.MethodPost, "/api/overview", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/x", http.StatusNotFound},
	}
	for _, c := range cases {
		rec := get(t, handler, c.method, c.target)
		if rec.Code != c.code {
			t.Errorf("%s %s: status %d, want %d", c.method, c.target, rec.Code, c.code)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("%s %s: Content-Type = %q", c.method, c.target, got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s %s: Cache-Control = %q", c.method, c.target, got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s %s: X-Content-Type-Options = %q", c.method, c.target, got)
		}
		if str(field(decode(t, rec), "error")) == "" {
			t.Errorf("%s %s: body is not a JSON error", c.method, c.target)
		}
	}
}

func TestFlowRowsNameTheClient(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	fl.rows = []flows.Flow{fl.live[0]}
	fl.rows[0].MAC = laptop
	stranger := fl.live[0]
	stranger.MAC = "aa:bb:cc:dd:ee:99"
	fl.rows = append(fl.rows, stranger)
	rows := field(decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/flows")), "rows")
	if got := str(field(at(rows, 0), "client")); got != "Laptop" {
		t.Errorf("rows[0].client = %q, want Laptop", got)
	}
	if got := str(field(at(rows, 1), "client")); got != "aa:bb:cc:dd:ee:99" {
		t.Errorf("rows[1].client = %q, want the MAC when the device is unnamed", got)
	}
}

func TestAppNamesComeFromTheCatalogWithoutLiveFlows(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	// No live flow carries an app now, so only the catalog and the id remain.
	fl.live = nil
	// github is missing from the catalog too: its name must fall back to its id.
	delete(ap, "github")

	apps := decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/apps?range=24h"))
	if got := str(field(at(apps, 0), "app", "name")); got != "Netflix" {
		t.Errorf("apps[0].app.name = %q without live flows, want Netflix from the catalog", got)
	}
	if got := str(field(at(apps, 0), "app", "category")); got != "video" {
		t.Errorf("apps[0].app.category = %q, want video from the catalog", got)
	}
	if got := str(field(at(apps, 1), "app", "name")); got != "github" {
		t.Errorf("apps[1].app.name = %q, want the id github when no source names it", got)
	}

	overview := decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/overview"))
	if got := str(field(at(field(overview, "topApps"), 0), "name")); got != "Netflix" {
		t.Errorf("topApps[0].name = %q, want Netflix from the catalog", got)
	}
}

// fakeService is the running service: when it started and how many
// connection events the kernel could not deliver.
type fakeService struct {
	started time.Time
	lost    uint64
}

func (f fakeService) Started() time.Time { return f.started }
func (f fakeService) LostEvents() uint64 { return f.lost }

// A series may mix steps (hours before a restart, minutes after it), so each
// point says how long it covers, and the console divides by that rather than
// by the step the range would normally use.
func TestTheSeriesSaysWhichStepItUsed(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	h.points = []rollup.Point{
		{At: now0.Add(-time.Hour), Step: 30 * time.Minute, By: map[string]rollup.Bytes{"": {Down: 3600}}},
		{At: now0.Add(-30 * time.Minute), Step: time.Minute, By: map[string]rollup.Bytes{"": {Down: 60}}},
	}
	handler := New(fl, h, inv, w, ap, clock, nil)
	for _, path := range []string{"/api/overview?range=1h", "/api/devices/" + laptop + "/traffic?range=1h"} {
		v := decode(t, get(t, handler, http.MethodGet, path))
		series := v
		if strings.HasPrefix(path, "/api/overview") {
			series = field(v, "series")
		}
		if got := num(field(at(series, 0), "stepSec")); got != 1800 {
			t.Errorf("%s: the first point covers 1800 s: %v", path, got)
		}
		if got := num(field(at(series, 1), "stepSec")); got != 60 {
			t.Errorf("%s: the second point covers 60 s: %v", path, got)
		}
	}
}

// The classifier names an unclassified flow "Unknown · <organisation>", and
// that organisation belongs to that one flow. The app "unknown" across the
// house is named Unknown, never after whichever live flow happened to be
// read last.
func TestUnknownIsNeverNamedAfterOneFlow(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	fl.live = append(fl.live, flows.Flow{
		MAC: phone, Local: netip.MustParseAddrPort("192.168.1.11:50000"),
		Remote: netip.MustParseAddrPort("1.1.1.1:443"), Proto: 6, Dir: flows.Out,
		App: enrich.App{ID: "unknown", Name: "Unknown · CLOUDFLARENET", Category: "unknown"},
	})
	v := decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/overview?range=24h"))
	for _, a := range arr(field(v, "topApps")) {
		if str(field(a, "id")) == "unknown" && str(field(a, "name")) != "Unknown" {
			t.Fatalf("Unknown is named after one flow: %v", a)
		}
	}
}

// A device in a long download makes no new handshake and finishes no
// connection for an hour, yet it is plainly online: a flow of its moved bytes
// a minute ago. So is a device the kernel has just found reachable.
func TestALongDownloadKeepsADeviceOnline(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	// The phone was last seen an hour ago; its download moved bytes a minute ago.
	fl.live = append(fl.live, flows.Flow{MAC: phone, Updated: now0.Add(-time.Minute), Start: now0.Add(-2 * time.Hour)})
	// The TV was seen long ago too, but the neighbour table has it reachable.
	inv.devs[2].Last = now0.Add(-3 * time.Hour)
	inv.devs[2].Reachable = true
	handler := New(fl, h, inv, w, ap, clock, nil)
	online := map[string]bool{}
	for _, d := range arr(decode(t, get(t, handler, http.MethodGet, "/api/devices"))) {
		online[str(field(d, "mac"))], _ = field(d, "online").(bool)
	}
	if !online[phone] || !online[tv] || !online[laptop] {
		t.Fatalf("every device is online: %v", online)
	}
	v := decode(t, get(t, handler, http.MethodGet, "/api/overview?range=24h"))
	if got := num(field(v, "clients", "online")); got != 3 {
		t.Fatalf("three clients online: %v", got)
	}
}

// "New in the last 24 h" means nothing until the service has watched for 24
// h: after a fresh start every device would be new. Until then it is null.
func TestNewDevicesWaitForADayOfRunning(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	young := New(fl, h, inv, w, ap, clock, fakeService{started: now0.Add(-2 * time.Hour)})
	v := decode(t, get(t, young, http.MethodGet, "/api/overview?range=24h"))
	if c, ok := field(v, "clients").(map[string]any); !ok || c["new24h"] != nil {
		t.Fatalf("new24h is null before a day of running: %v", field(v, "clients"))
	}
	old := New(fl, h, inv, w, ap, clock, fakeService{started: now0.Add(-25 * time.Hour)})
	v = decode(t, get(t, old, http.MethodGet, "/api/overview?range=24h"))
	if got := num(field(v, "clients", "new24h")); got != 1 { // the phone, first seen 2 h ago
		t.Fatalf("after a day it counts: %v", field(v, "clients"))
	}
}

// The kernel can drop connection events when the router is busy, and the
// console says how many rather than looking complete.
func TestLostConnectionEventsAreShown(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	v := decode(t, get(t, New(fl, h, inv, w, ap, clock, fakeService{started: now0.Add(-48 * time.Hour), lost: 7}), http.MethodGet, "/api/overview"))
	if got, ok := field(v, "lostEvents").(float64); !ok || got != 7 {
		t.Fatalf("lostEvents: %v", field(v, "lostEvents"))
	}
}

// Traffic between two devices at home never crossed the line. The WAN chart
// and the countries leave it out; a device's own views still show it, under
// the country LAN.
func TestLocalTrafficIsNotWAN(t *testing.T) {
	store := rollup.NewStore(rollup.Options{Now: clock})
	at := now0.Add(-10 * time.Minute)
	store.Add(flows.Delta{At: at, MAC: laptop, App: "web", CC: "US", Up: 100, Down: 900, First: true})
	store.Add(flows.Delta{At: at, MAC: laptop, App: "smb", CC: flows.LANCountry, Up: 5000, Down: 50000, First: true})
	fl, _, inv, w, ap := fixture()
	handler := New(fl, store, inv, w, ap, clock, nil)

	v := decode(t, get(t, handler, http.MethodGet, "/api/overview?range=1h&split=none"))
	var down float64
	for _, p := range arr(field(v, "series")) {
		down += num(field(p, "down", "total"))
	}
	if down != 900 {
		t.Fatalf("the WAN chart holds only what crossed the line: %v", down)
	}
	for _, c := range arr(field(v, "countries")) {
		if str(field(c, "cc")) == flows.LANCountry {
			t.Fatalf("the house is not a country: %v", field(v, "countries"))
		}
	}
	if got := num(field(at0(arr(field(v, "countries"))), "share")); got != 1 {
		t.Fatalf("the US is all of the WAN traffic: %v", got)
	}
	d := decode(t, get(t, handler, http.MethodGet, "/api/devices/"+laptop+"/destinations?range=1h"))
	found := false
	for _, c := range arr(field(d, "countries")) {
		found = found || str(field(c, "cc")) == flows.LANCountry
	}
	if !found {
		t.Fatalf("the device's own view shows its local traffic: %v", d)
	}
}

func at0(a []any) any {
	if len(a) == 0 {
		return nil
	}
	return a[0]
}

// The house's own traffic is not a country: a device that talked to the US
// and to another device at home has talked to one country.
func TestLANIsNotACountry(t *testing.T) {
	store := rollup.NewStore(rollup.Options{Now: clock})
	at := now0.Add(-10 * time.Minute)
	store.Add(flows.Delta{At: at, MAC: laptop, App: "web", CC: "US", Up: 100, Down: 900, First: true})
	store.Add(flows.Delta{At: at, MAC: laptop, App: "smb", CC: flows.LANCountry, Up: 5000, Down: 50000, First: true})
	fl, _, inv, w, ap := fixture()
	handler := New(fl, store, inv, w, ap, clock, nil)
	d := decode(t, get(t, handler, http.MethodGet, "/api/devices/"+laptop))
	if got := num(field(d, "countryCount")); got != 1 {
		t.Fatalf("one country, not the house: %v", got)
	}
	v := decode(t, get(t, handler, http.MethodGet, "/api/overview?range=1h"))
	for _, c := range arr(field(v, "topClients")) {
		for _, cc := range arr(field(c, "countries")) {
			if str(cc) == flows.LANCountry {
				t.Fatalf("a top client lists the house as a country: %v", c)
			}
		}
	}
}

// liveFlow is a live connection of a device moving at a rate now.
func liveFlow(mac, remote string, app enrich.App, name, cc, org string, down, up float64) flows.Flow {
	return flows.Flow{
		MAC: mac, Local: netip.MustParseAddrPort("192.168.1.10:50000"),
		Remote: netip.MustParseAddrPort(remote), Proto: 6, Dir: flows.Out,
		Name: name, NameSrc: "sni", App: app, Geo: enrich.ASInfo{CC: cc, ASN: 64500, Org: org},
		Start: now0.Add(-90 * time.Second), Updated: now0,
		Up: ct.Counters{Bytes: uint64(up) * 10}, Down: ct.Counters{Bytes: uint64(down) * 10},
		RateUp: up, RateDown: down,
	}
}

// A device's live view says what it is doing now: its rate, its apps with
// their busiest connection and a sparkline, its connections busiest first,
// the places it reaches (the house and the unknown included, marked like
// elsewhere), and the ring the chart draws, each point with its step.
func TestTheLiveViewShowsAppsConnectionsAndPlaces(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	netflix, github := ap["netflix"], ap["github"]
	smb := enrich.App{ID: "smb", Name: "Files", Category: "lan"}
	fl.live = []flows.Flow{
		liveFlow(laptop, "203.0.113.5:443", netflix, "video.example", "DE", "Netflix", 3000, 100),
		liveFlow(laptop, "203.0.113.6:443", netflix, "cdn.example", "DE", "Netflix", 1000, 50),
		liveFlow(laptop, "198.51.100.9:443", github, "git.example", "US", "GitHub", 200, 400),
		liveFlow(laptop, "192.168.1.20:445", smb, "", flows.LANCountry, "", 500, 500),
		liveFlow(laptop, "192.0.2.1:443", enrich.App{}, "", "--", "", 10, 10),
		liveFlow(phone, "203.0.113.5:443", netflix, "video.example", "DE", "Netflix", 9999, 9999),
	}
	fl.rates = map[string]flows.Rate{laptop: {Up: 1060, Down: 4710}}
	var pts []flows.LivePoint
	for i := 0; i < 70; i++ {
		pts = append(pts, flows.LivePoint{
			At: now0.Add(time.Duration(i-69) * 2 * time.Second), Step: 2 * time.Second,
			Apps: []flows.AppRate{{App: "netflix", Down: float32(i)}},
		})
	}
	pts[69].Step = 5 * time.Second
	fl.series = map[string][]flows.LivePoint{laptop: pts}

	handler := New(fl, h, inv, w, ap, clock, nil)
	v := decode(t, get(t, handler, http.MethodGet, "/api/devices/"+strings.ToUpper(laptop)+"/live"))
	if str(field(v, "at")) != ts(now0) || num(field(v, "stepSec")) != 5 {
		t.Fatalf("when, and the step of the newest point: %v %v", field(v, "at"), field(v, "stepSec"))
	}
	if num(field(v, "rate", "down")) != 4710 || num(field(v, "rate", "up")) != 1060 {
		t.Fatalf("the device's rate now: %v", field(v, "rate"))
	}
	if num(field(v, "flows")) != 5 {
		t.Fatalf("five live connections, the phone's not among them: %v", field(v, "flows"))
	}

	apps := arr(field(v, "apps"))
	if len(apps) != 4 {
		t.Fatalf("netflix, smb, github and unknown: %v", apps)
	}
	nf := apps[0]
	if str(field(nf, "app", "id")) != "netflix" || str(field(nf, "app", "name")) != "Netflix" ||
		num(field(nf, "down")) != 4000 || num(field(nf, "up")) != 150 || num(field(nf, "flows")) != 2 {
		t.Fatalf("netflix first, both its connections added up: %v", nf)
	}
	if str(field(nf, "domain")) != "video.example" || str(field(nf, "src")) != "sni" ||
		str(field(nf, "cc")) != "DE" || str(field(nf, "org")) != "Netflix" {
		t.Fatalf("an app shows its busiest connection: %v", nf)
	}
	spark := arr(field(nf, "spark"))
	if len(spark) != 60 || num(spark[0]) != 10 || num(spark[59]) != 69 {
		t.Fatalf("the sparkline is the app's last 60 down rates: %d %v", len(spark), spark)
	}
	if str(field(apps[3], "app", "id")) != "unknown" || str(field(apps[3], "app", "name")) != "Unknown" {
		t.Fatalf("a connection with no app yet is Unknown: %v", apps[3])
	}
	if s := arr(field(apps[3], "spark")); len(s) != 60 || num(s[59]) != 0 {
		t.Fatalf("an app absent from the ring draws zeros: %v", s)
	}

	conns := arr(field(v, "conns"))
	if len(conns) != 5 {
		t.Fatalf("every connection of the device: %v", conns)
	}
	c0 := conns[0]
	if str(field(c0, "name")) != "video.example" || str(field(c0, "app", "id")) != "netflix" ||
		num(field(c0, "down")) != 3000 || num(field(c0, "up")) != 100 ||
		num(field(c0, "bytesDown")) != 30000 || num(field(c0, "bytesUp")) != 1000 ||
		num(field(c0, "durSec")) != 90 || num(field(c0, "port")) != 443 || num(field(c0, "proto")) != 6 ||
		str(field(c0, "cc")) != "DE" || str(field(c0, "org")) != "Netflix" || num(field(c0, "asn")) != 64500 ||
		str(field(c0, "src")) != "sni" {
		t.Fatalf("the busiest connection first, with everything the card shows: %v", c0)
	}
	for i := 1; i < len(conns); i++ {
		if num(field(conns[i], "down"))+num(field(conns[i], "up")) > num(field(conns[i-1], "down"))+num(field(conns[i-1], "up")) {
			t.Fatalf("connections busiest first: %v", conns)
		}
	}

	places := arr(field(v, "places"))
	if len(places) != 4 {
		t.Fatalf("DE/Netflix, the house, US/GitHub and the unknown: %v", places)
	}
	// A place's organisation leads to its network, so the place carries its AS.
	if num(field(places[0], "asn")) != 64500 {
		t.Errorf("places[0].asn = %v, want 64500", field(places[0], "asn"))
	}
	if str(field(places[0], "cc")) != "DE" || num(field(places[0], "down")) != 4000 || field(places[0], "other") != false {
		t.Fatalf("the busiest place first: %v", places[0])
	}
	if str(field(places[1], "cc")) != flows.LANCountry || field(places[1], "other") != false {
		t.Fatalf("the device's own view keeps the house: %v", places[1])
	}
	if str(field(places[3], "cc")) != "--" || field(places[3], "other") != true {
		t.Fatalf("an unknown place is marked: %v", places[3])
	}

	series := arr(field(v, "series"))
	if len(series) != 70 || num(field(series[69], "stepSec")) != 5 || num(field(series[69], "down", "netflix")) != 69 ||
		str(field(series[69], "t")) != ts(now0) || field(series[69], "up") == nil {
		t.Fatalf("the ring, each point with its step: %d %v", len(series), at(series, 69))
	}

	if rec := get(t, handler, http.MethodGet, "/api/devices/aa:bb:cc:dd:ee:99/live"); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown device is 404: %d", rec.Code)
	}
	if rec := get(t, handler, http.MethodGet, "/api/devices/router/live"); rec.Code != http.StatusBadRequest {
		t.Fatalf("the router is not a device: %d", rec.Code)
	}
}

// A device with hundreds of short connections answers with its 50 busiest,
// its apps added up, and a small body.
func TestTheLiveViewIsBounded(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	fl.live = nil
	for i := 0; i < 400; i++ {
		f := liveFlow(laptop, "203.0.113."+strconv.Itoa(1+i%250)+":"+strconv.Itoa(1000+i), ap["netflix"],
			"n"+strconv.Itoa(i)+".example", "DE", "Netflix", float64(i), 1)
		fl.live = append(fl.live, f)
	}
	var pts []flows.LivePoint
	for i := 0; i < 150; i++ {
		pts = append(pts, flows.LivePoint{At: now0, Step: 2 * time.Second, Apps: []flows.AppRate{{App: "netflix", Down: 1}}})
	}
	fl.series = map[string][]flows.LivePoint{laptop: pts}
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/live")
	v := decode(t, rec)
	conns := arr(field(v, "conns"))
	if len(conns) != 50 {
		t.Fatalf("at most 50 connections, not %d", len(conns))
	}
	if num(field(conns[0], "down")) != 399 || num(field(conns[49], "down")) != 350 {
		t.Fatalf("the 50 busiest: %v .. %v", field(conns[0], "down"), field(conns[49], "down"))
	}
	if apps := arr(field(v, "apps")); len(apps) != 1 || num(field(apps[0], "flows")) != 400 {
		t.Fatalf("one app with all 400 connections: %v", apps)
	}
	if num(field(v, "flows")) != 400 {
		t.Fatalf("the count is of every connection: %v", field(v, "flows"))
	}
	if n := len(arr(field(v, "places"))); n > maxDestinations {
		t.Fatalf("places are bounded: %d", n)
	}
	if n := rec.Body.Len(); n > 48<<10 {
		t.Fatalf("the answer stays small: %d bytes", n)
	}
}

// Every live poll is reported, so the service can sync faster while someone
// watches; other routes and a device that does not exist are not polls.
func TestALivePollIsReported(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	polls := 0
	handler := New(fl, h, inv, w, ap, clock, nil, OnLivePoll(func() { polls++ }))
	get(t, handler, http.MethodGet, "/api/devices/"+laptop+"/live")
	get(t, handler, http.MethodGet, "/api/devices/"+laptop+"/live")
	get(t, handler, http.MethodGet, "/api/devices/"+laptop)
	get(t, handler, http.MethodGet, "/api/devices/aa:bb:cc:dd:ee:99/live")
	if polls != 2 {
		t.Fatalf("two live polls, not %d", polls)
	}
	// Without the option the route still answers.
	if rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/live"); rec.Code != http.StatusOK {
		t.Fatalf("no callback is no error: %d", rec.Code)
	}
}

// The live view asks the flow table for one device, under the one spelling
// of its MAC the table files it by, for its flows, its ring and its rate
// alike, and never reads the whole table.
func TestTheLiveViewReadsOneDeviceByOneKey(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	inv.devs[0].MAC = strings.ToUpper(laptop)
	fl.rates = map[string]flows.Rate{laptop: {Up: 7, Down: 70}}
	fl.series = map[string][]flows.LivePoint{laptop: {{At: now0, Step: 2 * time.Second,
		Apps: []flows.AppRate{{App: "netflix", Up: 7, Down: 70}}}}}
	v := decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/live"))
	if num(field(v, "flows")) != 2 || num(field(v, "rate", "down")) != 70 || len(arr(field(v, "series"))) != 1 ||
		num(field(at(field(v, "series"), 0), "up", "netflix")) != 7 {
		t.Fatalf("flows, rate and ring under the same key: %v", v)
	}
	if fl.liveCalls != 0 {
		t.Fatalf("the live view read the whole table %d times", fl.liveCalls)
	}
}

// Pairs answers as the store does: for each key of dim, Top with that key
// set, ranked by by.
// Holders is what Top answers under each device's and each app's filter, as
// the store's one pass answers it.
func (h *fakeHistory) Holders(dim, key string, from, to time.Time) rollup.HoldersOf {
	var out rollup.HoldersOf
	sum := func(f rollup.Filter) (rollup.Bytes, bool) {
		var b rollup.Bytes
		ok := false
		for _, r := range h.Top(f, dim, from, to, 0) {
			if strings.EqualFold(r.Key, key) {
				b.Down, b.Up, ok = b.Down+r.Bytes.Down, b.Up+r.Bytes.Up, true
				if r.Extra["org"] != "" {
					out.Org = r.Extra["org"]
				}
			}
		}
		return b, ok
	}
	for _, m := range h.Top(rollup.Filter{}, "mac", from, to, 0) {
		if b, ok := sum(rollup.Filter{MAC: m.Key}); ok {
			out.MACs = append(out.MACs, rollup.Row{Key: m.Key, Bytes: b})
		}
	}
	for _, a := range h.Top(rollup.Filter{}, "app", from, to, 0) {
		if b, ok := sum(rollup.Filter{App: a.Key}); ok {
			out.Apps = append(out.Apps, rollup.Row{Key: a.Key, Bytes: b})
		}
	}
	if dim == "asn" {
		for _, p := range h.Top(rollup.Filter{}, "place", from, to, 0) {
			if p.Extra["asn"] == key {
				out.CCs = append(out.CCs, rollup.Row{Key: p.Extra["cc"], Bytes: p.Bytes})
				if out.Org == "" {
					out.Org = p.Extra["org"]
				}
			}
		}
	}
	sort.SliceStable(out.MACs, func(i, j int) bool {
		return out.MACs[i].Bytes.Down+out.MACs[i].Bytes.Up > out.MACs[j].Bytes.Down+out.MACs[j].Bytes.Up
	})
	sort.SliceStable(out.Apps, func(i, j int) bool {
		return out.Apps[i].Bytes.Down+out.Apps[i].Bytes.Up > out.Apps[j].Bytes.Down+out.Apps[j].Bytes.Up
	})
	return out
}

func (h *fakeHistory) Pairs(f rollup.Filter, dim, by string, from, to time.Time) map[string][]rollup.Row {
	out := map[string][]rollup.Row{}
	for _, k := range h.Top(f, dim, from, to, 0) {
		g := f
		switch dim {
		case "app":
			g.App = k.Key
		case "cc":
			g.CC = k.Key
		}
		out[k.Key] = h.Top(g, by, from, to, 0)
	}
	return out
}
