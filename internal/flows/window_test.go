package flows

import (
	"testing"
	"time"

	"netryx/internal/ct"
)

// The live table can be read by what each flow moved over the last minutes:
// a download that ran all along ranks above a flow that only just opened,
// and the order holds still between two reads.
func TestLiveFlowsCanBeReadByRecentVolume(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Sync(nil, t0)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.New, 2, phone, 51002, youtube, 443, 0, 0), t0)
	// 12:00: flow 1 moves 20000 down, flow 2 100.
	tb.Sync([]ct.Event{watch(1, 0, 20000), watch(2, 0, 100)}, t0.Add(30*time.Second))
	// 12:05: flow 2 moves 5000 more, flow 1 10.
	tb.Sync([]ct.Event{watch(1, 0, 20010), watch(2, 0, 5100)}, t0.Add(5*time.Minute+20*time.Second))
	tb.Sync([]ct.Event{watch(1, 0, 20010), watch(2, 0, 5100)}, t0.Add(5*time.Minute+30*time.Second))

	ids := func(rows []Flow) []uint32 {
		var out []uint32
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	rows, next := tb.Query(Query{Live: true, Window: time.Minute})
	if got := ids(rows); len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("over a minute %v, want flow 2 first", got)
	}
	if rows[0].WinDown != 5000 || rows[1].WinDown != 10 || next != "" {
		t.Errorf("window bytes %d, %d (next %q), want 5000 and 10", rows[0].WinDown, rows[1].WinDown, next)
	}
	rows, _ = tb.Query(Query{Live: true, Window: 10 * time.Minute})
	if got := ids(rows); got[0] != 1 || rows[0].WinDown != 20010 || rows[1].WinDown != 5100 {
		t.Errorf("over ten minutes %v %+v, want flow 1 first with 20010", got, rows)
	}
	// Without a window the live table still answers newest first.
	rows, _ = tb.Query(Query{Live: true})
	if got := ids(rows); got[0] != 2 {
		t.Errorf("newest first %v", got)
	}
}

// Minutes older than the window, and minutes the flow did not move in, count
// for nothing; a flow quiet for longer than the slots hold reads zero.
func TestOldMinutesFallOutOfTheWindow(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Sync(nil, t0)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 0, 7000)}, t0.Add(10*time.Second))
	tb.Sync([]ct.Event{watch(1, 0, 7000)}, t0.Add(4*time.Minute))
	rows, _ := tb.Query(Query{Live: true, Window: 3 * time.Minute})
	if len(rows) != 1 || rows[0].WinDown != 0 {
		t.Fatalf("bytes from 12:00 read at 12:04 over 3 min: %+v", rows)
	}
	tb.Sync([]ct.Event{watch(1, 0, 9000)}, t0.Add(4*time.Minute+30*time.Second))
	tb.Sync([]ct.Event{watch(1, 0, 9000)}, t0.Add(20*time.Minute))
	rows, _ = tb.Query(Query{Live: true, Window: 10 * time.Minute})
	if len(rows) != 1 || rows[0].WinDown != 0 {
		t.Fatalf("a flow quiet for 15 min over 10 min: %+v", rows)
	}
}

// A slot is reused every 11 minutes: what it held then is cleared when the
// flow next moves, so an old minute is never read as a recent one.
func TestASlotReusedLaterHoldsNothingOld(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Sync(nil, t0)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 0, 4000)}, t0.Add(10*time.Second))                // minute 0
	tb.Sync([]ct.Event{watch(1, 0, 4100)}, t0.Add(10*time.Minute+10*time.Second)) // minute 10
	tb.Sync([]ct.Event{watch(1, 0, 4150)}, t0.Add(12*time.Minute+10*time.Second)) // minute 12
	rows, _ := tb.Query(Query{Live: true, Window: 3 * time.Minute})
	if len(rows) != 1 || rows[0].WinDown != 150 {
		t.Fatalf("over 3 min at 12:12 %+v, want 150: minute 0's 4000 must not come back as minute 11", rows)
	}
}

// A reused key is a new flow: its window starts empty, never with what the
// connection before it moved.
func TestAReusedKeyStartsItsWindowEmpty(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Sync(nil, t0)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 0, 50000)}, t0.Add(10*time.Second))
	// The counter falls: the kernel reused the id for a new connection.
	tb.Sync([]ct.Event{watch(1, 0, 30)}, t0.Add(20*time.Second))
	tb.Sync([]ct.Event{watch(1, 0, 70)}, t0.Add(30*time.Second))
	rows, _ := tb.Query(Query{Live: true, Window: 3 * time.Minute})
	if len(rows) != 1 || rows[0].WinDown > 70 {
		t.Fatalf("the new connection on the old key %+v: its window must hold only its own bytes", rows)
	}
}
