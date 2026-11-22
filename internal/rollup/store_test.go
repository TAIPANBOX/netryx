package rollup

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"netryx/internal/flows"
)

// base is a UTC hour well in the past, so the wall clock never clamps it.
var base = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

const (
	phone  = "4e:c2:94:9f:07:79"
	laptop = "a8:5e:45:01:02:03"
)

// newTestStore pins the store's clock, so "the future" is a fixed point.
func newTestStore(o Options, now time.Time) *Store {
	s := NewStore(o)
	s.now = func() time.Time { return now }
	return s
}

var later = base.AddDate(1, 0, 0)

// longAgo is a range start no hour ring of these tests reaches, so a
// question starting there is answered by the days.
var longAgo = base.AddDate(0, 0, -20)

func delta(at time.Time, mac, app, cc string, up, down uint64) flows.Delta {
	return flows.Delta{At: at, MAC: mac, App: app, CC: cc, Up: up, Down: down}
}

func (b Bytes) plus(o Bytes) Bytes { return add(b, o) }

// feed adds d as the router would at the moment it happens: the store's
// clock is d.At, so its bucket is still open.
func feed(s *Store, d flows.Delta) { addAt(s, d.At, d) }

// saveAt runs SaveClosed with the store's clock at now.
func saveAt(s *Store, dir string, now time.Time) (int, error) {
	s.now = func() time.Time { return now }
	return SaveClosed(s, dir)
}

// addAt adds d with the store's clock at now.
func addAt(s *Store, now time.Time, d flows.Delta) {
	s.now = func() time.Time { return now }
	s.Add(d)
}

func rowsByKey(rows []Row) map[string]Row {
	m := map[string]Row{}
	for _, r := range rows {
		m[r.Key] = r
	}
	return m
}

func sumRows(rows []Row) Bytes {
	var t Bytes
	for _, r := range rows {
		t = t.plus(r.Bytes)
	}
	return t
}

func sumSeries(ps []Point) Bytes {
	var t Bytes
	for _, p := range ps {
		for _, b := range p.By {
			t = t.plus(b)
		}
	}
	return t
}

func TestTotalsEqualTheSumOfDeltas(t *testing.T) {
	macs := []string{phone, laptop, "tv", "nas"}
	apps := []string{"youtube", "netflix", "google", "unknown"}
	ccs := []string{"GB", "US", "--"}
	for seed := uint64(1); seed <= 200; seed++ {
		r := rand.New(rand.NewPCG(seed, seed*7+1))
		// A small device bound makes the marginals evict, so their totals
		// are proved to keep every byte they push out.
		s := newTestStore(Options{TopPerDevice: 1 + r.IntN(6), MaxKeysPerHour: 30}, later)
		want := map[string]Bytes{}
		var total Bytes
		first := base.Add(3 * time.Hour)
		var ds []flows.Delta
		for i := 0; i < 300; i++ {
			ds = append(ds, flows.Delta{
				At:     base.Add(time.Duration(r.Int64N(int64(3 * time.Hour)))),
				MAC:    macs[r.IntN(len(macs))],
				App:    apps[r.IntN(len(apps))],
				CC:     ccs[r.IntN(len(ccs))],
				Domain: string(rune('a' + r.IntN(20))),
				ASN:    uint32(r.IntN(12)),
				Org:    "ORG",
				Up:     r.Uint64N(1 << 20),
				Down:   r.Uint64N(1 << 24),
				First:  r.IntN(3) == 0,
			})
		}
		// Time runs forward on the router, so the deltas arrive in order.
		sort.Slice(ds, func(i, j int) bool { return ds[i].At.Before(ds[j].At) })
		for _, d := range ds {
			feed(s, d)
			if d.At.Before(first) {
				first = d.At
			}
			b := Bytes{Up: d.Up, Down: d.Down}
			if d.First {
				b.Flows = 1
			}
			want[d.MAC] = want[d.MAC].plus(b)
			total = total.plus(b)
		}
		end := base.Add(3*time.Hour - time.Second)
		check := func(what string, rows []Row) {
			t.Helper()
			got := rowsByKey(rows)
			if len(got) != len(want) {
				t.Fatalf("seed %d, %s: %d devices, want %d: %v", seed, what, len(got), len(want), got)
			}
			for mac, w := range want {
				if got[mac].Bytes != w {
					t.Fatalf("seed %d, %s: %s has %+v, want %+v", seed, what, mac, got[mac].Bytes, w)
				}
			}
		}
		// The marginals keep a bounded number of rows, and what they push
		// out goes to "other", so every device still adds up.
		margins := func(what string, from time.Time) {
			t.Helper()
			for _, dim := range []string{"asn", "domain"} {
				for mac, w := range want {
					if got := sumRows(s.Top(Filter{MAC: mac}, dim, from, end, 0)); got != w {
						t.Fatalf("seed %d, %s: %s marginal of %s adds up to %+v, want %+v", seed, what, dim, mac, got, w)
					}
				}
			}
		}
		// From the first delta on, the minutes have it all and answer.
		if !s.minutesCover(first) {
			t.Fatalf("seed %d: the minutes should answer from %v", seed, first)
		}
		check("minutes", s.Top(Filter{}, "mac", first, end, 0))
		if got := sumSeries(s.Series(Filter{}, first, end, time.Minute, "")); got != total {
			t.Fatalf("seed %d: the minute series adds up to %+v, want %+v", seed, got, total)
		}
		if got := sumSeries(s.Series(Filter{}, base, end, time.Hour, "mac")); got != total {
			t.Fatalf("seed %d: the hour series adds up to %+v, want %+v", seed, got, total)
		}
		if got := sumSeries(s.Series(Filter{}, longAgo, end, 24*time.Hour, "app")); got != total {
			t.Fatalf("seed %d: the day series adds up to %+v, want %+v", seed, got, total)
		}
		margins("hours", base)
		margins("days", longAgo)
		check("days", s.Top(Filter{}, "mac", longAgo, end, 0))
		// Two days on, the minutes are gone and the hours answer alone.
		feed(s, delta(base.Add(48*time.Hour), "late", "x", "--", 1, 1))
		if s.minutesCover(first) {
			t.Fatalf("seed %d: two days on the minutes cannot answer", seed)
		}
		check("hours", rowsWithout(s.Top(Filter{}, "mac", base, end, 0), "late"))
		// Ten days on, the hours are gone too and the days answer.
		feed(s, delta(base.Add(10*24*time.Hour), "later", "x", "--", 1, 1))
		check("days alone", rowsWithout(rowsWithout(s.Top(Filter{}, "mac", base, end, 0), "late"), "later"))
		margins("days alone", base)
	}
}

func rowsWithout(rows []Row, key string) []Row {
	var out []Row
	for _, r := range rows {
		if r.Key != key {
			out = append(out, r)
		}
	}
	return out
}

func TestSeriesSplitsByApp(t *testing.T) {
	s := newTestStore(Options{}, later)
	feed(s, delta(base.Add(30*time.Second), phone, "youtube", "US", 100, 1000))
	feed(s, delta(base.Add(40*time.Second), phone, "netflix", "US", 10, 200))
	feed(s, delta(base.Add(50*time.Second), phone, "youtube", "GB", 1, 1))
	// Exactly on the minute belongs to the minute it starts, not the one
	// before.
	feed(s, delta(base.Add(2*time.Minute), phone, "youtube", "US", 7, 70))
	feed(s, delta(base.Add(2*time.Minute+10*time.Second), laptop, "youtube", "US", 5, 50))

	ps := s.Series(Filter{MAC: phone}, base, base.Add(3*time.Minute), time.Minute, "app")
	if len(ps) != 4 {
		t.Fatalf("one point per minute from the first to the last inclusive: %d", len(ps))
	}
	for i, p := range ps {
		if !p.At.Equal(base.Add(time.Duration(i) * time.Minute)) {
			t.Fatalf("point %d is at %v", i, p.At)
		}
	}
	if got := ps[0].By; len(got) != 2 || got["youtube"] != (Bytes{Up: 101, Down: 1001}) || got["netflix"] != (Bytes{Up: 10, Down: 200}) {
		t.Fatalf("the first minute splits by app: %v", got)
	}
	if len(ps[1].By) != 0 || ps[1].By == nil {
		t.Fatalf("an empty minute is an empty, non-nil split: %#v", ps[1].By)
	}
	if got := ps[2].By; len(got) != 1 || got["youtube"] != (Bytes{Up: 7, Down: 70}) {
		t.Fatalf("the filter keeps the laptop out, and the boundary delta is here: %v", got)
	}
	if got := ps[3].By; len(got) != 0 {
		t.Fatalf("the last minute is empty: %v", got)
	}

	// A range is inclusive at both ends: from and to on the same minute is
	// that minute.
	if got := sumRows(s.Top(Filter{}, "app", base.Add(2*time.Minute), base.Add(2*time.Minute), 0)); got != (Bytes{Up: 12, Down: 120}) {
		t.Fatalf("a one-minute range holds the delta stamped on it: %+v", got)
	}
	if got := sumRows(s.Top(Filter{}, "app", base.Add(time.Minute), base.Add(2*time.Minute-time.Nanosecond), 0)); got != (Bytes{}) {
		t.Fatalf("a range ending just before the minute does not: %+v", got)
	}

	whole := s.Series(Filter{}, base, base.Add(3*time.Minute), time.Minute, "")
	if got := whole[2].By; len(got) != 1 || got[""] != (Bytes{Up: 12, Down: 120}) {
		t.Fatalf("unsplit puts everything under one key: %v", got)
	}
	if got, ok := whole[1].By[""]; !ok || got != (Bytes{}) {
		t.Fatalf("an empty unsplit step still says zero: %v", whole[1].By)
	}
	if s.Series(Filter{}, base, base.Add(time.Minute), time.Minute, "domain") != nil {
		t.Fatal("an unknown split is refused, not guessed")
	}
}

