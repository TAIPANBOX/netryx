package rollup

import (
	"testing"
	"time"
)

// When the history first saw a device: the start of the first hour it moved
// bytes in, or past the hour ring's reach the start of its first day; zero
// for a device the history never saw. It outlives a restart, which the
// inventory's own first sight does not.
func TestFirstSeenIsTheFirstHourTheHistoryHoldsForTheDevice(t *testing.T) {
	now := base.Add(20 * 24 * time.Hour)
	s := newTestStore(Options{}, now)
	old := now.Add(-10 * 24 * time.Hour).Truncate(24 * time.Hour).Add(13*time.Hour + 20*time.Minute)
	recent := now.Add(-3 * 24 * time.Hour).Truncate(24 * time.Hour).Add(5*time.Hour + 40*time.Minute)
	feed(s, delta(old.Add(-24*time.Hour), "bb", "web", "US", 1, 1)) // another device, earlier
	feed(s, delta(old, "aa", "web", "US", 1, 1))
	feed(s, delta(recent, "aa", "web", "US", 1, 1))
	feed(s, delta(recent, "cc", "web", "US", 1, 1))
	feed(s, delta(now.Add(-time.Hour), "cc", "web", "US", 1, 1))
	from := now.Add(-30 * 24 * time.Hour)
	if got, want := s.FirstSeen("aa", from), old.Truncate(24*time.Hour); !got.Equal(want) {
		t.Errorf("a device first seen past the hour ring: %v, want its first day %v", got, want)
	}
	if got, want := s.FirstSeen("cc", from), recent.Truncate(time.Hour); !got.Equal(want) {
		t.Errorf("a device first seen inside the hour ring: %v, want its first hour %v", got, want)
	}
	if got := s.FirstSeen("aa", now.Add(-5*24*time.Hour)); !got.Equal(recent.Truncate(time.Hour)) {
		t.Errorf("seen only from five days ago: %v, want %v", got, recent.Truncate(time.Hour))
	}
	if got := s.FirstSeen("zz", from); !got.IsZero() {
		t.Errorf("a device the history never saw: %v", got)
	}
}
