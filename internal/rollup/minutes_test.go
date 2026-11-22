package rollup

import (
	"strings"
	"testing"
	"time"
)

// The minutes are kept in RAM across a restart of netryx: written at a stop,
// read at the start, so the lists' last hour and the Now columns go on where
// they were; only a reboot of the router empties them.
func TestTheMinutesSurviveARestart(t *testing.T) {
	a := newTestStore(Options{}, later)
	feed(a, delta(base.Add(40*time.Second), laptop, "netflix", "GB", 0, 7))
	feed(a, delta(base.Add(3*time.Minute), laptop, "netflix", "GB", 0, 300))
	feed(a, delta(base.Add(5*time.Minute+20*time.Second), phone, "youtube", "US", 10, 500))
	b, err := a.MinutesJSON()
	if err != nil {
		t.Fatal(err)
	}

	// The new process starts a minute later.
	n := newTestStore(Options{}, later)
	n.now = func() time.Time { return base.Add(6*time.Minute + 30*time.Second) }
	if err := n.LoadMinutes(b); err != nil {
		t.Fatal(err)
	}
	a.now = n.now
	want := a.Sparks(Filter{}, "mac", 10*time.Minute, 2*time.Minute)
	got := n.Sparks(Filter{}, "mac", 10*time.Minute, 2*time.Minute)
	if got.Known != want.Known || len(got.Keys) != len(want.Keys) {
		t.Fatalf("known %d keys %v, want %d %v", got.Known, got.Keys, want.Known, want.Keys)
	}
	for k, w := range want.Keys {
		for i := range w {
			if got.Keys[k][i] != w[i] {
				t.Fatalf("%s: %v, want %v", k, got.Keys[k], w)
			}
		}
	}
	rows, covered := n.Recent(Filter{}, "mac", 10*time.Minute)
	if r := rowsByKey(rows); r[phone].Bytes.Down != 500 || covered != 5*time.Minute+50*time.Second {
		t.Errorf("recent after a restart: %+v over %v", rows, covered)
	}
	// The hours are saved on their own: loading the minutes adds nothing to them.
	if top := n.Top(Filter{}, "mac", base.Add(-2*time.Hour), base.Add(time.Hour), 0); len(top) != 0 {
		t.Errorf("the minutes leaked into the hours: %+v", top)
	}
}

// What is read back is held to the new store's bounds and clock: minutes the
// ring no longer reaches, or later than the clock, are left out; a store that
// has taken a delta takes no file; a damaged file is refused.
func TestLoadedMinutesKeepToTheRingAndTheClock(t *testing.T) {
	a := newTestStore(Options{}, later)
	feed(a, delta(base.Add(1*time.Minute), phone, "youtube", "US", 0, 100))
	feed(a, delta(base.Add(30*time.Minute), phone, "youtube", "US", 0, 200))
	b, _ := a.MinutesJSON()

	// Two hours later both minutes are older than the kept hour: gone.
	n := newTestStore(Options{}, later)
	n.now = func() time.Time { return base.Add(150 * time.Minute) }
	if err := n.LoadMinutes(b); err != nil {
		t.Fatal(err)
	}
	if rows, _ := n.Recent(Filter{}, "mac", 10*time.Minute); len(rows) != 0 {
		t.Errorf("minutes outside the ring were loaded: %+v", rows)
	}
	// A clock set back: the minutes ahead of it are not loaded.
	m := newTestStore(Options{}, later)
	m.now = func() time.Time { return base.Add(10 * time.Minute) }
	if err := m.LoadMinutes(b); err != nil {
		t.Fatal(err)
	}
	// Once the clock reaches it, a minute that was ahead of it would show
	// bytes nobody counted at that time.
	m.now = func() time.Time { return base.Add(31 * time.Minute) }
	if rows, _ := m.Recent(Filter{}, "mac", 3*time.Minute); len(rows) != 0 {
		t.Errorf("a minute ahead of the clock was loaded: %+v", rows)
	}
	// After a delta, a file is refused rather than mixed in.
	busy := newTestStore(Options{}, later)
	feed(busy, delta(base, laptop, "netflix", "GB", 0, 1))
	if err := busy.LoadMinutes(b); err == nil {
		t.Error("a store that has begun took a file")
	}
	for _, bad := range []string{"", "{", `{"v":9,"minutes":[]}`, strings.Repeat("x", 10)} {
		if err := newTestStore(Options{}, later).LoadMinutes([]byte(bad)); err == nil {
			t.Errorf("%q was loaded", bad)
		}
	}
}

// A minute read back holds at most the keys a minute may hold.
func TestALoadedMinuteIsHeldToTheKeyCap(t *testing.T) {
	a := newTestStore(Options{}, later)
	for i := 0; i < 3000; i++ {
		feed(a, delta(base.Add(time.Minute), "aa:bb:cc:dd:"+hex2(i/256)+":"+hex2(i%256), "youtube", "US", 0, 1))
	}
	b, _ := a.MinutesJSON()
	n := newTestStore(Options{}, later)
	n.now = func() time.Time { return base.Add(2 * time.Minute) }
	if err := n.LoadMinutes(b); err != nil {
		t.Fatal(err)
	}
	if got := len(n.mins[n.mr.index(base.Add(time.Minute))].Keys); got > 2001 {
		t.Errorf("a loaded minute holds %d keys", got)
	}
	// The same as the store that wrote it, and no byte lost.
	a.now = n.now
	want, _ := a.Recent(Filter{}, "app", 10*time.Minute)
	rows, _ := n.Recent(Filter{}, "app", 10*time.Minute)
	var sum uint64
	for _, r := range rows {
		sum += r.Bytes.Down
	}
	if sum != 3000 || len(rows) != len(want) {
		t.Errorf("after the load %+v, before %+v", rows, want)
	}
}

func hex2(i int) string { return string("0123456789abcdef"[i/16]) + string("0123456789abcdef"[i%16]) }
