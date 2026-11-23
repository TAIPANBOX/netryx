// Package daily keeps one small file per closed UTC day, so the console can
// compare a device with its own last 30 days: for every hardware address the
// house saw, 24 hourly totals down and up; which hours the router was
// counting at all; and, at the day's close, every rule's hit counter and every
// network interface's byte counters. No domain and nothing per destination.
//
// A file is <dir>/YYYY-MM-DD.day, a packfile frame (magic NRXDAILY, version,
// SHA-256 trailer) around this body, little-endian:
//
//	unit        uint32  bytes per stored unit: 1024 (a KiB)
//	date        uint32  the day, in days since 1970-01-01 UTC
//	mask        uint32  bit h set: the router was counting in hour h (bits 24-31 zero)
//	leftOut     uint32  devices past MaxDevices, the least busy, not in the file
//	countersAt  int64   unix seconds the counters were read; 0: they were not
//	devices     uint32  count, then per device in order of address:
//	              mac [6]byte, down [24]uint32, up [24]uint32 (units, rounded up)
//	hits        uint32  count, then per rule in order of name:
//	              len uint8 (1-64), name, packets uint64
//	ifaces      uint32  count, then per interface in order of name:
//	              len uint8 (1-64), name, rx uint64, tx uint64 (bytes)
//
// A file is written once, after its day has closed, through a temporary file
// renamed over its name, and never written again; files of days more than
// Keep days back are deleted. A damaged, cut-short, misnamed or other-version
// file is skipped and named, and its day reads as unknown, never as zero.
package daily

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"netryx/internal/packfile"
	"netryx/internal/rollup"
)

const (
	// Magic and Version are the frame of a day's file.
	Magic   = "NRXDAILY"
	Version = 1
	// Unit is the bytes of one stored unit: a KiB. A uint32 of KiB holds
	// 4 TiB in an hour, a 10 Gbit/s line flat out for 53 minutes; an hour
	// past it is held at the top.
	Unit = 1024
	// Keep is how many days are kept: the files of days further back are
	// deleted.
	Keep = 30
	// MaxDevices bounds the devices of one day, as the live ring bounds its
	// own: past it the busiest are kept and the rest counted in LeftOut.
	MaxDevices = 512
	// MaxCounters and MaxIfaces bound the counters of one day.
	MaxCounters = 1024
	MaxIfaces   = 256
	// Ext is the extension of a day's file.
	Ext = ".day"

	maxName   = 64
	devSize   = 6 + 2*24*4
	fixedSize = 4*4 + 8
	// maxFile bounds a file read back: the largest day this writes is about
	// 200 KB.
	maxFile = 1 << 20
	day     = 24 * time.Hour
	// closeLag is how long after its end a day counts as closed, as the
	// rollup closes its buckets.
	closeLag = time.Minute
	// ringDays is how far back the hour ring can still answer a whole day.
	ringDays = 7
)

// clockFloor is the earliest time a router's clock is believed set: before
// it nothing is written or deleted, since the days would be 1970's.
var clockFloor = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Device is one hardware address's day, in units of Unit.
type Device struct {
	MAC      [6]byte
	Down, Up [24]uint32
}

// Counter is one rule's hit counter at the close, by rule id.
type Counter struct {
	Name  string
	Value uint64
}

// Iface is one network interface's byte counters at the close, by name.
type Iface struct {
	Name   string
	Rx, Tx uint64
}

// Day is one closed UTC day.
type Day struct {
	Date time.Time
	// Mask has bit h set when the router was counting in hour h.
	Mask    uint32
	Devices []Device
	// LeftOut counts devices past MaxDevices that the file does not hold.
	LeftOut uint32
	// CountersAt is when Hits and Ifaces were read; zero when they were not
	// (a day written after the router missed its close).
	CountersAt time.Time
	Hits       []Counter
	Ifaces     []Iface
}

// Counters is what the console reads at a day's close.
type Counters struct {
	At     time.Time
	Hits   map[string]uint64
	Ifaces map[string][2]uint64 // rx, tx
}

