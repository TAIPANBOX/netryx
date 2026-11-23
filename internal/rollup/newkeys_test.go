package rollup

import (
	"testing"
	"time"
)

// What is new for a device: an app or a country it used since the split
// (the start of yesterday) and never in the days before it, with when it was
// first seen and what it moved since. The days before are read from the day
// ring, so the comparison reaches back as far as the store keeps days.
func TestNewKeysAreWhatADeviceUsedSinceTheSplitAndNeverBefore(t *testing.T) {
	now := base.Add(20 * 24 * time.Hour)
	s := newTestStore(Options{}, now)
	split := now.Truncate(24 * time.Hour).Add(-24 * time.Hour) // the start of yesterday
	old := now.Add(-10 * 24 * time.Hour)
	// youtube and US for weeks; tiktok and CN only since yesterday; netflix
	// last used ten days ago and again today is not new.
	feed(s, delta(old, "aa", "youtube", "US", 10, 100))
	feed(s, delta(old, "aa", "netflix", "US", 10, 100))
	feed(s, delta(split.Add(2*time.Hour), "aa", "tiktok", "CN", 5, 50))
	feed(s, delta(now.Add(-time.Hour), "aa", "tiktok", "CN", 5, 70))
	feed(s, delta(now.Add(-time.Hour), "aa", "netflix", "US", 1, 1))
	feed(s, delta(now.Add(-time.Hour), "aa", "youtube", "US", 1, 1))
	// Another device's use says nothing about this one.
	feed(s, delta(old, "bb", "tiktok", "CN", 1, 1))
	s.now = func() time.Time { return now }

	apps := s.NewKeys("aa", "app", split, now.Add(-30*24*time.Hour), now)
	if len(apps) != 1 || apps[0].Key != "tiktok" {
		t.Fatalf("new apps %+v, want tiktok alone", apps)
	}
	if !apps[0].First.Equal(split.Add(2 * time.Hour).Truncate(time.Hour)) {
		t.Errorf("tiktok first seen %v, want the hour it began", apps[0].First)
	}
	if apps[0].Bytes.Down != 120 {
		t.Errorf("tiktok moved %d down since the split, want 120", apps[0].Bytes.Down)
	}
	ccs := s.NewKeys("aa", "cc", split, now.Add(-30*24*time.Hour), now)
	if len(ccs) != 1 || ccs[0].Key != "CN" {
		t.Fatalf("new countries %+v, want CN alone", ccs)
	}
	if got := s.NewKeys("bb", "app", split, now.Add(-30*24*time.Hour), now); len(got) != 0 {
		t.Errorf("a device that used nothing since the split has new %+v", got)
	}
	if got := s.NewKeys("aa", "domain", split, now.Add(-30*24*time.Hour), now); got != nil {
		t.Errorf("a dimension keys do not carry answered %+v", got)
	}
}
