// Package rollup adds flow deltas up into the numbers the console draws: a
// minute ring for the last day, an hour ring for the last week and a day ring
// for the last month, and per hour and per day the organisations and domains
// each device spoke to.
package rollup

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"netryx/internal/flows"
)

// Key is the finest cut the rings keep. Domains and organisations are not in
// it: they would multiply the keys, so they live in the bounded marginals.
type Key struct{ MAC, App, CC string }

// Bytes is what one key moved. Flows counts deltas with First set, so a flow
// is counted once however many deltas it sends.
type Bytes struct{ Up, Down, Flows uint64 }

func add(a, b Bytes) Bytes {
	return Bytes{Up: a.Up + b.Up, Down: a.Down + b.Down, Flows: a.Flows + b.Flows}
}

func (b Bytes) total() uint64 { return b.Up + b.Down }

// Options sizes the store. Zero fields take the defaults: a day of minutes,
// a week of hours, thirty days, 5000 keys an hour or day, 50 rows a device.
// The key cap also caps the rows of each marginal of a bucket, a minute
// holds at most 1000 keys (or the cap, if lower), and the intern table holds
// at most four times the cap.
type Options struct {
	MinuteSlots, HourSlots, DaySlots, MaxKeysPerHour, TopPerDevice int
	// Now is the store's clock, time.Now when nil. The service passes its
	// own clock so a test can move it the way NTP moves a router's.
	Now func() time.Time
}

// Filter narrows what is summed. An empty field matches everything; NotCC
// leaves one country out (the house's own traffic, from the WAN chart).
type Filter struct{ MAC, App, CC, NotCC string }

func (f Filter) match(k Key) bool {
	return (f.MAC == "" || f.MAC == k.MAC) && (f.App == "" || f.App == k.App) && (f.CC == "" || f.CC == k.CC) &&
		(f.NotCC == "" || f.NotCC != k.CC)
}

// Point is one step of a series. By is keyed by the split, or by "" when
// the series is not split; it is never nil.
type Point struct {
	At time.Time
	// Step is how long the point covers, from At.
	Step time.Duration
	By   map[string]Bytes
}

// Row is one line of a top list.
type Row struct {
	Key   string
	Bytes Bytes
	Extra map[string]string
}

const (
	other   = "other"
	otherCC = "--"
	day     = 24 * time.Hour
	// minuteKeys caps a minute's keys: a minute is a sixtieth of an hour,
	// and there are 1440 of them in memory.
	minuteKeys = 1000
	// skew is how far ahead of the clock a delta may be stamped and still
	// keep its own time. Past it the clock, not the delta, is believed:
	// otherwise one bad stamp turns the rings and wipes them. It is also
	// how long after its end a bucket counts as closed, so a delta that
	// arrives a little late still reaches the bucket's file.
	skew = time.Minute
)

// closed says whether the bucket starting at at, step long, has closed by
// now: from a minute after its end it takes no delta and may be written as
// final. Add and the saves share it, so they never disagree on the instant.
func closed(at time.Time, step time.Duration, now time.Time) bool {
	return !at.Add(step + skew).After(now)
}

// minuteBucket is one minute of traffic by device, app and country.
type minuteBucket struct {
	At      time.Time
	Keys    map[Key]Bytes
	compact bool
}

