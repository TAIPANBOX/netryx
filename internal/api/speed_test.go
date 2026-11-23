package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"netryx/internal/wan"
)

// averagingWAN is a line whose recent readings average to fixed rates.
type averagingWAN struct {
	fakeWAN
	asked []time.Duration
}

func (a *averagingWAN) Average(span time.Duration) (rx, tx float64, covered time.Duration) {
	a.asked = append(a.asked, span)
	return 1000, 100, span
}

// The Speed now tile is the line's average over the window picked (one,
// three or ten minutes, three by default), with the last reading beside it.
func TestTheSpeedTileIsTheWindowsAverage(t *testing.T) {
	fl, h, inv, _, ap := fixture()
	w := &averagingWAN{fakeWAN: fakeWAN{st: wan.Status{Up: true, RxRate: 9999, TxRate: 999}}}
	srv := New(fl, h, inv, w, ap, clock, nil)
	for q, span := range map[string]time.Duration{"": 3 * time.Minute, "&window=1m": time.Minute, "&window=10m": 10 * time.Minute} {
		w.asked = nil
		rec := get(t, srv, http.MethodGet, "/api/overview?range=1h"+q)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: %d", q, rec.Code)
		}
		var v struct {
			WAN struct {
				RxRate float64 `json:"rxRate"`
				Avg    *struct {
					Rx, Tx float64
					Sec    float64
				} `json:"avg"`
			} `json:"wan"`
		}
		json.Unmarshal(rec.Body.Bytes(), &v)
		if len(w.asked) != 1 || w.asked[0] != span {
			t.Fatalf("%q: asked %v, want %v", q, w.asked, span)
		}
		if v.WAN.Avg == nil || v.WAN.Avg.Rx != 1000 || v.WAN.Avg.Tx != 100 || v.WAN.Avg.Sec != span.Seconds() || v.WAN.RxRate != 9999 {
			t.Fatalf("%q: wan %s", q, rec.Body.String())
		}
	}
	if rec := get(t, srv, http.MethodGet, "/api/overview?window=5m"); rec.Code != http.StatusBadRequest {
		t.Fatalf("window 5m: %d, want 400", rec.Code)
	}
	// A line that keeps no readings has no average: null, never 0.
	rec := get(t, New(fl, h, inv, fakeWAN{st: wan.Status{Up: true}}, ap, clock, nil), http.MethodGet, "/api/overview")
	var v map[string]map[string]any
	json.Unmarshal(rec.Body.Bytes(), &v)
	if a, ok := v["wan"]["avg"]; !ok || a != nil {
		t.Fatalf("no readings: avg %v, want null", a)
	}
}