// Units is b in units of Unit, rounded up so a byte is never a quiet hour, and
// held at the top past what a uint32 holds rather than wrapping.
func Units(b uint64) uint32 {
	u := b / Unit
	if b%Unit != 0 {
		u++
	}
	if u > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(u)
}

// Name is the file name of the day starting at date.
func Name(date time.Time) string { return date.UTC().Format("2006-01-02") + Ext }

// parseName is the day a file name names: only the exact name Name writes.
func parseName(n string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", strings.TrimSuffix(n, Ext))
	return t, err == nil && Name(t) == n
}

func dayNumber(t time.Time) uint32 { return uint32(t.Unix() / int64(day/time.Second)) }

// FromHours makes a day of the hour ring's: hardware addresses only (the
// router's own traffic, the catch-all and unnamed addresses are no device),
// the busiest MaxDevices of them, in order of address.
func FromHours(h rollup.DayHours) Day {
	d := Day{Date: h.Day.UTC().Truncate(day), Mask: h.Mask & (1<<24 - 1)}
	type cand struct {
		mac   [6]byte
		v     [2][24]uint64
		total uint64
	}
	by := map[[6]byte]*cand{}
	for k, v := range h.By {
		hw, err := net.ParseMAC(k)
		if err != nil || len(hw) != 6 || v == nil {
			continue
		}
		mac := [6]byte(hw)
		c := by[mac]
		if c == nil {
			c = &cand{mac: mac}
			by[mac] = c
		}
		for i := 0; i < 2; i++ {
			for x := 0; x < 24; x++ {
				c.v[i][x] = sat(c.v[i][x], v[i][x])
				c.total = sat(c.total, v[i][x])
			}
		}
	}
	cs := make([]*cand, 0, len(by))
	for _, c := range by {
		cs = append(cs, c)
	}
	if len(cs) > MaxDevices {
		sort.Slice(cs, func(i, j int) bool {
			if cs[i].total != cs[j].total {
				return cs[i].total > cs[j].total
			}
			return bytes.Compare(cs[i].mac[:], cs[j].mac[:]) < 0
		})
		d.LeftOut = uint32(len(cs) - MaxDevices)
		cs = cs[:MaxDevices]
	}
	sort.Slice(cs, func(i, j int) bool { return bytes.Compare(cs[i].mac[:], cs[j].mac[:]) < 0 })
	for _, c := range cs {
		dev := Device{MAC: c.mac}
		for x := 0; x < 24; x++ {
			if d.Mask&(1<<x) == 0 {
				continue // an hour the router was not counting holds nothing
			}
			dev.Down[x], dev.Up[x] = Units(c.v[0][x]), Units(c.v[1][x])
		}
		d.Devices = append(d.Devices, dev)
	}
	return d
}

func sat(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}

