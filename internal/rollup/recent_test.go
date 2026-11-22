package rollup

import (
	"testing"
	"time"
)

// The live columns are the bytes of the last minutes over the time they
// cover: a steady figure, not the last two seconds.
func TestRecentSumsTheMinutesOfTheWindow(t *testing.T) {
	s := newTestStore(Options{}, later)
	now := base.Add(10*time.Minute + 30*time.Second)
	feed(s, delta(base.Add(1*time.Minute), phone, "youtube", "US", 1, 1000)) // before the window
	feed(s, delta(base.Add(7*time.Minute+10*time.Second), phone, "youtube", "US", 10, 600))
	feed(s, delta(base.Add(9*time.Minute), laptop, "netflix", "GB", 20, 300))
	feed(s, delta(base.Add(10*time.Minute+5*time.Second), phone, "netflix", "GB", 30, 60))
	s.now = func() time.Time { return now }

	rows, covered := s.Recent(Filter{}, "mac", 3*time.Minute)
	// now-3m is 10:07:30, so the window starts at 10:07:00 and covers 3.5 minutes.
	if covered != 3*time.Minute+30*time.Second {
		t.Errorf("covered %v, want 3m30s", covered)
	}
	got := rowsByKey(rows)
	if got[phone].Bytes.Down != 660 || got[phone].Bytes.Up != 40 || got[laptop].Bytes.Down != 300 {
		t.Errorf("rows %+v", rows)
	}
	if rows[0].Key != phone {
		t.Errorf("busiest first: %+v", rows)
	}
	apps, _ := s.Recent(Filter{MAC: phone}, "app", 10*time.Minute)
	if a := rowsByKey(apps); a["youtube"].Bytes.Down != 1600 || a["netflix"].Bytes.Down != 60 {
		t.Errorf("phone's apps over 10 min %+v", apps)
	}
	// A minute back from 10:10:30 is 10:09:30: the window starts at 10:09.
	ccs, _ := s.Recent(Filter{NotCC: "US"}, "cc", time.Minute)
	if c := rowsByKey(ccs); len(c) != 1 || c["GB"].Bytes.Down != 360 {
		t.Errorf("countries over a minute, US left out: %+v", ccs)
	}
}

// After a restart the minutes start again: the window covers only what the
// store has seen, so a figure is never divided by time nobody counted.
func TestRecentCoversOnlyWhatTheStoreSaw(t *testing.T) {
	s := newTestStore(Options{}, later)
	feed(s, delta(base.Add(40*time.Second), phone, "youtube", "US", 0, 900))
	s.now = func() time.Time { return base.Add(90 * time.Second) }
	rows, covered := s.Recent(Filter{}, "mac", 10*time.Minute)
	if covered != 90*time.Second || len(rows) != 1 {
		t.Errorf("covered %v rows %+v, want 1m30s from the store's first minute", covered, rows)
	}
	if rows, covered := newTestStore(Options{}, later).Recent(Filter{}, "mac", time.Minute); rows != nil || covered != 0 {
		t.Errorf("an empty store answered %+v over %v", rows, covered)
	}
}

func TestRecentReadsOnlyTheDimensionsKeysCarry(t *testing.T) {
	s := newTestStore(Options{}, later)
	feed(s, delta(base, phone, "youtube", "US", 1, 1))
	for _, dim := range []string{"asn", "domain", ""} {
		if rows, _ := s.Recent(Filter{}, dim, time.Minute); rows != nil {
			t.Errorf("Recent(%q) = %+v", dim, rows)
		}
	}
}
