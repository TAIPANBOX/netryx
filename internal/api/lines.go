package api

// The house's lines over the last 30 days, from the counters every daily file
// read at its close: what the primary and the backup lines carried each day,
// and how much the backup carried on the days it was used. Nothing new is
// written.

import (
	"context"
	"net/http"
	"time"

	"netryx/internal/daily"
	"netryx/internal/netview"
)

// lineUsedMin is how much a backup line must carry in a day to count as used:
// above what a failover check's pings move (a few MiB a day).
const lineUsedMin = 32 << 20

// lineDay is what each line carried in one day; nil when it cannot be known.
type lineDay struct {
	Date            time.Time
	Primary, Backup *uint64
}

// lineVolumes answers each day of days with the difference between its close
// and the close before, summed over the networks named. A day is unknown when
// either close is missing or was not read at the close, when the router was
// not counting all of it, or when a counter of a named network fell or is
// missing (a restart, or the network not yet there).
func lineVolumes(days []daily.DayCounters, primary, backup []string) []lineDay {
	out := make([]lineDay, len(days))
	for i, d := range days {
		out[i].Date = d.Date
		if i == 0 {
			continue
		}
		prev := days[i-1]
		if !d.Known || !prev.Known || d.At.IsZero() || prev.At.IsZero() || d.Mask != 1<<24-1 {
			continue
		}
		sum := func(names []string) *uint64 {
			if len(names) == 0 {
				return nil
			}
			var b uint64
			for _, n := range names {
				c, ok1 := d.Ifaces[n]
				p, ok2 := prev.Ifaces[n]
				if !ok1 || !ok2 || c[0] < p[0] || c[1] < p[1] {
					return nil
				}
				b += c[0] - p[0] + c[1] - p[1]
			}
			return &b
		}
		out[i].Primary, out[i].Backup = sum(primary), sum(backup)
	}
	return out
}

// linesSum is the backup line's month: the days it carried at least
// lineUsedMin, all it carried on the days known, its share of what the lines
// carried on the days it was used (nil when none was used or the primary is
// not known for them), its busiest day, and the days not known.
type linesSum struct {
	UsedDays    int
	BackupBytes uint64
	Share       *float64
	Busiest     *lineDay
	UnknownDays int
}

func linesSummary(days []lineDay) linesSum {
	var s linesSum
	var used, all uint64
	shareKnown := true
	for i := range days {
		d := &days[i]
		if d.Backup == nil {
			s.UnknownDays++
			continue
		}
		s.BackupBytes += *d.Backup
		if *d.Backup < lineUsedMin {
			continue
		}
		s.UsedDays++
		if s.Busiest == nil || *d.Backup > *s.Busiest.Backup {
			s.Busiest = d
		}
		if d.Primary == nil {
			shareKnown = false
			continue
		}
		used += *d.Backup
		all += *d.Backup + *d.Primary
	}
	if s.UsedDays > 0 && shareKnown && all > 0 {
		x := float64(used) / float64(all)
		s.Share = &x
	}
	return s
}

type lineJSON struct {
	Name   string `json:"name"`
	Proto  string `json:"proto"`
	Device string `json:"device"`
	Up     bool   `json:"up"`
}

type lineDayJSON struct {
	Date    string  `json:"date"`
	Primary *uint64 `json:"primary"`
	Backup  *uint64 `json:"backup"`
}

type linesJSON struct {
	Primary     []lineJSON    `json:"primary"`
	Backup      []lineJSON    `json:"backup"`
	Days        []lineDayJSON `json:"days"`
	UsedDays    int           `json:"usedDays"`
	UsedMin     uint64        `json:"usedMin"`
	BackupBytes uint64        `json:"backupBytes"`
	Share       *float64      `json:"share"`
	Busiest     *lineDayJSON  `json:"busiest"`
	UnknownDays int           `json:"unknownDays"`
}

func (s *server) lines(r *http.Request) (any, error) {
	if s.net == nil {
		return nil, httpError{http.StatusServiceUnavailable, "the router's network view is not available"}
	}
	ctx, cancel := context.WithTimeout(r.Context(), netReadTimeout)
	defer cancel()
	primary, backup := netview.Lines(s.net.View(ctx))
	out := linesJSON{Primary: []lineJSON{}, Backup: []lineJSON{}, Days: []lineDayJSON{}, UsedMin: lineUsedMin}
	names := func(l []netview.Iface, into *[]lineJSON) (n []string) {
		for _, i := range l {
			n = append(n, i.Name)
			*into = append(*into, lineJSON{Name: i.Name, Proto: i.Proto, Device: i.Device, Up: i.Up})
		}
		return n
	}
	pn, bn := names(primary, &out.Primary), names(backup, &out.Backup)
	var days []daily.DayCounters
	if s.daily != nil {
		days = s.daily.Counters(s.now())
	} else {
		for _, d := range daily.Window(s.now()) {
			days = append(days, daily.DayCounters{Date: d})
		}
	}
	vol := lineVolumes(days, pn, bn)
	js := func(d lineDay) lineDayJSON {
		return lineDayJSON{Date: d.Date.UTC().Format("2006-01-02"), Primary: d.Primary, Backup: d.Backup}
	}
	for _, d := range vol {
		out.Days = append(out.Days, js(d))
	}
	sum := linesSummary(vol)
	out.UsedDays, out.BackupBytes, out.Share, out.UnknownDays = sum.UsedDays, sum.BackupBytes, sum.Share, sum.UnknownDays
	if sum.Busiest != nil {
		b := js(*sum.Busiest)
		out.Busiest = &b
	}
	return out, nil
}