func TestSeriesSplitsByCountry(t *testing.T) {
	s := newTestStore(Options{}, later)
	feed(s, delta(base.Add(1*time.Minute), phone, "youtube", "US", 1, 10))
	feed(s, delta(base.Add(4*time.Minute), phone, "bbc", "GB", 2, 20))
	feed(s, delta(base.Add(6*time.Minute), phone, "youtube", "US", 3, 30))
	feed(s, delta(base.Add(70*time.Minute), laptop, "bbc", "GB", 4, 40))

	// The minutes are complete from the first one kept; the step is still
	// aligned to five minutes of UTC.
	five := s.Series(Filter{}, base.Add(time.Minute), base.Add(9*time.Minute), 5*time.Minute, "cc")
	if len(five) != 2 || !five[0].At.Equal(base) {
		t.Fatalf("two five-minute steps: %d", len(five))
	}
	if got := five[0].By; got["US"] != (Bytes{Up: 1, Down: 10}) || got["GB"] != (Bytes{Up: 2, Down: 20}) {
		t.Fatalf("the first five minutes split by country: %v", got)
	}
	if got := five[1].By; len(got) != 1 || got["US"] != (Bytes{Up: 3, Down: 30}) {
		t.Fatalf("the second five minutes: %v", got)
	}

	hours := s.Series(Filter{CC: "GB"}, base, base.Add(2*time.Hour), time.Hour, "cc")
	if len(hours) != 3 {
		t.Fatalf("three hourly points: %d", len(hours))
	}
	if hours[0].By["GB"] != (Bytes{Up: 2, Down: 20}) || hours[1].By["GB"] != (Bytes{Up: 4, Down: 40}) || len(hours[0].By) != 1 {
		t.Fatalf("hours split by country and honour the filter: %v %v", hours[0].By, hours[1].By)
	}

	byMAC := s.Series(Filter{App: "bbc"}, base, base.Add(time.Hour), time.Hour, "mac")
	if byMAC[0].By[phone] != (Bytes{Up: 2, Down: 20}) || byMAC[1].By[laptop] != (Bytes{Up: 4, Down: 40}) {
		t.Fatalf("a split by device: %v %v", byMAC[0].By, byMAC[1].By)
	}

	days := s.Series(Filter{}, longAgo, base, 24*time.Hour, "cc")
	last := days[len(days)-1]
	if !last.At.Equal(base.Truncate(24*time.Hour)) || last.By["GB"] != (Bytes{Up: 6, Down: 60}) || last.By["US"] != (Bytes{Up: 4, Down: 40}) {
		t.Fatalf("the day splits by country: %+v", last)
	}
}

func TestSeriesChoosesTheFinestRingThatCovers(t *testing.T) {
	s := newTestStore(Options{MinuteSlots: 60, HourSlots: 24, DaySlots: 10}, later)
	feed(s, delta(base, phone, "a", "GB", 1, 1))
	feed(s, delta(base.Add(3*time.Hour), phone, "a", "GB", 2, 2))
	now := base.Add(3 * time.Hour)

	if ps := s.Series(Filter{}, now.Add(-30*time.Minute), now, time.Minute, ""); len(ps) != 31 {
		t.Fatalf("a range inside the minutes is by the minute: %d", len(ps))
	}
	// Before the minute ring reaches, the hours answer, by the hour; the
	// hour the minutes begin inside is cut where they begin (11:01), and the
	// minutes answer from there.
	ps := s.Series(Filter{}, base, now, time.Minute, "")
	contiguous(t, ps)
	if len(ps) != 2+1+60 || ps[0].Step != time.Hour || ps[0].By[""] != (Bytes{Up: 1, Down: 1}) ||
		ps[2].Step != time.Minute || ps[62].Step != time.Minute || ps[62].By[""] != (Bytes{Up: 2, Down: 2}) {
		t.Fatalf("past the minutes the hours answer, by the hour: %d %v", len(ps), ps)
	}
	// Before the hour ring reaches, the days answer, by the day, and the day
	// the hours begin inside is cut where they begin.
	ps = s.Series(Filter{}, base.Add(-2*24*time.Hour), now, time.Hour, "")
	contiguous(t, ps)
	if ps[0].Step != 24*time.Hour || ps[len(ps)-1].Step != time.Hour || len(ps) > 3+24 ||
		sumSeries(ps) != (Bytes{Up: 3, Down: 3}) || ps[len(ps)-1].By[""] != (Bytes{Up: 2, Down: 2}) {
		t.Fatalf("past the hours the days answer, by the day: %d %v", len(ps), ps)
	}
	if ps := s.Series(Filter{}, base, now, 24*time.Hour, ""); len(ps) != 1 {
		t.Fatalf("a step of a day is read from the days: %d", len(ps))
	}
	// The marginals step down the same way.
	if got := sumRows(s.Top(Filter{}, "domain", base.Add(-2*24*time.Hour), now, 0)); got != (Bytes{Up: 3, Down: 3}) {
		t.Fatalf("old marginals come from the days: %+v", got)
	}
}

func TestFlowsAreCountedOncePerFlow(t *testing.T) {
	s := newTestStore(Options{}, later)
	one := delta(base.Add(time.Minute), phone, "youtube", "US", 10, 100)
	one.First = true
	feed(s, one)
	one.First = false
	one.At = one.At.Add(time.Minute)
	feed(s, one)
	one.At = one.At.Add(time.Minute)
	feed(s, one)
	other := delta(base.Add(5*time.Minute), phone, "netflix", "US", 1, 1)
	other.First = true
	feed(s, other)

	top := s.Top(Filter{}, "mac", base, base.Add(time.Hour), 0)
	if len(top) != 1 || top[0].Bytes != (Bytes{Up: 31, Down: 301, Flows: 2}) {
		t.Fatalf("three deltas of one flow and one of another are two flows: %+v", top)
	}
	if got := sumSeries(s.Series(Filter{}, base, base.Add(time.Hour), time.Hour, "")); got.Flows != 2 {
		t.Fatalf("the hours count flows the same way: %+v", got)
	}
	if got := sumRows(s.Top(Filter{}, "mac", longAgo, base, 0)); got.Flows != 2 {
		t.Fatalf("the days count flows the same way: %+v", got)
	}
}

func TestOldBucketsFallOffTheRing(t *testing.T) {
	s := newTestStore(Options{MinuteSlots: 60, HourSlots: 24, DaySlots: 3}, later)
	feed(s, delta(base, phone, "youtube", "US", 100, 100))
	// Twenty-four hours on, the same slot of a 24-slot ring is reused: the
	// phone's hour must be gone, not added to the laptop's.
	feed(s, delta(base.Add(24*time.Hour), laptop, "youtube", "US", 5, 5))
	if s.hourAt(base) != nil {
		t.Fatal("the hour a day old is still held by a 24-hour ring")
	}
	top := s.Top(Filter{}, "mac", base.Add(time.Hour), base.Add(25*time.Hour), 0)
	if len(top) != 1 || top[0].Key != laptop || top[0].Bytes != (Bytes{Up: 5, Down: 5}) {
		t.Fatalf("the slot holds only the new hour: %+v", top)
	}
	ps := s.Series(Filter{}, base.Add(24*time.Hour), base.Add(24*time.Hour+59*time.Minute), time.Minute, "")
	if len(ps) != 60 || ps[0].By[""] != (Bytes{Up: 5, Down: 5}) || sumSeries(ps) != (Bytes{Up: 5, Down: 5}) {
		t.Fatalf("the minutes answer for the last hour, and the old phone minute is not read back: %d %v", len(ps), ps[0].By)
	}
	// An hour is open until a minute after it ends, so a delta from its
	// first minute can still arrive after a 60-minute ring has moved past
	// that minute: the hours and days keep it, the minutes drop it.
	feed(s, delta(base.Add(25*time.Hour), laptop, "youtube", "US", 1, 1))
	addAt(s, base.Add(26*time.Hour+30*time.Second), delta(base.Add(25*time.Hour+10*time.Second), laptop, "youtube", "US", 2, 2))
	addAt(s, base.Add(26*time.Hour+30*time.Second), delta(base.Add(26*time.Hour+20*time.Second), laptop, "youtube", "US", 0, 0))
	addAt(s, base.Add(26*time.Hour+40*time.Second), delta(base.Add(25*time.Hour+10*time.Second), laptop, "youtube", "US", 4, 4))
	if s.Dropped() != 1 || s.Late() != 0 {
		t.Fatalf("a delta of an open hour older than the minutes reach is dropped by the minutes only: %d %d", s.Dropped(), s.Late())
	}
	if got := sumRows(s.Top(Filter{}, "mac", base.Add(25*time.Hour), base.Add(25*time.Hour), 0)); got != (Bytes{Up: 7, Down: 7}) {
		t.Fatalf("the hour keeps it: %+v", got)
	}
	// Three days on, a three-day ring has let the first day go.
	feed(s, delta(base.Add(3*24*time.Hour), laptop, "youtube", "US", 7, 7))
	days := rowsByKey(s.Top(Filter{}, "mac", longAgo, base.Add(4*24*time.Hour), 0))
	if _, ok := days[phone]; ok || days[laptop].Bytes != (Bytes{Up: 19, Down: 19}) {
		t.Fatalf("the first day fell off the days: %+v", days)
	}
}

func TestADeltaFromTheFutureLandsInTheNewestBucket(t *testing.T) {
	now := base.Add(time.Hour + 30*time.Second)
	s := newTestStore(Options{MinuteSlots: 60, HourSlots: 24}, now)
	s.Add(delta(base.Add(10*time.Minute), phone, "a", "US", 1, 1))
	// A clock gone wrong would otherwise turn the rings and wipe them.
	s.Add(delta(now.Add(48*time.Hour), phone, "a", "US", 2, 2))
	ps := s.Series(Filter{}, base, now, time.Hour, "")
	if len(ps) != 2 || ps[0].By[""] != (Bytes{Up: 1, Down: 1}) || ps[1].By[""] != (Bytes{Up: 2, Down: 2}) {
		t.Fatalf("the future delta is counted now and the past stays: %v", ps)
	}
	if s.Dropped() != 0 {
		t.Fatalf("clamped is not dropped: %d", s.Dropped())
	}
	// Up to a minute ahead is ordinary clock skew and keeps its own time.
	s.Add(delta(now.Add(50*time.Second), phone, "a", "US", 4, 4))
	m := s.Series(Filter{}, now.Add(50*time.Second), now.Add(50*time.Second), time.Minute, "")
	if len(m) != 1 || m[0].By[""] != (Bytes{Up: 4, Down: 4}) {
		t.Fatalf("skew under a minute is kept: %v", m)
	}
}

func TestKeysPastTheCapFoldIntoOther(t *testing.T) {
	s := newTestStore(Options{MaxKeysPerHour: 3}, later)
	for i, app := range []string{"a", "b", "c", "d", "e"} {
		feed(s, delta(base.Add(time.Duration(i)*time.Minute), phone, app, "US", 1, 10))
	}
	apps := rowsByKey(s.Top(Filter{MAC: phone}, "app", base, base.Add(time.Hour), 0))
	if len(apps) != 4 || apps["other"].Bytes != (Bytes{Up: 2, Down: 20}) {
		t.Fatalf("three named keys, the rest in other: %v", apps)
	}
	if cc := rowsByKey(s.Top(Filter{App: "other"}, "cc", base, base.Add(time.Hour), 0)); cc["--"].Bytes != (Bytes{Up: 2, Down: 20}) {
		t.Fatalf("the fold has no country: %v", cc)
	}
	// Hours and days fold the same way, so the totals agree after the
	// minutes go.
	for _, from := range []time.Time{base.Add(-49 * time.Hour), longAgo} {
		got := rowsByKey(s.Top(Filter{MAC: phone}, "app", from, base.Add(time.Hour), 0))
		if got["other"].Bytes != (Bytes{Up: 2, Down: 20}) {
			t.Fatalf("from %v the fold is the same: %v", from, got)
		}
	}
	// Many devices each bring their own "other"; past twice the cap they
	// share one, so a bucket stays bounded whatever arrives.
	for i := 0; i < 50; i++ {
		feed(s, delta(base.Add(10*time.Minute), string(rune('A'+i)), "x", "US", 1, 1))
	}
	for name, b := range map[string]*bucket{"hour": s.hourAt(base), "day": s.dayAt(base)} {
		if len(b.Keys) > 2*3+1 {
			t.Fatalf("the %s holds %d keys with a cap of 3", name, len(b.Keys))
		}
		if b.Keys[Key{"other", "other", "--"}].Up == 0 {
			t.Fatalf("devices past the bound share one key in the %s: %v", name, b.Keys)
		}
	}
	if got := sumRows(s.Top(Filter{}, "mac", base, base.Add(time.Hour), 0)); got.Up != 5+50 {
		t.Fatalf("folding loses no byte: %+v", got)
	}
}