// check says why d cannot be a day's file, or nil. Encode and Decode share it,
// so nothing is written that would not be read back.
func check(d Day) error {
	if !d.Date.Equal(d.Date.Truncate(day)) || d.Date.Before(time.Unix(0, 0)) {
		return fmt.Errorf("the date %v is not a UTC midnight", d.Date)
	}
	if d.Mask >= 1<<24 {
		return fmt.Errorf("the hours mask %#x names an hour past 23", d.Mask)
	}
	if len(d.Devices) > MaxDevices {
		return fmt.Errorf("%d devices, at most %d", len(d.Devices), MaxDevices)
	}
	if d.LeftOut > 0 && len(d.Devices) != MaxDevices {
		return fmt.Errorf("%d devices left out of %d, fewer than the %d kept", d.LeftOut, len(d.Devices), MaxDevices)
	}
	for i, dev := range d.Devices {
		if i > 0 && bytes.Compare(d.Devices[i-1].MAC[:], dev.MAC[:]) >= 0 {
			return fmt.Errorf("device %d is out of order or repeated", i)
		}
		for x := 0; x < 24; x++ {
			if d.Mask&(1<<x) == 0 && (dev.Down[x] != 0 || dev.Up[x] != 0) {
				return fmt.Errorf("device %d moved bytes in hour %d, which the router was not counting", i, x)
			}
		}
	}
	if d.CountersAt.IsZero() {
		if len(d.Hits) > 0 || len(d.Ifaces) > 0 {
			return errors.New("counters with no time they were read")
		}
	} else if d.CountersAt.Before(d.Date.Add(day)) {
		return fmt.Errorf("counters read at %v, before the day closed", d.CountersAt)
	}
	if len(d.Hits) > MaxCounters || len(d.Ifaces) > MaxIfaces {
		return fmt.Errorf("%d counters and %d interfaces, at most %d and %d", len(d.Hits), len(d.Ifaces), MaxCounters, MaxIfaces)
	}
	names := func(what string, n int, name func(int) string) error {
		for i := 0; i < n; i++ {
			s := name(i)
			if len(s) == 0 || len(s) > maxName {
				return fmt.Errorf("%s %d has a name of %d bytes", what, i, len(s))
			}
			if i > 0 && name(i-1) >= s {
				return fmt.Errorf("%s %d is out of order or repeated", what, i)
			}
		}
		return nil
	}
	if err := names("counter", len(d.Hits), func(i int) string { return d.Hits[i].Name }); err != nil {
		return err
	}
	return names("interface", len(d.Ifaces), func(i int) string { return d.Ifaces[i].Name })
}

// encodeBody writes d as a body, whatever it holds.
func encodeBody(d Day) []byte {
	le := binary.LittleEndian
	b := make([]byte, 0, fixedSize+4+len(d.Devices)*devSize+8+len(d.Hits)*(9+16)+len(d.Ifaces)*(17+16))
	b = le.AppendUint32(b, Unit)
	b = le.AppendUint32(b, dayNumber(d.Date))
	b = le.AppendUint32(b, d.Mask)
	b = le.AppendUint32(b, d.LeftOut)
	var at int64
	if !d.CountersAt.IsZero() {
		at = d.CountersAt.Unix()
	}
	b = le.AppendUint64(b, uint64(at))
	b = le.AppendUint32(b, uint32(len(d.Devices)))
	for _, dev := range d.Devices {
		b = append(b, dev.MAC[:]...)
		for _, v := range dev.Down {
			b = le.AppendUint32(b, v)
		}
		for _, v := range dev.Up {
			b = le.AppendUint32(b, v)
		}
	}
	b = le.AppendUint32(b, uint32(len(d.Hits)))
	for _, h := range d.Hits {
		b = append(b, byte(len(h.Name)))
		b = append(b, h.Name...)
		b = le.AppendUint64(b, h.Value)
	}
	b = le.AppendUint32(b, uint32(len(d.Ifaces)))
	for _, f := range d.Ifaces {
		b = append(b, byte(len(f.Name)))
		b = append(b, f.Name...)
		b = le.AppendUint64(b, f.Rx)
		b = le.AppendUint64(b, f.Tx)
	}
	return b
}

// Encode frames d as a day's file, or says why it cannot be one.
func Encode(d Day) ([]byte, error) {
	if err := check(d); err != nil {
		return nil, err
	}
	return packfile.Seal(Magic, Version, encodeBody(d)), nil
}

// reader takes a body apart, failing once anything is short.
type reader struct {
	b   []byte
	bad bool
}

