package rollup

import (
	"testing"
	"time"
)

// A list's sparkline is each row's last hour in steps, read from the minutes
// in one pass for every row. Steps before the store's first minute are time
// nobody counted, and say so, so a restart never draws as a quiet house.
func TestSparksAreEachKeysMinutesInSteps(t *testing.T) {
	s := newTestStore(Options{}, later)
	feed(s, delta(base.Add(40*time.Second), laptop, "netflix", "GB", 0, 7)) // the store's first delta
	feed(s, delta(base.Add(1*time.Minute), phone, "youtube", "US", 10, 1000))
	feed(s, delta(base.Add(3*time.Minute), laptop, "netflix", "GB", 0, 300))
	feed(s, delta(base.Add(5*time.Minute+20*time.Second), phone, "youtube", "US", 0, 500))
	feed(s, delta(base.Add(10*time.Minute+5*time.Second), phone, "netflix", "GB", 0, 60))
	s.now = func() time.Time { return base.Add(10*time.Minute + 30*time.Second) }

	// Ten minutes in steps of two: the last step is the one still filling.
	sp := s.Sparks(Filter{}, "mac", 10*time.Minute, 2*time.Minute)
	if !sp.From.Equal(base.Add(2*time.Minute)) || sp.Step != 2*time.Minute || sp.Known != 0 {
		t.Fatalf("from %v step %v known %d, want 10:02, 2m, 0", sp.From, sp.Step, sp.Known)
	}
	want := map[string][]uint64{phone: {0, 500, 0, 0, 60}, laptop: {300, 0, 0, 0, 0}}
	for k, w := range want {
		got := sp.Keys[k]
		if len(got) != len(w) {
			t.Fatalf("%s: %v, want %v", k, got, w)
		}
		for i := range w {
			if got[i] != w[i] {
				t.Fatalf("%s: %v, want %v", k, got, w)
			}
		}
	}

	// Twenty minutes reach back before the first delta: those steps are not known.
	sp = s.Sparks(Filter{}, "mac", 20*time.Minute, 2*time.Minute)
	if sp.Known != 4 {
		t.Errorf("known from step %d, want 4 (the step holding 10:00:40)", sp.Known)
	}
	if got := sp.Keys[phone]; len(got) != 10 || got[4] != 1010 || got[6] != 500 {
		t.Errorf("phone over 20 min: %v", got)
	}

	// The filter narrows the rows as Top's does.
	sp = s.Sparks(Filter{App: "youtube"}, "mac", 10*time.Minute, 2*time.Minute)
	if _, ok := sp.Keys[laptop]; ok || sp.Keys[phone][4] != 0 {
		t.Errorf("youtube only: %v", sp.Keys)
	}
	apps := s.Sparks(Filter{}, "app", 10*time.Minute, 2*time.Minute)
	if apps.Keys["netflix"][0] != 300 || apps.Keys["netflix"][4] != 60 {
		t.Errorf("by app: %v", apps.Keys)
	}

	if bad := s.Sparks(Filter{}, "domain", 10*time.Minute, 2*time.Minute); bad.Keys != nil {
		t.Errorf("a dimension the keys do not carry: %v", bad)
	}
	if bad := s.Sparks(Filter{}, "mac", time.Minute, 2*time.Minute); bad.Keys != nil {
		t.Errorf("a span shorter than its step: %v", bad)
	}
}

// Before any delta the store knows no step at all.
func TestAnEmptyStoreKnowsNoStep(t *testing.T) {
	s := newTestStore(Options{}, base)
	sp := s.Sparks(Filter{}, "mac", time.Hour, 2*time.Minute)
	if sp.Known != 30 || len(sp.Keys) != 0 {
		t.Errorf("known %d keys %v, want 30 and none", sp.Known, sp.Keys)
	}
}