func TestTopDomainsPerDeviceAreBounded(t *testing.T) {
	s := newTestStore(Options{TopPerDevice: 3}, later)
	heavy := delta(base, phone, "youtube", "US", 0, 1_000_000)
	heavy.Domain = "googlevideo.com"
	heavy.ASN, heavy.Org = 15169, "GOOGLE"
	feed(s, heavy)
	var total uint64 = 1_000_000
	for i := 0; i < 20; i++ {
		d := delta(base.Add(time.Duration(i)*time.Second), phone, "web", "GB", 0, uint64(100+i))
		d.Domain = string(rune('a'+i)) + ".example"
		d.ASN, d.Org = uint32(64500+i), "SMALL"
		feed(s, d)
		total += uint64(100 + i)
	}
	rows := s.Top(Filter{MAC: phone}, "domain", base, base.Add(time.Hour), 0)
	named := rowsWithout(rows, "other")
	if len(named) != 3 {
		t.Fatalf("three named domains and other: %+v", rows)
	}
	if rows[0].Key != "googlevideo.com" || rows[0].Bytes.Down != 1_000_000 || rows[0].Extra["app"] != "youtube" {
		t.Fatalf("the heaviest domain stays with its exact bytes and its app: %+v", rows[0])
	}
	if got := sumRows(rows).Down; got != total {
		t.Fatalf("nothing pushed out is lost: %d want %d", got, total)
	}
	if got := s.Top(Filter{MAC: phone, App: "youtube"}, "domain", base, base.Add(time.Hour), 0); len(got) != 1 || got[0].Key != "googlevideo.com" {
		t.Fatalf("domains narrow by app: %+v", got)
	}
	asn := s.Top(Filter{MAC: phone}, "asn", base, base.Add(time.Hour), 2)
	if len(asn) != 2 || asn[0].Key != "15169" || asn[0].Extra["org"] != "GOOGLE" {
		t.Fatalf("organisations are keyed by number and named: %+v", asn)
	}
	for name, b := range map[string]*bucket{"hour": s.hourAt(base), "day": s.dayAt(base)} {
		if n := len(b.Dom[phone].Rows); n > 3 {
			t.Fatalf("the %s domain marginal holds %d rows for one device", name, n)
		}
		if n := len(b.ASN[phone].Rows); n > 3 {
			t.Fatalf("the %s organisation marginal holds %d rows for one device", name, n)
		}
	}
	if got := s.Top(Filter{MAC: laptop}, "domain", base, base.Add(time.Hour), 0); len(got) != 0 {
		t.Fatalf("another device has none: %+v", got)
	}
}

func TestMarginalsAreBoundedInTotalUnderAFlood(t *testing.T) {
	s := newTestStore(Options{MaxKeysPerHour: 10, TopPerDevice: 5}, later)
	var total uint64
	for i := 0; i < 200; i++ {
		mac := fmt.Sprintf("02:00:00:00:%02x:%02x", i/256, i%256)
		for j := 0; j < 3; j++ {
			d := delta(base.Add(time.Duration(i)*time.Second), mac, "web", "GB", 0, 1)
			d.Domain = fmt.Sprintf("d%d-%d.example", i, j)
			d.ASN, d.Org = uint32(1000+i*3+j), "FLOOD"
			feed(s, d)
			total++
		}
	}
	for name, b := range map[string]*bucket{"hour": s.hourAt(base), "day": s.dayAt(base)} {
		asn, dom := 0, 0
		for _, m := range b.ASN {
			asn += len(m.Rows)
		}
		for _, m := range b.Dom {
			dom += len(m.Rows)
		}
		if asn > 10+1 || dom > 10+1 {
			t.Fatalf("the %s marginals hold %d organisation and %d domain rows with a cap of 10", name, asn, dom)
		}
		if len(b.ASN) > 10+1 || len(b.Dom) > 10+1 {
			t.Fatalf("the %s marginals hold %d and %d devices", name, len(b.ASN), len(b.Dom))
		}
	}
	for _, from := range []time.Time{base, longAgo} {
		for _, dim := range []string{"asn", "domain"} {
			if got := sumRows(s.Top(Filter{}, dim, from, base.Add(time.Hour), 0)); got.Down != total {
				t.Fatalf("from %v the %s rows still add up: %d want %d", from, dim, got.Down, total)
			}
		}
	}
	// What fitted before the flood is still named exactly.
	first := "02:00:00:00:00:00"
	if got := s.Top(Filter{MAC: first}, "domain", base, base.Add(time.Hour), 0); len(got) != 3 || got[0].Bytes.Down != 1 {
		t.Fatalf("the first device keeps its rows: %+v", got)
	}
}

func TestTopNarrowsByTheFilterAndRanksByBytes(t *testing.T) {
	s := newTestStore(Options{}, later)
	feed(s, delta(base, phone, "youtube", "US", 1, 500))
	feed(s, delta(base, phone, "bbc", "GB", 1, 300))
	feed(s, delta(base, laptop, "youtube", "US", 1, 900))
	feed(s, delta(base, laptop, "steam", "DE", 1, 100))

	apps := s.Top(Filter{}, "app", base, base.Add(time.Hour), 0)
	if len(apps) != 3 || apps[0].Key != "youtube" || apps[0].Bytes.Down != 1400 || apps[2].Key != "steam" {
		t.Fatalf("apps ranked by bytes: %+v", apps)
	}
	if got := s.Top(Filter{}, "app", base, base.Add(time.Hour), 1); len(got) != 1 {
		t.Fatalf("n limits the rows: %+v", got)
	}
	if got := s.Top(Filter{CC: "US"}, "mac", base, base.Add(time.Hour), 0); len(got) != 2 || got[0].Key != laptop {
		t.Fatalf("devices narrowed by country: %+v", got)
	}
	if got := s.Top(Filter{MAC: laptop, App: "steam"}, "cc", base, base.Add(time.Hour), 0); len(got) != 1 || got[0].Key != "DE" {
		t.Fatalf("countries narrowed by device and app: %+v", got)
	}
	if got := s.Top(Filter{}, "app", base.Add(time.Hour), base.Add(2*time.Hour), 0); len(got) != 0 {
		t.Fatalf("a range after the data is empty: %+v", got)
	}
	if got := s.Top(Filter{}, "app", base.Add(-time.Hour), base.Add(-time.Second), 0); len(got) != 0 {
		t.Fatalf("a range ending a second before the data excludes it: %+v", got)
	}
	if s.Top(Filter{}, "colour", base, base.Add(time.Hour), 0) != nil {
		t.Fatal("an unknown dimension is refused")
	}
}

func populated() *Store { return populatedWith(nil) }

// populatedWith lets a test prepare the store before the deltas go in.
func populatedWith(prep func(*Store)) *Store {
	s := newTestStore(Options{TopPerDevice: 4}, later)
	if prep != nil {
		prep(s)
	}
	r := rand.New(rand.NewPCG(4, 4))
	var ds []flows.Delta
	for i := 0; i < 500; i++ {
		d := flows.Delta{
			At:     base.Add(time.Duration(r.Int64N(int64(5 * time.Hour)))),
			MAC:    []string{phone, laptop, "tv"}[r.IntN(3)],
			App:    []string{"youtube", "bbc", "steam"}[r.IntN(3)],
			CC:     []string{"GB", "US"}[r.IntN(2)],
			Domain: string(rune('a' + r.IntN(10))),
			ASN:    uint32(r.IntN(8)),
			Up:     r.Uint64N(1000), Down: r.Uint64N(100000),
			First: r.IntN(2) == 0,
		}
		d.Org = "ORG" + string(rune('0'+d.ASN))
		ds = append(ds, d)
	}
	// Time runs forward on the router.
	sort.Slice(ds, func(i, j int) bool { return ds[i].At.Before(ds[j].At) })
	for _, d := range ds {
		feed(s, d)
	}
	return s
}

// closedAll is a moment by which every hour of populated() has closed, and
// its day has not.
var closedAll = base.Add(5*time.Hour + 2*time.Minute)

// dayClosed is a moment by which the day of base has closed.
var dayClosed = base.Truncate(24 * time.Hour).Add(24*time.Hour + 2*time.Minute)

func sameTops(t *testing.T, what string, a, b *Store, from, to time.Time) {
	t.Helper()
	for _, dim := range []string{"mac", "app", "cc", "asn", "domain"} {
		for _, f := range []Filter{{}, {MAC: phone}} {
			x, y := a.Top(f, dim, from, to, 0), b.Top(f, dim, from, to, 0)
			if len(x) == 0 || !sameRows(x, y) {
				t.Fatalf("%s: %s %+v:\n%+v\nwant\n%+v", what, dim, f, y, x)
			}
		}
	}
}

func TestHistorySurvivesARestart(t *testing.T) {
	s := populated()
	dir := t.TempDir()
	if n, err := saveAt(s, dir, closedAll); err != nil || n != 5 {
		t.Fatalf("five closed hours are written: %d %v", n, err)
	}
	back := newTestStore(Options{TopPerDevice: 4}, later)
	if err := LoadDir(back, dir); err != nil {
		t.Fatal(err)
	}
	end := base.Add(5 * time.Hour)
	sameTops(t, "the hours after a restart", s, back, base, end)
	a := s.Series(Filter{}, base, end, time.Hour, "app")
	b := back.Series(Filter{}, base, end, time.Hour, "app")
	if len(a) != len(b) || len(a) != 6 {
		t.Fatalf("the hour series has %d points after a restart, want %d", len(b), len(a))
	}
	for i := range a {
		if !a[i].At.Equal(b[i].At) || !sameSplit(a[i].By, b[i].By) {
			t.Fatalf("hour %d after a restart: %v want %v", i, b[i].By, a[i].By)
		}
	}
	// The day was still open, so it has no file: its closed hours rebuild
	// it. Its keys are exact. Its marginals are the hours' top rows added
	// up, which need not be the rows a live day kept, so they are held to
	// each device's total, not to the same rows.
	for _, dim := range []string{"mac", "app", "cc"} {
		if x, y := s.Top(Filter{}, dim, longAgo, end, 0), back.Top(Filter{}, dim, longAgo, end, 0); !sameRows(x, y) {
			t.Fatalf("the rebuilt day by %s:\n%+v\nwant\n%+v", dim, y, x)
		}
	}
	for _, mac := range []string{phone, laptop, "tv"} {
		want := sumRows(s.Top(Filter{MAC: mac}, "mac", longAgo, end, 0))
		for _, dim := range []string{"asn", "domain"} {
			if got := sumRows(back.Top(Filter{MAC: mac}, dim, longAgo, end, 0)); got != want || got == (Bytes{}) {
				t.Fatalf("the rebuilt day's %s rows of %s add up to %+v, want %+v", dim, mac, got, want)
			}
		}
	}

	// Once the day closes it is written once, and a restart reads it from
	// its own file, not again from the hours as well.
	if n, err := saveAt(s, dir, dayClosed); err != nil || n != 1 {
		t.Fatalf("the closed day is written: %d %v", n, err)
	}
	again := newTestStore(Options{TopPerDevice: 4}, later)
	if err := LoadDir(again, dir); err != nil {
		t.Fatal(err)
	}
	sameTops(t, "the day from its file", s, again, longAgo, end)
	sameTops(t, "the hours beside it", s, again, base, end)
}

