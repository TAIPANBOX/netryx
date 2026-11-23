package daily

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"netryx/internal/flows"
	"netryx/internal/packfile"
	"netryx/internal/rollup"
)

var day0 = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

const mac1, mac2 = "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"

func hw(s string) [6]byte {
	h, err := net.ParseMAC(s)
	if err != nil || len(h) != 6 {
		panic(s)
	}
	return [6]byte(h)
}

// fakeHours stands in for the hour ring: the days it answers, as given.
type fakeHours map[int64]rollup.DayHours

func (f fakeHours) DayHours(d time.Time) (rollup.DayHours, bool) {
	h, ok := f[d.Unix()]
	return h, ok
}

// fullDay is a day the router counted whole: mac1 moves (h+1)*scale bytes
// down and a tenth of that up in hour h.
func fullDay(d time.Time, scale uint64) rollup.DayHours {
	v := &[2][24]uint64{}
	for h := 0; h < 24; h++ {
		v[0][h] = uint64(h+1) * scale
		v[1][h] = uint64(h+1) * scale / 10
	}
	return rollup.DayHours{Day: d, Mask: 1<<24 - 1, By: map[string]*[2][24]uint64{mac1: v, "router": {}, "other": {}}}
}

func noCounters() Counters { return Counters{} }

func open(t *testing.T, dir string, now time.Time) *Store {
	t.Helper()
	s, err := Open(dir, now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

// A day is written once, by the first look after it has closed (a minute
// after its end), and a file that is there is never written again, whatever
// the ring says later.
func TestADayIsWrittenOnceWhenItClosesAndNeverRewritten(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, day0)
	src := fakeHours{day0.Unix(): fullDay(day0, 1<<20)}
	path := filepath.Join(dir, Name(day0))

	if n, err := s.CloseDays(src, noCounters, day0.Add(24*time.Hour+30*time.Second)); n != 0 || err != nil {
		t.Fatalf("half a minute after its end the day is still open: wrote %d (%v)", n, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("an open day is on flash")
	}
	if n, err := s.CloseDays(src, noCounters, day0.Add(24*time.Hour+2*time.Minute)); n != 1 || err != nil {
		t.Fatalf("the first look after it closed writes it: wrote %d (%v)", n, err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the closed day is at %s: %v", path, err)
	}
	got, ok := s.Get(day0)
	if !ok || got.Mask != 1<<24-1 || len(got.Devices) != 1 || got.Devices[0].MAC != hw(mac1) || got.Devices[0].Down[23] != 24<<10 {
		t.Fatalf("the written day in memory: %+v %v", got, ok)
	}

	// The ring changes (a late merge, a forged file): the day stays as written.
	src[day0.Unix()] = fullDay(day0, 7<<20)
	if n, err := s.CloseDays(src, noCounters, day0.Add(25*time.Hour+2*time.Minute)); n != 0 || err != nil {
		t.Fatalf("a later look writes the day again: wrote %d (%v)", n, err)
	}
	if ok, err := s.Write(FromHours(src[day0.Unix()])); ok || err != nil {
		t.Fatalf("Write over a written day: %v %v", ok, err)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(first, again) {
		t.Fatalf("the day's file was rewritten")
	}
	if g, _ := s.Get(day0); g.Devices[0].Down[23] != 24<<10 {
		t.Fatalf("memory took the rewrite: %d", g.Devices[0].Down[23])
	}
	// And a new start reads the same day back.
	s2 := open(t, dir, day0.Add(26*time.Hour))
	g2, ok := s2.Get(day0)
	if !ok || !reflect.DeepEqual(g2, got) {
		t.Fatalf("read back after a restart: %+v, want %+v", g2, got)
	}
}

// The service is stopped at 10:40 and started again at 10:45: the hour ring
// takes the hours back from the history files, so the day's file holds every
// hour, the restarted one with both of its halves.
func TestADayWrittenAfterAMidDayRestartHoldsEveryHourTheRingHolds(t *testing.T) {
	hist, dir := t.TempDir(), t.TempDir()
	clk := day0
	now := func() time.Time { return clk }
	add := func(s *rollup.Store, at time.Time, down uint64) {
		clk = at
		s.Add(flows.Delta{At: at, MAC: mac1, App: "web", CC: "GB", Down: down, Up: down / 4, First: true})
	}
	s1 := rollup.NewStore(rollup.Options{Now: now})
	for h := 0; h <= 10; h++ {
		add(s1, day0.Add(time.Duration(h)*time.Hour+20*time.Minute), uint64(h+1)<<20)
		if h < 10 {
			clk = day0.Add(time.Duration(h+1)*time.Hour + 5*time.Minute)
			if _, err := rollup.SaveClosed(s1, hist); err != nil {
				t.Fatal(err)
			}
		}
	}
	clk = day0.Add(10*time.Hour + 40*time.Minute)
	if err := rollup.SaveOpen(s1, hist); err != nil {
		t.Fatal(err)
	}

	clk = day0.Add(10*time.Hour + 45*time.Minute)
	s2 := rollup.NewStore(rollup.Options{Now: now})
	if err := rollup.LoadDir(s2, hist); err != nil {
		t.Fatal(err)
	}
	add(s2, day0.Add(10*time.Hour+50*time.Minute), 5<<20) // the second half of 10:00
	for h := 11; h < 24; h++ {
		add(s2, day0.Add(time.Duration(h)*time.Hour+20*time.Minute), uint64(h+1)<<20)
	}
	clk = day0.Add(24*time.Hour + 2*time.Minute)
	if _, err := rollup.SaveClosed(s2, hist); err != nil {
		t.Fatal(err)
	}
	d := open(t, dir, clk)
	if n, err := d.CloseDays(s2, noCounters, clk); n != 1 || err != nil {
		t.Fatalf("wrote %d (%v)", n, err)
	}
	got, ok := d.Get(day0)
	if !ok || got.Mask != 1<<24-1 {
		t.Fatalf("the restart cost the day hours: mask %024b", got.Mask)
	}
	for h := 0; h < 24; h++ {
		want := uint32(h+1) << 10
		if h == 10 {
			want += 5 << 10
		}
		if got.Devices[0].Down[h] != want {
			t.Errorf("hour %d: %d KiB down, want %d", h, got.Devices[0].Down[h], want)
		}
	}
}

// Hours the router was not counting are not in the mask, so the reader sees
// them as unknown; an hour the router counted in which a device moved
// nothing is a counted zero. A day with no file is unknown as a whole.
func TestHoursTheRouterWasOffAreNotInTheMask(t *testing.T) {
	dir := t.TempDir()
	clk := day0
	s := rollup.NewStore(rollup.Options{Now: func() time.Time { return clk }})
	for h := 0; h < 24; h++ {
		if h >= 8 && h < 15 {
			continue // switched off from 08:00 to 15:00
		}
		clk = day0.Add(time.Duration(h)*time.Hour + 30*time.Minute)
		s.Add(flows.Delta{At: clk, MAC: mac2, App: "web", CC: "GB", Down: 1 << 20, First: true})
		if h != 3 {
			s.Add(flows.Delta{At: clk, MAC: mac1, App: "web", CC: "GB", Down: 2 << 20, First: true})
		}
	}
	clk = day0.Add(24*time.Hour + 2*time.Minute)
	d := open(t, dir, clk)
	if n, err := d.CloseDays(s, noCounters, clk); n != 1 || err != nil {
		t.Fatalf("wrote %d (%v)", n, err)
	}
	var want uint32 = 1<<24 - 1
	for h := 8; h < 15; h++ {
		want &^= 1 << h
	}
	days := d.Days(hw(mac1), clk)
	if len(days) != Keep {
		t.Fatalf("%d days, want %d", len(days), Keep)
	}
	last := days[len(days)-1]
	if !last.Date.Equal(day0) || !last.Known || last.Mask != want {
		t.Fatalf("the day: %v known %v mask %024b, want %024b", last.Date, last.Known, last.Mask, want)
	}
	if last.Down[3] != 0 || last.Mask&(1<<3) == 0 {
		t.Errorf("a quiet hour the router counted is a counted zero: %d, mask bit %v", last.Down[3], last.Mask&(1<<3) != 0)
	}
	if last.Down[4] != 2<<20 {
		t.Errorf("hour 4: %d bytes, want %d", last.Down[4], 2<<20)
	}
	for _, x := range days[:len(days)-1] {
		if x.Known || x.Mask != 0 {
			t.Errorf("%v has no file and reads as known", x.Date)
		}
	}
	if !days[0].Date.Equal(day0.Add(-29 * 24 * time.Hour)) {
		t.Errorf("the first day is %v, want 29 days before the last", days[0].Date)
	}
	// A device the day does not hold reads as counted zeros, not unknown.
	other := d.Days(hw("aa:bb:cc:dd:ee:99"), clk)
	if o := other[len(other)-1]; !o.Known || o.Mask != want || o.Down != [24]uint64{} {
		t.Errorf("a device the router did not see that day: %+v", o)
	}
}

// One bad file never costs the others: a damaged, cut-short, other-version
// or misnamed file is skipped and named, and every other file loads.
func TestADamagedFileIsSkippedAndNamedAndTheRestLoad(t *testing.T) {
	dir := t.TempDir()
	now := day0.Add(30 * time.Hour)
	s := open(t, dir, now)
	var dates []time.Time
	for i := 5; i >= 0; i-- {
		d := day0.Add(-time.Duration(i) * 24 * time.Hour)
		dates = append(dates, d)
		if ok, err := s.Write(FromHours(fullDay(d, uint64(i+1)<<20))); !ok || err != nil {
			t.Fatalf("write %v: %v %v", d, ok, err)
		}
	}
	path := func(i int) string { return filepath.Join(dir, Name(dates[i])) }
	read := func(i int) []byte {
		b, err := os.ReadFile(path(i))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// 0: one byte flipped in the middle.
	b := read(0)
	b[len(b)/2] ^= 1
	os.WriteFile(path(0), b, 0o644)
	// 1: cut short.
	b = read(1)
	os.WriteFile(path(1), b[:len(b)/2], 0o644)
	// 2: a version this does not read, with a good checksum.
	b = read(2)
	os.WriteFile(path(2), packfile.Seal(Magic, Version+1, b[12:len(b)-32]), 0o644)
	// 3: holds day 4, named for day 3.
	os.WriteFile(path(3), read(4), 0o644)
	// Names that are not a day's file are not ours, and not read.
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "2026-11-1.day"), read(5), 0o644)

	s2, err := Open(dir, now)
	if err == nil {
		t.Fatal("four bad files and no error")
	}
	for i := 0; i < 4; i++ {
		if !strings.Contains(err.Error(), Name(dates[i])) {
			t.Errorf("the error does not name %s: %v", Name(dates[i]), err)
		}
	}
	if strings.Contains(err.Error(), "notes.txt") || strings.Contains(err.Error(), "2026-11-1.day") {
		t.Errorf("a file that is not ours is named: %v", err)
	}
	for i, d := range dates {
		_, ok := s2.Get(d)
		if want := i >= 4; ok != want {
			t.Errorf("%s loaded %v, want %v", Name(d), ok, want)
		}
	}
	days := s2.Days(hw(mac1), now)
	for _, x := range days {
		for i := 0; i < 4; i++ {
			if x.Date.Equal(dates[i]) && x.Known {
				t.Errorf("the bad file of %v reads as a known day", x.Date)
			}
		}
	}
	if s2.Len() != 2 {
		t.Errorf("%d days in memory, want 2", s2.Len())
	}
	// And the bad files are left alone: a damaged day is never written over.
	src := fakeHours{}
	for _, d := range dates {
		src[d.Unix()] = fullDay(d, 1)
	}
	if n, err := s2.CloseDays(src, noCounters, now); n != 0 || err != nil {
		t.Errorf("a look rewrote %d files (%v)", n, err)
	}
}

// A file is checked whole before it is believed: its checksum can be right
// and its content still impossible.
func TestAFileThatCannotBeADayIsRefused(t *testing.T) {
	good := FromHours(fullDay(day0, 1<<20))
	good.Devices = append(good.Devices, Device{MAC: hw(mac2)})
	body := encodeBody(good)
	if _, err := Decode(packfile.Seal(Magic, Version, body), day0); err != nil {
		t.Fatalf("the good day is refused: %v", err)
	}
	reseal := func(f func([]byte) []byte) []byte {
		return packfile.Seal(Magic, Version, f(append([]byte(nil), body...)))
	}
	forged := func(f func(*Day)) []byte {
		d := FromHours(fullDay(day0, 1<<20))
		d.Devices = append(d.Devices, Device{MAC: hw(mac2)})
		f(&d)
		return packfile.Seal(Magic, Version, encodeBody(d))
	}
	for name, b := range map[string][]byte{
		"another unit":         reseal(func(b []byte) []byte { binary.LittleEndian.PutUint32(b, 1000); return b }),
		"cut before its end":   reseal(func(b []byte) []byte { return b[:len(b)-3] }),
		"bytes after its end":  reseal(func(b []byte) []byte { return append(b, 0) }),
		"a 25th hour":          forged(func(d *Day) { d.Mask |= 1 << 24 }),
		"devices out of order": forged(func(d *Day) { d.Devices[0], d.Devices[1] = d.Devices[1], d.Devices[0] }),
		"a device twice":       forged(func(d *Day) { d.Devices[1] = d.Devices[0] }),
		"bytes in an hour the router was off": forged(func(d *Day) {
			d.Mask &^= 1 << 5
		}),
		"too many devices": forged(func(d *Day) {
			d.Devices = make([]Device, MaxDevices+1)
			for i := range d.Devices {
				binary.BigEndian.PutUint32(d.Devices[i].MAC[2:], uint32(i))
			}
		}),
		"counters out of order": forged(func(d *Day) {
			d.CountersAt = day0.Add(24 * time.Hour)
			d.Hits = []Counter{{"b", 1}, {"a", 2}}
		}),
		"an empty interface name": forged(func(d *Day) {
			d.CountersAt = day0.Add(24 * time.Hour)
			d.Ifaces = []Iface{{"", 1, 2}}
		}),
		"counters with no time they were read": forged(func(d *Day) { d.Hits = []Counter{{"a", 1}} }),
	} {
		if _, err := Decode(b, day0); err == nil {
			t.Errorf("%s: believed", name)
		}
	}
	if _, err := Decode(packfile.Seal(Magic, Version, body), day0.Add(24*time.Hour)); err == nil {
		t.Errorf("a day named for another date: believed")
	}
}

// Files of days more than 30 days back are deleted, and so is a write a
// power cut stopped; memory holds what the files hold. A clock that was
// never set deletes nothing.
func TestFilesPastThirtyDaysAreDeleted(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 23, 0, 30, 0, 0, time.UTC)
	s := open(t, dir, now)
	for d := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); d.Before(now.Truncate(24 * time.Hour)); d = d.Add(24 * time.Hour) {
		if ok, err := s.Write(FromHours(fullDay(d, 1<<10))); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	stale := filepath.Join(dir, "."+Name(day0)+".123456")
	os.WriteFile(stale, []byte("half"), 0o644)
	if err := s.Prune(time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if n, _ := filepath.Glob(filepath.Join(dir, "*"+Ext)); len(n) != 53 {
		t.Fatalf("a clock never set deleted files: %d left of 53", len(n))
	}
	if err := s.Prune(now); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"+Ext))
	if len(files) != Keep {
		t.Fatalf("%d files kept, want %d", len(files), Keep)
	}
	if filepath.Base(files[0]) != "2026-10-24"+Ext || filepath.Base(files[len(files)-1]) != "2026-11-22"+Ext {
		t.Errorf("kept %s to %s, want 2026-10-24 to 2026-11-22", filepath.Base(files[0]), filepath.Base(files[len(files)-1]))
	}
	if _, err := os.Stat(stale); err == nil {
		t.Errorf("a stopped write is still there")
	}
	if s.Len() != Keep {
		t.Errorf("%d days in memory, want %d", s.Len(), Keep)
	}
	if _, ok := s.Get(time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)); ok {
		t.Errorf("a deleted day is still in memory")
	}
}

// What every rule's counter and every interface's counters said at the close
// come back as written; only the day that has just closed carries them, and
// a day written late for a router that was off at its close carries none.
func TestRuleHitsAndInterfaceCountersRoundTrip(t *testing.T) {
	d := FromHours(fullDay(day0, 1<<20))
	d.CountersAt = day0.Add(24*time.Hour + 2*time.Minute)
	d.Hits = []Counter{{"f_dot", 3}, {"iso", 0}, {"kids_tiktok", 12345678901234}}
	d.Ifaces = []Iface{{"lan", 1 << 50, 77}, {"wan", math.MaxUint64, 1}, {"wwan", 0, 0}}
	b, err := Encode(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, day0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, d) {
		t.Fatalf("read back %+v\nwant %+v", got, d)
	}

	dir := t.TempDir()
	now := day0.Add(24*time.Hour + 5*time.Minute)
	s := open(t, dir, now)
	prev := day0.Add(-24 * time.Hour)
	src := fakeHours{day0.Unix(): fullDay(day0, 1), prev.Unix(): fullDay(prev, 1)}
	asked := 0
	counters := func() Counters {
		asked++
		return Counters{At: now, Hits: map[string]uint64{"r1": 7, "f_dot": 2}, Ifaces: map[string][2]uint64{"wan": {100, 200}, "lan": {5, 6}}}
	}
	if n, err := s.CloseDays(src, counters, now); n != 2 || err != nil {
		t.Fatalf("wrote %d (%v)", n, err)
	}
	if asked != 1 {
		t.Errorf("the counters were read %d times, want once", asked)
	}
	s2 := open(t, dir, now)
	y, _ := s2.Get(day0)
	if !y.CountersAt.Equal(now) || !reflect.DeepEqual(y.Hits, []Counter{{"f_dot", 2}, {"r1", 7}}) ||
		!reflect.DeepEqual(y.Ifaces, []Iface{{"lan", 5, 6}, {"wan", 100, 200}}) {
		t.Errorf("yesterday's counters: %v %+v %+v", y.CountersAt, y.Hits, y.Ifaces)
	}
	o, _ := s2.Get(prev)
	if !o.CountersAt.IsZero() || len(o.Hits) != 0 || len(o.Ifaces) != 0 {
		t.Errorf("a day written late carries counters read after its close: %v %+v %+v", o.CountersAt, o.Hits, o.Ifaces)
	}
}

// The unit is a KiB: more than 4 GiB in an hour is stored exactly, a byte is
// never rounded down to a quiet hour, and an hour past what the unit holds
// is held at the top rather than wrapping to a small number.
func TestAnHourOverFourGiBDoesNotWrap(t *testing.T) {
	for in, want := range map[uint64]uint32{0: 0, 1: 1, 1024: 1, 1025: 2, 5 << 30: 5 << 20, 1 << 50: math.MaxUint32} {
		if got := Units(in); got != want {
			t.Errorf("Units(%d) = %d, want %d", in, got, want)
		}
	}
	h := fullDay(day0, 1)
	h.By[mac1][0][0] = 5 << 30
	h.By[mac1][1][1] = 1 << 45
	dir := t.TempDir()
	now := day0.Add(25 * time.Hour)
	s := open(t, dir, now)
	if ok, err := s.Write(FromHours(h)); !ok || err != nil {
		t.Fatal(ok, err)
	}
	s2 := open(t, dir, now)
	days := s2.Days(hw(mac1), now)
	last := days[len(days)-1]
	if last.Down[0] != 5<<30 {
		t.Errorf("5 GiB in an hour reads as %d bytes", last.Down[0])
	}
	if last.Up[1] != uint64(math.MaxUint32)*Unit {
		t.Errorf("32 TiB in an hour reads as %d bytes, want the unit's top", last.Up[1])
	}
}

// Only hardware addresses are kept: the router's own traffic, the catch-all
// and an address nobody named are not devices. Past 512 devices the busiest
// are kept and the rest are counted as left out.
func TestADayKeepsOnlyHardwareAddressesAndAtMostTheBusiest512(t *testing.T) {
	h := rollup.DayHours{Day: day0, Mask: 1, By: map[string]*[2][24]uint64{
		"router": {{1 << 30}}, "other": {{1 << 30}}, "192.168.1.5": {{1 << 30}},
		"AA:BB:CC:DD:EE:0F": {{3 << 20}}, "00:00:00:00:fe:80:00:00:00:00:00:00:00:00:00:00:00:00:00:01": {{1 << 30}},
	}}
	for i := 0; i < 600; i++ {
		h.By[fmt.Sprintf("02:00:00:00:%02x:%02x", i>>8, i&0xff)] = &[2][24]uint64{{uint64(i+1) << 10}}
	}
	d := FromHours(h)
	if len(d.Devices) != MaxDevices || d.LeftOut != 601-MaxDevices {
		t.Fatalf("%d devices, %d left out; want %d and %d", len(d.Devices), d.LeftOut, MaxDevices, 601-MaxDevices)
	}
	for i := 1; i < len(d.Devices); i++ {
		if bytes.Compare(d.Devices[i-1].MAC[:], d.Devices[i].MAC[:]) >= 0 {
			t.Fatalf("devices are not in order of address at %d", i)
		}
	}
	var small, big bool
	for _, x := range d.Devices {
		if x.MAC == hw("aa:bb:cc:dd:ee:0f") {
			big = true
		}
		if x.MAC == hw("02:00:00:00:00:00") {
			small = true
		}
	}
	if !big || small {
		t.Errorf("the busiest is kept %v, the quietest is left out %v", big, !small)
	}
	if _, err := Encode(d); err != nil {
		t.Errorf("a full day does not encode: %v", err)
	}
}
