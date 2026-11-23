package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"netryx/internal/daily"
	"netryx/internal/netview"
)

const gib = 1 << 30

// closes builds the window's days, each read at its close, with the
// counters the function gives for day i (nil leaves the network out).
func closes(f func(i int) map[string][2]uint64) []daily.DayCounters {
	var out []daily.DayCounters
	for i, d := range daily.Window(now0) {
		c := daily.DayCounters{Date: d, Known: true, Mask: 1<<24 - 1, At: d.Add(24*time.Hour + 2*time.Minute), Ifaces: map[string][2]uint64{}}
		for k, v := range f(i) {
			c.Ifaces[k] = v
		}
		out = append(out, c)
	}
	return out
}

// A day's volume on a line is its counters at the day's close less those at
// the close before; a day is unknown when either close is missing, when the
// router was not counting all of it, or when a counter fell (a restart).
func TestALineCarriesTheDifferenceOfTwoCloses(t *testing.T) {
	days := closes(func(i int) map[string][2]uint64 {
		return map[string][2]uint64{"wan": {uint64(i) * 10 * gib, uint64(i) * gib}, "wwan": {uint64(i) * 32 * mib, 0}}
	})
	days[10].Known = false                    // no file: day 10 and day 11 unknown
	days[15].Mask &^= 1 << 3                  // the router was off for an hour of day 15
	days[20].At = time.Time{}                 // day 20 written after its close: 20 and 21 unknown
	days[25].Ifaces["wwan"] = [2]uint64{1, 0} // the modem's counter restarted: 25 unknown, 26 from it
	got := lineVolumes(days, []string{"wan"}, []string{"wwan"})
	if len(got) != len(days) {
		t.Fatalf("%d days, want %d", len(got), len(days))
	}
	if got[0].Backup != nil || got[0].Primary != nil {
		t.Errorf("the first day has no close before it: %+v", got[0])
	}
	if b, p := got[5].Backup, got[5].Primary; b == nil || *b != 32*mib || p == nil || *p != 11*gib {
		t.Errorf("an ordinary day: backup %v primary %v, want 32 MiB and 11 GiB", b, p)
	}
	for _, i := range []int{10, 11, 15, 20, 21, 25} {
		if got[i].Backup != nil {
			t.Errorf("day %d should be unknown: %+v", i, got[i])
		}
	}
	if b := got[26].Backup; b == nil || *b != 26*32*mib-1 {
		t.Errorf("the day after a restart counts from it: %v", b)
	}
}

// The backup line's summary: the days it carried at least 32 MiB, what it
// carried in all, its share of what the lines carried on those days, and its
// busiest day.
func TestTheBackupLineSaysTheDaysItCarriedAndItsShare(t *testing.T) {
	use := map[int]uint64{8: gib, 9: 3 * gib, 17: 512 * mib}
	var wwan uint64
	days := closes(func(i int) map[string][2]uint64 {
		wwan += use[i]
		wwan += 3 * mib // its checks
		return map[string][2]uint64{"wan": {uint64(i) * 9 * gib, uint64(i) * gib}, "wwan": {wwan, 0}}
	})
	s := linesSummary(lineVolumes(days, []string{"wan"}, []string{"wwan"}))
	if s.UsedDays != 3 {
		t.Errorf("%d days used, want 3", s.UsedDays)
	}
	if s.Busiest == nil || !s.Busiest.Date.Equal(days[9].Date) || *s.Busiest.Backup != 3*gib+3*mib {
		t.Errorf("busiest %+v, want day 9", s.Busiest)
	}
	if want := uint64(29*3*mib) + 4*gib + 512*mib; s.BackupBytes != want {
		t.Errorf("backup in all %d, want %d", s.BackupBytes, want)
	}
	used := float64(4*gib+512*mib+9*mib) / float64(4*gib+512*mib+9*mib+3*10*gib)
	if s.Share == nil || *s.Share < used-1e-6 || *s.Share > used+1e-6 {
		t.Errorf("share %v, want %v", s.Share, used)
	}
	if s.UnknownDays != 1 {
		t.Errorf("%d unknown days, want the first", s.UnknownDays)
	}
}

func TestTheLinesAreServedWithTheBackupFoundInTheView(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	view := netview.View{Interfaces: []netview.Iface{
		{Name: "wan", Proto: "dhcp", Device: "eth0", Up: true, WAN: true, Primary: true},
		{Name: "lte", Proto: "qmi", Device: "wwan0", Up: false},
	}}
	fd := &fakeDaily{counters: closes(func(i int) map[string][2]uint64 {
		return map[string][2]uint64{"eth0-ignored": {1, 1}, "wan": {uint64(i) * gib, 0}, "lte": {uint64(i) * 64 * mib, 0}}
	})}
	srv := New(fl, h, inv, w, ap, clock, nil, WithDaily(fd), WithNetwork(fakeNet{view}))
	rec := get(t, srv, http.MethodGet, "/api/lines")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v struct {
		Primary []struct{ Name string } `json:"primary"`
		Backup  []struct {
			Name, Proto string
			Up          bool
		} `json:"backup"`
		Days []struct {
			Date    string  `json:"date"`
			Primary *uint64 `json:"primary"`
			Backup  *uint64 `json:"backup"`
		} `json:"days"`
		UsedDays int     `json:"usedDays"`
		UsedMin  uint64  `json:"usedMin"`
		Share    float64 `json:"share"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Primary) != 1 || v.Primary[0].Name != "wan" || len(v.Backup) != 1 || v.Backup[0].Name != "lte" || v.Backup[0].Proto != "qmi" {
		t.Fatalf("lines %+v %+v", v.Primary, v.Backup)
	}
	if len(v.Days) != 30 || v.Days[29].Date != "2026-11-19" || v.Days[29].Backup == nil || *v.Days[29].Backup != 64*mib || v.UsedDays != 29 || v.UsedMin != 32*mib {
		t.Errorf("%s", rec.Body)
	}
	// Without the view nothing is known about the lines.
	if rec := get(t, New(fl, h, inv, w, ap, clock, nil, WithDaily(fd)), http.MethodGet, "/api/lines"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without the network view: %d", rec.Code)
	}
}
