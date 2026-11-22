package rules

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"
)

// MinuteBytes is what an app must move for a device in one minute, up and down
// together, for that minute to count against a time limit (spec 6.6: 64 KB, so
// idle push connections do not eat the allowance).
const MinuteBytes = 65536

// Usage counts today's minutes of use per device and app for the time limits.
//
// It is fed the same deltas serve hands the rollup, not read back from the
// rollup: the rollup folds keys under a flood and keys by (mac, app, cc), so
// reading it would undercount exactly when a device is busiest.
//
// A minute counts once it has closed. Today is the router's day: the counts
// restart at local midnight in loc, not at UTC midnight. Only the (mac, app)
// pairs some limit rule covers are kept (Track), so the table is bounded by
// the limit rules and their members. A pair with the MAC "" stands for every
// device (a limit rule for everyone); the flow table bounds the devices.
type Usage struct {
	mu      sync.Mutex
	loc     *time.Location
	day     string                     // YYYY-MM-DD in loc of the counts
	tracked map[string]map[string]bool // mac -> app
	minutes map[string]map[string]int  // mac -> app -> counted minutes today
	open    map[string]map[string]*openMinute
}

type openMinute struct {
	start time.Time // the minute, truncated
	bytes uint64
}

// NewUsage returns an empty table in the router's zone.
func NewUsage(loc *time.Location) *Usage {
	if loc == nil {
		loc = time.UTC
	}
	return &Usage{
		loc:     loc,
		tracked: map[string]map[string]bool{},
		minutes: map[string]map[string]int{},
		open:    map[string]map[string]*openMinute{},
	}
}

// Track sets the pairs to count; counts and open minutes of pairs no longer
// tracked are dropped.
func (u *Usage) Track(pairs map[string]map[string]bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.tracked = map[string]map[string]bool{}
	for mac, apps := range pairs {
		for app, on := range apps {
			if on {
				if u.tracked[mac] == nil {
					u.tracked[mac] = map[string]bool{}
				}
				u.tracked[mac][app] = true
			}
		}
	}
	for mac, apps := range u.minutes {
		for app := range apps {
			if !u.tracks(mac, app) {
				delete(apps, app)
			}
		}
		if len(apps) == 0 {
			delete(u.minutes, mac)
		}
	}
	for mac, apps := range u.open {
		for app := range apps {
			if !u.tracks(mac, app) {
				delete(apps, app)
			}
		}
		if len(apps) == 0 {
			delete(u.open, mac)
		}
	}
}

// tracks says whether a pair is counted: its own entry or the wildcard.
func (u *Usage) tracks(mac, app string) bool {
	return u.tracked[mac][app] || u.tracked[""][app]
}

// Snapshot is a copy of today's counts, mac -> app -> minutes.
func (u *Usage) Snapshot() map[string]map[string]int {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make(map[string]map[string]int, len(u.minutes))
	for mac, apps := range u.minutes {
		m := make(map[string]int, len(apps))
		for app, n := range apps {
			m[app] = n
		}
		out[mac] = m
	}
	return out
}

// Len is the number of pairs holding a count or an open minute.
func (u *Usage) Len() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	seen := map[[2]string]bool{}
	for mac, apps := range u.minutes {
		for app := range apps {
			seen[[2]string{mac, app}] = true
		}
	}
	for mac, apps := range u.open {
		for app := range apps {
			seen[[2]string{mac, app}] = true
		}
	}
	return len(seen)
}

// Add takes one delta's bytes for a device and app, at its time.
func (u *Usage) Add(mac, app string, up, down uint64, at time.Time) {
	// Only a hardware address can name a device in spent_<id>: an address the
	// resolver has not matched yet, or a service key, is not counted.
	norm, ok := normMAC(mac)
	if !ok {
		return
	}
	mac = norm
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.tracks(mac, app) {
		return
	}
	u.closeLocked(at)
	start := at.Truncate(time.Minute)
	if u.open[mac] == nil {
		u.open[mac] = map[string]*openMinute{}
	}
	m := u.open[mac][app]
	if m == nil || !m.start.Equal(start) {
		if m != nil && start.After(m.start) {
			u.count(mac, app, m)
		}
		if m != nil && start.Before(m.start) {
			// A late delta for a minute already left behind: it is not
			// counted, so time is given back rather than taken.
			return
		}
		m = &openMinute{start: start}
		u.open[mac][app] = m
	}
	m.bytes += up + down
}

// Close closes the minutes that ended before now and restarts the counts at
// the first call after local midnight.
func (u *Usage) Close(now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closeLocked(now)
}

func (u *Usage) closeLocked(now time.Time) {
	for mac, apps := range u.open {
		for app, m := range apps {
			if !now.Before(m.start.Add(time.Minute)) {
				u.count(mac, app, m)
				delete(apps, app)
			}
		}
		if len(apps) == 0 {
			delete(u.open, mac)
		}
	}
	day := now.In(u.loc).Format(time.DateOnly)
	if u.day != day {
		if u.day != "" {
			u.minutes = map[string]map[string]int{}
		}
		u.day = day
	}
}

// count adds a closed minute to the day it belongs to; a minute of an earlier
// day than the table's is dropped.
func (u *Usage) count(mac, app string, m *openMinute) {
	if m.bytes < MinuteBytes {
		return
	}
	day := m.start.In(u.loc).Format(time.DateOnly)
	if u.day != "" && day < u.day {
		return
	}
	if u.day != day {
		u.minutes = map[string]map[string]int{}
		u.day = day
	}
	if u.minutes[mac] == nil {
		u.minutes[mac] = map[string]int{}
	}
	u.minutes[mac][app]++
}

// Minutes is today's counted minutes for a device and app.
func (u *Usage) Minutes(mac, app string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.minutes[mac][app]
}

type usageFile struct {
	Date   string                    `json:"date"`
	Counts map[string]map[string]int `json:"counts"`
}

// Save writes today's counts by rename, so a crash leaves the old file.
func (u *Usage) Save(path string) error {
	u.mu.Lock()
	b, err := json.Marshal(usageFile{Date: u.day, Counts: u.minutes})
	u.mu.Unlock()
	if err != nil {
		return err
	}
	return writeFile(path, b)
}

// Load reads counts saved today (in the router's zone) and ignores a file of
// another day. A missing file is no error; a damaged one is.
func (u *Usage) Load(path string, now time.Time) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f usageFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	today := now.In(u.loc).Format(time.DateOnly)
	if f.Date != today {
		return nil
	}
	u.day = today
	u.minutes = map[string]map[string]int{}
	for mac, apps := range f.Counts {
		for app, n := range apps {
			if u.tracks(mac, app) && n > 0 && n <= MaxLimitMinutes {
				if u.minutes[mac] == nil {
					u.minutes[mac] = map[string]int{}
				}
				u.minutes[mac][app] = n
			}
		}
	}
	return nil
}