func (r *reader) take(n int) []byte {
	if r.bad || n > len(r.b) {
		r.bad = true
		return nil
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}

func (r *reader) u8() byte {
	if b := r.take(1); b != nil {
		return b[0]
	}
	return 0
}

func (r *reader) u32() uint32 {
	if b := r.take(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

func (r *reader) u64() uint64 {
	if b := r.take(8); b != nil {
		return binary.LittleEndian.Uint64(b)
	}
	return 0
}

// count reads a count of records of at least size bytes each, refusing one
// past max or past what the rest of the body can hold.
func (r *reader) count(size, max int) (int, error) {
	n := r.u32()
	if r.bad {
		return 0, errors.New("cut short")
	}
	if int64(n) > int64(max) || int64(n)*int64(size) > int64(len(r.b)) {
		return 0, fmt.Errorf("a count of %d does not fit the file", n)
	}
	return int(n), nil
}

// Decode checks a day's file whole and returns the day it holds, which must
// be the day starting at date.
func Decode(b []byte, date time.Time) (Day, error) {
	body, err := packfile.Check(b, Magic, Version)
	if err != nil {
		return Day{}, err
	}
	r := &reader{b: body}
	if u := r.u32(); u != Unit {
		return Day{}, fmt.Errorf("a unit of %d bytes, this reads %d", u, Unit)
	}
	var d Day
	num := r.u32()
	d.Date = time.Unix(int64(num)*int64(day/time.Second), 0).UTC()
	if want := date.UTC().Truncate(day); !d.Date.Equal(want) || !date.Equal(want) {
		return Day{}, fmt.Errorf("holds %s, named for %s", d.Date.Format("2006-01-02"), date.UTC().Format("2006-01-02"))
	}
	d.Mask = r.u32()
	d.LeftOut = r.u32()
	at := int64(r.u64())
	if at < 0 {
		return Day{}, errors.New("counters read before 1970")
	}
	if at != 0 {
		d.CountersAt = time.Unix(at, 0).UTC()
	}
	n, err := r.count(devSize, MaxDevices)
	if err != nil {
		return Day{}, fmt.Errorf("devices: %w", err)
	}
	if n > 0 {
		d.Devices = make([]Device, n)
	}
	for i := range d.Devices {
		dev := &d.Devices[i]
		copy(dev.MAC[:], r.take(6))
		for x := range dev.Down {
			dev.Down[x] = r.u32()
		}
		for x := range dev.Up {
			dev.Up[x] = r.u32()
		}
	}
	name := func() string { return string(r.take(int(r.u8()))) }
	if n, err = r.count(9, MaxCounters); err != nil {
		return Day{}, fmt.Errorf("counters: %w", err)
	}
	if n > 0 {
		d.Hits = make([]Counter, n)
	}
	for i := range d.Hits {
		d.Hits[i].Name = name()
		d.Hits[i].Value = r.u64()
	}
	if n, err = r.count(17, MaxIfaces); err != nil {
		return Day{}, fmt.Errorf("interfaces: %w", err)
	}
	if n > 0 {
		d.Ifaces = make([]Iface, n)
	}
	for i := range d.Ifaces {
		d.Ifaces[i].Name = name()
		d.Ifaces[i].Rx = r.u64()
		d.Ifaces[i].Tx = r.u64()
	}
	if r.bad {
		return Day{}, errors.New("cut short")
	}
	if len(r.b) != 0 {
		return Day{}, fmt.Errorf("%d bytes after the day's end", len(r.b))
	}
	if err := check(d); err != nil {
		return Day{}, err
	}
	return d, nil
}

// Window is the days a reader is answered for at now: the Keep days before
// now's UTC day, oldest first.
func Window(now time.Time) []time.Time {
	today := now.UTC().Truncate(day)
	out := make([]time.Time, 0, Keep)
	for i := Keep; i >= 1; i-- {
		out = append(out, today.Add(-time.Duration(i)*day))
	}
	return out
}

// HourSource is the hour ring: rollup.Store.
type HourSource interface {
	DayHours(time.Time) (rollup.DayHours, bool)
}

// Store is the days on flash, all held in memory. One lock covers both.
type Store struct {
	dir  string
	mu   sync.RWMutex
	days map[uint32]*Day
}

// Open loads the days in dir, at start: the newest Keep files. A file that is
// damaged, cut short, of another version, misnamed or (once the clock is set)
// of a day not yet closed is skipped and named in the returned error, and
// every other file still loads. A missing directory is a first start.
func Open(dir string, now time.Time) (*Store, error) {
	s := &Store{dir: dir, days: map[uint32]*Day{}}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	type file struct {
		at   time.Time
		path string
	}
	var files []file
	var errs []error
	set := !now.Before(clockFloor)
	for _, e := range entries {
		at, ok := parseName(e.Name())
		if !ok {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if set && at.Add(day+closeLag).After(now) {
			errs = append(errs, fmt.Errorf("%s: a day not closed by the clock (%s)", path, now.UTC().Format(time.RFC3339)))
			continue
		}
		files = append(files, file{at, path})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at.After(files[j].at) })
	if len(files) > Keep {
		files = files[:Keep]
	}
	for _, f := range files {
		d, err := readDay(f.path, f.at)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.path, err))
			continue
		}
		s.days[dayNumber(d.Date)] = &d
	}
	return s, errors.Join(errs...)
}

