package rules

import (
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

func london(t *testing.T) *time.Location {
	t.Helper()
	l, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// render writes matches as sorted "Mon,Tue 20:00:00-22:59:59" strings.
func render(ms []TimeMatch) []string {
	var out []string
	for _, m := range ms {
		var d []string
		for _, w := range m.Days {
			d = append(d, w.String()[:3])
		}
		day := strings.Join(d, ",")
		if day == "" {
			day = "*"
		}
		out = append(out, fmt.Sprintf("%s %s-%s", day, m.From, m.To))
	}
	sort.Strings(out)
	return out
}

func wantSet(t *testing.T, got []TimeMatch, want ...string) {
	t.Helper()
	sort.Strings(want)
	g := render(got)
	if strings.Join(g, "|") != strings.Join(want, "|") {
		t.Fatalf("matches\n got %q\nwant %q", g, want)
	}
}

func win(days []time.Weekday, from, to int) Rule {
	return Rule{ID: "r", Enabled: true, Days: days, From: from, To: to}
}

func TestAWindowAcrossMidnightBelongsToTheDayItStarts(t *testing.T) {
	loc := london(t)
	at := time.Date(2026, 7, 6, 12, 0, 0, 0, loc)
	r := win([]time.Weekday{time.Monday}, 21*60, 7*60)
	wantSet(t, Compile(r, loc, at, 60),
		"Mon 20:00:00-22:59:59", "Tue 23:00:00-23:59:59", "Tue 00:00:00-05:59:59")
	if !Active(r, loc, time.Date(2026, 7, 7, 3, 0, 0, 0, loc)) {
		t.Fatal("local Tue 03:00 is inside Monday's window")
	}
	if Active(r, loc, time.Date(2026, 7, 6, 20, 30, 0, 0, loc)) {
		t.Fatal("local Mon 20:30 is before the window")
	}
	if Active(r, loc, time.Date(2026, 7, 7, 21, 30, 0, 0, loc)) {
		t.Fatal("Tuesday is not a listed day")
	}
}

func TestTheSameRuleCompilesDifferentlyInWinter(t *testing.T) {
	loc := london(t)
	at := time.Date(2026, 12, 7, 12, 0, 0, 0, loc)
	r := win([]time.Weekday{time.Monday}, 21*60, 7*60)
	wantSet(t, Compile(r, loc, at, 0), "Mon 21:00:00-23:59:59", "Tue 00:00:00-06:59:59")
}

func TestAStaleKernelOffsetIsFollowed(t *testing.T) {
	loc := london(t)
	at := time.Date(2026, 7, 6, 12, 0, 0, 0, loc)
	r := win([]time.Weekday{time.Monday}, 21*60, 7*60)
	wantSet(t, Compile(r, loc, at, 0), "Mon 20:00:00-23:59:59", "Tue 00:00:00-05:59:59")
}

func TestAnEveryDayRuleIsOneMatchWhenItDoesNotCross(t *testing.T) {
	loc := london(t)
	at := time.Date(2026, 7, 6, 12, 0, 0, 0, loc)
	wantSet(t, Compile(win(nil, 9*60, 17*60), loc, at, 60), "* 08:00:00-15:59:59")
	// a weekday list that shares one hour range is one match too
	wantSet(t, Compile(win([]time.Weekday{time.Monday, time.Wednesday}, 9*60, 17*60), loc, at, 60),
		"Mon,Wed 08:00:00-15:59:59")
}

func TestAlwaysHasNoTimeMatch(t *testing.T) {
	loc := london(t)
	fixed := time.Date(2026, 7, 6, 12, 0, 0, 0, loc)
	if ms := Compile(win(nil, -1, -1), loc, fixed, 0); ms != nil {
		t.Fatalf("always compiles to %v", ms)
	}
	if !Active(win(nil, -1, -1), loc, fixed) {
		t.Fatal("an always rule is active")
	}
}

func TestNextRecompileIsTheZonesTransition(t *testing.T) {
	loc := london(t)
	got := NextRecompile(loc, time.Date(2026, 7, 6, 12, 0, 0, 0, loc))
	want := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	fixed := time.FixedZone("EAT", 3*3600)
	at := time.Date(2026, 7, 6, 12, 0, 0, 0, fixed)
	if got := NextRecompile(fixed, at); !got.Equal(at.Add(24 * time.Hour)) {
		t.Fatalf("a zone with no transition rechecks in a day, got %v", got)
	}
}

// kernelMatches evaluates matches as the kernel does: the hour from UTC
// seconds since midnight (inclusive range), the day from UTC shifted by kernelEast.
func kernelMatches(t *testing.T, ms []TimeMatch, ts time.Time, kernelEast int) bool {
	t.Helper()
	u := ts.UTC()
	sec := u.Hour()*3600 + u.Minute()*60 + u.Second()
	day := u.Add(time.Duration(kernelEast) * time.Minute).Weekday()
	for _, m := range ms {
		from, to := secs(t, m.From), secs(t, m.To)
		if sec < from || sec > to {
			continue
		}
		if len(m.Days) == 0 {
			return true
		}
		for _, d := range m.Days {
			if d == day {
				return true
			}
		}
	}
	return false
}

var clockForm = regexp.MustCompile(`^\d\d:\d\d:\d\d$`)

func secs(t *testing.T, s string) int {
	t.Helper()
	if !clockForm.MatchString(s) {
		t.Fatalf("%q is not HH:MM:SS", s)
	}
	var h, m, x int
	if _, err := fmt.Sscanf(s, "%d:%d:%d", &h, &m, &x); err != nil {
		t.Fatal(err)
	}
	return h*3600 + m*60 + x
}

func TestCompiledMatchesAgreeWithActive(t *testing.T) {
	zone := func(name string) *time.Location {
		l, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	cases := []struct {
		name  string
		loc   *time.Location
		day   time.Time // a Monday with no transition in its week
		kerns []int
	}{
		{"London BST", zone("Europe/London"), time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC), []int{60, 0, 180, -600, 840}},
		{"Kolkata", zone("Asia/Kolkata"), time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC), []int{330, 0, -600, 840}},
		{"St Johns summer", zone("America/St_Johns"), time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC), []int{-150, 0, -210, 840}},
		{"St Johns winter", zone("America/St_Johns"), time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC), []int{-210, 0, -150, -600}},
	}
	for _, c := range cases {
		y, m, d := c.day.Date()
		start := time.Date(y, m, d, 0, 0, 0, 0, c.loc) // local Monday midnight
		for seed := int64(1); seed <= 30; seed++ {
			rng := rand.New(rand.NewSource(seed))
			var days []time.Weekday
			for d := 0; d < 7; d++ {
				if rng.Intn(2) == 0 {
					days = append(days, time.Weekday(d))
				}
			}
			r := win(days, rng.Intn(1440), rng.Intn(1440))
			if seed%3 == 0 {
				// days without a window: the whole of each listed local day
				if len(days) == 0 {
					days = []time.Weekday{time.Weekday(rng.Intn(7))}
				}
				r = win(days, -1, -1)
			}
			for _, ke := range c.kerns {
				ms := Compile(r, c.loc, start, ke)
				// Every minute of the week, at second :00 and second :59.
				for i := 0; i < 7*1440; i++ {
					for _, sec := range []int{0, 59} {
						ts := start.Add(time.Duration(i)*time.Minute + time.Duration(sec)*time.Second)
						want := Active(r, c.loc, ts)
						if got := kernelMatches(t, ms, ts, ke); got != want {
							t.Fatalf("%s seed %d rule %+v kernelEast %d at %s (%s): matches say %v, Active says %v; %q",
								c.name, seed, r, ke, ts, ts.UTC().Weekday(), got, want, render(ms))
						}
					}
				}
			}
		}
	}
}

func TestDaysWithoutAWindowAreWholeLocalDays(t *testing.T) {
	loc := london(t)
	at := time.Date(2026, 7, 6, 12, 0, 0, 0, loc)
	r := win([]time.Weekday{time.Monday}, -1, -1)
	// Monday 00:00-24:00 BST is Sunday 23:00 UTC to Monday 22:59:59 UTC.
	wantSet(t, Compile(r, loc, at, 0), "Sun 23:00:00-23:59:59", "Mon 00:00:00-22:59:59")
	mon := time.Date(2026, 7, 6, 8, 0, 0, 0, loc)
	if !Active(r, loc, mon) || Active(r, loc, mon.AddDate(0, 0, 1)) {
		t.Fatal("days only: active on Monday, not on Tuesday")
	}
}
