// Package journal keeps the interventions on the house's devices: who let a
// device through, which limit ran out, who let it out of quarantine or
// isolated it, which rule blocked it and who cut its connection, with when.
//
// The journal is one file of JSON lines, appended to and never rewritten
// in place: an intervention is one short line, so a day of them costs flash
// a few pages. It keeps the newest MaxEntries of the last Keep; the file is
// written whole again, through a temporary file renamed over it, only when it
// holds twice that. Every line is checked when it is read: a damaged one is
// skipped and counted, and the rest load. A line a power cut tore is never
// glued to the next one.
package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// MaxEntries is how many interventions are kept.
	MaxEntries = 1000
	// Keep is how far back they are kept.
	Keep = 30 * 24 * time.Hour
	// blockEvery is how often a rule blocking one device is written.
	blockEvery = time.Hour
	maxLine    = 1024
	maxField   = 64
	maxDetail  = 128
)

// The kinds of intervention. Start marks when the journal began.
const (
	Allow      = "allow"
	LimitSpent = "limit_spent"
	Approved   = "approved"
	Isolated   = "isolated"
	Released   = "released"
	Cut        = "cut"
	Blocked    = "blocked"
	Start      = "start"
)

var kinds = map[string]bool{Allow: true, LimitSpent: true, Approved: true, Isolated: true, Released: true, Cut: true, Blocked: true, Start: true}
var bys = map[string]bool{"": true, "owner": true, "agent": true, "router": true}

// clockSet is the earliest date a clock is believed: before it, nothing is
// dropped as old.
var clockSet = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Entry is one intervention. MAC is the device, or for an allowance the
// member it names (a device or a group); ID is the allowance's section; By
// is who did it: the owner, the agent, or the router itself (a rule, a limit).
type Entry struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	ID     string    `json:"id,omitempty"`
	MAC    string    `json:"mac,omitempty"`
	Rule   string    `json:"rule,omitempty"`
	Name   string    `json:"name,omitempty"`
	By     string    `json:"by,omitempty"`
	Until  time.Time `json:"until,omitzero"`
	Detail string    `json:"detail,omitempty"`
}

func (e Entry) valid() bool {
	if e.At.IsZero() || !kinds[e.Kind] || !bys[e.By] || len(e.Detail) > maxDetail {
		return false
	}
	for _, f := range []string{e.ID, e.MAC, e.Rule, e.Name} {
		if len(f) > maxField {
			return false
		}
	}
	return true
}

// Stats is what the journal wrote: lines appended and their bytes, and whole
// rewrites and theirs.
type Stats struct{ Appends, Bytes, Rewrites, RewriteBytes int }

// Store is the journal.
type Store struct {
	mu        sync.Mutex
	path      string
	now       func() time.Time
	entries   []Entry // oldest first, at most MaxEntries, none older than Keep
	keys      map[string]bool
	lastBlock map[string]time.Time
	fileLines int  // lines the file holds, kept or not
	needNL    bool // the file ends inside a line a power cut tore
	skipped   int
	stats     Stats
}

// Open reads the journal at path, or starts it there. A file anyone but its
// owner may read is made the owner's alone.
func Open(path string, now func() time.Time) (*Store, error) {
	s := &Store{path: path, now: now, keys: map[string]bool{}, lastBlock: map[string]time.Time{}}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a plain file", path)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(path, 0o600); err != nil {
				return nil, err
			}
		}
	}
	if len(b) > 0 {
		s.needNL = b[len(b)-1] != '\n'
		for _, line := range bytes.Split(b, []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			s.fileLines++
			var e Entry
			if len(line) > maxLine || json.Unmarshal(line, &e) != nil || !e.valid() {
				s.skipped++
				continue
			}
			s.entries = append(s.entries, e)
		}
		slices.SortStableFunc(s.entries, func(a, b Entry) int { return a.At.Compare(b.At) })
		s.trim()
		for _, e := range s.entries {
			s.remember(e)
		}
		return s, nil
	}
	if _, err := s.append(Entry{At: now(), Kind: Start}); err != nil {
		return nil, err
	}
	return s, nil
}