// readDay reads one file: a plain file, not a link, of a size a day can be.
func readDay(path string, at time.Time) (Day, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return Day{}, err
	}
	if !fi.Mode().IsRegular() {
		return Day{}, errors.New("not a plain file")
	}
	if fi.Size() > maxFile {
		return Day{}, fmt.Errorf("%d bytes, more than a day can be", fi.Size())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Day{}, err
	}
	return Decode(b, at)
}

// Write writes d once: if its day is held, or any file has its name (a
// damaged one included), nothing is written and it says false. The file is
// written beside its name and renamed over it.
func (s *Store) Write(d Day) (bool, error) {
	d.Date = d.Date.UTC()
	b, err := Encode(d)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.days[dayNumber(d.Date)] != nil {
		return false, nil
	}
	path := filepath.Join(s.dir, Name(d.Date))
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return false, err
	}
	if err := packfile.WriteFile(path, b); err != nil {
		return false, err
	}
	if f, err := os.Open(s.dir); err == nil {
		f.Sync() // the rename is durable once the directory is
		f.Close()
	}
	// What memory holds is what a restart reads back.
	got, err := Decode(b, d.Date)
	if err != nil {
		return true, err
	}
	s.days[dayNumber(got.Date)] = &got
	return true, nil
}

// Prune deletes the files of days more than Keep days before now's day, and
// any write a power cut stopped, and lets go of those days in memory. Before
// the clock is set it deletes nothing.
func (s *Store) Prune(now time.Time) error {
	if now.Before(clockFloor) {
		return nil
	}
	cut := dayNumber(now.UTC().Truncate(day).Add(-Keep * day))
	s.mu.Lock()
	defer s.mu.Unlock()
	for n := range s.days {
		if n < cut {
			delete(s.days, n)
		}
	}
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		at, ok := parseName(name)
		stale := strings.HasPrefix(name, ".") && strings.Contains(name, Ext+".")
		if stale || (ok && dayNumber(at) < cut) {
			if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Len is how many days are held.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.days)
}

// Get returns the day starting at date, if held. Its slices are shared and
// must not be changed.
func (s *Store) Get(date time.Time) (Day, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d := s.days[dayNumber(date.UTC())]
	if d == nil {
		return Day{}, false
	}
	return *d, true
}

// CloseDays writes every day of the last week that has closed by now, that the
// hour ring answers whole and in which the router counted at least one hour,
// and that has no file yet; then it prunes. Only yesterday's file carries the
// counters, read once and only if it is written: a day written later than
// that would carry counters of another moment. Before the clock is set it
// writes nothing.
func (s *Store) CloseDays(src HourSource, counters func() Counters, now time.Time) (int, error) {
	if now.Before(clockFloor) {
		return 0, nil
	}
	today := now.UTC().Truncate(day)
	written := 0
	var errs []error
	for i := ringDays; i >= 1; i-- {
		d := today.Add(-time.Duration(i) * day)
		if d.Add(day + closeLag).After(now) {
			continue
		}
		if _, ok := s.Get(d); ok {
			continue
		}
		if _, err := os.Lstat(filepath.Join(s.dir, Name(d))); !errors.Is(err, fs.ErrNotExist) {
			continue // written, or damaged: never written over
		}
		h, ok := src.DayHours(d)
		if !ok || h.Mask == 0 {
			continue
		}
		dd := FromHours(h)
		if i == 1 && counters != nil {
			withCounters(&dd, counters(), now)
		}
		ok, err := s.Write(dd)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", Name(d), err))
		}
		if ok {
			written++
		}
	}
	if err := s.Prune(now); err != nil {
		errs = append(errs, err)
	}
	return written, errors.Join(errs...)
}

