package rules

import (
	"fmt"
	"sort"
	"time"
	_ "time/tzdata" // so LoadLocation works on a router with no zoneinfo
)

const (
	minDay  = 1440
	minWeek = 7 * minDay
)

// TimeMatch is one nft time match: the listed weekdays (in the kernel's frame,
// empty = every day) and an inclusive range of seconds-of-day in UTC, written
// "HH:MM:SS". Both ends are inclusive in nft, so an interval ending at 23:00
// is written "22:59:59". No range wraps midnight.
type TimeMatch struct {
	Days     []time.Weekday
	From, To string
}

// Compile turns the rule's local window into matches that, evaluated by the
// kernel (`meta hour` in UTC, `meta day` shifted by kernelEast minutes east of
// UTC), cover exactly the window at the offset loc has at `at`. A window with
// To <= From runs into the next day and belongs to the day it starts. Days
// without a window are the whole of each listed local day. Nil for an always
// rule.
func Compile(r Rule, loc *time.Location, at time.Time, kernelEast int) []TimeMatch {
	r, always := wholeDays(r)
	if always {
		return nil
	}
	_, off := at.In(loc).Zone()
	off /= 60
	days := r.Days
	if len(days) == 0 {
		days = []time.Weekday{0, 1, 2, 3, 4, 5, 6}
	}
	length := r.To - r.From
	if length <= 0 {
		length += minDay
	}
	type piece struct {
		day      time.Weekday
		from, to int // seconds of UTC day, inclusive
	}
	var pieces []piece
	for _, d := range days {
		// Local week minute to UTC minute (not reduced, so the week may wrap).
		a := int(d)*minDay + r.From - off
		end := a + length
		for a < end {
			b := end
			if n := nextBoundary(a, 0); n < b {
				b = n
			}
			if n := nextBoundary(a, kernelEast); n < b {
				b = n
			}
			kd := mod(floorDiv(a+kernelEast, minDay), 7)
			pieces = append(pieces, piece{
				day:  time.Weekday(kd),
				from: mod(a, minDay) * 60,
				to:   (mod(b-1, minDay)+1)*60 - 1,
			})
			a = b
		}
	}
	type key struct{ from, to int }
	group := map[key]map[time.Weekday]bool{}
	for _, p := range pieces {
		k := key{p.from, p.to}
		if group[k] == nil {
			group[k] = map[time.Weekday]bool{}
		}
		group[k][p.day] = true
	}
	var keys []key
	for k := range group {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].from != keys[j].from {
			return keys[i].from < keys[j].from
		}
		return keys[i].to < keys[j].to
	})
	var out []TimeMatch
	for _, k := range keys {
		m := TimeMatch{From: clock(k.from), To: clock(k.to)}
		if len(group[k]) < 7 {
			for d := time.Weekday(0); d < 7; d++ {
				if group[k][d] {
					m.Days = append(m.Days, d)
				}
			}
		}
		out = append(out, m)
	}
	return out
}

// wholeDays settles what a rule without a window means. No days and no window
// is always. Days without a window are the whole of each listed local day,
// 00:00 to 24:00, which is a window with From == To (a full day). A rule with a
// window is returned as it is.
func wholeDays(r Rule) (Rule, bool) {
	if r.From >= 0 && r.To >= 0 {
		return r, false
	}
	if len(r.Days) == 0 {
		return r, true
	}
	r.From, r.To = 0, 0
	return r, false
}

// nextBoundary is the first minute after a that is a midnight in the frame
// shifted by east minutes.
func nextBoundary(a, east int) int {
	return (floorDiv(a+east, minDay)+1)*minDay - east
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func mod(a, b int) int { return a - floorDiv(a, b)*b }

func clock(sec int) string {
	return fmt.Sprintf("%02d:%02d:%02d", sec/3600, sec/60%60, sec%60)
}

// Active reports whether the rule's window covers local time t. An always
// rule is always active. It does not use Compile.
func Active(r Rule, loc *time.Location, t time.Time) bool {
	r, always := wholeDays(r)
	if always {
		return true
	}
	l := t.In(loc)
	m := l.Hour()*60 + l.Minute()
	listed := func(d time.Weekday) bool {
		if len(r.Days) == 0 {
			return true
		}
		for _, x := range r.Days {
			if x == d {
				return true
			}
		}
		return false
	}
	if r.To > r.From {
		return listed(l.Weekday()) && m >= r.From && m < r.To
	}
	// Runs into the next day (To <= From; equal means a full day).
	if listed(l.Weekday()) && m >= r.From {
		return true
	}
	return listed((l.Weekday()+6)%7) && m < r.To
}

// NextRecompile is the zone's next transition after at, or at + 24h when the
// zone has none.
func NextRecompile(loc *time.Location, at time.Time) time.Time {
	if _, end := at.In(loc).ZoneBounds(); !end.IsZero() {
		return end
	}
	return at.Add(24 * time.Hour)
}

// NextMidnight is the next local midnight in loc after t: the end of a spent
// limit. A day can be 23 or 25 hours long, so it is found on the calendar,
// not by adding 24 hours.
func NextMidnight(loc *time.Location, t time.Time) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc)
}
