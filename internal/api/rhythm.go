package api

// A device's rhythm: its days set against its own median day, as Genaryx's
// agent rhythm does for an agent. Read from the daily files only. Yesterday
// is compared with the median of the whole days since the device was first
// seen; a comparison that cannot be made is refused and said so, never made
// against a zero.

import (
	"net"
	"net/http"
	"slices"
	"time"

	"netryx/internal/daily"
)

const (
	// rhythmMinDays is how many whole days a device needs for "unusual" to
	// mean anything.
	rhythmMinDays = 7
	// trendMinDays is how many whole days each week needs for a trend.
	trendMinDays = 4
	// awakeMin is how much an hour must move to count as awake.
	awakeMin = 1 << 20
)

type rhythmDay struct {
	Date time.Time
	// Bytes is nil for a day with no file or before the device was seen.
	Bytes *uint64
	// Whole says the router counted every hour of it since the device was seen.
	Whole bool
}

// awakeHour is an hour of yesterday, in the router's zone, in which the
// device moved at least awakeMin while its usual for that hour is nothing.
type awakeHour struct {
	Hour  int    `json:"hour"`
	Bytes uint64 `json:"bytes"`
}

type rhythm struct {
	// Confidence is "no_data" (no counted day), "too_new" (fewer than
	// rhythmMinDays whole days: counts, no comparison) or "normal".
	Confidence string
	WholeDays  int
	MedianDay  *uint64
	Days       []rhythmDay
	Yesterday  rhythmDay
	// TimesMedian is yesterday over the median day, nil when it cannot be
	// said: too new, yesterday not whole, or a median day of nothing.
	TimesMedian *float64
	// Trend is the last seven days' mean whole day against the seven
	// before: rising, falling, steady or unknown.
	Trend string
	// BusiestShare is the busiest whole day's share of all whole days.
	BusiestShare float64
	Awake        []awakeHour
}

func rhythmOf(days []daily.DayOf, since time.Time, u usualOf, loc *time.Location) rhythm {
	r := rhythm{Trend: "unknown", Awake: []awakeHour{}}
	from := since.Truncate(time.Hour)
	var whole []uint64
	var total, peak uint64
	for _, d := range days {
		rd := rhythmDay{Date: d.Date}
		if d.Known {
			var b uint64
			counted, all := 0, 0
			for h := 0; h < 24; h++ {
				if d.Date.Add(time.Duration(h) * time.Hour).Before(from) {
					continue
				}
				all++
				if d.Mask&(1<<h) != 0 {
					counted++
					b += d.Down[h] + d.Up[h]
				}
			}
			if all > 0 {
				rd.Bytes = &b
				rd.Whole = all == 24 && counted == 24
			}
			if rd.Whole {
				whole = append(whole, b)
				total += b
				peak = max(peak, b)
			}
		}
		r.Days = append(r.Days, rd)
	}
	r.WholeDays = len(whole)
	if len(r.Days) > 0 {
		r.Yesterday = r.Days[len(r.Days)-1]
	}
	known := false
	for _, d := range r.Days {
		known = known || d.Bytes != nil
	}
	switch {
	case !known:
		r.Confidence = "no_data"
		return r
	case r.WholeDays < rhythmMinDays:
		r.Confidence = "too_new"
	default:
		r.Confidence = "normal"
	}
	if total > 0 {
		r.BusiestShare = float64(peak) / float64(total)
	}
	if len(whole) > 0 {
		sorted := slices.Clone(whole)
		slices.Sort(sorted)
		m := sorted[len(sorted)/2]
		if len(sorted)%2 == 0 {
			a := sorted[len(sorted)/2-1]
			m = a + (m-a)/2
		}
		r.MedianDay = &m
	}
	if r.Confidence == "normal" && r.Yesterday.Whole && r.MedianDay != nil && *r.MedianDay > 0 {
		x := float64(*r.Yesterday.Bytes) / float64(*r.MedianDay)
		x = float64(int64(x*100+0.5)) / 100
		r.TimesMedian = &x
	}
	r.Trend = trendOf(r.Days)
	// Drift: hours of yesterday it is usually asleep in.
	if len(days) > 0 && r.Yesterday.Bytes != nil {
		y := days[len(days)-1]
		for h := 0; h < 24; h++ {
			at := y.Date.Add(time.Duration(h) * time.Hour)
			if y.Mask&(1<<h) == 0 || at.Before(from) {
				continue
			}
			b := y.Down[h] + y.Up[h]
			wd, hr := slotOf(at, loc)
			if m := u.Median[wd][hr]; b >= awakeMin && m != nil && *m == 0 {
				r.Awake = append(r.Awake, awakeHour{Hour: hr, Bytes: b})
			}
		}
	}
	return r
}

