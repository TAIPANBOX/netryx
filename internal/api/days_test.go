package api

import (
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"netryx/internal/daily"
)

type fakeDaily struct {
	days  []daily.DayOf
	asked [][6]byte
	at    []time.Time
}

func (f *fakeDaily) Days(mac [6]byte, now time.Time) []daily.DayOf {
	f.asked = append(f.asked, mac)
	f.at = append(f.at, now)
	return f.days
}

type dayJSONTest struct {
	Date  string    `json:"date"`
	Known bool      `json:"known"`
	Mask  uint32    `json:"mask"`
	Down  []*uint64 `json:"down"`
	Up    []*uint64 `json:"up"`
}

// A device's days come oldest first, in bytes: a day with no file is
// unknown, an hour the router was not counting is null rather than zero, and
// a counted quiet hour is zero. A MAC that is no device is a 404, a word that
// is no MAC a 400.
func TestADeviceAnswersItsDaysWithUnknownDaysAsUnknown(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	first := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	fd := &fakeDaily{}
	for i := 0; i < daily.Keep; i++ {
		fd.days = append(fd.days, daily.DayOf{Date: first.Add(time.Duration(i) * 24 * time.Hour)})
	}
	last := &fd.days[daily.Keep-1]
	last.Known, last.Mask = true, 1<<9|0xff
	last.Down[0], last.Up[0], last.Down[8] = 2048, 1024, 0
	srv := New(fl, h, inv, w, ap, clock, nil, WithDaily(fd))
	rec := get(t, srv, http.MethodGet, "/api/devices/"+laptop+"/days")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v struct {
		Unit string        `json:"unit"`
		Days []dayJSONTest `json:"days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Unit != "bytes" || len(v.Days) != daily.Keep {
		t.Fatalf("unit %q, %d days", v.Unit, len(v.Days))
	}
	if d := v.Days[0]; d.Date != "2026-10-21" || d.Known || d.Down != nil || d.Up != nil || d.Mask != 0 {
		t.Errorf("an unknown day: %+v", d)
	}
	d := v.Days[daily.Keep-1]
	if d.Date != "2026-11-19" || !d.Known || d.Mask != 1<<9|0xff || len(d.Down) != 24 || len(d.Up) != 24 {
		t.Fatalf("a known day: %+v", d)
	}
	if d.Down[0] == nil || *d.Down[0] != 2048 || d.Up[0] == nil || *d.Up[0] != 1024 {
		t.Errorf("hour 0 down %v up %v, want 2048 and 1024", d.Down[0], d.Up[0])
	}
	if d.Down[7] == nil || *d.Down[7] != 0 {
		t.Errorf("a counted quiet hour is zero: %v", d.Down[7])
	}
	if d.Down[8] != nil || d.Up[8] != nil || d.Down[23] != nil {
		t.Errorf("hours the router was not counting are null: %v %v %v", d.Down[8], d.Up[8], d.Down[23])
	}
	want, _ := net.ParseMAC(laptop)
	if len(fd.asked) != 1 || fd.asked[0] != [6]byte(want) || !fd.at[0].Equal(now0) {
		t.Errorf("asked for %v at %v", fd.asked, fd.at)
	}
	if rec := get(t, srv, http.MethodGet, "/api/devices/02:00:00:00:00:99/days"); rec.Code != 404 {
		t.Errorf("an unknown device: %d", rec.Code)
	}
	if rec := get(t, srv, http.MethodGet, "/api/devices/nonsense/days"); rec.Code != 400 {
		t.Errorf("a malformed MAC: %d", rec.Code)
	}
	// Without the store every day of the window is unknown, never zero.
	rec = get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/days")
	v.Days = nil
	json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != 200 || len(v.Days) != daily.Keep || v.Days[daily.Keep-1].Date != "2026-11-19" {
		t.Fatalf("without the store: %d %s", rec.Code, rec.Body)
	}
	for _, d := range v.Days {
		if d.Known || d.Down != nil {
			t.Errorf("without the store %s reads as known", d.Date)
		}
	}
}