// A Go map grows by doubling and keeps the room it grew into, and a
// marginal that evicts and inserts leaves more behind; a closed bucket takes
// nothing new, so its maps are rebuilt at their exact size. Contents and
// order of nothing change.
func exact[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func exactMargins[K comparable](ms map[string]*margin[K]) map[string]*margin[K] {
	out := make(map[string]*margin[K], len(ms))
	for mac, m := range ms {
		out[mac] = &margin[K]{Rows: exact(m.Rows), Other: m.Other}
	}
	return out
}

func (bk *bucket) compactNow() {
	bk.Keys = exact(bk.Keys)
	bk.ASN = exactMargins(bk.ASN)
	bk.Dom = exactMargins(bk.Dom)
	bk.compact = true
}

// bucket is one hour or one day: the same keys as a minute, plus the
// marginals. Exported fields are what gob saves; the row counts are
// rebuilt when a bucket is loaded.
type bucket struct {
	At   time.Time
	Keys map[Key]Bytes
	ASN  map[string]*margin[asnKey] // by MAC
	Dom  map[string]*margin[domKey] // by MAC

	asnRows, domRows int
	// compact says the maps were rebuilt at their exact size once the
	// bucket closed. A merge into it clears it, so it is rebuilt again.
	compact bool
}

// bucketV1 is a bucket as version 1 files hold it: organisations by number
// alone, domains without how they were named.
type bucketV1 struct {
	At   time.Time
	Keys map[Key]Bytes
	ASN  map[string]*margin[uint32]
	Dom  map[string]*margin[domKey]
}

func (v bucketV1) upgrade() *bucket {
	bk := &bucket{At: v.At, Keys: v.Keys, Dom: v.Dom, ASN: map[string]*margin[asnKey]{}}
	for mac, m := range v.ASN {
		if m == nil {
			continue
		}
		up := &margin[asnKey]{Rows: map[asnKey]cell{}, Other: m.Other}
		for asn, c := range m.Rows {
			up.Rows[asnKey{ASN: asn}] = c
		}
		bk.ASN[mac] = up
	}
	return bk
}

func newBucket(at time.Time) *bucket {
	return &bucket{
		At:   at,
		Keys: map[Key]Bytes{},
		ASN:  map[string]*margin[asnKey]{},
		Dom:  map[string]*margin[domKey]{},
	}
}

type domKey struct{ App, Domain string }

// asnKey is where an app of a device talks to: a country and the network
// that answers there.
type asnKey struct {
	App, CC string
	ASN     uint32
}

// margin is one device's rows of a marginal. It never holds more than the
// store's TopPerDevice rows; what it pushes out is kept in Other, so the
// rows plus Other always add up to what was counted under the device.
type margin[K comparable] struct {
	Rows  map[K]cell
	Other Bytes
}

// cell is one marginal row. Org names an organisation row; Src says how a
// domain row was last named.
type cell struct {
	B   Bytes
	Org string
	Src string
}

// add counts b under k. A newcomer to a full margin is always let in and the
// smallest row makes way: a row only ever under-counts, never claims bytes
// that belonged to something else, and a busy newcomer outgrows the small
// rows it competes with.
func (m *margin[K]) add(k K, b Bytes, org, src string, top int) {
	if c, ok := m.Rows[k]; ok {
		c.B = add(c.B, b)
		if org != "" {
			c.Org = org
		}
		if src != "" {
			c.Src = src
		}
		m.Rows[k] = c
		return
	}
	if len(m.Rows) >= top {
		var victim K
		first := true
		var least uint64
		for rk, c := range m.Rows {
			if first || c.B.total() < least {
				victim, least, first = rk, c.B.total(), false
			}
		}
		m.Other = add(m.Other, m.Rows[victim].B)
		delete(m.Rows, victim)
	}
	m.Rows[k] = cell{B: b, Org: org, Src: src}
}

// count adds b to row k of mac in a marginal whose rows, over all devices,
// are held to limit (*n is how many it has). A device's own bound only
// swaps rows, so it is always allowed. Past the total a new row folds into
// the device's zero row (no organisation, no domain), or, if the device has
// none, into the shared "other" device's zero row: a flood of made-up MACs
// costs at most one row more than the cap.
func count[K comparable](ms map[string]*margin[K], n *int, mac string, k, zero K, b Bytes, org, src string, top, limit int) {
	if m := ms[mac]; m != nil {
		if _, ok := m.Rows[k]; ok || len(m.Rows) >= top {
			m.add(k, b, org, src, top)
			return
		}
	}
	if *n >= limit {
		k, org, src = zero, "", ""
		if m := ms[mac]; m == nil || !hasRow(m, zero) {
			mac = other
		}
		if m := ms[mac]; m != nil && (hasRow(m, zero) || len(m.Rows) >= top) {
			m.add(k, b, org, src, top)
			return
		}
	}
	m := ms[mac]
	if m == nil {
		m = &margin[K]{Rows: map[K]cell{}}
		ms[mac] = m
	}
	before := len(m.Rows)
	m.add(k, b, org, src, top)
	*n += len(m.Rows) - before
}

func hasRow[K comparable](m *margin[K], k K) bool {
	_, ok := m.Rows[k]
	return ok
}

// fold returns the key k is counted under in keys. Past the cap a key folds
// into its device's "other"; past twice the cap the devices share one, so a
// flood of made-up MACs cannot grow the bucket either.
func fold(keys map[Key]Bytes, k Key, limit int) Key {
	if _, ok := keys[k]; ok || len(keys) < limit {
		return k
	}
	mine := Key{MAC: k.MAC, App: other, CC: otherCC}
	if _, ok := keys[mine]; ok || len(keys) < 2*limit {
		return mine
	}
	return Key{MAC: other, App: other, CC: otherCC}
}

// countInto adds one delta (its strings already interned) to an hour or a
// day, and returns the key it was counted under.
func (bk *bucket) countInto(d flows.Delta, b Bytes, o Options) Key {
	k := fold(bk.Keys, Key{MAC: d.MAC, App: d.App, CC: d.CC}, o.MaxKeysPerHour)
	bk.Keys[k] = add(bk.Keys[k], b)
	// The marginals follow the key's device, so a device folded into the
	// shared key cannot open marginals of its own either.
	mac := d.MAC
	if k.MAC == other {
		mac = other
	}
	count(bk.ASN, &bk.asnRows, mac, asnKey{App: d.App, CC: d.CC, ASN: d.ASN}, asnKey{}, b, d.Org, "", o.TopPerDevice, o.MaxKeysPerHour)
	count(bk.Dom, &bk.domRows, mac, domKey{App: d.App, Domain: d.Domain}, domKey{}, b, "", d.NameSrc, o.TopPerDevice, o.MaxKeysPerHour)
	return k
}

// ring places buckets by time: slot i holds the bucket whose start, counted
// in steps since 1970, is i modulo the ring size. A slot is only read for
// the bucket it says it holds, so a slot the ring has not reached yet never
// leaks an old bucket into a new answer.
type ring struct {
	step   time.Duration
	n      int
	newest time.Time // start of the newest bucket accepted
}

func (r *ring) index(at time.Time) int {
	i := int((at.Unix() / int64(r.step/time.Second)) % int64(r.n))
	if i < 0 {
		i += r.n
	}
	return i
}

// oldest is the start of the oldest bucket still inside the ring.
func (r *ring) oldest() time.Time {
	return r.newest.Add(-time.Duration(r.n-1) * r.step)
}

func (r *ring) holds(at time.Time) bool {
	return !r.newest.IsZero() && !at.Before(r.oldest()) && !at.After(r.newest)
}

// covers says whether the ring reaches back to t.
func (r *ring) covers(t time.Time) bool {
	return !r.newest.IsZero() && !t.UTC().Truncate(r.step).Before(r.oldest())
}

// admit moves the ring forward to b if b is newer, and says whether b is
// still inside it.
func (r *ring) admit(b time.Time) bool {
	if r.newest.IsZero() || b.After(r.newest) {
		r.newest = b
	}
	return !b.Before(r.oldest())
}

// coarse is the hour ring or the day ring.
type coarse struct {
	ring
	b []*bucket
}

// at returns the bucket starting at b, made if need be, or nil if b is older
// than the ring reaches.
func (c *coarse) at(b time.Time) *bucket {
	if !c.admit(b) {
		return nil
	}
	i := c.index(b)
	if bk := c.b[i]; bk != nil && bk.At.Equal(b) {
		return bk
	}
	c.b[i] = newBucket(b)
	return c.b[i]
}

// get returns the bucket starting at t's step, or nil.
func (c *coarse) get(t time.Time) *bucket {
	b := t.UTC().Truncate(c.step)
	if !c.holds(b) {
		return nil
	}
	if bk := c.b[c.index(b)]; bk != nil && bk.At.Equal(b) {
		return bk
	}
	return nil
}

// each returns the buckets the ring holds that start in [first, last].
func (c *coarse) each(first, last time.Time) []*bucket {
	var out []*bucket
	for _, bk := range c.b {
		if bk != nil && c.holds(bk.At) && !bk.At.Before(first) && !bk.At.After(last) {
			out = append(out, bk)
		}
	}
	return out
}

// Store is the rollup. It has its own lock: deltas arrive from several
// goroutines, in no particular time order, while handlers read.
type Store struct {
	mu   sync.RWMutex
	o    Options
	now  func() time.Time
	mr   ring
	mins []*minuteBucket
	hrs  coarse
	dys  coarse
	// since is where the minutes start to be complete: the store's clock
	// at its first delta. The minutes are not saved, so before it only the
	// hours and days know what happened, and a delta stamped earlier does
	// not make the minutes before it whole.
	since   time.Time
	dropped uint64
	late    uint64
	minCap  int
	// What LoadDir and the saves know of the directory: loaded says the
	// first LoadDir ran; taken holds the files whose content is in RAM;
	// future the files skipped as later than the clock, for a later
	// LoadDir; finalDay the days read from final files, which their hours
	// must not be added to again.
	loaded   bool
	taken    map[fileKey]bool
	future   map[fileKey]bool
	finalDay map[int64]bool
	// nextSweep is when the next bucket can close, so Add looks for
	// buckets to compact once an hour, not on every delta.
	nextSweep time.Time
	// strs holds each distinct string once, so the same MAC, app or domain
	// in a thousand buckets is one allocation, not a thousand.
	strs   map[string]string
	strMax int
}

// NewStore makes an empty store.
func NewStore(o Options) *Store {
	if o.MinuteSlots <= 0 {
		o.MinuteSlots = 1440
	}
	if o.HourSlots <= 0 {
		o.HourSlots = 168
	}
	if o.DaySlots <= 0 {
		o.DaySlots = 30
	}
	if o.MaxKeysPerHour <= 0 {
		o.MaxKeysPerHour = 5000
	}
	if o.TopPerDevice <= 0 {
		o.TopPerDevice = 50
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Store{
		o:        o,
		now:      now,
		mr:       ring{step: time.Minute, n: o.MinuteSlots},
		mins:     make([]*minuteBucket, o.MinuteSlots),
		hrs:      coarse{ring: ring{step: time.Hour, n: o.HourSlots}, b: make([]*bucket, o.HourSlots)},
		dys:      coarse{ring: ring{step: day, n: o.DaySlots}, b: make([]*bucket, o.DaySlots)},
		strs:     map[string]string{},
		strMax:   4 * o.MaxKeysPerHour,
		minCap:   min(minuteKeys, o.MaxKeysPerHour),
		taken:    map[fileKey]bool{},
		future:   map[fileKey]bool{},
		finalDay: map[int64]bool{},
	}
}

// intern returns the stored copy of x. A full table hands x back as it is:
// the count is still right, only the memory is not shared.
func (s *Store) intern(x string) string {
	if v, ok := s.strs[x]; ok {
		return v
	}
	if len(s.strs) >= s.strMax {
		return x
	}
	// A copy, so a string cut from a larger buffer does not keep it alive.
	x = strings.Clone(x)
	s.strs[x] = x
	return x
}

// reintern rebuilds the intern table from the buckets the rings still hold,
// and lets go of the buckets they have moved past. It runs when the hour
// ring moves on and the table is over half full, so the strings of traffic
// long gone do not keep new strings out.
func (s *Store) reintern() {
	s.strs = map[string]string{}
	keep := func(x string) {
		if len(s.strs) < s.strMax {
			s.strs[x] = x
		}
	}
	for i, m := range s.mins {
		if m == nil {
			continue
		}
		if !s.mr.holds(m.At) {
			s.mins[i] = nil
			continue
		}
		for k := range m.Keys {
			keep(k.MAC)
			keep(k.App)
			keep(k.CC)
		}
	}
	for _, c := range []*coarse{&s.hrs, &s.dys} {
		for i, bk := range c.b {
			if bk == nil {
				continue
			}
			if !c.holds(bk.At) {
				c.b[i] = nil
				continue
			}
			for k := range bk.Keys {
				keep(k.MAC)
				keep(k.App)
				keep(k.CC)
			}
			for mac, m := range bk.ASN {
				keep(mac)
				for ak, cl := range m.Rows {
					keep(ak.App)
					keep(ak.CC)
					keep(cl.Org)
				}
			}
			for mac, m := range bk.Dom {
				keep(mac)
				for dk, cl := range m.Rows {
					keep(dk.App)
					keep(dk.Domain)
					keep(cl.Src)
				}
			}
		}
	}
}

// Dropped counts deltas a ring refused because they were older than it
// reaches, though their hour was still open.
func (s *Store) Dropped() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dropped
}

// Late counts deltas that arrived after their hour had closed. They are not
// added anywhere: the closed hour may already be on flash as final, and a
// byte that a restart would forget must not be shown before it.
func (s *Store) Late() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.late
}

