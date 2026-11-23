package journal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

const tv = "02:4e:58:00:00:01"

func open(t *testing.T, path string, c *clock) *Store {
	t.Helper()
	s, err := Open(path, c.Now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// An intervention is written as one line appended to the file, 0600, and is
// read back after a restart, newest first, for the device it is about or a
// group it belongs to.
func TestAnInterventionIsAppendedAndReadBackAfterARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "interventions.jsonl")
	c := &clock{t0}
	s := open(t, path, c)
	c.t = t0.Add(time.Minute)
	s.Add(Entry{Kind: Allow, ID: "a1", MAC: tv, Rule: "r_games", Name: "Games", By: "agent", Until: t0.Add(31 * time.Minute)})
	c.t = t0.Add(2 * time.Minute)
	s.Add(Entry{Kind: Approved, MAC: "02:4e:58:00:00:02", By: "owner"})
	c.t = t0.Add(3 * time.Minute)
	s.Add(Entry{Kind: Allow, ID: "a2", MAC: "kids", Rule: "", By: "owner", Until: t0.Add(time.Hour)})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("no file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	if n := strings.Count(string(b), "\n"); n != 4 {
		t.Errorf("%d lines, want the start and three interventions", n)
	}
	s2 := open(t, path, c)
	got := s2.Of(tv, "kids", time.Time{})
	if len(got) != 2 || got[0].ID != "a2" || got[1].ID != "a1" || !got[1].At.Equal(t0.Add(time.Minute)) || got[1].By != "agent" {
		t.Errorf("the TV's interventions after a restart: %+v", got)
	}
	if !s2.Since().Equal(t0) {
		t.Errorf("the journal began %v, want %v", s2.Since(), t0)
	}
}

// The same allowance, the same spent limit of a day, and a rule blocking the
// same device more than once an hour are one intervention, also across a
// restart.
func TestAnInterventionIsWrittenOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "interventions.jsonl")
	c := &clock{t0}
	s := open(t, path, c)
	allow := Entry{Kind: Allow, ID: "a1", MAC: tv, Rule: "r1", By: "owner", Until: t0.Add(time.Hour)}
	spent := Entry{Kind: LimitSpent, MAC: tv, Rule: "r2", By: "router", Until: t0.Add(12 * time.Hour)}
	block := Entry{Kind: Blocked, MAC: tv, Rule: "r3", By: "owner"}
	for i := 0; i < 3; i++ {
		s.Add(allow)
		s.Add(spent)
		s.Add(block)
		c.t = c.t.Add(10 * time.Minute)
	}
	s = open(t, path, c)
	s.Add(allow)
	s.Add(spent)
	s.Add(block) // 30 minutes after the first: still the same hour's block
	if n := len(s.Of(tv, "", time.Time{})); n != 3 {
		t.Errorf("%d interventions, want 3", n)
	}
	allow.Until = t0.Add(2 * time.Hour) // the allowance extended
	s.Add(allow)
	c.t = t0.Add(61 * time.Minute)
	s.Add(block)
	if n := len(s.Of(tv, "", time.Time{})); n != 5 {
		t.Errorf("%d interventions after an extension and an hour, want 5", n)
	}
}

// Every line is checked when the journal is read: a damaged line, a line
// with a kind it does not know or an overlong field is skipped and counted,
// and the rest load. A line a power cut tore is not glued to the next one.
func TestADamagedLineIsSkippedAndCountedAndTheRestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "interventions.jsonl")
	good := `{"at":"2026-11-20T11:00:00Z","kind":"approved","mac":"` + tv + `","by":"owner"}`
	body := good + "\n" +
		"not json at all\n" +
		`{"at":"2026-11-20T11:01:00Z","kind":"wipe","mac":"` + tv + `"}` + "\n" +
		`{"at":"2026-11-20T11:02:00Z","kind":"approved","mac":"` + strings.Repeat("x", 200) + `"}` + "\n" +
		`{"at":"","kind":"approved","mac":"` + tv + `"}` + "\n" +
		`{"at":"2026-11-20T11:03:00Z","kind":"isolated","mac":"` + tv + `","by":"own` // torn
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &clock{t0}
	s := open(t, path, c)
	if s.Skipped() != 5 {
		t.Errorf("%d lines skipped, want 5", s.Skipped())
	}
	s.Add(Entry{Kind: Isolated, MAC: tv, By: "owner"})
	s = open(t, path, c)
	got := s.Of(tv, "", time.Time{})
	if len(got) != 2 || got[0].Kind != Isolated || got[1].Kind != Approved {
		t.Errorf("after the torn line: %+v", got)
	}
	if s.Skipped() != 5 {
		t.Errorf("%d skipped after appending past a torn line, want still 5", s.Skipped())
	}
}

// The journal keeps the newest 1000 interventions of the last 30 days; the
// file is written whole again only when it holds twice that, through a
// temporary file renamed over it, 0600.
func TestTheJournalIsBoundedAndRewrittenOnlyAtTwiceItsSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "interventions.jsonl")
	c := &clock{t0}
	s := open(t, path, c)
	for i := 0; i < 2*MaxEntries-1; i++ {
		c.t = c.t.Add(time.Second)
		s.Add(Entry{Kind: Approved, MAC: tv, By: "owner"})
	}
	if st := s.Stats(); st.Rewrites != 0 || st.Appends != 2*MaxEntries {
		t.Errorf("before twice the size: %+v", st)
	}
	c.t = c.t.Add(time.Second)
	s.Add(Entry{Kind: Approved, MAC: tv, By: "owner"})
	if st := s.Stats(); st.Rewrites != 1 {
		t.Errorf("at twice the size: %+v", st)
	}
	if n := len(s.Of(tv, "", time.Time{})); n != MaxEntries {
		t.Errorf("%d kept, want %d", n, MaxEntries)
	}
	b, _ := os.ReadFile(path)
	if n := strings.Count(string(b), "\n"); n != MaxEntries {
		t.Errorf("the rewritten file holds %d lines, want %d", n, MaxEntries)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("rewritten: %v %v", err, fi)
	}
	// Thirty days later the old ones are gone from what is read.
	c.t = c.t.Add(31 * 24 * time.Hour)
	s = open(t, path, c)
	if n := len(s.Of(tv, "", time.Time{})); n != 0 {
		t.Errorf("%d interventions older than 30 days still read", n)
	}
}

// A file others could read is made the owner's alone when it is opened.
func TestAJournalOthersCanReadIsMadePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "interventions.jsonl")
	os.WriteFile(path, nil, 0o644)
	open(t, path, &clock{t0})
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("%v %v, want mode 0600", err, fi)
	}
}