// withCounters puts what was read at the close into d: names a file can hold,
// in order, at most MaxCounters and MaxIfaces of them.
func withCounters(d *Day, c Counters, now time.Time) {
	at := c.At
	if at.IsZero() {
		at = now
	}
	if at.Before(d.Date.Add(day)) {
		return
	}
	for n, v := range c.Hits {
		if len(n) > 0 && len(n) <= maxName {
			d.Hits = append(d.Hits, Counter{n, v})
		}
	}
	sort.Slice(d.Hits, func(i, j int) bool { return d.Hits[i].Name < d.Hits[j].Name })
	if len(d.Hits) > MaxCounters {
		d.Hits = d.Hits[:MaxCounters]
	}
	for n, v := range c.Ifaces {
		if len(n) > 0 && len(n) <= maxName {
			d.Ifaces = append(d.Ifaces, Iface{n, v[0], v[1]})
		}
	}
	sort.Slice(d.Ifaces, func(i, j int) bool { return d.Ifaces[i].Name < d.Ifaces[j].Name })
	if len(d.Ifaces) > MaxIfaces {
		d.Ifaces = d.Ifaces[:MaxIfaces]
	}
	d.CountersAt = time.Unix(at.Unix(), 0).UTC()
}

// DayOf is one day of a device as a reader is answered: Known says a file
// holds the day; Down and Up are bytes, and an hour not in Mask was not
// counted, whatever its value.
type DayOf struct {
	Date     time.Time
	Known    bool
	Mask     uint32
	Down, Up [24]uint64
}

// Days answers one device's days over the Window at now, oldest first. A day
// with no file is unknown; in a known day, a device the file does not hold
// moved nothing in the hours the router counted.
func (s *Store) Days(mac [6]byte, now time.Time) []DayOf {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []DayOf
	for _, date := range Window(now) {
		o := DayOf{Date: date}
		if d := s.days[dayNumber(date)]; d != nil {
			o.Known, o.Mask = true, d.Mask
			i := sort.Search(len(d.Devices), func(i int) bool { return bytes.Compare(d.Devices[i].MAC[:], mac[:]) >= 0 })
			if i < len(d.Devices) && d.Devices[i].MAC == mac {
				for x := 0; x < 24; x++ {
					o.Down[x] = uint64(d.Devices[i].Down[x]) * Unit
					o.Up[x] = uint64(d.Devices[i].Up[x]) * Unit
				}
			}
		}
		out = append(out, o)
	}
	return out
}

// DayCounters is one day of the window with what its close read.
type DayCounters struct {
	Date   time.Time
	Known  bool
	Mask   uint32
	At     time.Time
	Ifaces map[string][2]uint64
}

// Counters answers each day of the Window at now, oldest first, with the
// counters its close read: a day with no file is unknown, and one written
// after the router missed its close has a zero At and no counters.
func (s *Store) Counters(now time.Time) []DayCounters {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []DayCounters
	for _, date := range Window(now) {
		o := DayCounters{Date: date, Ifaces: map[string][2]uint64{}}
		if d := s.days[dayNumber(date)]; d != nil {
			o.Known, o.Mask, o.At = true, d.Mask, d.CountersAt
			for _, f := range d.Ifaces {
				o.Ifaces[f.Name] = [2]uint64{f.Rx, f.Tx}
			}
		}
		out = append(out, o)
	}
	return out
}
