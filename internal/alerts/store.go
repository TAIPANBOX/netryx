// Package alerts keeps what the owner is told about (spec 6.9): a new device,
// a device trying a blocked country, an unusual upload, and the quarantine's
// own troubles; and sends them through Hermes's Telegram bot (telegram.go).
package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The kinds of alert, MaxAlerts how many are kept, and maxKeys how many
// "already told" keys (a new device is told once ever).
const (
	NewDevice       = "new_device"
	BlockedCountry  = "blocked_country"
	Upload          = "upload"
	QuarantineFull  = "quarantine_full"
	BaselineRebuilt = "baseline_rebuilt"
	QuarantineOff   = "quarantine_off"
	NewDevices      = "new_devices"

	MaxAlerts = 200
	maxKeys   = 10000
)

// Alert is one thing the owner is told. Sent is "" until delivered
// ("telegram", or "console" when the channel is the console only); Problem
// says why it was not sent.
type Alert struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	MAC     string    `json:"mac,omitempty"`
	Country string    `json:"country,omitempty"`
	Bytes   uint64    `json:"bytes,omitempty"`
	Count   int       `json:"count,omitempty"`
	At      time.Time `json:"at"`
	Sent    string    `json:"sent,omitempty"`
	Problem string    `json:"problem,omitempty"`
}

// Store keeps the newest MaxAlerts alerts and what was already told, in
// <state>/alerts.json, written by rename whenever it changes (alerts are rare:
// every kind is rate limited).
type Store struct {
	path string
	now  func() time.Time
	loc  *time.Location

	mu     sync.Mutex
	alerts []Alert          // oldest first
	keys   map[string]int64 // told key -> when, unix
	seq    int
	tries  map[string]try // alert id -> sends tried (in memory)
	writes int            // files written, for the tests
}

type storeFile struct {
	Alerts []Alert          `json:"alerts"`
	Keys   map[string]int64 `json:"keys"`
}

// NewStore loads path when it is there; a damaged file starts empty.
func NewStore(path string, now func() time.Time, loc *time.Location) *Store {
	if now == nil {
		now = time.Now
	}
	if loc == nil {
		loc = time.UTC
	}
	s := &Store{path: path, now: now, loc: loc, keys: map[string]int64{}, tries: map[string]try{}}
	if b, err := os.ReadFile(path); err == nil {
		var f storeFile
		if json.Unmarshal(b, &f) == nil {
			for _, a := range f.Alerts {
				if known(a.Kind) {
					s.alerts = append(s.alerts, a)
				}
			}
			if len(s.alerts) > MaxAlerts {
				s.alerts = s.alerts[len(s.alerts)-MaxAlerts:]
			}
			for k, v := range f.Keys {
				s.keys[k] = v
			}
		}
	}
	return s
}

func known(kind string) bool {
	switch kind {
	case NewDevice, BlockedCountry, Upload, QuarantineFull, BaselineRebuilt, QuarantineOff, NewDevices:
		return true
	}
	return false
}

// key is what makes a repeat: a new device once ever, the rest once a local
// day (per device and country for a try, per device for an upload).
func (s *Store) key(a Alert, now time.Time) string {
	day := now.In(s.loc).Format(time.DateOnly)
	switch a.Kind {
	case NewDevice:
		return NewDevice + "|" + a.MAC
	case BlockedCountry:
		return BlockedCountry + "|" + a.MAC + "|" + a.Country + "|" + day
	case Upload:
		return Upload + "|" + a.MAC + "|" + day
	case NewDevices:
		// Once an hour, so a later flood the same day is told too.
		return NewDevices + "|" + day + now.In(s.loc).Format("|15")
	default:
		return a.Kind + "|" + day
	}
}

// Raise stores a and says whether it was new; a repeat within its limit is
// dropped.
func (s *Store) Raise(a Alert) bool {
	if !known(a.Kind) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	k := s.key(a, now)
	if _, told := s.keys[k]; told {
		return false
	}
	s.keys[k] = now.Unix()
	s.trimKeys(now)
	if a.At.IsZero() {
		a.At = now
	}
	s.seq++
	a.ID = fmt.Sprintf("%d-%d", now.UnixNano(), s.seq)
	a.Sent, a.Problem = "", ""
	s.alerts = append(s.alerts, a)
	for len(s.alerts) > MaxAlerts {
		s.dropOldest()
	}
	s.saveLocked()
	return true
}

// dropOldest lets go of the oldest new-device alert, or the oldest alert when
// there is none: a flood of new devices never pushes out a country try or an
// unusual upload.
func (s *Store) dropOldest() {
	for i, a := range s.alerts {
		if rank(a.Kind) == 3 {
			s.alerts = append(s.alerts[:i], s.alerts[i+1:]...)
			return
		}
	}
	s.alerts = s.alerts[1:]
}

// trimKeys drops the daily keys of earlier days and, past maxKeys, the oldest.
func (s *Store) trimKeys(now time.Time) {
	day := now.In(s.loc).Format(time.DateOnly)
	for k := range s.keys {
		if len(k) > 10 && k[len(k)-11] == '|' && k[len(k)-10:] != day && isDate(k[len(k)-10:]) {
			delete(s.keys, k)
		}
		if strings.HasPrefix(k, NewDevices+"|") && !strings.HasPrefix(k, NewDevices+"|"+day) {
			delete(s.keys, k)
		}
	}
	if len(s.keys) <= maxKeys {
		return
	}
	type kv struct {
		k string
		v int64
	}
	all := make([]kv, 0, len(s.keys))
	for k, v := range s.keys {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v < all[j].v })
	for _, e := range all[:len(all)-maxKeys] {
		delete(s.keys, e.k)
	}
}

func isDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// List is the alerts, newest first.
func (s *Store) List() []Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Alert, len(s.alerts))
	for i, a := range s.alerts {
		out[len(s.alerts)-1-i] = a
	}
	return out
}

// Unsent is the alerts not delivered yet, oldest first.
func (s *Store) Unsent() []Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Alert
	for _, a := range s.alerts {
		if a.Sent == "" {
			out = append(out, a)
		}
	}
	return out
}

// Mark records a delivery ("telegram", "console") or, with sent "", why it
// failed.
func (s *Store) Mark(id, sent, problem string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markLocked(id, sent, problem) {
		s.saveLocked()
	}
}

// markLocked changes an alert's delivery and says whether anything changed:
// an unchanged mark writes nothing to flash.
func (s *Store) markLocked(id, sent, problem string) bool {
	for i := range s.alerts {
		if s.alerts[i].ID == id {
			if s.alerts[i].Sent == sent && s.alerts[i].Problem == problem {
				return false
			}
			s.alerts[i].Sent, s.alerts[i].Problem = sent, problem
			return true
		}
	}
	return false
}

// markAll applies marks and writes the file once if any changed.
func (s *Store) markAll(marks [][3]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, m := range marks {
		changed = s.markLocked(m[0], m[1], m[2]) || changed
	}
	if changed {
		s.saveLocked()
	}
}

func (s *Store) saveLocked() {
	s.writes++
	b, err := json.Marshal(storeFile{Alerts: s.alerts, Keys: s.keys})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".alerts-*")
	if err != nil {
		return
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		os.Remove(tmp.Name())
	}
}

// UploadFloor and UploadFactor are spec 6.9's thresholds, and UploadHistory
// how much history a device needs before its upload can be unusual.
const (
	UploadFloor   = 200e6
	UploadFactor  = 4
	UploadHistory = 24 * time.Hour
)

// Unusual says an hour's upload is more than four times the device's 7-day
// hourly maximum and at least 200 MB, for a device with a day of history.
func Unusual(up, weekMax uint64, have time.Duration) bool {
	return have >= UploadHistory && up >= UploadFloor && up > UploadFactor*weekMax
}

// MaxTries is how often an alert is sent before it is left in the console.
const MaxTries = 3

// retryAfter is how long a failed send waits before the next try.
const retryAfter = 10 * time.Minute

// Deliver sends the unsent alerts: on the console channel they are marked
// shown; without a bot each keeps the reason; a failed send is tried again
// after retryAfter, MaxTries times in all, and keeps its reason. It runs on
// the sender's own goroutine, never the engine's.
func (s *Store) Deliver(ctx context.Context, channel string, bot Bot, why string, send func(context.Context, Bot, string) error, name func(string) string) {
	now := s.now()
	unsent := s.Unsent()
	// The more serious kinds first, so a flood of new devices never spends
	// the hour's budget before an upload or a country try is told.
	sort.SliceStable(unsent, func(i, j int) bool { return rank(unsent[i].Kind) < rank(unsent[j].Kind) })
	s.mu.Lock()
	live := map[string]bool{}
	for _, a := range unsent {
		live[a.ID] = true
	}
	for id := range s.tries {
		if !live[id] {
			delete(s.tries, id)
		}
	}
	s.mu.Unlock()
	var batch [][3]string
	defer func() { s.markAll(batch) }()
	for _, a := range unsent {
		switch {
		case channel == "console":
			batch = append(batch, [3]string{a.ID, "console", ""})
			continue
		case why != "":
			// Marked once per reason: the same reason changes nothing and
			// writes nothing.
			batch = append(batch, [3]string{a.ID, "", why})
			continue
		}
		s.mu.Lock()
		t := s.tries[a.ID]
		due := t.n < MaxTries && (t.n == 0 || now.Sub(t.last) >= retryAfter)
		if due {
			s.tries[a.ID] = try{n: t.n + 1, last: now}
		}
		s.mu.Unlock()
		if !due {
			continue
		}
		if err := send(ctx, bot, Text(a, name)); err != nil {
			if errors.Is(err, ErrTooMany) {
				// A local refusal is not a try: give it back and stop for
				// this round, the budget is spent.
				s.mu.Lock()
				s.tries[a.ID] = t
				s.mu.Unlock()
				s.Mark(a.ID, "", err.Error())
				return
			}
			s.Mark(a.ID, "", err.Error())
			continue
		}
		s.mu.Lock()
		delete(s.tries, a.ID)
		s.mu.Unlock()
		s.Mark(a.ID, "telegram", "")
	}
}

// rank orders the kinds by how much they matter now.
func rank(kind string) int {
	switch kind {
	case Upload:
		return 0
	case BlockedCountry:
		return 1
	case BaselineRebuilt, QuarantineFull, QuarantineOff:
		return 2
	}
	return 3
}

type try struct {
	n    int
	last time.Time
}
