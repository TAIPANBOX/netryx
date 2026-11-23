package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"netryx/internal/daily"
	"netryx/internal/inventory"
)

// fullDays is the 30-day window at now0, every day known and counted whole,
// the device quiet in every hour.
func fullDays() []daily.DayOf {
	var out []daily.DayOf
	for _, d := range daily.Window(now0) {
		out = append(out, daily.DayOf{Date: d, Known: true, Mask: 1<<24 - 1})
	}
	return out
}

// dayAt finds the day of the window with that date.
func dayAt(t *testing.T, days []daily.DayOf, y int, m time.Month, d int) *daily.DayOf {
	t.Helper()
	want := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	for i := range days {
		if days[i].Date.Equal(want) {
			return &days[i]
		}
	}
	t.Fatalf("%v is not in the window", want)
	return nil
}

func median(t *testing.T, u usualOf, wd time.Weekday, h int) (uint64, bool) {
	t.Helper()
	if v := u.Median[wd][h]; v != nil {
		return *v, true
	}
	return 0, false
}

// The usual of a slot of the router's week is the median of what the device
// moved (down and up together) in that hour of that weekday over the days
// kept, read in the router's zone: an hour the router was not counting and a
// day with no file are no sample, a counted quiet hour is a sample of zero.
func TestTheUsualIsTheMedianOfEachHourOfTheWeekInTheRoutersZone(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*3600)
	days := fullDays()
	// Monday 20:00 in the router's zone is 18:00 UTC: four Mondays.
	for _, c := range []struct {
		d        int
		m        time.Month
		down, up uint64
	}{{26, time.October, 100, 0}, {2, time.November, 200, 100}, {9, time.November, 150, 50}, {16, time.November, 1000, 0}} {
		x := dayAt(t, days, 2026, c.m, c.d)
		x.Down[18], x.Up[18] = c.down, c.up
	}
	// Tuesday 01:00 there is Monday 23:00 UTC: the slot is the router's day.
	dayAt(t, days, 2026, time.October, 26).Down[23] = 50
	dayAt(t, days, 2026, time.November, 2).Down[23] = 70
	nov9 := dayAt(t, days, 2026, time.November, 9)
	nov9.Down[23], nov9.Mask = 999, nov9.Mask&^(1<<23) // not counting: no sample
	dayAt(t, days, 2026, time.November, 16).Down[23] = 90
	// Wednesday 12:00 there is 10:00 UTC; the first Wednesday has no file.
	oct21 := dayAt(t, days, 2026, time.October, 21)
	oct21.Known, oct21.Down[10] = false, 999

	u := usualWeek(days, time.Time{}, loc)
	if v, ok := median(t, u, time.Monday, 20); !ok || v != 250 || u.Samples[time.Monday][20] != 4 {
		t.Errorf("Monday 20:00 is %d (%v) of %d samples, want the median 250 of 4", v, ok, u.Samples[time.Monday][20])
	}
	if v, ok := median(t, u, time.Tuesday, 1); !ok || v != 70 || u.Samples[time.Tuesday][1] != 3 {
		t.Errorf("Tuesday 01:00 is %d (%v) of %d samples, want 70 of 3", v, ok, u.Samples[time.Tuesday][1])
	}
	if v, ok := median(t, u, time.Wednesday, 12); !ok || v != 0 || u.Samples[time.Wednesday][12] != 4 {
		t.Errorf("Wednesday 12:00 is %d (%v) of %d samples, want 0 of 4", v, ok, u.Samples[time.Wednesday][12])
	}
	if u.KnownDays != 29 {
		t.Errorf("%d known days, want 29", u.KnownDays)
	}
}

// Only what the router saw of the device counts: hours before it was first
// seen are not quiet hours of it, and a slot with fewer than two samples has
// no usual.
func TestTheUsualStartsWhenTheDeviceWasFirstSeen(t *testing.T) {
	days := fullDays()
	dayAt(t, days, 2026, time.November, 16).Down[18] = 1000
	since := time.Date(2026, 11, 14, 10, 20, 0, 0, time.UTC) // a Saturday
	u := usualWeek(days, since, time.UTC)
	if _, ok := median(t, u, time.Monday, 18); ok || u.Samples[time.Monday][18] != 1 {
		t.Errorf("one Monday since it was first seen gives %d samples and a usual %v", u.Samples[time.Monday][18], u.Median[time.Monday][18])
	}
	if u.Samples[time.Saturday][9] != 0 || u.Samples[time.Saturday][10] != 1 {
		t.Errorf("the hour before it was seen has %d samples, the hour it was seen in %d; want 0 and 1",
			u.Samples[time.Saturday][9], u.Samples[time.Saturday][10])
	}
	if u.KnownDays != 6 {
		t.Errorf("%d known days since 14 November, want 6", u.KnownDays)
	}
}

