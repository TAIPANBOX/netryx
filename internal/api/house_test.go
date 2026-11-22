package api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"netryx/internal/rollup"
)

// The Apps screen: every app the house used in the period, with its share
// of the house's bytes and the clients that used it, busiest first.
func TestTheHouseAppsListNamesTheClientsOfEachApp(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/apps?range=24h")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	list := arr(decode(t, rec))
	if len(list) != 3 {
		t.Fatalf("%d apps, want 3", len(list))
	}
	nf := list[0]
	if got := str(field(nf, "app", "name")); got != "Netflix" {
		t.Errorf("first app %q, want Netflix", got)
	}
	if got := str(field(nf, "app", "category")); got != "video" {
		t.Errorf("category %q, want video", got)
	}
	if num(field(nf, "down")) != 2500 || num(field(nf, "up")) != 500 {
		t.Errorf("netflix bytes %v/%v, want 2500/500", field(nf, "down"), field(nf, "up"))
	}
	if got := num(field(nf, "share")); got != 0.5 {
		t.Errorf("share %v, want 0.5 of the house's 6000 bytes", got)
	}
	if got := num(field(nf, "clients")); got != 2 {
		t.Errorf("clients %v, want 2", got)
	}
	top := arr(field(nf, "top"))
	if len(top) != 2 || str(field(top[0], "name")) != "Laptop" || str(field(top[1], "mac")) != phone {
		t.Errorf("top clients %v, want Laptop then the phone", top)
	}
}

