package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"netryx/internal/inventory"
)

const mib = 1 << 20

// Yesterday is set against the device's own median day: the median of the
// whole days since it was first seen, a day the router was not counting all
// of is drawn as partial and is no sample, and a day with no file is unknown.
func TestRhythmComparesYesterdayWithTheMedianDay(t *testing.T) {
	days := fullDays()
	for i := range days {
		days[i].Down[12] = 100 * mib
	}
	days[3].Down[12] = 300 * mib
	days[5].Mask &^= 1 << 2 // the router was off for an hour
	days[5].Down[12] = 5000 * mib
	days[7].Known = false
	days[len(days)-1].Down[12] = 850 * mib
	r := rhythmOf(days, time.Time{}, usualWeek(days, time.Time{}, time.UTC), time.UTC)
	if r.Confidence != "normal" || r.WholeDays != 28 {
		t.Fatalf("confidence %q over %d whole days, want normal over 28", r.Confidence, r.WholeDays)
	}
	if r.MedianDay == nil || *r.MedianDay != 100*mib {
		t.Errorf("median day %v, want 100 MiB", r.MedianDay)
	}
	if r.Yesterday.Bytes == nil || *r.Yesterday.Bytes != 850*mib || !r.Yesterday.Whole || r.TimesMedian == nil || *r.TimesMedian != 8.5 {
		t.Errorf("yesterday %+v, %v times the median; want 850 MiB, 8.5", r.Yesterday, r.TimesMedian)
	}
	if d := r.Days[5]; d.Whole || d.Bytes == nil {
		t.Errorf("a partly counted day %+v: drawn with its bytes, not whole", d)
	}
	if d := r.Days[7]; d.Bytes != nil {
		t.Errorf("a day with no file %+v is unknown", d)
	}
	if len(r.Days) != 30 || !r.Days[29].Date.Equal(days[29].Date) {
		t.Errorf("%d days, the last %v", len(r.Days), r.Days[len(r.Days)-1].Date)
	}
	// 850 of the 28 whole days' 26*100 + 300 + 850.
	if want := 850.0 / (26*100 + 300 + 850); r.BusiestShare < want-1e-9 || r.BusiestShare > want+1e-9 {
		t.Errorf("busiest share %v, want %v", r.BusiestShare, want)
	}
}

// The comparison is refused, never made against nothing: a device counted on
// fewer than seven whole days is too new, one with no counted day has no
// data, a yesterday the router did not count whole is not compared, and a
// median day of nothing has no multiple.
func TestRhythmRefusesAComparisonItCannotMake(t *testing.T) {
	days := fullDays()
	for i := range days {
		days[i].Down[12] = 100 * mib
	}
	since := days[25].Date.Add(3 * time.Hour) // days 26..29 whole, day 25 from 03:00
	r := rhythmOf(days, since, usualWeek(days, since, time.UTC), time.UTC)
	if r.Confidence != "too_new" || r.WholeDays != 4 || r.TimesMedian != nil || r.Yesterday.Bytes == nil {
		t.Errorf("four whole days: %q, %d whole, times %v, yesterday %v", r.Confidence, r.WholeDays, r.TimesMedian, r.Yesterday.Bytes)
	}
	if r.Days[24].Bytes != nil || r.Days[25].Whole {
		t.Errorf("before it was seen %+v; the day it was first seen %+v is partial", r.Days[24], r.Days[25])
	}

	none := fullDays()
	for i := range none {
		none[i].Known = false
	}
	if r := rhythmOf(none, time.Time{}, usualOf{}, time.UTC); r.Confidence != "no_data" || r.MedianDay != nil {
		t.Errorf("no file: %q, median %v", r.Confidence, r.MedianDay)
	}

	partial := fullDays()
	for i := range partial {
		partial[i].Down[12] = 100 * mib
	}
	partial[29].Mask &^= 1
	if r := rhythmOf(partial, time.Time{}, usualOf{}, time.UTC); r.Confidence != "normal" || r.Yesterday.Whole || r.TimesMedian != nil {
		t.Errorf("a partly counted yesterday: whole %v, times %v", r.Yesterday.Whole, r.TimesMedian)
	}

	quiet := fullDays()
	quiet[29].Down[12] = 10 * mib
	if r := rhythmOf(quiet, time.Time{}, usualOf{}, time.UTC); r.MedianDay == nil || *r.MedianDay != 0 || r.TimesMedian != nil {
		t.Errorf("a median day of nothing: median %v, times %v", r.MedianDay, r.TimesMedian)
	}
}