func TestOnlyClosedBucketsAreWrittenAndEachOnce(t *testing.T) {
	dir := t.TempDir()
	if err := LoadDir(newTestStore(Options{}, later), filepath.Join(dir, "none")); err != nil {
		t.Fatalf("a first start has no directory yet, and that is fine: %v", err)
	}
	s := newTestStore(Options{}, later)
	feed(s, delta(base.Add(10*time.Minute), phone, "a", "GB", 1, 1))
	feed(s, delta(base.Add(70*time.Minute), phone, "a", "GB", 2, 2))
	hourFile := func(at time.Time) string {
		return filepath.Join(dir, "hours", strconv.FormatInt(at.Unix()/3600, 10)+".gob")
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	if n, err := saveAt(s, dir, base.Add(90*time.Minute)); err != nil || n != 1 {
		t.Fatalf("inside the second hour only the first is closed: %d %v", n, err)
	}
	if !exists(hourFile(base)) || exists(hourFile(base.Add(time.Hour))) {
		t.Fatal("the closed hour is on disk and the open one is not")
	}
	if n, err := saveAt(s, dir, base.Add(100*time.Minute)); err != nil || n != 0 {
		t.Fatalf("a second save in the same hour writes nothing: %d %v", n, err)
	}
	// An hour closes a minute after it ends, so a delta that arrives a
	// little late still reaches its file.
	if n, _ := saveAt(s, dir, base.Add(2*time.Hour+30*time.Second)); n != 0 {
		t.Fatalf("an hour is not closed the instant it ends: %d", n)
	}
	addAt(s, base.Add(2*time.Hour+30*time.Second), delta(base.Add(119*time.Minute), phone, "a", "GB", 4, 4))
	if n, err := saveAt(s, dir, base.Add(2*time.Hour+2*time.Minute)); err != nil || n != 1 {
		t.Fatalf("crossing an hour writes one file: %d %v", n, err)
	}
	back := newTestStore(Options{}, later)
	if err := LoadDir(back, dir); err != nil {
		t.Fatal(err)
	}
	if got := sumRows(back.Top(Filter{}, "mac", base.Add(time.Hour), base.Add(time.Hour), 0)); got != (Bytes{Up: 6, Down: 6}) {
		t.Fatalf("the second hour's file holds its late delta: %+v", got)
	}
	dayFile := filepath.Join(dir, "days", "20260302.gob")
	if exists(dayFile) {
		t.Fatal("an open day is not written")
	}
	if n, err := saveAt(s, dir, dayClosed); err != nil || n != 1 || !exists(dayFile) {
		t.Fatalf("a closed day is written once: %d %v", n, err)
	}
	if n, err := saveAt(s, dir, dayClosed.Add(time.Minute)); err != nil || n != 0 {
		t.Fatalf("and not again: %d %v", n, err)
	}
}

func TestOneCorruptFileDoesNotEmptyTheStore(t *testing.T) {
	s := populated()
	dir := t.TempDir()
	if _, err := saveAt(s, dir, closedAll); err != nil {
		t.Fatal(err)
	}
	hours := filepath.Join(dir, "hours")
	name := func(at time.Time) string { return strconv.FormatInt(at.Unix()/3600, 10) + ".gob" }
	victim := filepath.Join(hours, name(base.Add(2*time.Hour)))
	whole, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	// Garbage under a good name, a file of another version, and a good
	// file under the name of another hour.
	junk := filepath.Join(hours, name(base.Add(-time.Hour)))
	os.WriteFile(junk, []byte("not a gob stream at all"), 0o600)
	var v2 bytes.Buffer
	enc := gob.NewEncoder(&v2)
	enc.Encode(header{Version: 3, Saved: base})
	enc.Encode(newBucket(base.Add(-2 * time.Hour)))
	versioned := filepath.Join(hours, name(base.Add(-2*time.Hour)))
	os.WriteFile(versioned, v2.Bytes(), 0o600)
	misnamed := filepath.Join(hours, name(base.Add(-3*time.Hour)))
	os.WriteFile(misnamed, whole, 0o600)
	// A file this does not name is not its business, and not an error.
	os.WriteFile(filepath.Join(hours, "README"), []byte("hours of netryx"), 0o600)

	want := s.Series(Filter{}, base, base.Add(4*time.Hour), time.Hour, "")
	for cut := 0; cut < len(whole); cut += 1 + len(whole)/40 {
		if err := os.WriteFile(victim, whole[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		back := newTestStore(Options{TopPerDevice: 4}, later)
		err := LoadDir(back, dir)
		if err == nil {
			t.Fatalf("a file cut at %d of %d bytes was not reported", cut, len(whole))
		}
		if strings.Contains(err.Error(), "README") {
			t.Fatalf("a file of another name is reported: %v", err)
		}
		for _, bad := range []string{victim, junk, versioned, misnamed} {
			if !strings.Contains(err.Error(), filepath.Base(bad)) {
				t.Fatalf("the report does not name %s: %v", filepath.Base(bad), err)
			}
		}
		got := back.Series(Filter{}, base, base.Add(4*time.Hour), time.Hour, "")
		for i := range want {
			w := want[i].By[""]
			if i == 2 {
				w = Bytes{}
			}
			if got[i].By[""] != w {
				t.Fatalf("cut %d: hour %d has %+v, want %+v: every good file loads", cut, i, got[i].By[""], w)
			}
		}
	}
}

func TestFilesPastRetentionAreDeleted(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(Options{HourSlots: 24, DaySlots: 3}, later)
	feed(s, delta(base.Add(10*time.Minute), phone, "a", "GB", 1, 1))
	if n, err := saveAt(s, dir, dayClosed); err != nil || n != 2 {
		t.Fatalf("the hour and its day are written: %d %v", n, err)
	}
	hours, days := filepath.Join(dir, "hours"), filepath.Join(dir, "days")
	os.WriteFile(filepath.Join(hours, "notes.txt"), []byte("kept"), 0o600)
	os.WriteFile(filepath.Join(hours, "123.gob.tmp4567"), []byte("cut short"), 0o600)

	feed(s, delta(base.Add(5*24*time.Hour+10*time.Minute), phone, "a", "GB", 1, 1))
	if n, err := saveAt(s, dir, base.Add(5*24*time.Hour+2*time.Hour)); err != nil || n != 1 {
		t.Fatalf("the new hour is written: %d %v", n, err)
	}
	list := func(d string) []string {
		es, _ := os.ReadDir(d)
		var out []string
		for _, e := range es {
			out = append(out, e.Name())
		}
		sort.Strings(out)
		return out
	}
	newHour := strconv.FormatInt(base.Add(5*24*time.Hour).Unix()/3600, 10) + ".gob"
	if got := list(hours); len(got) != 2 || got[0] != newHour || got[1] != "notes.txt" {
		t.Fatalf("hours past 24 h and a write cut short are gone, other files stay: %v", got)
	}
	if got := list(days); len(got) != 0 {
		t.Fatalf("a day past three days is gone: %v", got)
	}
}

func TestAfterARestartMinuteQuestionsAreAnsweredFromHours(t *testing.T) {
	s := populated()
	dir := t.TempDir()
	if _, err := saveAt(s, dir, closedAll); err != nil {
		t.Fatal(err)
	}
	back := newTestStore(Options{}, closedAll)
	if err := LoadDir(back, dir); err != nil {
		t.Fatal(err)
	}
	// Minutes are not saved; zeros would read as a quiet house.
	ps := back.Series(Filter{}, base, base.Add(5*time.Hour), 5*time.Minute, "")
	if len(ps) != 6 || ps[0].At != base || ps[0].Step != time.Hour {
		t.Fatalf("the series falls back to hourly points, and says so: %d %+v", len(ps), ps)
	}
	if got, want := sumSeries(ps), sumSeries(s.Series(Filter{}, base, base.Add(5*time.Hour), time.Hour, "")); got != want {
		t.Fatalf("the hourly fallback holds all of it: %+v want %+v", got, want)
	}
	// New minutes start being kept; before the first of them the hour
	// answers, cut where they begin.
	feed(back, delta(base.Add(6*time.Hour+time.Minute), phone, "x", "GB", 1, 1))
	ps = back.Series(Filter{}, base.Add(6*time.Hour), base.Add(6*time.Hour+2*time.Minute), time.Minute, "")
	contiguous(t, ps)
	if len(ps) != 3 || ps[0].Step != time.Minute || ps[1].By[""] != (Bytes{Up: 1, Down: 1}) || sumSeries(ps) != (Bytes{Up: 1, Down: 1}) {
		t.Fatalf("a range starting before the first kept minute is the hour's rest, then minutes: %d %+v", len(ps), ps)
	}
	if ps := back.Series(Filter{}, base.Add(6*time.Hour+time.Minute), base.Add(6*time.Hour+2*time.Minute), time.Minute, ""); len(ps) != 2 {
		t.Fatalf("a range the minutes cover is by the minute: %d", len(ps))
	}
}

func sameRows(a, b []Row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].Bytes != b[i].Bytes || len(a[i].Extra) != len(b[i].Extra) {
			return false
		}
		for k, v := range a[i].Extra {
			if b[i].Extra[k] != v {
				return false
			}
		}
	}
	return true
}

func sameSplit(a, b map[string]Bytes) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func assertEmptyAndUsable(t *testing.T, s *Store, what string) {
	t.Helper()
	if got := s.Top(Filter{}, "mac", longAgo, later, 0); len(got) != 0 {
		t.Fatalf("%s: the store is not empty: %+v", what, got)
	}
	if got := s.Top(Filter{}, "domain", longAgo, later, 0); len(got) != 0 {
		t.Fatalf("%s: the marginals are not empty: %+v", what, got)
	}
	feed(s, delta(base, phone, "x", "GB", 1, 2))
	if got := s.Top(Filter{}, "mac", base, base.Add(time.Hour), 0); len(got) != 1 || got[0].Bytes != (Bytes{Up: 1, Down: 2}) {
		t.Fatalf("%s: the store does not work after a failed load: %+v", what, got)
	}
}

func TestALoadedFileIsHeldToTheBoundsOfTheStoreThatLoadsIt(t *testing.T) {
	s := newTestStore(Options{TopPerDevice: 20}, later)
	feed(s, delta(base.Add(-5*24*time.Hour), laptop, "web", "GB", 1, 1))
	for i := 0; i < 20; i++ {
		d := delta(base, phone, "web", "GB", 0, uint64(10+i))
		d.Domain = string(rune('a' + i))
		feed(s, d)
	}
	dir := t.TempDir()
	if _, err := saveAt(s, dir, dayClosed); err != nil {
		t.Fatal(err)
	}
	// A device that holds only pushed-out bytes, as a file written under
	// other options could, keeps them as a row.
	onlyOther := newBucket(base.Add(-time.Hour))
	onlyOther.Dom[laptop] = &margin[domKey]{Rows: map[domKey]cell{}, Other: Bytes{Down: 9}}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	enc.Encode(header{Version: version, Saved: base})
	enc.Encode(onlyOther)
	os.WriteFile(filepath.Join(dir, "hours", strconv.FormatInt(base.Add(-time.Hour).Unix()/3600, 10)+".gob"), buf.Bytes(), 0o600)

	small := newTestStore(Options{TopPerDevice: 5, HourSlots: 24, DaySlots: 3}, base.Add(2*time.Hour))
	if err := LoadDir(small, dir); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]*bucket{"hour": small.hourAt(base), "day": small.dayAt(base)} {
		if n := len(b.Dom[phone].Rows); n > 5 {
			t.Fatalf("a smaller bound applies to the loaded %s: %d rows", name, n)
		}
	}
	if got := sumRows(small.Top(Filter{MAC: phone}, "domain", base, base.Add(time.Hour), 0)).Down; got != 390 {
		t.Fatalf("and keeps every byte: %d", got)
	}
	if got := small.Top(Filter{MAC: laptop}, "domain", base.Add(-time.Hour), base.Add(-time.Hour), 0); len(got) != 1 || got[0].Bytes.Down != 9 {
		t.Fatalf("pushed-out bytes alone: %+v", got)
	}
	if got := small.Top(Filter{MAC: laptop}, "mac", longAgo, base.Add(-2*24*time.Hour), 0); len(got) != 0 {
		t.Fatalf("hours and days outside smaller rings are not loaded: %+v", got)
	}
}

