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
