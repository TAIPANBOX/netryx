package rollup

// The minutes kept across a restart of netryx: the last hour of them, written
// at a stop to a file in RAM (cmd/netryx puts it in /tmp/netryx through
// internal/ramfile) and read at the start, so the lists' last hour and the
// Now columns go on where they were. Only a reboot of the router empties
// them. The hours and days are saved on their own and take nothing from here.

import (
	"encoding/json"
	"errors"
	"time"
)

// keptMinutes is how far back the kept file reaches: the lists' hour and one
// minute more, for the one still filling.
const keptMinutes = 61

// MaxMinutesFile bounds what LoadMinutes is given to read.
const MaxMinutesFile = 16 << 20

const minutesVersion = 1

type minutesFile struct {
	V       int          `json:"v"`
	Since   time.Time    `json:"since"`
	FirstAt time.Time    `json:"firstAt"`
	Minutes []minuteJSON `json:"minutes"`
}

// minuteJSON is one minute; each key is [mac, app, cc, up, down, flows].
type minuteJSON struct {
	At   time.Time `json:"at"`
	Keys [][]any   `json:"keys"`
}

// MinutesJSON is the last hour of minutes as LoadMinutes reads them.
func (s *Store) MinutesJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f := minutesFile{V: minutesVersion, Since: s.since, FirstAt: s.firstAt, Minutes: []minuteJSON{}}
	if !s.since.IsZero() {
		from := s.mr.newest.Add(-(keptMinutes - 1) * time.Minute)
		for _, m := range s.mins {
			if m == nil || !s.mr.holds(m.At) || m.At.Before(from) || m.At.Before(s.since) || len(m.Keys) == 0 {
				continue
			}
			mj := minuteJSON{At: m.At, Keys: make([][]any, 0, len(m.Keys))}
			for k, b := range m.Keys {
				mj.Keys = append(mj.Keys, []any{k.MAC, k.App, k.CC, b.Up, b.Down, b.Flows})
			}
			f.Minutes = append(f.Minutes, mj)
		}
	}
	return json.Marshal(f)
}

// LoadMinutes takes the minutes of a file MinutesJSON wrote, before the store
// takes any delta. Only minutes inside the kept hour of this store's clock
// are taken, a minute ahead of the clock is not, and each minute is held to
// the minute's key cap as Add holds it.
func (s *Store) LoadMinutes(b []byte) error {
	var f minutesFile
	if err := json.Unmarshal(b, &f); err != nil {
		return errors.New("the minutes file is damaged")
	}
	if f.V != minutesVersion {
		return errors.New("the minutes file is of another version")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.since.IsZero() {
		return errors.New("the store has begun counting; the minutes file is not mixed in")
	}
	now := s.now().UTC()
	from := now.Truncate(time.Minute).Add(-(keptMinutes - 1) * time.Minute)
	var first time.Time
	for _, mj := range f.Minutes {
		at := mj.At.UTC().Truncate(time.Minute)
		if at.Before(from) || at.After(now) || at.Before(f.Since.UTC().Truncate(time.Minute)) {
			continue
		}
		m := s.minuteFor(at)
		if m == nil {
			continue
		}
		for _, kv := range mj.Keys {
			k, bb, ok := keyOf(kv)
			if !ok {
				continue
			}
			k = Key{MAC: s.intern(k.MAC), App: s.intern(k.App), CC: s.intern(k.CC)}
			k = fold(m.Keys, k, s.minCap)
			m.Keys[k] = add(m.Keys[k], bb)
		}
		if first.IsZero() || at.Before(first) {
			first = at
		}
	}
	if first.IsZero() {
		return nil
	}
	// The minutes are whole from where the old process began counting, or
	// from the oldest minute kept, whichever is later.
	s.since = first
	if since := f.Since.UTC(); since.After(first) {
		s.since = since
	}
	s.firstAt = s.since
	if fa := f.FirstAt.UTC(); fa.After(s.firstAt) && !fa.After(now) {
		s.firstAt = fa
	}
	return nil
}

func keyOf(kv []any) (Key, Bytes, bool) {
	if len(kv) != 6 {
		return Key{}, Bytes{}, false
	}
	str := func(v any) (string, bool) {
		x, ok := v.(string)
		return x, ok && len(x) <= 128
	}
	num := func(v any) (uint64, bool) {
		x, ok := v.(float64)
		return uint64(x), ok && x >= 0 && x <= 1<<53
	}
	mac, ok1 := str(kv[0])
	app, ok2 := str(kv[1])
	cc, ok3 := str(kv[2])
	up, ok4 := num(kv[3])
	down, ok5 := num(kv[4])
	flows, ok6 := num(kv[5])
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) {
		return Key{}, Bytes{}, false
	}
	return Key{MAC: mac, App: app, CC: cc}, Bytes{Up: up, Down: down, Flows: flows}, true
}