func TestTheStoreIsSafeToReadWhileItIsWritten(t *testing.T) {
	s := newTestStore(Options{}, base.Add(59*time.Minute))
	dir := t.TempDir()
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				d := delta(base.Add(time.Duration(i)*10*time.Second), phone, "a", "GB", 1, 1)
				d.First = i == 0
				d.Domain = "x"
				s.Add(d)
			}
		}(g)
	}
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				s.Top(Filter{}, "domain", base, base.Add(time.Hour), 5)
				s.Series(Filter{}, base, base.Add(time.Hour), time.Minute, "app")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			_ = SaveOpen(s, dir)
		}
	}()
	wg.Wait()
	if got := sumRows(s.Top(Filter{}, "mac", base, base.Add(time.Hour), 0)); got != (Bytes{Up: 1200, Down: 1200, Flows: 4}) {
		t.Fatalf("every concurrent delta is counted once: %+v", got)
	}
}

func TestSeriesIsBoundedAndRefusesNonsense(t *testing.T) {
	s := newTestStore(Options{MinuteSlots: 60, HourSlots: 24, DaySlots: 5}, later)
	feed(s, delta(base, phone, "a", "GB", 1, 1))
	if s.Series(Filter{}, base, base.Add(-time.Minute), time.Minute, "") != nil {
		t.Fatal("a range that ends before it starts has no points")
	}
	if s.Series(Filter{}, base, base.Add(time.Hour), 0, "") != nil {
		t.Fatal("a zero step has no points")
	}
	// Ten years by the hour is read from the days and the hours, each cut to
	// its ring.
	ps := s.Series(Filter{}, base.AddDate(-10, 0, 0), base, time.Hour, "")
	if len(ps) > 5+24 || sumSeries(ps) != (Bytes{Up: 1, Down: 1}) {
		t.Fatalf("a huge range is bounded to the ring and still finds the data: %d %+v", len(ps), sumSeries(ps))
	}
	ps = s.Series(Filter{}, base, base.AddDate(10, 0, 0), time.Hour, "")
	if len(ps) > 24 || sumSeries(ps) != (Bytes{Up: 1, Down: 1}) {
		t.Fatalf("a range far into the future is bounded too: %d", len(ps))
	}
	days := s.Series(Filter{}, base, base.Add(47*time.Hour), 24*time.Hour, "")
	if len(days) == 0 || !days[0].At.Equal(base.Truncate(24*time.Hour)) {
		t.Fatalf("a day step is aligned to UTC midnight: %+v", days)
	}
}

