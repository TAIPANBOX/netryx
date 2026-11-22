package api

import (
	"net/http"
	"testing"
	"time"

	"netryx/internal/flows"
	"netryx/internal/rollup"
)

func withRecent(h *fakeHistory) {
	h.covered = 150 * time.Second
	h.recent = map[string][]rollup.Row{
		"mac|||":               {row(laptop, 30000, 3000, 2)},
		"mac|" + laptop + "||": {row(laptop, 30000, 3000, 2)},
		"app|||":               {row("netflix", 15000, 1500, 1)},
		"cc|||":                {row("DE", 7500, 750, 1)},
		"app|" + laptop + "||": {row("netflix", 4500, 450, 1)},
	}
}

// The Now columns are the average over the window the viewer picked, from
// the minutes the router keeps: bytes moved over the time they cover.
func TestTheNowColumnsAreTheWindowsAverage(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	withRecent(h)
	api := New(fl, h, inv, w, ap, clock, nil)

	devs := arr(decode(t, get(t, api, http.MethodGet, "/api/devices?window=3m")))
	lap := byKey(devs, "mac", laptop)
	if num(field(lap, "recent", "down")) != 200 || num(field(lap, "recent", "up")) != 20 || num(field(lap, "recentSec")) != 150 {
		t.Errorf("laptop recent %v over %v, want 200 and 20 B/s over 150 s", field(lap, "recent"), field(lap, "recentSec"))
	}
	if ph := byKey(devs, "mac", phone); num(field(ph, "recent", "down")) != 0 {
		t.Errorf("a device that moved nothing in the window: %v", field(ph, "recent"))
	}
	if d := decode(t, get(t, api, http.MethodGet, "/api/devices/"+laptop)); num(field(d, "recent", "down")) != 200 {
		t.Errorf("the device card's recent %v", field(d, "recent"))
	}
	apps := arr(decode(t, get(t, api, http.MethodGet, "/api/apps?window=3m")))
	if got := num(field(apps[0], "recent", "down")); got != 100 {
		t.Errorf("netflix recent %v, want 100", got)
	}
	cs := arr(field(decode(t, get(t, api, http.MethodGet, "/api/destinations?window=3m")), "countries"))
	if got := num(field(cs[0], "recent", "down")); got != 50 {
		t.Errorf("DE recent %v, want 50", got)
	}
	dapps := arr(decode(t, get(t, api, http.MethodGet, "/api/devices/"+laptop+"/apps?window=3m")))
	if got := num(field(dapps[0], "recent", "down")); got != 30 {
		t.Errorf("the laptop's netflix recent %v, want 30", got)
	}
}

// The window is one, three or ten minutes, three when not given.
func TestTheWindowIsOneThreeOrTenMinutes(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	withRecent(h)
	api := New(fl, h, inv, w, ap, clock, nil)
	get(t, api, http.MethodGet, "/api/devices")
	get(t, api, http.MethodGet, "/api/devices?window=1m")
	get(t, api, http.MethodGet, "/api/apps?window=10m")
	want := []time.Duration{3 * time.Minute, time.Minute, 10 * time.Minute}
	if len(h.spans) != 3 || h.spans[0] != want[0] || h.spans[1] != want[1] || h.spans[2] != want[2] {
		t.Errorf("spans asked %v, want %v", h.spans, want)
	}
	for _, bad := range []string{"5m", "3", "1h"} {
		if rec := get(t, api, http.MethodGet, "/api/devices?window="+bad); rec.Code != http.StatusBadRequest {
			t.Errorf("window %q: status %d, want 400", bad, rec.Code)
		}
	}
	// No minutes yet (just started): no figure, never a division by nothing.
	h.covered = 0
	rec := get(t, api, http.MethodGet, "/api/devices")
	if rec.Code != http.StatusOK {
		t.Fatalf("with nothing covered: status %d: %s", rec.Code, rec.Body.String())
	}
	devs := arr(decode(t, rec))
	if r := field(byKey(devs, "mac", laptop), "recent", "down"); num(r) != 0 {
		t.Errorf("recent with nothing covered %v", r)
	}
}

// The live flows can be read by what each moved over the window.
func TestLiveFlowsAreAskedByTheWindow(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	fl.rows = []flows.Flow{{MAC: laptop, WinUp: 7, WinDown: 70}}
	api := New(fl, h, inv, w, ap, clock, nil)
	body := decode(t, get(t, api, http.MethodGet, "/api/flows?live=1&window=10m"))
	if fl.got.Window != 10*time.Minute || !fl.got.Live {
		t.Errorf("query %+v, want live over 10 minutes", fl.got)
	}
	if r := at(field(body, "rows"), 0); num(field(r, "winDown")) != 70 || num(field(r, "winUp")) != 7 {
		t.Errorf("row %v", r)
	}
	get(t, api, http.MethodGet, "/api/flows?live=1")
	if fl.got.Window != 0 {
		t.Errorf("a live read without a window is newest first: %+v", fl.got)
	}
	get(t, api, http.MethodGet, "/api/flows?window=10m")
	if fl.got.Window != 0 {
		t.Errorf("the journal is never read by a window: %+v", fl.got)
	}
	if rec := get(t, api, http.MethodGet, "/api/flows?live=1&window=7m"); rec.Code != http.StatusBadRequest {
		t.Errorf("window 7m: status %d", rec.Code)
	}
}