// The trend is the last seven days against the seven before, each the mean
// of its whole days, and unknown with fewer than four whole days in either.
func TestRhythmTrendIsTheLastWeekAgainstTheOneBefore(t *testing.T) {
	for _, c := range []struct {
		before, last uint64
		dropLast     int
		want         string
	}{
		{100, 130, 0, "rising"},
		{100, 79, 0, "falling"},
		{100, 110, 0, "steady"},
		{100, 300, 4, "unknown"},
	} {
		days := fullDays()
		for i := range days {
			v := c.before
			if i >= 23 {
				v = c.last
			}
			days[i].Down[12] = v * mib
		}
		for i := 0; i < c.dropLast; i++ {
			days[29-i].Known = false
		}
		if r := rhythmOf(days, time.Time{}, usualOf{}, time.UTC); r.Trend != c.want {
			t.Errorf("%d then %d (%d last days unknown): %q, want %q", c.before, c.last, c.dropLast, r.Trend, c.want)
		}
	}
}

// Drift: the hours of yesterday, in the router's zone, in which the device
// moved at least a megabyte while it usually moves nothing then.
func TestRhythmFindsActivityInHoursItUsuallySleeps(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*3600)
	days := fullDays()
	for i := range days {
		days[i].Down[12] = 100 * mib // 14:00 there: usually busy
	}
	y := &days[len(days)-1]
	y.Down[1] = 5 * mib // 03:00 there, usually nothing
	y.Up[1] = 1 * mib
	y.Down[2] = mib / 2    // 04:00 there: under a megabyte
	y.Down[12] = 900 * mib // busy, but busy is usual then
	r := rhythmOf(days, time.Time{}, usualWeek(days, time.Time{}, loc), loc)
	if len(r.Awake) != 1 || r.Awake[0].Hour != 3 || r.Awake[0].Bytes != 6*mib {
		t.Errorf("awake hours %+v, want 03:00 with 6 MiB", r.Awake)
	}
}

type rhythmJSONTest struct {
	Confidence string `json:"confidence"`
	WholeDays  int    `json:"wholeDays"`
	MedianDay  *uint64
	Days       []struct {
		Date  string  `json:"date"`
		Bytes *uint64 `json:"bytes"`
		Whole bool    `json:"whole"`
	} `json:"days"`
	Yesterday struct {
		Date  string  `json:"date"`
		Bytes *uint64 `json:"bytes"`
		Whole bool    `json:"whole"`
	} `json:"yesterday"`
	TimesMedian  *float64 `json:"timesMedian"`
	Trend        string   `json:"trend"`
	BusiestShare float64  `json:"busiestShare"`
	Awake        []struct {
		Hour  int    `json:"hour"`
		Bytes uint64 `json:"bytes"`
	} `json:"awake"`
	Zone string `json:"zone"`
}

func TestADeviceAnswersItsRhythm(t *testing.T) {
	fl, h, _, w, ap := fixture()
	inv := fakeInv{devs: []inventory.Device{{MAC: laptop, Name: "Laptop", First: now0.Add(-time.Minute), Last: now0}}}
	fd := &fakeDaily{days: fullDays()}
	for i := range fd.days {
		fd.days[i].Down[0] = 100 * mib
	}
	fd.days[29].Down[0] = 200 * mib
	srv := New(fl, h, inv, w, ap, clock, nil, WithDaily(fd))
	rec := get(t, srv, http.MethodGet, "/api/devices/"+laptop+"/rhythm")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v rhythmJSONTest
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Confidence != "normal" || v.WholeDays != 30 || len(v.Days) != 30 || v.Days[0].Date != "2026-10-21" ||
		v.Yesterday.Date != "2026-11-19" || v.TimesMedian == nil || *v.TimesMedian != 2 || v.Trend != "steady" || v.Zone != "UTC" || v.Awake == nil {
		t.Errorf("%s", rec.Body)
	}
	if rec := get(t, srv, http.MethodGet, "/api/devices/02:00:00:00:00:99/rhythm"); rec.Code != 404 {
		t.Errorf("an unknown device: %d", rec.Code)
	}
	if rec := get(t, srv, http.MethodGet, "/api/devices/nonsense/rhythm"); rec.Code != 400 {
		t.Errorf("a malformed MAC: %d", rec.Code)
	}
	// Without the daily store there is no data, never a quiet month.
	rec = get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/rhythm")
	v = rhythmJSONTest{}
	json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Confidence != "no_data" {
		t.Errorf("without the store: %s", rec.Body)
	}
}