// A catch-all is a row of its own, never counted as a client.
func TestTheHouseAppsCountOnlyClients(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	// The router moved more of it than any client: the row still names the
	// clients first.
	h.top["mac||netflix|"] = append([]rollup.Row{row("router", 90000, 10, 1)}, append(h.top["mac||netflix|"], row("other", 5, 5, 1))...)
	list := arr(decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/apps")))
	if got := num(field(list[0], "clients")); got != 2 {
		t.Errorf("clients %v, want 2: the router and other are not clients", got)
	}
	top := arr(field(list[0], "top"))
	if len(top) != 2 || str(field(top[0], "mac")) != laptop || str(field(top[1], "mac")) != phone {
		t.Errorf("top %v, want the two clients and not the router", top)
	}
}

// A row with no client behind it (the router's own traffic, addresses with
// no hardware address) names whose traffic it is instead.
func TestARowWithoutClientsSaysWhoseTrafficItIs(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	h.top["cc|||"] = append(h.top["cc|||"], row("GB", 10, 10, 1))
	h.top["mac|||GB"] = []rollup.Row{row("router", 10, 10, 1)}
	body := decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/destinations"))
	gb := byKey(field(body, "countries"), "cc", "GB")
	top := arr(field(gb, "top"))
	if num(field(gb, "clients")) != 0 || len(top) != 1 || str(field(top[0], "mac")) != "router" || field(top[0], "other") != true {
		t.Errorf("GB %v, want no clients and the router's row", gb)
	}
}

func TestTheHouseAppsListIsBounded(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	var rows []rollup.Row
	for i := 0; i < maxHouseApps+50; i++ {
		rows = append(rows, row("app"+strconv.Itoa(i), uint64(10000-i), 0, 1))
	}
	h.top["app|||"] = rows
	list := arr(decode(t, get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/apps?range=30d")))
	if len(list) != maxHouseApps {
		t.Fatalf("%d apps, want %d", len(list), maxHouseApps)
	}
	for _, a := range list {
		if len(arr(field(a, "top"))) > houseTopClients {
			t.Fatalf("top clients %v longer than %d", field(a, "top"), houseTopClients)
		}
	}
}

// One app across the house: who used it, where it went, how it was named,
// and how its traffic moved over the period.
func TestAnAppDetailShowsItsClientsPlacesDomainsAndSeries(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	h.top["place||netflix|"] = []rollup.Row{withPlace(row("DE/2906", 2500, 500, 3), "DE", "2906", "Netflix")}
	h.top["domain||netflix|"] = []rollup.Row{withApp(row("video.example", 2500, 500, 3), "netflix", "sni")}
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/apps/netflix?range=7d")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if str(field(body, "app", "name")) != "Netflix" || num(field(body, "down")) != 2500 || num(field(body, "share")) != 0.5 {
		t.Errorf("head %v", body)
	}
	if got := num(field(body, "clientCount")); got != 2 {
		t.Errorf("clientCount %v, want 2", got)
	}
	cl := arr(field(body, "clients"))
	if len(cl) != 2 || str(field(cl[0], "name")) != "Laptop" || num(field(cl[0], "down")) != 2000 {
		t.Errorf("clients %v", cl)
	}
	pl := at(field(body, "places"), 0)
	if str(field(pl, "cc")) != "DE" || str(field(pl, "asn")) != "2906" || str(field(pl, "org")) != "Netflix" {
		t.Errorf("places[0] %v", pl)
	}
	if got := str(field(at(field(body, "domains"), 0), "name")); got != "video.example" {
		t.Errorf("domains[0] %q", got)
	}
	if len(h.series) != 1 || h.series[0].f != (rollup.Filter{App: "netflix"}) || h.series[0].step != time.Hour {
		t.Errorf("series asked %+v, want app netflix at an hour's step", h.series)
	}
	if len(arr(field(body, "series"))) == 0 {
		t.Errorf("no series")
	}
}

// An app the catalog does not know and the period never saw is not there; an
// id that could not be one is refused before anything is read.
func TestAnAppNobodyKnowsIs404AndAMalformedIdIs400(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	api := New(fl, h, inv, w, ap, clock, nil)
	if rec := get(t, api, http.MethodGet, "/api/apps/nosuchapp"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown app: status %d, want 404", rec.Code)
	}
	for _, id := range []string{"%3Cscript%3E", "Netflix", "a%20b", "-x"} {
		if rec := get(t, api, http.MethodGet, "/api/apps/"+id); rec.Code != http.StatusBadRequest {
			t.Errorf("id %q: status %d, want 400", id, rec.Code)
		}
	}
	// Known to the catalog but quiet in this period: there, with nothing moved.
	h.top["app|||"] = nil
	rec := get(t, api, http.MethodGet, "/api/apps/github?range=1h")
	if rec.Code != http.StatusOK || num(field(decode(t, rec), "down")) != 0 {
		t.Errorf("quiet known app: status %d %s", rec.Code, rec.Body.String())
	}
	// Unknown traffic is always an app of its own.
	if rec := get(t, api, http.MethodGet, "/api/apps/unknown"); rec.Code != http.StatusOK {
		t.Errorf("unknown: status %d", rec.Code)
	}
}

// The Destinations screen: the countries, networks and domains the house
// reached. The house's own traffic is not a destination, and shares are of
// what crossed the line.
func TestTheHouseDestinationsLeaveTheHouseOut(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	h.top["cc|||"] = append(h.top["cc|||"], row("LAN", 9000, 9000, 9))
	h.top["mac|||DE"] = []rollup.Row{row(laptop, 3000, 1000, 5)}
	h.top["mac|||US"] = []rollup.Row{row(phone, 1000, 500, 2), row(tv, 500, 0, 1)}
	h.top["mac|||LAN"] = []rollup.Row{row(tv, 9000, 9000, 9)}
	h.top["place|||"] = append(h.top["place|||"], withPlace(row("LAN/0", 9000, 9000, 9), "LAN", "0", ""))
	h.top["domain|||"] = []rollup.Row{withApp(row("video.example", 2500, 500, 3), "netflix", "sni")}
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/destinations?range=24h")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	cs := arr(field(body, "countries"))
	if len(cs) != 2 {
		t.Fatalf("countries %v, want DE and US only", cs)
	}
	de, us := cs[0], cs[1]
	if str(field(de, "cc")) != "DE" || num(field(de, "share")) != 4000.0/6000 || num(field(de, "clients")) != 1 {
		t.Errorf("DE %v", de)
	}
	if num(field(us, "clients")) != 2 || str(field(at(field(us, "top"), 0), "name")) != "Phone" {
		t.Errorf("US %v", us)
	}
	nets := arr(field(body, "networks"))
	if len(nets) != 3 || str(field(nets[0], "asn")) != "2906" || str(field(nets[0], "cc")) != "DE" || str(field(nets[0], "org")) != "Netflix" {
		t.Errorf("networks %v, want the three internet places, Netflix first", nets)
	}
	if d := at(field(body, "domains"), 0); str(field(d, "name")) != "video.example" || str(field(d, "app", "id")) != "netflix" {
		t.Errorf("domains[0] %v", d)
	}
}

// One country: who in the house went there, with which apps, to which
// networks, and when.
func TestACountryDetailShowsItsClientsAppsAndNetworks(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	h.top["mac|||DE"] = []rollup.Row{row(laptop, 3000, 1000, 5)}
	h.top["app|||DE"] = []rollup.Row{row("netflix", 2000, 500, 3), row("github", 1000, 500, 2)}
	h.top["place|||DE"] = []rollup.Row{
		withPlace(row("DE/2906", 2000, 500, 3), "DE", "2906", "Netflix"),
		withPlace(row("DE/16509", 1000, 500, 2), "DE", "16509", "Amazon"),
	}
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/destinations/de?range=1h")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if str(field(body, "cc")) != "DE" || num(field(body, "down")) != 3000 || num(field(body, "share")) != 4000.0/6000 {
		t.Errorf("head %v", body)
	}
	if got := num(field(body, "clientCount")); got != 1 {
		t.Errorf("clientCount %v, want 1", got)
	}
	if c := at(field(body, "clients"), 0); str(field(c, "name")) != "Laptop" {
		t.Errorf("clients %v", field(body, "clients"))
	}
	if a := at(field(body, "apps"), 1); str(field(a, "app", "name")) != "GitHub" {
		t.Errorf("apps %v", field(body, "apps"))
	}
	if n := at(field(body, "networks"), 1); str(field(n, "org")) != "Amazon" {
		t.Errorf("networks %v", field(body, "networks"))
	}
	if len(h.series) != 1 || h.series[0].f != (rollup.Filter{CC: "DE"}) || h.series[0].step != time.Minute {
		t.Errorf("series asked %+v, want country DE at a minute's step", h.series)
	}
}

func TestACountryCodeMustBeTwoLetters(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	api := New(fl, h, inv, w, ap, clock, nil)
	for _, cc := range []string{"lan", "deu", "d1", "%3Cx%3E"} {
		if rec := get(t, api, http.MethodGet, "/api/destinations/"+cc); rec.Code != http.StatusBadRequest {
			t.Errorf("cc %q: status %d, want 400", cc, rec.Code)
		}
	}
}
