package rollup

import (
	"testing"
	"time"

	"netryx/internal/flows"
)

// A day is answered from the hour ring only once it has closed, and only
// while the ring still reaches its first hour: a day the ring has partly
// forgotten would read its forgotten hours as hours the router was off.
func TestADayIsAnsweredOnlyOnceClosedAndWholeInTheRing(t *testing.T) {
	d0 := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	clk := d0
	s := NewStore(Options{HourSlots: 30, Now: func() time.Time { return clk }})
	add := func(h int) {
		clk = d0.Add(time.Duration(h)*time.Hour + 10*time.Minute)
		s.Add(flows.Delta{At: clk, MAC: "aa:bb:cc:dd:ee:01", App: "web", CC: "GB", Down: uint64(100 * (h + 1)), Up: 10, First: true})
	}
	for h := 0; h < 30; h++ {
		if h != 6 {
			add(h)
		}
	}
	got, ok := s.DayHours(d0)
	if !ok {
		t.Fatal("a closed day the ring holds whole is not answered")
	}
	if got.Mask != (1<<24-1)&^(1<<6) {
		t.Errorf("mask %024b, want every hour but 6", got.Mask)
	}
	if v := got.By["aa:bb:cc:dd:ee:01"]; v == nil || v[0][5] != 600 || v[1][5] != 10 || v[0][6] != 0 {
		t.Errorf("the device's hours: %v", v)
	}
	if _, ok := s.DayHours(d0.Add(24 * time.Hour)); ok {
		t.Error("an open day is answered")
	}
	add(30) // the ring now starts at 01:00 of that day
	if _, ok := s.DayHours(d0); ok {
		t.Error("a day the ring no longer holds whole is answered")
	}
}