// trendOf compares the mean whole day of the last seven days with that of
// the seven before; rising at a quarter more, falling at a fifth less.
func trendOf(days []rhythmDay) string {
	if len(days) < 14 {
		return "unknown"
	}
	mean := func(w []rhythmDay) (float64, bool) {
		var sum float64
		n := 0
		for _, d := range w {
			if d.Whole {
				sum += float64(*d.Bytes)
				n++
			}
		}
		if n < trendMinDays {
			return 0, false
		}
		return sum / float64(n), true
	}
	last, ok1 := mean(days[len(days)-7:])
	before, ok2 := mean(days[len(days)-14 : len(days)-7])
	switch {
	case !ok1 || !ok2:
		return "unknown"
	case before == 0 && last == 0:
		return "steady"
	case last >= before*1.25:
		return "rising"
	case last <= before*0.8:
		return "falling"
	}
	return "steady"
}

type rhythmDayJSON struct {
	Date  string  `json:"date"`
	Bytes *uint64 `json:"bytes"`
	Whole bool    `json:"whole"`
}

type rhythmJSON struct {
	Confidence   string          `json:"confidence"`
	WholeDays    int             `json:"wholeDays"`
	MedianDay    *uint64         `json:"medianDay"`
	Days         []rhythmDayJSON `json:"days"`
	Yesterday    rhythmDayJSON   `json:"yesterday"`
	TimesMedian  *float64        `json:"timesMedian"`
	Trend        string          `json:"trend"`
	BusiestShare float64         `json:"busiestShare"`
	Awake        []awakeHour     `json:"awake"`
	Zone         string          `json:"zone"`
}

func dayJSONOf(d rhythmDay) rhythmDayJSON {
	return rhythmDayJSON{Date: d.Date.UTC().Format("2006-01-02"), Bytes: d.Bytes, Whole: d.Whole}
}

func (s *server) rhythm(r *http.Request) (any, error) {
	d, err := s.deviceFor(r)
	if err != nil {
		return nil, err
	}
	hw, _ := net.ParseMAC(d.MAC)
	now, loc := s.now(), s.zone()
	var days []daily.DayOf
	if s.daily != nil && len(hw) == 6 {
		days = s.daily.Days([6]byte(hw), now)
	} else {
		for _, t := range daily.Window(now) {
			days = append(days, daily.DayOf{Date: t})
		}
	}
	since := d.First
	if f := firstMoved(days); !f.IsZero() && (since.IsZero() || f.Before(since)) {
		since = f
	}
	rh := rhythmOf(days, since, usualWeek(days, since, loc), loc)
	out := rhythmJSON{
		Confidence: rh.Confidence, WholeDays: rh.WholeDays, MedianDay: rh.MedianDay,
		Yesterday: dayJSONOf(rh.Yesterday), TimesMedian: rh.TimesMedian, Trend: rh.Trend,
		BusiestShare: rh.BusiestShare, Awake: rh.Awake, Zone: loc.String(),
		Days: make([]rhythmDayJSON, 0, len(rh.Days)),
	}
	for _, x := range rh.Days {
		out.Days = append(out.Days, dayJSONOf(x))
	}
	return out, nil
}