func TestASaveThatCannotWriteSaysSo(t *testing.T) {
	s := populated()
	// A state directory that cannot be made is reported, not ignored.
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if n, err := saveAt(s, filepath.Join(file, "state"), closedAll); err == nil || n != 0 {
		t.Fatalf("a state directory under a file: %d %v", n, err)
	}
	if os.Getuid() == 0 {
		return
	}
	// A directory that cannot be written leaves what is there alone.
	dir := t.TempDir()
	if _, err := saveAt(s, dir, base.Add(3*time.Hour+2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	hours := filepath.Join(dir, "hours")
	before, _ := os.ReadDir(hours)
	os.Chmod(hours, 0o500)
	defer os.Chmod(hours, 0o700)
	if n, err := saveAt(s, dir, closedAll); err == nil || n != 0 {
		t.Fatalf("a read-only directory: %d %v", n, err)
	}
	after, _ := os.ReadDir(hours)
	if len(after) != len(before) {
		t.Fatalf("a failed save changed the directory: %d files, was %d", len(after), len(before))
	}
}

func TestTimeBefore1970StillHasASlot(t *testing.T) {
	r := ring{step: time.Minute, n: 60}
	for _, at := range []time.Time{time.Unix(-61, 0), time.Unix(-3600, 0), time.Unix(0, 0)} {
		if i := r.index(at); i < 0 || i >= 60 {
			t.Fatalf("%v maps to slot %d", at, i)
		}
	}
}

func TestTheSameNameUnderTwoAppsIsTwoRows(t *testing.T) {
	s := newTestStore(Options{}, later)
	for _, app := range []string{"youtube", "google"} {
		d := delta(base, phone, app, "US", 5, 5)
		d.Domain = "www.google.com"
		feed(s, d)
	}
	rows := s.Top(Filter{}, "domain", base, base, 0)
	if len(rows) != 2 || rows[0].Extra["app"] != "google" || rows[1].Extra["app"] != "youtube" {
		t.Fatalf("one row per app, ties in a fixed order: %+v", rows)
	}
	if s.hourAt(base.Add(-48*time.Hour)) != nil || s.hourAt(base.Add(time.Hour)) != nil {
		t.Fatal("an hour the ring does not hold has no bucket")
	}
}

// fresh returns a copy of x in its own memory, as a delta from another
// flow would carry it.
func fresh(x string) string { return string([]byte(x)) }

func TestStringsAreStoredOnce(t *testing.T) {
	s := newTestStore(Options{}, later)
	for i := 0; i < 3; i++ {
		d := delta(base.Add(time.Duration(i)*time.Minute), fresh(phone), fresh("youtube"), fresh("US"), 1, 1)
		d.Domain, d.Org, d.ASN = fresh("googlevideo.com"), fresh("GOOGLE"), 15169
		feed(s, d)
	}
	seen := map[string]map[*byte]bool{}
	note := func(x string) {
		if seen[x] == nil {
			seen[x] = map[*byte]bool{}
		}
		seen[x][unsafe.StringData(x)] = true
	}
	for _, m := range s.mins {
		if m == nil {
			continue
		}
		for k := range m.Keys {
			note(k.MAC)
			note(k.App)
			note(k.CC)
		}
	}
	for _, b := range []*bucket{s.hourAt(base), s.dayAt(base)} {
		for k := range b.Keys {
			note(k.MAC)
			note(k.App)
		}
		for mac, m := range b.ASN {
			note(mac)
			for _, c := range m.Rows {
				note(c.Org)
			}
		}
		for mac, m := range b.Dom {
			note(mac)
			for dk := range m.Rows {
				note(dk.App)
				note(dk.Domain)
			}
		}
	}
	for _, x := range []string{phone, "youtube", "US", "googlevideo.com", "GOOGLE"} {
		if n := len(seen[x]); n != 1 {
			t.Fatalf("%q is stored %d times", x, n)
		}
	}
}

func TestTheInternTableIsBounded(t *testing.T) {
	s := newTestStore(Options{MaxKeysPerHour: 10, MinuteSlots: 2, HourSlots: 2, DaySlots: 2}, later)
	for i := 0; i < 500; i++ {
		d := delta(base, phone, "web", "GB", 0, 1)
		d.Domain = fmt.Sprintf("d%d.example", i)
		feed(s, d)
	}
	if n := len(s.strs); n > s.strMax {
		t.Fatalf("the intern table holds %d strings, bound %d", n, s.strMax)
	}
	if got := sumRows(s.Top(Filter{}, "domain", base, base, 0)).Down; got != 500 {
		t.Fatalf("a full table still counts every delta: %d", got)
	}
	// Once the rings have moved past them, the dead strings are let go and
	// new ones are shared again.
	feed(s, delta(base.Add(8*24*time.Hour), laptop, "web", "GB", 0, 1))
	if n := len(s.strs); n > s.strMax/2 {
		t.Fatalf("after the rings move on the table still holds %d strings", n)
	}
	for i := 0; i < 2; i++ {
		d := delta(base.Add(8*24*time.Hour), laptop, "web", "GB", 0, 1)
		d.Domain = fresh("new.example")
		feed(s, d)
	}
	h := s.hourAt(base.Add(8 * 24 * time.Hour))
	if h == nil || len(h.Dom[laptop].Rows) != 2 {
		t.Fatalf("the new hour has its rows: %+v", h)
	}
	if got := s.strs["new.example"]; unsafe.StringData(got) == nil {
		t.Fatal("a new string after the purge is interned again")
	}
}

// headerOf reads the header of a bucket file.
func headerOf(t *testing.T, path string) header {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var hd header
	if err := gob.NewDecoder(f).Decode(&hd); err != nil {
		t.Fatal(err)
	}
	return hd
}

func TestAShutdownMidHourLosesNothing(t *testing.T) {
	dir := t.TempDir()
	hourFile := filepath.Join(dir, "hours", strconv.FormatInt(base.Unix()/3600, 10)+".gob")
	dayFile := filepath.Join(dir, "days", "20260302.gob")

	before := newTestStore(Options{}, base.Add(20*time.Minute))
	feed(before, delta(base.Add(5*time.Minute), phone, "a", "GB", 1, 1))
	feed(before, delta(base.Add(10*time.Minute), phone, "a", "GB", 2, 2))
	if err := SaveOpen(before, dir); err != nil {
		t.Fatal(err)
	}
	if !headerOf(t, hourFile).Open {
		t.Fatal("the open hour is written, marked open")
	}
	if _, err := os.Stat(dayFile); err == nil {
		t.Fatal("the open day is not written: its hours rebuild it")
	}

	// The upgrade restarts in the same hour and the hour carries on.
	after := newTestStore(Options{}, base.Add(25*time.Minute))
	if err := LoadDir(after, dir); err != nil {
		t.Fatal(err)
	}
	feed(after, delta(base.Add(30*time.Minute), phone, "a", "GB", 4, 4))
	if got := sumRows(after.Top(Filter{}, "mac", base, base, 0)); got != (Bytes{Up: 7, Down: 7}) {
		t.Fatalf("the hour holds both halves: %+v", got)
	}

	if n, err := saveAt(after, dir, base.Add(time.Hour+2*time.Minute)); err != nil || n != 1 {
		t.Fatalf("the closed hour rewrites the open file: %d %v", n, err)
	}
	if headerOf(t, hourFile).Open {
		t.Fatal("the rewritten hour is final")
	}
	if n, err := saveAt(after, dir, base.Add(time.Hour+30*time.Minute)); err != nil || n != 0 {
		t.Fatalf("and is rewritten only once: %d %v", n, err)
	}
	if n, err := saveAt(after, dir, dayClosed); err != nil || n != 1 || headerOf(t, dayFile).Open {
		t.Fatalf("the closed day is written once: %d %v", n, err)
	}

	again := newTestStore(Options{}, later)
	if err := LoadDir(again, dir); err != nil {
		t.Fatal(err)
	}
	for _, from := range []time.Time{base, longAgo} {
		if got := sumRows(again.Top(Filter{}, "mac", from, base, 0)); got != (Bytes{Up: 7, Down: 7}) {
			t.Fatalf("from %v the saved totals are the sum of both halves: %+v", from, got)
		}
	}
}

func TestALoadedOpenHourIsNotMistakenForClosed(t *testing.T) {
	dir := t.TempDir()
	hourFile := filepath.Join(dir, "hours", strconv.FormatInt(base.Unix()/3600, 10)+".gob")
	first := newTestStore(Options{}, base.Add(10*time.Minute))
	feed(first, delta(base.Add(5*time.Minute), phone, "a", "GB", 1, 1))
	// A minute after the previous hour ended it is not closed yet: a
	// shutdown then writes it too, as open, so nothing waits on the next
	// hourly save.
	feed(first, delta(base.Add(-30*time.Minute), laptop, "a", "GB", 8, 8))
	first.now = func() time.Time { return base.Add(30 * time.Second) }
	if err := SaveOpen(first, dir); err != nil {
		t.Fatal(err)
	}
	prev := filepath.Join(dir, "hours", strconv.FormatInt(base.Add(-time.Hour).Unix()/3600, 10)+".gob")
	if !headerOf(t, prev).Open {
		t.Fatal("an hour inside its grace minute is written as open")
	}

	second := newTestStore(Options{}, base.Add(15*time.Minute))
	if err := LoadDir(second, dir); err != nil {
		t.Fatal(err)
	}
	if n, err := saveAt(second, dir, base.Add(20*time.Minute)); err != nil || n != 1 {
		t.Fatalf("only the previous hour has closed, and its open file is rewritten: %d %v", n, err)
	}
	if !headerOf(t, hourFile).Open {
		t.Fatal("the current hour's open file is left alone while it is open")
	}
	feed(second, delta(base.Add(40*time.Minute), phone, "a", "GB", 2, 2))
	if n, err := saveAt(second, dir, base.Add(time.Hour+2*time.Minute)); err != nil || n != 1 {
		t.Fatalf("an open file on disk is not a closed bucket: %d %v", n, err)
	}
	third := newTestStore(Options{}, later)
	if err := LoadDir(third, dir); err != nil {
		t.Fatal(err)
	}
	if got := sumRows(third.Top(Filter{MAC: phone}, "mac", base, base, 0)); got != (Bytes{Up: 3, Down: 3}) {
		t.Fatalf("the hour on disk has what came after the restart: %+v", got)
	}
}

func hourPath(dir string, at time.Time) string {
	return filepath.Join(dir, "hours", strconv.FormatInt(at.Unix()/3600, 10)+".gob")
}

func TestAPowerCutAfterARestartLosesNoPartOfTheDay(t *testing.T) {
	dir := t.TempDir()
	at := func(h, m int) time.Time {
		return base.Truncate(24 * time.Hour).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	var total Bytes
	put := func(s *Store, when time.Time, n uint64) {
		feed(s, delta(when, phone, "a", "GB", n, n))
		total = total.plus(Bytes{Up: n, Down: n})
	}
	// 09:10 and 10:10 happen; 10:30 is an upgrade.
	first := newTestStore(Options{}, at(9, 0))
	put(first, at(9, 10), 1)
	if _, err := saveAt(first, dir, at(10, 5)); err != nil {
		t.Fatal(err)
	}
	put(first, at(10, 10), 2)
	first.now = func() time.Time { return at(10, 30) }
	if err := SaveOpen(first, dir); err != nil {
		t.Fatal(err)
	}
	// The restarted service runs on to 15:00, saving each hour as it closes.
	second := newTestStore(Options{}, at(10, 35))
	if err := LoadDir(second, dir); err != nil {
		t.Fatal(err)
	}
	for h := 10; h <= 14; h++ {
		put(second, at(h, 40), uint64(10*h))
		if _, err := saveAt(second, dir, at(h+1, 2)); err != nil {
			t.Fatal(err)
		}
	}
	// An open day file left by older code is not believed either.
	stale := newBucket(at(0, 0))
	stale.Keys[Key{phone, "a", "GB"}] = Bytes{Up: 1000, Down: 1000}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	enc.Encode(header{Version: version, Saved: at(10, 30), Open: true})
	enc.Encode(stale)
	os.WriteFile(filepath.Join(dir, "days", "20260302.gob"), buf.Bytes(), 0o600)

	// Power cut at 15:03: no SaveOpen. The next start rebuilds the day.
	third := newTestStore(Options{}, at(15, 10))
	if err := LoadDir(third, dir); err != nil {
		t.Fatal(err)
	}
	put(third, at(15, 20), 7)
	if n, err := saveAt(third, dir, dayClosed); err != nil || n != 2 {
		t.Fatalf("the last hour and the day are written: %d %v", n, err)
	}
	last := newTestStore(Options{}, later)
	if err := LoadDir(last, dir); err != nil {
		t.Fatal(err)
	}
	if got := sumRows(last.Top(Filter{}, "mac", longAgo, at(23, 0), 0)); got != total {
		t.Fatalf("the day is every hour of it: %+v, want %+v", got, total)
	}
	if got := sumSeries(last.Series(Filter{}, at(0, 0), at(23, 0), time.Hour, "")); got != total {
		t.Fatalf("and its hours agree: %+v, want %+v", got, total)
	}
}

func TestALateDeltaForAClosedHourIsCountedAsLate(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(Options{}, later)
	addAt(s, base.Add(30*time.Minute), delta(base.Add(10*time.Minute), phone, "a", "GB", 1, 1))
	// Inside its grace minute the hour still takes a delta.
	addAt(s, base.Add(time.Hour+30*time.Second), delta(base.Add(59*time.Minute), phone, "a", "GB", 2, 2))
	if n, err := saveAt(s, dir, base.Add(time.Hour+2*time.Minute)); err != nil || n != 1 {
		t.Fatalf("the hour is written: %d %v", n, err)
	}
	// After it, the file is final and a delta for it would live only in RAM.
	addAt(s, base.Add(time.Hour+5*time.Minute), delta(base.Add(50*time.Minute), phone, "a", "GB", 4, 4))
	if s.Late() != 1 || s.Dropped() != 0 {
		t.Fatalf("a delta for a closed hour is late, not dropped: late %d dropped %d", s.Late(), s.Dropped())
	}
	want := Bytes{Up: 3, Down: 3}
	for _, from := range []time.Time{base, longAgo} {
		if got := sumRows(s.Top(Filter{}, "mac", from, base.Add(time.Hour), 0)); got != want {
			t.Fatalf("from %v the late delta is not counted: %+v", from, got)
		}
	}
	back := newTestStore(Options{}, later)
	if err := LoadDir(back, dir); err != nil {
		t.Fatal(err)
	}
	if got := sumRows(back.Top(Filter{}, "mac", base, base, 0)); got != want {
		t.Fatalf("what a restart reads is what was in RAM: %+v", got)
	}
}

func TestTheMinutesStartWhenTheStoreDoes(t *testing.T) {
	s := newTestStore(Options{}, base.Add(10*time.Minute))
	s.Add(delta(base.Add(10*time.Minute), phone, "a", "GB", 1, 1))
	// A delta stamped before the store started does not make the minutes
	// before it complete.
	s.Add(delta(base.Add(2*time.Minute), phone, "a", "GB", 1, 1))
	if s.minutesCover(base.Add(2 * time.Minute)) {
		t.Fatal("the minutes claim a range from before the store started")
	}
	if !s.minutesCover(base.Add(10 * time.Minute)) {
		t.Fatal("the minutes answer from when the store started")
	}
}

func TestAFileFromTheFutureIsNotLoaded(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(Options{}, later)
	feed(s, delta(base, phone, "a", "GB", 1, 1))
	feed(s, delta(base.Add(5*time.Hour), phone, "a", "GB", 2, 2))
	if _, err := saveAt(s, dir, base.Add(6*time.Hour+2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The router starts with its clock behind, before NTP.
	back := newTestStore(Options{}, base.Add(30*time.Minute))
	err := LoadDir(back, dir)
	if err == nil || !strings.Contains(err.Error(), filepath.Base(hourPath(dir, base.Add(5*time.Hour)))) {
		t.Fatalf("a file later than the clock is named: %v", err)
	}
	if got := sumRows(back.Top(Filter{}, "mac", base, base.Add(6*time.Hour), 0)); got != (Bytes{Up: 1, Down: 1}) {
		t.Fatalf("it is not loaded, and the rest is: %+v", got)
	}
}

func TestSaveOpenNeverRewritesAFinalFile(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(Options{}, later)
	feed(s, delta(base.Add(10*time.Minute), phone, "a", "GB", 1, 1))
	if n, err := saveAt(s, dir, base.Add(time.Hour+2*time.Minute)); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	path := hourPath(dir, base)
	was, _ := os.ReadFile(path)
	// A clock set back makes the written hour look open again.
	s.now = func() time.Time { return base.Add(20 * time.Minute) }
	if err := SaveOpen(s, dir); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.ReadFile(path); !bytes.Equal(now, was) || headerOf(t, path).Open {
		t.Fatal("a final file was rewritten as open")
	}
}

func TestASecondLoadDirAddsNothingTwice(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(Options{}, later)
	feed(s, delta(base, phone, "a", "GB", 1, 1))
	if _, err := saveAt(s, dir, closedAll); err != nil {
		t.Fatal(err)
	}
	back := newTestStore(Options{}, later)
	for i := 0; i < 3; i++ {
		if err := LoadDir(back, dir); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if got := sumRows(back.Top(Filter{}, "mac", base, base, 0)); got != (Bytes{Up: 1, Down: 1}) {
			t.Fatalf("call %d adds the files again: %+v", i+1, got)
		}
		if got := sumRows(back.Top(Filter{}, "mac", longAgo, base, 0)); got != (Bytes{Up: 1, Down: 1}) {
			t.Fatalf("call %d adds the day again: %+v", i+1, got)
		}
	}
	// Nor does a save read back what this store loaded.
	if _, err := saveAt(back, dir, dayClosed); err != nil {
		t.Fatal(err)
	}
	if got := sumRows(back.Top(Filter{}, "mac", base, base, 0)); got != (Bytes{Up: 1, Down: 1}) {
		t.Fatalf("a save read a loaded file back: %+v", got)
	}
}

func TestAMinuteHoldsAtMostAThousandKeys(t *testing.T) {
	s := newTestStore(Options{}, later)
	if s.o.MaxKeysPerHour != 5000 {
		t.Fatalf("the default key cap is %d", s.o.MaxKeysPerHour)
	}
	for i := 0; i < 2500; i++ {
		feed(s, delta(base, fmt.Sprintf("dev%04d", i), "a", "GB", 1, 1))
	}
	var m *minuteBucket
	for _, x := range s.mins {
		if x != nil && x.At.Equal(base) {
			m = x
		}
	}
	if len(m.Keys) > 2*1000+1 {
		t.Fatalf("the minute holds %d keys", len(m.Keys))
	}
	if h := s.hourAt(base); len(h.Keys) != 2500 {
		t.Fatalf("the hour, under its own cap, names all of them: %d", len(h.Keys))
	}
	if got := sumSeries(s.Series(Filter{}, base, base, time.Minute, "")); got.Up != 2500 {
		t.Fatalf("folding the minute loses no byte: %+v", got)
	}
}

func TestABucketClosesAMinuteAfterItEnds(t *testing.T) {
	end := base.Add(time.Hour)
	if closed(base, time.Hour, end.Add(skew-time.Nanosecond)) {
		t.Fatal("an hour is still open a nanosecond before its grace minute ends")
	}
	if !closed(base, time.Hour, end.Add(skew)) {
		t.Fatal("an hour is closed when its grace minute ends")
	}
	dir := t.TempDir()
	s := newTestStore(Options{}, later)
	addAt(s, end.Add(skew-time.Nanosecond), delta(base.Add(10*time.Minute), phone, "a", "GB", 1, 1))
	addAt(s, end.Add(skew), delta(base.Add(20*time.Minute), phone, "a", "GB", 2, 2))
	if s.Late() != 1 || sumRows(s.Top(Filter{}, "mac", base, base, 0)) != (Bytes{Up: 1, Down: 1}) {
		t.Fatalf("Add takes the first and refuses the second: late %d", s.Late())
	}
	if n, _ := saveAt(s, dir, end.Add(skew-time.Nanosecond)); n != 0 {
		t.Fatalf("save sees the hour open at the same instant Add does: %d", n)
	}
	if n, _ := saveAt(s, dir, end.Add(skew)); n != 1 {
		t.Fatalf("and closed at the same instant: %d", n)
	}
}

func TestAnUnloadedOpenHourIsMergedNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	first := newTestStore(Options{}, later)
	feed(first, delta(base.Add(5*time.Minute), phone, "a", "GB", 1, 1))
	feed(first, delta(base.Add(10*time.Minute), phone, "a", "GB", 2, 2))
	first.now = func() time.Time { return base.Add(20 * time.Minute) }
	if err := SaveOpen(first, dir); err != nil {
		t.Fatal(err)
	}
	// The router boots with its clock days behind: the open hour is in the
	// future and is skipped.
	second := newTestStore(Options{}, base.Add(-3*24*time.Hour))
	if err := LoadDir(second, dir); err == nil {
		t.Fatal("the hour from the future is named")
	}
	// NTP sets the clock and the hour carries on, then closes.
	feed(second, delta(base.Add(40*time.Minute), phone, "a", "GB", 4, 4))
	if n, err := saveAt(second, dir, base.Add(time.Hour+2*time.Minute)); err != nil || n != 1 {
		t.Fatalf("the closed hour is written: %d %v", n, err)
	}
	want := Bytes{Up: 7, Down: 7}
	if got := sumRows(second.Top(Filter{}, "mac", base, base, 0)); got != want {
		t.Fatalf("the open file on disk was merged into RAM: %+v", got)
	}
	// A later LoadDir does not add the merged file a second time.
	if err := LoadDir(second, dir); err != nil {
		t.Fatal(err)
	}
	if got := sumRows(second.Top(Filter{}, "mac", longAgo, base, 0)); got != want {
		t.Fatalf("the day holds both halves once: %+v", got)
	}
	if n, err := saveAt(second, dir, dayClosed); err != nil || n != 1 {
		t.Fatalf("the day is written: %d %v", n, err)
	}
	back := newTestStore(Options{}, later)
	if err := LoadDir(back, dir); err != nil {
		t.Fatal(err)
	}
	for _, from := range []time.Time{base, longAgo} {
		if got := sumRows(back.Top(Filter{}, "mac", from, base, 0)); got != want {
			t.Fatalf("from %v the files hold both halves: %+v", from, got)
		}
	}
}

func TestFilesSkippedBeforeTheClockWasSetAreLoadedLater(t *testing.T) {
	dir := t.TempDir()
	first := newTestStore(Options{}, later)
	feed(first, delta(base.Add(-4*24*time.Hour), laptop, "a", "GB", 5, 5))
	feed(first, delta(base.Add(10*time.Minute), phone, "a", "GB", 1, 1))
	feed(first, delta(base.Add(70*time.Minute), phone, "a", "GB", 2, 2))
	if _, err := saveAt(first, dir, base.Add(2*time.Hour+2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	second := newTestStore(Options{}, base.Add(-3*24*time.Hour))
	err := LoadDir(second, dir)
	for _, at := range []time.Time{base, base.Add(time.Hour)} {
		if err == nil || !strings.Contains(err.Error(), filepath.Base(hourPath(dir, at))) {
			t.Fatalf("the hour %v is named as from the future: %v", at, err)
		}
	}
	if got := sumRows(second.Top(Filter{}, "mac", longAgo, later, 0)); got != (Bytes{Up: 5, Down: 5}) {
		t.Fatalf("only the past loads before NTP: %+v", got)
	}
	second.now = func() time.Time { return base.Add(3 * time.Hour) }
	for i := 0; i < 2; i++ {
		if err := LoadDir(second, dir); err != nil {
			t.Fatalf("call %d after NTP: %v", i+2, err)
		}
		if got := sumRows(second.Top(Filter{}, "mac", base, base.Add(time.Hour), 0)); got != (Bytes{Up: 3, Down: 3}) {
			t.Fatalf("call %d: the skipped hours are loaded once: %+v", i+2, got)
		}
		if got := sumRows(second.Top(Filter{MAC: phone}, "mac", longAgo, base.Add(time.Hour), 0)); got != (Bytes{Up: 3, Down: 3}) {
			t.Fatalf("call %d: and rebuild their day once: %+v", i+2, got)
		}
		if got := sumRows(second.Top(Filter{MAC: laptop}, "mac", longAgo, base, 0)); got != (Bytes{Up: 5, Down: 5}) {
			t.Fatalf("call %d: what loaded at boot is not loaded again: %+v", i+2, got)
		}
	}
}

func TestPlacesOfAnAppCarryCountryAndOrganisation(t *testing.T) {
	s := newTestStore(Options{}, later)
	to := func(mac, app, cc string, asn uint32, org string, down uint64) {
		d := delta(base, mac, app, cc, 0, down)
		d.ASN, d.Org = asn, org
		feed(s, d)
	}
	to(phone, "youtube", "US", 15169, "GOOGLE", 100)
	to(phone, "youtube", "GB", 15169, "GOOGLE", 50)
	to(phone, "youtube", "NL", 16509, "AMAZON", 30)
	to(phone, "netflix", "US", 2906, "NETFLIX", 70)
	to(laptop, "youtube", "US", 15169, "GOOGLE", 9)
	for _, from := range []time.Time{base, longAgo} {
		places := s.Top(Filter{MAC: phone, App: "youtube"}, "place", from, base, 0)
		if len(places) != 3 {
			t.Fatalf("from %v: one row per country and organisation: %+v", from, places)
		}
		p := places[0]
		if p.Key != "US/15169" || p.Bytes.Down != 100 || p.Extra["cc"] != "US" || p.Extra["asn"] != "15169" || p.Extra["org"] != "GOOGLE" {
			t.Fatalf("from %v: the top place: %+v", from, p)
		}
		if places[1].Key != "GB/15169" || places[2].Key != "NL/16509" || places[2].Extra["org"] != "AMAZON" {
			t.Fatalf("from %v: the other places: %+v", from, places)
		}
		orgs := s.Top(Filter{MAC: phone, App: "youtube"}, "asn", from, base, 0)
		if len(orgs) != 2 || orgs[0].Key != "15169" || orgs[0].Bytes.Down != 150 || orgs[0].Extra["org"] != "GOOGLE" {
			t.Fatalf("from %v: organisations of an app add up its countries: %+v", from, orgs)
		}
		us := s.Top(Filter{MAC: phone, CC: "US"}, "asn", from, base, 0)
		if len(us) != 2 || us[0].Key != "15169" || us[1].Key != "2906" {
			t.Fatalf("from %v: organisations narrow by country: %+v", from, us)
		}
		all := s.Top(Filter{App: "youtube"}, "place", from, base, 1)
		if len(all) != 1 || all[0].Bytes.Down != 109 {
			t.Fatalf("from %v: a place over every device: %+v", from, all)
		}
	}
}

func TestDomainsKeepHowTheyWereNamed(t *testing.T) {
	s := newTestStore(Options{}, later)
	named := func(at time.Time, domain, src string) {
		d := delta(at, phone, "youtube", "US", 0, 1)
		d.Domain, d.NameSrc = domain, src
		feed(s, d)
	}
	named(base, "googlevideo.com", "sni")
	named(base.Add(time.Hour), "googlevideo.com", "quic")
	named(base.Add(time.Hour+time.Minute), "googlevideo.com", "")
	named(base, "x.example", "dns")
	rows := rowsByKey(s.Top(Filter{MAC: phone}, "domain", base, base.Add(time.Hour), 0))
	if rows["googlevideo.com"].Extra["src"] != "quic" || rows["googlevideo.com"].Bytes.Down != 3 {
		t.Fatalf("a domain says how it was last named: %+v", rows["googlevideo.com"])
	}
	if rows["x.example"].Extra["src"] != "dns" {
		t.Fatalf("each domain its own: %+v", rows["x.example"])
	}
	if got := rowsByKey(s.Top(Filter{MAC: phone}, "domain", longAgo, base, 0))["googlevideo.com"].Extra["src"]; got != "quic" {
		t.Fatalf("the day keeps it too: %q", got)
	}
}

func TestVersion1FilesStillLoad(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "hours"), 0o755)
	old := bucketV1{
		At:   base,
		Keys: map[Key]Bytes{{phone, "youtube", "US"}: {Down: 10}},
		ASN:  map[string]*margin[uint32]{phone: {Rows: map[uint32]cell{15169: {B: Bytes{Down: 10}, Org: "GOOGLE"}}}},
		Dom:  map[string]*margin[domKey]{phone: {Rows: map[domKey]cell{{"youtube", "googlevideo.com"}: {B: Bytes{Down: 10}}}}},
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	enc.Encode(header{Version: 1, Saved: base})
	enc.Encode(old)
	path := hourPath(dir, base)
	os.WriteFile(path, buf.Bytes(), 0o600)

	s := newTestStore(Options{}, base.Add(30*time.Minute))
	if err := LoadDir(s, dir); err != nil {
		t.Fatal(err)
	}
	if got := s.Top(Filter{MAC: phone}, "asn", base, base, 0); len(got) != 1 || got[0].Key != "15169" || got[0].Extra["org"] != "GOOGLE" || got[0].Bytes.Down != 10 {
		t.Fatalf("a version 1 organisation row: %+v", got)
	}
	if got := s.Top(Filter{MAC: phone}, "place", base, base, 0); len(got) != 1 || got[0].Extra["cc"] != "" || got[0].Extra["asn"] != "15169" {
		t.Fatalf("it has no country: %+v", got)
	}
	if got := s.Top(Filter{MAC: phone}, "domain", base, base, 0); len(got) != 1 || got[0].Extra["src"] != "" || got[0].Bytes.Down != 10 {
		t.Fatalf("and its domains no source: %+v", got)
	}
	// A final version 1 file is final: it is not rewritten.
	if n, err := saveAt(s, dir, base.Add(time.Hour+2*time.Minute)); err != nil || n != 0 {
		t.Fatalf("a final version 1 file is kept: %d %v", n, err)
	}
	if now, _ := os.ReadFile(path); !bytes.Equal(now, buf.Bytes()) {
		t.Fatal("the version 1 file changed")
	}
	// What is written now is version 2.
	feed(s, delta(base.Add(2*time.Hour), phone, "a", "GB", 1, 1))
	saveAt(s, dir, base.Add(3*time.Hour+2*time.Minute))
	if v := headerOf(t, hourPath(dir, base.Add(2*time.Hour))).Version; v != 2 {
		t.Fatalf("new files are version %d", v)
	}
}

func TestClosingABucketCompactsWithoutChangingIt(t *testing.T) {
	s := populated() // five hours from base, small device bound, evictions
	end := base.Add(5 * time.Hour)
	type view struct {
		tops   map[string][]Row
		series []Point
	}
	look := func(s *Store) view {
		v := view{tops: map[string][]Row{}}
		for _, from := range []time.Time{base, longAgo} {
			for _, dim := range []string{"mac", "app", "cc", "asn", "place", "domain"} {
				for _, f := range []Filter{{}, {MAC: phone}, {MAC: laptop, App: "bbc"}} {
					v.tops[fmt.Sprint(from, dim, f)] = s.Top(f, dim, from, end, 0)
				}
			}
		}
		v.series = s.Series(Filter{}, base, end, time.Hour, "app")
		return v
	}
	same := func(what string, a, b view) {
		t.Helper()
		full := 0
		for k, x := range a.tops {
			if !sameRows(x, b.tops[k]) {
				t.Fatalf("%s: %s changed:\n%+v\nwant\n%+v", what, k, b.tops[k], x)
			}
			if len(x) > 0 {
				full++
			}
		}
		if full < len(a.tops)*3/4 {
			t.Fatalf("%s: only %d of %d views have rows", what, full, len(a.tops))
		}
		for i := range a.series {
			if !sameSplit(a.series[i].By, b.series[i].By) {
				t.Fatalf("%s: hour %d changed", what, i)
			}
		}
	}
	// The same deltas into a store that never compacts are the reference.
	ref := populatedWith(func(s *Store) { s.nextSweep = later.AddDate(100, 0, 0) })
	before := look(ref)
	// populated() runs five hours: by its last delta the first four have
	// closed and are compacted, the fifth and the day are still open.
	for at := base; at.Before(end); at = at.Add(time.Hour) {
		if want := at.Before(base.Add(4 * time.Hour)); s.hourAt(at).compact != want || ref.hourAt(at).compact {
			t.Fatalf("the hour %v: compacted %v, want %v", at, s.hourAt(at).compact, want)
		}
	}
	if s.dayAt(base).compact {
		t.Fatal("the open day is compacted")
	}
	same("with four hours compacted", before, look(s))
	// The next day the house carries on: every hour of populated() and its
	// day have closed, the new hour and day have not.
	next := base.Add(24 * time.Hour)
	feed(s, delta(next, "late-device", "x", "GB", 1, 1))
	for at := base; at.Before(end); at = at.Add(time.Hour) {
		if !s.hourAt(at).compact {
			t.Fatalf("the closed hour %v is not compacted", at)
		}
	}
	if !s.dayAt(base).compact || s.dayAt(next).compact || s.hourAt(next).compact {
		t.Fatal("the closed day is compacted, the open hour and day are not")
	}
	same("after compaction", before, look(s))

	// Buckets loaded from disk are compacted as well, once closed.
	dir := t.TempDir()
	if _, err := saveAt(s, dir, next.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return next.Add(10 * time.Minute) }
	if err := SaveOpen(s, dir); err != nil {
		t.Fatal(err)
	}
	back := newTestStore(Options{TopPerDevice: 4}, next.Add(20*time.Minute))
	if err := LoadDir(back, dir); err != nil {
		t.Fatal(err)
	}
	if !back.hourAt(base).compact || !back.dayAt(base).compact {
		t.Fatal("closed buckets loaded from disk are compacted")
	}
	if back.hourAt(next).compact || back.dayAt(next).compact {
		t.Fatal("the open hour and day loaded from disk are not")
	}
	same("after a load", before, look(back))
}

// contiguous says every point starts where the one before it ends.
func contiguous(t *testing.T, ps []Point) {
	t.Helper()
	for i := 1; i < len(ps); i++ {
		if !ps[i-1].At.Add(ps[i-1].Step).Equal(ps[i].At) {
			t.Fatalf("point %d starts at %v, the one before ends at %v", i, ps[i].At, ps[i-1].At.Add(ps[i-1].Step))
		}
	}
}

// After a restart the minutes are empty until deltas arrive, and they were
// once read as "the minutes do not reach back, answer from the hours": every
// step became an hour and a chart dividing by its minute step drew speeds
// sixty times too high. The series is answered in minutes from the store's
// first delta on and from the hours before it, every point saying the step
// it covers, and no byte is counted twice.
func TestARestartStillAnswersTheLastHourInMinutes(t *testing.T) {
	dir := t.TempDir()
	before := newTestStore(Options{}, base)
	for m := 0; m < 150; m++ { // 09:00 to 11:29
		feed(before, delta(base.Add(time.Duration(m)*time.Minute), phone, "a", "GB", 1, 10))
	}
	restart := base.Add(150 * time.Minute) // 11:30
	before.now = func() time.Time { return restart }
	if err := SaveOpen(before, dir); err != nil {
		t.Fatal(err)
	}
	back := newTestStore(Options{}, restart)
	if err := LoadDir(back, dir); err != nil {
		t.Fatal(err)
	}
	for m := 0; m < 20; m++ { // 11:30 to 11:49
		feed(back, delta(restart.Add(time.Duration(m)*time.Minute), phone, "a", "GB", 2, 20))
	}
	now := restart.Add(19 * time.Minute)

	ps := back.Series(Filter{}, now.Add(-time.Hour), now, time.Minute, "")
	contiguous(t, ps)
	var fine []Point
	for _, p := range ps {
		if !p.At.Before(restart) {
			fine = append(fine, p)
		}
	}
	if len(fine) != 20 || !fine[19].At.Equal(now) {
		t.Fatalf("from the restart on, one point a minute: %d %+v", len(fine), ps)
	}
	for _, p := range fine {
		if p.Step != time.Minute || p.By[""].Up != 2 || p.By[""].Down != 20 {
			t.Fatalf("a minute after the restart is a minute of bytes: %+v", p)
		}
	}
	if got := sumSeries(ps); got.Up != 90+40 || got.Down != 900+400 {
		t.Fatalf("the hours before the restart and the minutes after add up once: %+v", got)
	}
	for _, p := range ps {
		if p.At.Before(restart) && p.Step <= 0 {
			t.Fatalf("a point before the restart says its step: %+v", p)
		}
	}

	// The day at ten-minute steps is the same: minutes where they are whole.
	day := back.Series(Filter{}, now.Add(-24*time.Hour), now, 10*time.Minute, "")
	contiguous(t, day)
	last := day[len(day)-1]
	if last.Step != 10*time.Minute || !last.At.Equal(restart.Add(10*time.Minute)) || last.By[""].Down != 200 {
		t.Fatalf("the newest ten minutes are ten minutes: %+v", last)
	}
}

// A day of minutes is the whole minute ring, and a ring's newest bucket is
// the current minute, so "the last 24 h" always started a minute before the
// ring and was answered by the hours. A week was the days for the same
// reason. Each is answered at its own step wherever the finer ring holds it.
func TestADayAndAWeekAreAnsweredAtTheirOwnSteps(t *testing.T) {
	s := newTestStore(Options{}, base)
	for m := 0; m < 3*24*60; m += 10 {
		feed(s, delta(base.Add(time.Duration(m)*time.Minute), phone, "a", "GB", 1, 10))
	}
	now := base.Add(3*24*time.Hour - 10*time.Minute)
	day := s.Series(Filter{}, now.Add(-24*time.Hour), now, 10*time.Minute, "")
	contiguous(t, day)
	tenMin := 0
	for _, p := range day {
		if p.Step == 10*time.Minute {
			tenMin++
		}
	}
	if tenMin < 140 || day[len(day)-1].By[""].Down != 10 {
		t.Fatalf("the day is drawn in ten-minute steps: %d of %d", tenMin, len(day))
	}
	for m := 3 * 24 * 60; m < 9*24*60; m += 60 {
		feed(s, delta(base.Add(time.Duration(m)*time.Minute), phone, "a", "GB", 1, 10))
	}
	now = base.Add(9*24*time.Hour - time.Hour)
	week := s.Series(Filter{}, now.Add(-7*24*time.Hour), now, time.Hour, "")
	contiguous(t, week)
	hours := 0
	for _, p := range week {
		if p.Step == time.Hour {
			hours++
		}
	}
	if hours < 160 || week[len(week)-1].By[""].Down != 10 {
		t.Fatalf("the week is drawn in hours: %d of %d", hours, len(week))
	}
}