// An hour belongs to the slot its middle falls in, in the router's zone: a
// DST change moves the UTC hour of a slot, and a half-hour zone is placed by
// the half it mostly covers.
func TestAnHourIsPlacedByItsMiddleInTheRoutersZone(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skip("no tzdata")
	}
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("no tzdata")
	}
	for _, c := range []struct {
		at   time.Time
		loc  *time.Location
		wd   time.Weekday
		hour int
	}{
		{time.Date(2026, 10, 21, 19, 0, 0, 0, time.UTC), london, time.Wednesday, 20}, // BST
		{time.Date(2026, 10, 28, 19, 0, 0, 0, time.UTC), london, time.Wednesday, 19}, // GMT
		{time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC), kolkata, time.Saturday, 5},  // 04:30 to 05:30
	} {
		if wd, h := slotOf(c.at, c.loc); wd != c.wd || h != c.hour {
			t.Errorf("%v in %v is %v %d:00, want %v %d:00", c.at, c.loc, wd, h, c.wd, c.hour)
		}
	}
}

type usualJSONTest struct {
	TooNew    bool        `json:"tooNew"`
	KnownDays int         `json:"knownDays"`
	Zone      string      `json:"zone"`
	Median    [][]*uint64 `json:"median"`
	Samples   [][]int     `json:"samples"`
	Now       *struct {
		From    string  `json:"from"`
		To      string  `json:"to"`
		Weekday int     `json:"weekday"`
		Hour    int     `json:"hour"`
		Bytes   *uint64 `json:"bytes"`
		Usual   *uint64 `json:"usual"`
		Samples int     `json:"samples"`
	} `json:"now"`
}