// Add counts one delta into the three rings and the marginals.
func (s *Store) Add(d flows.Delta) {
	b := Bytes{Up: d.Up, Down: d.Down}
	if d.First {
		b.Flows = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	if !now.Before(s.nextSweep) {
		s.sweep(now)
	}
	at := d.At.UTC()
	if at.After(now.Add(skew)) {
		at = now
	}
	if closed(at.Truncate(time.Hour), time.Hour, now) {
		s.late++
		return
	}
	if s.since.IsZero() {
		s.since = now.Truncate(time.Minute)
	}
	d.MAC, d.App, d.CC = s.intern(d.MAC), s.intern(d.App), s.intern(d.CC)
	d.Domain, d.Org, d.NameSrc = s.intern(d.Domain), s.intern(d.Org), s.intern(d.NameSrc)
	k := Key{MAC: d.MAC, App: d.App, CC: d.CC}
	moved := s.hrs.newest

	if h := s.hrs.at(at.Truncate(time.Hour)); h == nil {
		s.dropped++
	} else {
		s.reopen(&h.compact)
		k = h.countInto(d, b, s.o)
	}
	// The day folds by its own keys: a day has more of them than any of
	// its hours.
	if dd := s.dys.at(at.Truncate(day)); dd == nil {
		s.dropped++
	} else {
		s.reopen(&dd.compact)
		dd.countInto(d, b, s.o)
	}

	mb := at.Truncate(time.Minute)
	if m := s.minuteFor(mb); m == nil {
		s.dropped++
	} else {
		// The minute folds what its hour folded, and past its own smaller
		// cap folds the same way.
		s.reopen(&m.compact)
		k = fold(m.Keys, k, s.minCap)
		m.Keys[k] = add(m.Keys[k], b)
	}
	if s.hrs.newest.After(moved) && len(s.strs) > s.strMax/2 {
		s.reintern()
	}
}

func (s *Store) minuteFor(b time.Time) *minuteBucket {
	if !s.mr.admit(b) {
		return nil
	}
	i := s.mr.index(b)
	m := s.mins[i]
	if m == nil || !m.At.Equal(b) {
		m = &minuteBucket{At: b, Keys: map[Key]Bytes{}}
		s.mins[i] = m
	}
	return m
}

// reopen marks a compacted bucket that takes more (a merge from disk, or a
// clock stepped back) for compacting again at the next sweep.
func (s *Store) reopen(compact *bool) {
	if *compact {
		*compact = false
		s.nextSweep = time.Time{}
	}
}

// sweep compacts every bucket that has closed by now and is not compact
// yet: minutes once their hour has closed, hours and days once they have.
func (s *Store) sweep(now time.Time) {
	for _, m := range s.mins {
		if m != nil && !m.compact && s.mr.holds(m.At) && closed(m.At.Truncate(time.Hour), time.Hour, now) {
			m.Keys, m.compact = exact(m.Keys), true
		}
	}
	for _, c := range []*coarse{&s.hrs, &s.dys} {
		for _, bk := range c.b {
			if bk != nil && !bk.compact && c.holds(bk.At) && closed(bk.At, c.step, now) {
				bk.compactNow()
			}
		}
	}
	s.nextSweep = now.Truncate(time.Hour).Add(time.Hour + skew)
}

// merge adds a loaded bucket into dst through the same bounds and the same
// intern table as a live delta, so a file written under larger options, or
// forged, cannot grow the store past its own.
func (s *Store) merge(dst, src *bucket) {
	s.reopen(&dst.compact)
	for k, b := range src.Keys {
		k = Key{MAC: s.intern(k.MAC), App: s.intern(k.App), CC: s.intern(k.CC)}
		fk := fold(dst.Keys, k, s.o.MaxKeysPerHour)
		dst.Keys[fk] = add(dst.Keys[fk], b)
	}
	top, limit := s.o.TopPerDevice, s.o.MaxKeysPerHour
	for mac, m := range src.ASN {
		if m == nil {
			continue
		}
		mac = s.intern(mac)
		for ak, c := range m.Rows {
			ak = asnKey{App: s.intern(ak.App), CC: s.intern(ak.CC), ASN: ak.ASN}
			count(dst.ASN, &dst.asnRows, mac, ak, asnKey{}, c.B, s.intern(c.Org), "", top, limit)
		}
		mergeOther(dst.ASN, &dst.asnRows, mac, asnKey{}, m.Other, top, limit)
	}
	for mac, m := range src.Dom {
		if m == nil {
			continue
		}
		mac = s.intern(mac)
		for dk, c := range m.Rows {
			dk = domKey{App: s.intern(dk.App), Domain: s.intern(dk.Domain)}
			count(dst.Dom, &dst.domRows, mac, dk, domKey{}, c.B, "", s.intern(c.Src), top, limit)
		}
		mergeOther(dst.Dom, &dst.domRows, mac, domKey{}, m.Other, top, limit)
	}
}

// mergeOther adds a loaded device's pushed-out bytes to its Other, or, if
// the device has no margin here, as a zero row under the same total bound,
// so a file cannot bring in devices that hold nothing but Other.
func mergeOther[K comparable](ms map[string]*margin[K], n *int, mac string, zero K, b Bytes, top, limit int) {
	if b == (Bytes{}) {
		return
	}
	if m := ms[mac]; m != nil {
		m.Other = add(m.Other, b)
		return
	}
	count(ms, n, mac, zero, zero, b, "", "", top, limit)
}

// hourAt and dayAt return the bucket starting at t's hour or day, or nil.
func (s *Store) hourAt(t time.Time) *bucket { return s.hrs.get(t) }
func (s *Store) dayAt(t time.Time) *bucket  { return s.dys.get(t) }

// minutesCover says whether the minutes alone can answer from t on: t is
// inside the minute ring and not before the minutes became complete.
func (s *Store) minutesCover(t time.Time) bool {
	return s.mr.covers(t) && !t.UTC().Truncate(time.Minute).Before(s.since)
}

type level int

const (
	minutes level = iota
	hours
	days
)

// finest is the finest ring that reaches back to from, among those a step
// of this length may read.
func (s *Store) finest(from time.Time, step time.Duration) level {
	switch {
	case step < time.Hour && s.minutesCover(from):
		return minutes
	case step < day && s.hrs.covers(from):
		return hours
	}
	return days
}

func (s *Store) ringOf(l level) *ring {
	switch l {
	case minutes:
		return &s.mr
	case hours:
		return &s.hrs.ring
	}
	return &s.dys.ring
}

func (s *Store) coarseOf(l level) *coarse {
	if l == hours {
		return &s.hrs
	}
	return &s.dys
}

// eachKeys calls fn for every bucket of the ring whose start lies in
// [first, last] and which the ring still holds.
func (s *Store) eachKeys(l level, first, last time.Time, fn func(at time.Time, keys map[Key]Bytes)) {
	if l == minutes {
		for _, m := range s.mins {
			if m != nil && s.mr.holds(m.At) && !m.At.Before(first) && !m.At.After(last) {
				fn(m.At, m.Keys)
			}
		}
		return
	}
	for _, bk := range s.coarseOf(l).each(first, last) {
		fn(bk.At, bk.Keys)
	}
}

func splitKey(k Key, split string) (string, bool) {
	switch split {
	case "":
		return "", true
	case "mac":
		return k.MAC, true
	case "app":
		return k.App, true
	case "cc":
		return k.CC, true
	}
	return "", false
}

// Series returns one point per step from from to to, both inclusive, empty
// steps included so a chart can draw the gaps, and every point says the
// step it covers. A step under an hour reads the minutes and one under a day
// the hours, but only where that ring is complete: the minutes from the
// store's first delta (they are not saved) and inside the ring, the hours
// inside theirs. Before that the next ring answers at its own step, and the
// coarse bucket the finer ring begins inside answers for its part before the
// finer points: its total less theirs, so no byte is counted twice. A step
// finer than the ring read becomes one of its buckets. Steps are aligned to
// UTC; each part is never longer than the ring it reads.
func (s *Store) Series(f Filter, from, to time.Time, step time.Duration, split string) []Point {
	if _, ok := splitKey(Key{}, split); !ok || step <= 0 || to.Before(from) {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	fine, coarseL, stitched := days, days, false
	switch {
	case step < time.Hour:
		fine, coarseL, stitched = minutes, hours, true
	case step < day:
		fine, coarseL, stitched = hours, days, true
	}
	r := s.ringOf(fine)
	// A step is a whole number of the ring's buckets.
	if step < r.step {
		step = r.step
	}
	step = (step + r.step - 1) / r.step * r.step
	from, to = from.UTC(), to.UTC()
	start := from.Truncate(step)

	var out []Point
	if stitched {
		whole, ok := s.wholeFrom(fine)
		b := to.Add(step).Truncate(step) // past the end: no fine points
		if ok {
			// The first step boundary at or after where the finer ring is whole.
			b = whole.Add(step - 1).Truncate(step)
		}
		if b.After(start) {
			out = s.coarsePart(f, split, coarseL, fine, start, b, to)
			start = b
		}
	} else if !r.newest.IsZero() && start.Before(r.oldest()) {
		start = r.oldest().Truncate(step)
	}
	if to.Before(start) {
		return out
	}
	return append(out, s.part(f, split, fine, start, to, step)...)
}

// wholeFrom is where a ring's buckets begin to be complete, or false when it
// holds none: the minutes from the store's first delta, and every ring from
// the oldest bucket it still holds.
func (s *Store) wholeFrom(l level) (time.Time, bool) {
	r := s.ringOf(l)
	if r.newest.IsZero() {
		return time.Time{}, false
	}
	t := r.oldest()
	if l == minutes && s.since.After(t) {
		t = s.since
	}
	return t, true
}

// part is the points of one ring, one per step from start to to, capped at
// the ring's length.
func (s *Store) part(f Filter, split string, l level, start, to time.Time, step time.Duration) []Point {
	n := int(to.Sub(start)/step) + 1
	if limit := s.ringOf(l).n; n > limit {
		n = limit
	}
	ps := make([]Point, n)
	for i := range ps {
		ps[i] = s.point(start.Add(time.Duration(i)*step), step, split)
	}
	last := ps[n-1].At.Add(step - time.Nanosecond)
	s.eachKeys(l, start, last, func(at time.Time, keys map[Key]Bytes) {
		addKeys(ps[int(at.Sub(start)/step)].By, keys, f, split)
	})
	return ps
}

func (s *Store) point(at time.Time, step time.Duration, split string) Point {
	p := Point{At: at, Step: step, By: map[string]Bytes{}}
	if split == "" {
		p.By[""] = Bytes{}
	}
	return p
}

func addKeys(by map[string]Bytes, keys map[Key]Bytes, f Filter, split string) {
	for k, b := range keys {
		if f.match(k) {
			sk, _ := splitKey(k, split)
			by[sk] = add(by[sk], b)
		}
	}
}

// coarsePart answers [start, b) from the coarser ring at its own step, up to
// to: whole coarse buckets, and the bucket the finer points begin inside cut
// at b, holding its total less what the finer ring holds from b to its end.
func (s *Store) coarsePart(f Filter, split string, cl, fl level, start, b, to time.Time) []Point {
	c := s.coarseOf(cl)
	if c.newest.IsZero() {
		return nil
	}
	cs := c.step
	t := start.Truncate(cs)
	if t.Before(c.oldest()) {
		t = c.oldest()
	}
	var out []Point
	for ; t.Before(b) && !t.After(to) && len(out) < c.n; t = t.Add(cs) {
		end := t.Add(cs)
		if end.After(b) {
			end = b
		}
		p := s.point(t, end.Sub(t), split)
		if bk := c.get(t); bk != nil {
			addKeys(p.By, bk.Keys, f, split)
		}
		if end.Before(t.Add(cs)) {
			// The finer ring holds [b, t+cs) of this bucket; this point is the
			// rest of it.
			finer := map[string]Bytes{}
			s.eachKeys(fl, b, t.Add(cs-time.Nanosecond), func(_ time.Time, keys map[Key]Bytes) {
				addKeys(finer, keys, f, split)
			})
			for k, v := range finer {
				p.By[k] = less(p.By[k], v)
			}
		}
		out = append(out, p)
	}
	return out
}

// less is a minus b, never below zero: a finer ring folds its keys under a
// smaller cap, so under a flood a filtered sum of it can exceed the coarse
// one.
func less(a, b Bytes) Bytes {
	sub := func(x, y uint64) uint64 {
		if y > x {
			return 0
		}
		return x - y
	}
	return Bytes{Up: sub(a.Up, b.Up), Down: sub(a.Down, b.Down), Flows: sub(a.Flows, b.Flows)}
}

// Top ranks by bytes moved over the buckets starting in [from, to], read
// from the finest ring that reaches back to from. Organisations ("asn":
// Key the AS number, Extra org), places ("place": Key "<cc>/<asn>", Extra
// cc, asn, org) and domains ("domain": Extra app and src, how the domain was
// last named) come from the marginals of the hours, or of the days for an
// older range. They honour Filter.MAC and Filter.App, and organisations and
// places Filter.CC too. An n of zero or less returns every row.
func (s *Store) Top(f Filter, dim string, from, to time.Time, n int) []Row {
	s.mu.RLock()
	defer s.mu.RUnlock()
	acc := map[string]*Row{}
	get := func(key string) *Row {
		r := acc[key]
		if r == nil {
			r = &Row{Key: key}
			acc[key] = r
		}
		return r
	}
	from, to = from.UTC(), to.UTC()
	margins := func() []*bucket {
		l := s.finest(from, time.Hour)
		return s.coarseOf(l).each(from.Truncate(s.ringOf(l).step), to)
	}
	switch dim {
	case "mac", "app", "cc":
		l := s.finest(from, 0)
		s.eachKeys(l, from.Truncate(s.ringOf(l).step), to, func(_ time.Time, keys map[Key]Bytes) {
			for k, b := range keys {
				if f.match(k) {
					key, _ := splitKey(k, dim)
					r := get(key)
					r.Bytes = add(r.Bytes, b)
				}
			}
		})
	case "asn", "place":
		// Organisations add up over the countries an app reached them in;
		// places keep each country and organisation apart.
		for _, bk := range margins() {
			for mac, m := range bk.ASN {
				if f.MAC != "" && f.MAC != mac {
					continue
				}
				for ak, c := range m.Rows {
					if (f.App != "" && f.App != ak.App) || (f.CC != "" && f.CC != ak.CC) {
						continue
					}
					asn := strconv.FormatUint(uint64(ak.ASN), 10)
					var r *Row
					if dim == "asn" {
						r = get(asn)
						if r.Extra == nil || c.Org != "" {
							r.Extra = map[string]string{"org": c.Org}
						}
					} else {
						r = get(ak.CC + "/" + asn)
						if r.Extra == nil || c.Org != "" {
							r.Extra = map[string]string{"cc": ak.CC, "asn": asn, "org": c.Org}
						}
					}
					r.Bytes = add(r.Bytes, c.B)
				}
				// What was pushed out has no app or country, so it cannot
				// be narrowed by either.
				if f.App == "" && f.CC == "" && m.Other != (Bytes{}) {
					r := get(other)
					if r.Extra == nil {
						r.Extra = map[string]string{"org": ""}
						if dim == "place" {
							r.Extra["cc"], r.Extra["asn"] = "", ""
						}
					}
					r.Bytes = add(r.Bytes, m.Other)
				}
			}
		}
	case "domain":
		// The row says how its domain was named most recently: the source
		// of the newest bucket that has one.
		srcAt := map[*Row]time.Time{}
		for _, bk := range margins() {
			for mac, m := range bk.Dom {
				if f.MAC != "" && f.MAC != mac {
					continue
				}
				for dk, c := range m.Rows {
					if f.App != "" && f.App != dk.App {
						continue
					}
					// The same name under two apps is two rows: the app is
					// part of what the row says.
					r := get(dk.App + "\x00" + dk.Domain)
					if r.Extra == nil {
						r.Key, r.Extra = dk.Domain, map[string]string{"app": dk.App, "src": ""}
					}
					if c.Src != "" && !bk.At.Before(srcAt[r]) {
						r.Extra["src"], srcAt[r] = c.Src, bk.At
					}
					r.Bytes = add(r.Bytes, c.B)
				}
				// What was pushed out has no app, so it cannot be narrowed
				// by one.
				if f.App == "" && m.Other != (Bytes{}) {
					r := get("\x00" + other)
					r.Key, r.Extra = other, map[string]string{"app": "", "src": ""}
					r.Bytes = add(r.Bytes, m.Other)
				}
			}
		}
	default:
		return nil
	}
	rows := ranked(acc)
	if n > 0 && len(rows) > n {
		rows = rows[:n]
	}
	return rows
}

// ranked orders a top list: most bytes first, then by key, then by app.
func ranked(acc map[string]*Row) []Row {
	rows := make([]Row, 0, len(acc))
	for _, r := range acc {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if a, b := rows[i].Bytes.total(), rows[j].Bytes.total(); a != b {
			return a > b
		}
		if rows[i].Key != rows[j].Key {
			return rows[i].Key < rows[j].Key
		}
		return rows[i].Extra["app"] < rows[j].Extra["app"]
	})
	return rows
}

// Pairs answers, in one pass over the keys, what Top(f with dim set to k, by)
// answers for every k of dim: for each app or country, the devices (or apps,
// or countries) that moved its bytes, ranked as Top ranks them. dim and by
// are two different dimensions every key carries (mac, app, cc); anything
// else is nil.
func (s *Store) Pairs(f Filter, dim, by string, from, to time.Time) map[string][]Row {
	keyDim := func(d string) bool { return d == "mac" || d == "app" || d == "cc" }
	if !keyDim(dim) || !keyDim(by) || dim == by {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	acc := map[string]map[string]*Row{}
	from, to = from.UTC(), to.UTC()
	l := s.finest(from, 0)
	s.eachKeys(l, from.Truncate(s.ringOf(l).step), to, func(_ time.Time, keys map[Key]Bytes) {
		for k, b := range keys {
			if !f.match(k) {
				continue
			}
			a, _ := splitKey(k, dim)
			c, _ := splitKey(k, by)
			m := acc[a]
			if m == nil {
				m = map[string]*Row{}
				acc[a] = m
			}
			r := m[c]
			if r == nil {
				r = &Row{Key: c}
				m[c] = r
			}
			r.Bytes = add(r.Bytes, b)
		}
	})
	out := make(map[string][]Row, len(acc))
	for a, m := range acc {
		out[a] = ranked(m)
	}
	return out
}
