package daily

import (
	"testing"
	"time"
)

// Each day of the window with the counters read at its close: a day with no
// file is unknown, and a day written after the router missed its close has
// no counters.
func TestCountersAreEachDaysCloseOverTheWindow(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s, err := Open(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	d1 := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	if _, err := s.Write(Day{Date: d1, Mask: 1<<24 - 1, CountersAt: d2.Add(2 * time.Minute), Ifaces: []Iface{{Name: "wan", Rx: 100, Tx: 10}, {Name: "wwan", Rx: 5, Tx: 1}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(Day{Date: d2, Mask: 1<<20 - 1}); err != nil {
		t.Fatal(err)
	}
	got := s.Counters(now)
	if len(got) != Keep || !got[Keep-1].Date.Equal(d2) {
		t.Fatalf("%d days, the last %v", len(got), got[len(got)-1].Date)
	}
	a, b := got[Keep-2], got[Keep-1]
	if !a.Known || a.Mask != 1<<24-1 || !a.At.Equal(d2.Add(2*time.Minute)) || a.Ifaces["wan"] != [2]uint64{100, 10} || a.Ifaces["wwan"] != [2]uint64{5, 1} {
		t.Errorf("a day read at its close: %+v", a)
	}
	if !b.Known || b.Mask != 1<<20-1 || !b.At.IsZero() || len(b.Ifaces) != 0 {
		t.Errorf("a day written without its close: %+v", b)
	}
	if got[0].Known {
		t.Errorf("a day with no file: %+v", got[0])
	}
}
