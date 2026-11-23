package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"netryx/internal/rollup"
)

// What is new for a device: the apps and countries it used since the start
// of yesterday (UTC) and in none of the 29 days before, with when each was
// first seen, by the catalog's names; the catch-alls and the house's own
// traffic are not news.
func TestADeviceSaysWhatIsNewForIt(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	inv.devs[0].First = now0.Add(-20 * 24 * time.Hour)
	first := now0.Add(-5 * time.Hour).Truncate(time.Hour)
	h.newKeys = map[string][]rollup.NewRow{
		"app": {{Key: "netflix", First: first, Bytes: rollup.Bytes{Down: 900, Up: 9}}, {Key: "other", First: first}, {Key: "unknown", First: first}},
		"cc":  {{Key: "CN", First: first, Bytes: rollup.Bytes{Down: 50}}, {Key: "LAN", First: first}, {Key: "--", First: first}},
	}
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/new")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v struct {
		TooNew bool   `json:"tooNew"`
		Since  string `json:"since"`
		Apps   []struct {
			App   struct{ ID, Name string } `json:"app"`
			First string                    `json:"first"`
			Down  uint64                    `json:"down"`
		} `json:"apps"`
		Countries []struct {
			CC   string `json:"cc"`
			Down uint64 `json:"down"`
		} `json:"countries"`
	}
	json.Unmarshal(rec.Body.Bytes(), &v)
	if v.TooNew || v.Since != "2026-11-19T00:00:00Z" {
		t.Errorf("tooNew %v since %q, want false and the start of yesterday", v.TooNew, v.Since)
	}
	if len(v.Apps) != 1 || v.Apps[0].App.Name != "Netflix" || v.Apps[0].Down != 900 || v.Apps[0].First != first.Format(time.RFC3339) {
		t.Errorf("apps %+v, want Netflix alone with its bytes and first hour", v.Apps)
	}
	if len(v.Countries) != 1 || v.Countries[0].CC != "CN" {
		t.Errorf("countries %+v, want CN alone", v.Countries)
	}
	for _, c := range h.newAsked {
		if c.mac != laptop || !c.split.Equal(time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)) || !c.from.Equal(c.split.Add(-29*24*time.Hour)) || !c.now.Equal(now0) {
			t.Errorf("asked %+v, want the laptop, the start of yesterday and the 29 days before", c)
		}
	}
}

// A device the router has known for less than a week cannot have much that
// is new to it: it says so rather than calling everything new.
func TestADeviceSeenForLessThanAWeekIsTooNewToTell(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	inv.devs[0].First = now0.Add(-3 * 24 * time.Hour)
	h.newKeys = map[string][]rollup.NewRow{"app": {{Key: "netflix", First: now0}}}
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/new")
	var v struct {
		TooNew    bool              `json:"tooNew"`
		KnownDays int               `json:"knownDays"`
		Apps      []json.RawMessage `json:"apps"`
	}
	json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != 200 || !v.TooNew || v.KnownDays != 3 || len(v.Apps) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/02:00:00:00:00:99/new"); rec.Code != 404 {
		t.Errorf("an unknown device: %d", rec.Code)
	}
}
