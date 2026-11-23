package api

// "Usual for this device": the week of a device as the router knows it from
// its daily files, each hour of each weekday the median of what it moved in
// that hour over the days kept, in the router's zone, and the hour that just
// closed beside the usual of its slot.

import (
	"net"
	"net/http"
	"slices"
	"time"

	"netryx/internal/daily"
	"netryx/internal/rollup"
)

const (
	// usualMinDays is how many counted days a device needs before it has a
	// usual week.
	usualMinDays = 7
	// usualMinSamples is how many counted hours a slot needs for a usual.
	usualMinSamples = 2
)

// WithZone places the week in the router's zone; without it, UTC.
func WithZone(loc *time.Location) Option { return func(s *server) { s.loc = loc } }

func (s *server) zone() *time.Location {
	if s.loc == nil {
		return time.UTC
	}
	return s.loc
}

// usualOf is a device's week: by weekday (time.Weekday) and hour of the
// router's zone, the median bytes (down and up together) of the slot, nil
// with fewer than usualMinSamples counted hours, and how many there were.
type usualOf struct {
	Median    [7][24]*uint64
	Samples   [7][24]int
	KnownDays int
}

// slotOf is the weekday and hour of the router's zone that the hour starting
// at t belongs to: the one its middle falls in, so a half-hour zone places an
// hour by the half it mostly covers and a DST change moves the UTC hour.
func slotOf(t time.Time, loc *time.Location) (time.Weekday, int) {
	m := t.Add(30 * time.Minute).In(loc)
	return m.Weekday(), m.Hour()
}

// usualWeek reads the days' counted hours that start at or after since (the
// hour the device was first seen in): an hour the router was not counting and
// a day with no file are no sample, a counted quiet hour is a sample of zero.
// KnownDays counts the days that gave at least one sample.
func usualWeek(days []daily.DayOf, since time.Time, loc *time.Location) usualOf {
	var u usualOf
	var vals [7][24][]uint64
	from := since.Truncate(time.Hour)
	for _, d := range days {
		if !d.Known {
			continue
		}
		counted := false
		for h := 0; h < 24; h++ {
			at := d.Date.Add(time.Duration(h) * time.Hour)
			if d.Mask&(1<<h) == 0 || at.Before(from) {
				continue
			}
			counted = true
			wd, hr := slotOf(at, loc)
			vals[wd][hr] = append(vals[wd][hr], d.Down[h]+d.Up[h])
		}
		if counted {
			u.KnownDays++
		}
	}
	for wd := range vals {
		for hr, v := range vals[wd] {
			u.Samples[wd][hr] = len(v)
			if len(v) < usualMinSamples {
				continue
			}
			slices.Sort(v)
			m := v[len(v)/2]
			if len(v)%2 == 0 {
				a := v[len(v)/2-1]
				m = a + (m-a)/2
			}
			u.Median[wd][hr] = &m
		}
	}
	return u
}

type usualNowJSON struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Weekday int    `json:"weekday"`
	Hour    int    `json:"hour"`
	// Bytes is nil when the service did not see the whole hour.
	Bytes   *uint64 `json:"bytes"`
	Usual   *uint64 `json:"usual"`
	Samples int     `json:"samples"`
}

type usualJSON struct {
	TooNew    bool          `json:"tooNew"`
	KnownDays int           `json:"knownDays"`
	Zone      string        `json:"zone"`
	Median    [][]*uint64   `json:"median"`
	Samples   [][]int       `json:"samples"`
	Now       *usualNowJSON `json:"now"`
}

func (s *server) usual(r *http.Request) (any, error) {
	d, err := s.deviceFor(r)
	if err != nil {
		return nil, err
	}
	hw, _ := net.ParseMAC(d.MAC)
	now, loc := s.now(), s.zone()
	out := usualJSON{Zone: loc.String()}
	if s.daily == nil || len(hw) != 6 {
		out.TooNew = true
		return out, nil
	}
	days := s.daily.Days([6]byte(hw), now)
	// The inventory's first sight restarts with the service; the daily files
	// remember the first hour the device moved bytes.
	since := d.First
	if f := firstMoved(days); !f.IsZero() && (since.IsZero() || f.Before(since)) {
		since = f
	}
	if since.IsZero() {
		out.TooNew = true
		return out, nil
	}
	u := usualWeek(days, since, loc)
	out.KnownDays = u.KnownDays
	if u.KnownDays < usualMinDays {
		out.TooNew = true
		return out, nil
	}
	out.Median, out.Samples = make([][]*uint64, 7), make([][]int, 7)
	for wd := range u.Median {
		out.Median[wd], out.Samples[wd] = u.Median[wd][:], u.Samples[wd][:]
	}
	to := now.UTC().Truncate(time.Hour)
	from := to.Add(-time.Hour)
	wd, hr := slotOf(from, loc)
	n := &usualNowJSON{From: ts(from), To: ts(to), Weekday: int(wd), Hour: hr, Usual: u.Median[wd][hr], Samples: u.Samples[wd][hr]}
	if s.svc == nil || !s.svc.Started().After(from) {
		var b uint64
		for _, row := range s.h.Top(rollup.Filter{MAC: d.MAC}, "mac", from, to, 1) {
			b += row.Bytes.Down + row.Bytes.Up
		}
		n.Bytes = &b
	}
	out.Now = n
	return out, nil
}

// firstMoved is the first counted hour of the days in which the device moved
// any bytes, or zero when it moved none.
func firstMoved(days []daily.DayOf) time.Time {
	for _, d := range days {
		if !d.Known {
			continue
		}
		for h := 0; h < 24; h++ {
			if d.Mask&(1<<h) != 0 && d.Down[h]+d.Up[h] > 0 {
				return d.Date.Add(time.Duration(h) * time.Hour)
			}
		}
	}
	return time.Time{}
}
