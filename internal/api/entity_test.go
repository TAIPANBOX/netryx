package api

import (
	"net/http"
	"testing"
	"time"

	"netryx/internal/rollup"
)

// A click on a stretch of a chart asks the flows of that stretch: the journal
// filtered by time, and by network.
func TestFlowsCanBeAskedForAStretchOfTimeAndANetwork(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	api := New(fl, h, inv, w, ap, clock, nil)
	get(t, api, http.MethodGet, "/api/flows?from=2026-11-20T11:00:00Z&to=2026-11-20T11:10:00Z&asn=15169")
	if !fl.got.From.Equal(time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)) || !fl.got.To.Equal(time.Date(2026, 11, 20, 11, 10, 0, 0, time.UTC)) || fl.got.ASN != 15169 {
		t.Errorf("query %+v", fl.got)
	}
	for _, bad := range []string{"from=yesterday", "to=2026-13-01T00:00:00Z", "asn=AS1", "asn=-3"} {
		if rec := get(t, api, http.MethodGet, "/api/flows?"+bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", bad, rec.Code)
		}
	}
}

// A network's page: its organisation, the countries it was reached in, and
// the clients and apps that reached it.
func TestANetworkPageNamesItsCountriesClientsAndApps(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	h.top["asn|"+phone+"||"] = []rollup.Row{withOrg(row("2906", 300, 30, 1), "Netflix")}
	h.top["asn||netflix|"] = []rollup.Row{withOrg(row("2906", 2000, 500, 3), "Netflix")}
	h.top["asn||github|"] = []rollup.Row{withOrg(row("16509", 1000, 500, 2), "Amazon")}
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/networks/2906?range=24h")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if str(field(body, "org")) != "Netflix" || str(field(body, "asn")) != "2906" {
		t.Errorf("head %v", body)
	}
	if c := at(field(body, "countries"), 0); str(field(c, "cc")) != "DE" || num(field(c, "down")) != 2000 {
		t.Errorf("countries %v", field(body, "countries"))
	}
	cl := arr(field(body, "clients"))
	if len(cl) != 2 || str(field(cl[0], "name")) != "Laptop" || str(field(cl[1], "name")) != "Phone" {
		t.Errorf("clients %v, want the laptop then the phone", cl)
	}
	if a := arr(field(body, "apps")); len(a) != 1 || str(field(a[0], "app", "id")) != "netflix" {
		t.Errorf("apps %v, want netflix alone", a)
	}
	for _, bad := range []string{"AS2906", "0", "x"} {
		if rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/networks/"+bad); rec.Code != http.StatusBadRequest {
			t.Errorf("asn %q: status %d, want 400", bad, rec.Code)
		}
	}
}

// A domain's page: the apps it was named for and the clients that asked for it.
func TestADomainPageNamesItsAppsAndClients(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	h.top["domain|||"] = []rollup.Row{withApp(row("video.example", 2500, 500, 3), "netflix", "sni"), withApp(row("git.example", 1000, 500, 2), "github", "quic")}
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/domains/video.example?range=24h")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if str(field(body, "name")) != "video.example" || num(field(body, "down")) != 2500 {
		t.Errorf("head %v", body)
	}
	if a := arr(field(body, "apps")); len(a) != 1 || str(field(a[0], "app", "name")) != "Netflix" || str(field(a[0], "src")) != "sni" {
		t.Errorf("apps %v", a)
	}
	if c := arr(field(body, "clients")); len(c) != 1 || str(field(c[0], "name")) != "Laptop" {
		t.Errorf("clients %v", c)
	}
	for _, bad := range []string{"%3Cscript%3E", "a%20b", "a..b", strings100()} {
		if rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/domains/"+bad); rec.Code != http.StatusBadRequest {
			t.Errorf("domain %q: status %d, want 400", bad, rec.Code)
		}
	}
}

func strings100() string {
	b := make([]byte, 300)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

// The line's card asks only for the line: its state, address and quality,
// without reading the history the overview reads.
func TestTheLineCardAsksOnlyForTheLine(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/wan")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if str(field(body, "ipv4")) != "198.51.100.7" || num(field(body, "rxRate")) != 1000 || str(field(body, "device")) != "eth1" {
		t.Errorf("line %v", body)
	}
	if len(h.series) != 0 {
		t.Errorf("the line card read the history %d times", len(h.series))
	}
}

// Clients and Apps draw each row's last hour as a sparkline: the store's one
// pass, in steps of two minutes, with the steps nobody counted said.
func TestTheListsCarryAnHourOfSparkline(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	h.sparks = map[string]rollup.SparksOf{
		"mac": {Step: 2 * time.Minute, Known: 27, Keys: map[string][]uint64{laptop: append(make([]uint64, 29), 1200)}},
		"app": {Step: 2 * time.Minute, Known: 0, Keys: map[string][]uint64{"netflix": append([]uint64{5}, make([]uint64, 29)...)}},
	}
	api := New(fl, h, inv, w, ap, clock, nil)
	var lap, tvRow any
	for _, d := range arr(decode(t, get(t, api, http.MethodGet, "/api/devices"))) {
		switch str(field(d, "mac")) {
		case laptop:
			lap = d
		case tv:
			tvRow = d
		}
	}
	sp := field(lap, "spark")
	if num(field(sp, "stepSec")) != 120 || num(field(sp, "known")) != 27 || len(arr(field(sp, "bytes"))) != 30 || num(at(field(sp, "bytes"), 29)) != 1200 {
		t.Errorf("laptop spark %v", sp)
	}
	// A device that moved nothing in the hour still has its hour, all zero.
	if b := arr(field(field(tvRow, "spark"), "bytes")); len(b) != 30 || num(b[0]) != 0 {
		t.Errorf("tv spark %v", field(tvRow, "spark"))
	}
	for _, a := range arr(decode(t, get(t, api, http.MethodGet, "/api/apps?range=24h"))) {
		if str(field(a, "app", "id")) == "netflix" {
			if num(at(field(field(a, "spark"), "bytes"), 0)) != 5 {
				t.Errorf("netflix spark %v", field(a, "spark"))
			}
		}
	}
}