// trim drops what is older than Keep and past the newest MaxEntries.
func (s *Store) trim() {
	if now := s.now(); now.After(clockSet) {
		cut := now.Add(-Keep)
		i := 0
		for i < len(s.entries) && s.entries[i].At.Before(cut) {
			i++
		}
		s.entries = s.entries[i:]
	}
	if n := len(s.entries); n > MaxEntries {
		s.entries = slices.Clone(s.entries[n-MaxEntries:])
	}
}

func key(e Entry) string {
	switch e.Kind {
	case Allow:
		return "allow|" + e.ID + "|" + strconv.FormatInt(e.Until.Unix(), 10)
	case LimitSpent:
		return "spent|" + e.Rule + "|" + strings.ToLower(e.MAC) + "|" + strconv.FormatInt(e.Until.Unix(), 10)
	}
	return ""
}

func (s *Store) remember(e Entry) {
	if k := key(e); k != "" {
		s.keys[k] = true
	}
	if e.Kind == Blocked {
		bk := strings.ToLower(e.MAC) + "|" + e.Rule
		if e.At.After(s.lastBlock[bk]) {
			s.lastBlock[bk] = e.At
		}
	}
}

// Add writes an intervention, stamped now when it carries no time, unless it
// is one already written: the same allowance until the same time, the same
// limit spent until the same midnight, or the same rule blocking the same
// device within the hour. It says whether a line was written.
func (s *Store) Add(e Entry) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.At.IsZero() {
		e.At = s.now()
	}
	e.At, e.Until = e.At.UTC(), e.Until.UTC()
	if e.Kind == Start || !e.valid() {
		return false, fmt.Errorf("not an intervention: %+v", e)
	}
	if k := key(e); k != "" && s.keys[k] {
		return false, nil
	}
	if e.Kind == Blocked {
		if last, ok := s.lastBlock[strings.ToLower(e.MAC)+"|"+e.Rule]; ok && e.At.Sub(last) < blockEvery {
			return false, nil
		}
	}
	return s.append(e)
}

// append writes one line and, past twice the bound, the whole file again.
// The caller holds mu (or the store is not shared yet).
func (s *Store) append(e Entry) (bool, error) {
	line, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	out := append(line, '\n')
	if s.needNL {
		out = append([]byte{'\n'}, out...)
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	_, werr := f.Write(out)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return false, werr
	}
	s.needNL = false
	s.fileLines++
	s.stats.Appends++
	s.stats.Bytes += len(out)
	s.entries = append(s.entries, e)
	s.remember(e)
	s.trim()
	if s.fileLines > 2*MaxEntries {
		if err := s.rewrite(); err != nil {
			return true, err
		}
	}
	return true, nil
}

// rewrite writes the kept interventions whole, through a temporary file
// renamed over the journal. The caller holds mu.
func (s *Store) rewrite() error {
	var buf bytes.Buffer
	for _, e := range s.entries {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".interventions-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	s.fileLines = len(s.entries)
	s.stats.Rewrites++
	s.stats.RewriteBytes += buf.Len()
	return nil
}

// Of answers the interventions about a device, or about a group it belongs
// to, at or after since, newest first.
func (s *Store) Of(mac, group string, since time.Time) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Entry
	for i := len(s.entries) - 1; i >= 0; i-- {
		e := s.entries[i]
		if e.Kind == Start || e.At.Before(since) {
			continue
		}
		if strings.EqualFold(e.MAC, mac) || (group != "" && e.MAC == group) {
			out = append(out, e)
		}
	}
	return out
}

// Since is the time of the oldest intervention kept, or of the journal's
// start while it is kept; zero when nothing is.
func (s *Store) Since() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == 0 {
		return time.Time{}
	}
	return s.entries[0].At
}

// Skipped is how many lines of the file could not be read when it was opened.
func (s *Store) Skipped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skipped
}

// Stats is what this store wrote since it was opened.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}
