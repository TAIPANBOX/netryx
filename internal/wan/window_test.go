package wan

import (
	"testing"
	"time"
)

var w0 = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

// The line is read every 10 s; the tile shows the average over the window
// picked, each reading weighted by the seconds it covers.
func TestTheWindowAveragesTheLineOverItsSpan(t *testing.T) {
	var w Window
	w.Add(w0, 0, 0) // the first reading only starts the clock
	w.Add(w0.Add(10*time.Second), 100, 10)
	w.Add(w0.Add(20*time.Second), 200, 20)
	w.Add(w0.Add(40*time.Second), 400, 40) // a reading 20 s after the last
	now := w0.Add(40 * time.Second)
	rx, tx, cov := w.Average(now, time.Minute)
	if cov != 40*time.Second {
		t.Fatalf("covered %v, want the 40 s read", cov)
	}
	// (100*10 + 200*10 + 400*20) / 40
	if rx != 275 || tx != 27.5 {
		t.Fatalf("average %v/%v, want 275/27.5", rx, tx)
	}
	rx, _, cov = w.Average(now, 30*time.Second)
	if cov != 30*time.Second || rx != (200*10+400*20)/30.0 {
		t.Fatalf("over 30 s: %v over %v", rx, cov)
	}
	if rx, _, cov := (&Window{}).Average(now, time.Minute); rx != 0 || cov != 0 {
		t.Fatal("an empty window reads something")
	}
}

func TestTheWindowKeepsTenMinutesAtMost(t *testing.T) {
	var w Window
	for i := 0; i <= 200; i++ {
		w.Add(w0.Add(time.Duration(i)*10*time.Second), float64(i), 0)
	}
	if n := len(w.s); n > maxWindowSamples {
		t.Fatalf("%d readings kept, want at most %d", n, maxWindowSamples)
	}
	now := w0.Add(2000 * time.Second)
	if _, _, cov := w.Average(now, 10*time.Minute); cov != 10*time.Minute {
		t.Fatalf("10 minutes cover %v", cov)
	}
}

// A clock set back starts the window afresh rather than counting negative time.
func TestAClockSetBackStartsTheWindowAgain(t *testing.T) {
	var w Window
	w.Add(w0, 0, 0)
	w.Add(w0.Add(10*time.Second), 100, 0)
	w.Add(w0.Add(-time.Hour), 999, 0)
	w.Add(w0.Add(-time.Hour+10*time.Second), 50, 0)
	rx, _, cov := w.Average(w0.Add(-time.Hour+10*time.Second), time.Minute)
	if rx != 50 || cov != 10*time.Second {
		t.Fatalf("after the clock went back: %v over %v, want 50 over 10s", rx, cov)
	}
}