func usualFor(t *testing.T, srv http.Handler, mac string) usualJSONTest {
	t.Helper()
	rec := get(t, srv, http.MethodGet, "/api/devices/"+mac+"/usual")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v usualJSONTest
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// A device's usual week comes with the hour that just closed beside the
// usual of its slot, so the page can put both sides of the comparison: what
// it moved, what it usually moves then, and how many days that rests on.
func TestADeviceAnswersItsUsualWeekAndTheLastHour(t *testing.T) {
	fl, h, _, w, ap := fixture()
	inv := fakeInv{devs: []inventory.Device{{MAC: laptop, Name: "Laptop", First: now0.Add(-40 * 24 * time.Hour), Last: now0}}}
	fd := &fakeDaily{days: fullDays()}
	// now0 is a Friday at noon: the hour that closed is Friday 11:00.
	for i, d := range []int{23, 30} {
		dayAt(t, fd.days, 2026, time.October, d).Down[11] = uint64(100 * (i + 1))
	}
	for i, d := range []int{6, 13} {
		dayAt(t, fd.days, 2026, time.November, d).Down[11] = uint64(300 + 100*i)
	}
	h.top["mac|"+laptop+"||"] = append(h.top["mac|"+laptop+"||"], row(laptop, 900, 100, 3))
	srv := New(fl, h, inv, w, ap, clock, fakeService{started: now0.Add(-48 * time.Hour)}, WithDaily(fd))
	v := usualFor(t, srv, laptop)
	if v.TooNew || v.KnownDays != 30 || v.Zone != "UTC" || len(v.Median) != 7 || len(v.Median[5]) != 24 {
		t.Fatalf("%+v", v)
	}
	if m := v.Median[time.Friday][11]; m == nil || *m != 250 || v.Samples[time.Friday][11] != 4 {
		t.Errorf("Friday 11:00 usual %v of %d samples, want 250 of 4", m, v.Samples[time.Friday][11])
	}
	n := v.Now
	if n == nil || n.From != "2026-11-20T11:00:00Z" || n.To != "2026-11-20T12:00:00Z" || n.Weekday != int(time.Friday) || n.Hour != 11 {
		t.Fatalf("the hour that closed: %+v", n)
	}
	if n.Bytes == nil || *n.Bytes != 1000 || n.Usual == nil || *n.Usual != 250 || n.Samples != 4 {
		t.Errorf("both sides: moved %v, usual %v of %d samples; want 1000, 250 of 4", n.Bytes, n.Usual, n.Samples)
	}
	last := h.tops[len(h.tops)-1]
	if last.dim != "mac" || !last.from.Equal(now0.Add(-time.Hour)) || !last.to.Equal(now0) {
		t.Errorf("the last hour was read as %+v", last)
	}

	// A service that started inside that hour did not see all of it: the
	// hour is unknown, never a quiet one.
	srv = New(fl, h, inv, w, ap, clock, fakeService{started: now0.Add(-30 * time.Minute)}, WithDaily(fd))
	if n := usualFor(t, srv, laptop).Now; n == nil || n.Bytes != nil || n.Usual == nil {
		t.Errorf("a service started mid-hour: %+v", n)
	}

	// The router's zone places the week.
	loc := time.FixedZone("UTC+2", 2*3600)
	srv = New(fl, h, inv, w, ap, clock, fakeService{started: now0.Add(-48 * time.Hour)}, WithDaily(fd), WithZone(loc))
	if v := usualFor(t, srv, laptop); v.Zone != "UTC+2" || v.Now.Hour != 13 || v.Median[time.Friday][13] == nil || *v.Median[time.Friday][13] != 250 {
		t.Errorf("in UTC+2: zone %q, hour %d, Friday 13:00 %v", v.Zone, v.Now.Hour, v.Median[time.Friday][13])
	}

	if rec := get(t, srv, http.MethodGet, "/api/devices/02:00:00:00:00:99/usual"); rec.Code != 404 {
		t.Errorf("an unknown device: %d", rec.Code)
	}
	if rec := get(t, srv, http.MethodGet, "/api/devices/nonsense/usual"); rec.Code != 400 {
		t.Errorf("a malformed MAC: %d", rec.Code)
	}
}

// A device the router has counted on fewer than seven days has no usual yet,
// and says so rather than drawing a week of guesses.
func TestADeviceWithLessThanAWeekOfDaysHasNoUsualYet(t *testing.T) {
	fl, h, _, w, ap := fixture()
	inv := fakeInv{devs: []inventory.Device{{MAC: laptop, Name: "Laptop", First: now0.Add(-5 * 24 * time.Hour), Last: now0}}}
	srv := New(fl, h, inv, w, ap, clock, nil, WithDaily(&fakeDaily{days: fullDays()}))
	v := usualFor(t, srv, laptop)
	if !v.TooNew || v.KnownDays != 5 || v.Median != nil || v.Now != nil {
		t.Errorf("five days: %+v", v)
	}
	// Without the daily store nothing is known.
	inv.devs[0].First = now0.Add(-40 * 24 * time.Hour)
	srv = New(fl, h, inv, w, ap, clock, nil)
	if v := usualFor(t, srv, laptop); !v.TooNew || v.KnownDays != 0 {
		t.Errorf("without the store: %+v", v)
	}
}

// The inventory forgets when it first saw a device whenever the service
// restarts, but the daily files remember: a device that moved bytes on days
// the files hold keeps its usual week after a restart.
func TestTheUsualSurvivesARestartThatResetTheInventory(t *testing.T) {
	fl, h, _, w, ap := fixture()
	inv := fakeInv{devs: []inventory.Device{{MAC: laptop, Name: "Laptop", First: now0.Add(-10 * time.Minute), Last: now0}}}
	fd := &fakeDaily{days: fullDays()}
	for i := range fd.days {
		if !fd.days[i].Date.Before(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
			fd.days[i].Down[12] = 100
		}
	}
	srv := New(fl, h, inv, w, ap, clock, nil, WithDaily(fd))
	v := usualFor(t, srv, laptop)
	if v.TooNew || v.KnownDays != 19 {
		t.Errorf("after a restart: too new %v, %d known days; want the 19 days since it first moved bytes", v.TooNew, v.KnownDays)
	}
}
