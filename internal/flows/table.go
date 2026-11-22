package flows

import (
	"container/list"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"netryx/internal/ct"
	"netryx/internal/enrich"
)

// Options wires the table to the rest of the service. Every function may be
// nil; a nil one answers as if it knew nothing. All but Sink are called with
// the table's lock held, so they must not call back into the table; Sink is
// called after it is released.
type Options struct {
	// MaxLive and MaxJournal bound the two tables. A router has 128 MB in
	// the worst case and a misbehaving device can open connections as fast
	// as the kernel lets it.
	MaxLive, MaxJournal int
	// NameWait is how long a flow's bytes are held for a name. ct NEW fires
	// on the SYN, before any ClientHello, so a flow stamped on sight would be
	// stamped "unknown" for its whole life.
	NameWait time.Duration
	IsLocal  func(netip.Addr) bool
	MACOf    func(addr string) string
	NameOf   func(local, remote string) (string, string)
	Geo      func(netip.Addr) (enrich.ASInfo, bool)
	App      func(name, org string) enrich.App
	Sink     func(Delta)
}

// Rate is a device's throughput over the last Sync interval, in bytes/s.
type Rate struct{ Up, Down float64 }

// key is what identifies a flow. The kernel's id alone is reused once a
// connection is freed; with the original tuple a reused id is a new key.
type key struct {
	id           uint32
	src, dst     netip.Addr
	sport, dport uint16
	proto        uint8
}

// entry is a live flow and the bookkeeping behind its deltas.
type entry struct {
	Flow
	k key
	// lastUp and lastDown are the device-side byte counters at the last
	// event, so a delta is current minus last and nothing is added twice.
	lastUp, lastDown uint64
	// heldUp and heldDown are bytes moved before the flow was fixed. They
	// come out in one delta when it is.
	heldUp, heldDown uint64
	fixed            bool
	first            bool // the next delta is this flow's first
	swap             bool // ct's original direction is the far end's, so Up and Down turn round
	missing          int  // consecutive dumps this flow was absent from
	seq              uint64
	el               *list.Element // place in the eviction order
	// journaled says this connection already has its journal row: it was
	// pushed out once and readmitted. A later eviction or its real end adds
	// no second row, or churn in a full table would flush the journal.
	journaled bool
	// rateAt is this flow's previous rate sample and sinceUp, sinceDown the
	// bytes it moved after it. A flow's own sample, not the last Sync: one
	// that opened halfway through an interval moved its bytes in half of it.
	rateAt             time.Time
	sinceUp, sinceDown uint64
	// win holds the bytes (up, down) the flow moved in each of the last
	// winSlots minutes, by unix minute modulo winSlots; winAt is the minute
	// last written. 176 bytes a flow, 1.4 MB for a full table of 8192.
	win   [winSlots][2]uint64
	winAt int64
}

// winSlots is how many minutes a flow's recent volume reaches back: the
// longest window (10 minutes) and the minute in progress.
const winSlots = 11

// addWin files bytes the flow moved under the minute of now, clearing the
// minutes it skipped since it last moved.
func (en *entry) addWin(now time.Time, up, down uint64) {
	m := now.Unix() / 60
	if m != en.winAt {
		if en.winAt == 0 || m-en.winAt >= winSlots || m < en.winAt {
			en.win = [winSlots][2]uint64{}
		} else {
			for k := en.winAt + 1; k <= m; k++ {
				en.win[k%winSlots] = [2]uint64{}
			}
		}
		en.winAt = m
	}
	i := m % winSlots
	en.win[i][0] += up
	en.win[i][1] += down
}

// winBytes is what the flow moved over the window ending at now: the minute
// in progress and the whole minutes before it, back to now-span, to the
// minute (as rollup.Recent reads the house's minutes).
func (en *entry) winBytes(now time.Time, span time.Duration) (up, down uint64) {
	m := now.Unix() / 60
	first := now.Add(-span).Unix() / 60
	if first < m-winSlots+1 {
		first = m - winSlots + 1
	}
	for k := first; k <= m; k++ {
		if k > en.winAt {
			continue
		}
		up += en.win[k%winSlots][0]
		down += en.win[k%winSlots][1]
	}
	return up, down
}

// Table is the live flow table and its journal. One mutex guards it: the
// watcher, the Sync ticker and HTTP readers all reach it at once, and every
// operation is a map probe or a short scan.
type Table struct {
	o  Options
	mu sync.Mutex

	live    map[key]*entry
	liveSeq uint64
	// lru runs from the flow whose bytes moved longest ago to the latest, so
	// eviction takes the front instead of scanning the table.
	lru *list.List

	// finished remembers flows that ended, and in which Sync generation, so a
	// dump taken before their Destroy, or a second Destroy, cannot count them
	// again. A NEW for the key clears it: that is a new flow.
	finished *bounded[uint64]
	// evicted remembers flows pushed out while open, with their device-side
	// counters then, so a return or a Destroy can tell what was lost.
	evicted *bounded[[2]uint64]
	// syncs counts completed Syncs. Only the first dump after a flow ended
	// can have been taken before its Destroy. This holds only if dumps are
	// taken and applied one at a time: Sync calls must be serialised, with
	// no second dump taken before the first is applied.
	syncs uint64

	j journal

	// acc is the bytes each device moved since the last Sync; rates is the
	// result of the last interval.
	acc      map[string]*[2]uint64
	rates    map[string]Rate
	lastSync time.Time

	// series is each device's live ring: its rate per app at each Sync.
	series map[string]*ring

	dropped int
	lost    uint64
	// examined counts entries looked at to choose an eviction, so a test can
	// see eviction cost without timing it.
	examined int
}

// NewTable builds a table. Zero bounds take the plan's defaults.
func NewTable(o Options) *Table {
	if o.MaxLive <= 0 {
		o.MaxLive = 8192
	}
	if o.MaxJournal <= 0 {
		// About 18 MB at 360 bytes a row.
		o.MaxJournal = 50_000
	}
	if o.NameWait <= 0 {
		o.NameWait = 10 * time.Second
	}
	if o.IsLocal == nil {
		o.IsLocal = func(a netip.Addr) bool {
			return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast()
		}
	}
	if o.MACOf == nil {
		o.MACOf = func(string) string { return "" }
	}
	if o.NameOf == nil {
		o.NameOf = func(string, string) (string, string) { return "", "" }
	}
	if o.Geo == nil {
		o.Geo = func(netip.Addr) (enrich.ASInfo, bool) { return enrich.ASInfo{}, false }
	}
	if o.App == nil {
		o.App = func(string, string) enrich.App { return enrich.Unknown }
	}
	if o.Sink == nil {
		o.Sink = func(Delta) {}
	}
	return &Table{
		o:        o,
		live:     map[key]*entry{},
		lru:      list.New(),
		finished: newBounded[uint64](o.MaxLive),
		// A kernel table larger than MaxLive keeps that many more flows out
		// of the table at once; four times MaxLive covers a kernel table five
		// times the live one before a record is forgotten.
		evicted: newBounded[[2]uint64](4 * o.MaxLive),
		j:       journal{ring: make([]Flow, o.MaxJournal)},
		acc:     map[string]*[2]uint64{},
		rates:   map[string]Rate{},
		series:  map[string]*ring{},
	}
}

// addr converts at the boundary with ct. A 16-byte IPv4 address would
// otherwise come out as ::ffff:a.b.c.d and never match a lookup by string.
func addr(ip net.IP) netip.Addr {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}
	}
	return a.Unmap()
}

// keyOf names the flow an event is about. Loopback is no flow: it never
// leaves the router, and an SSH tunnel polling the console from 127.0.0.1
// must not become a client. Nor is a flow with an unspecified, broadcast or
// multicast end: a DHCP discover from 0.0.0.0 to 255.255.255.255 has no
// device, and once made 255.255.255.255 a row of its own.
func keyOf(e ct.Event) (key, bool) {
	k := key{
		id: e.ID, src: addr(e.Orig.SrcIP), dst: addr(e.Orig.DstIP),
		sport: e.Orig.SrcPort, dport: e.Orig.DstPort, proto: e.Orig.Proto,
	}
	return k, isEnd(k.src) && isEnd(k.dst)
}

var limitedBroadcast = netip.AddrFrom4([4]byte{255, 255, 255, 255})

// isEnd says whether an address can be one end of a device's flow.
func isEnd(a netip.Addr) bool {
	return a.IsValid() && !a.IsLoopback() && !a.IsUnspecified() && !a.IsMulticast() && a != limitedBroadcast
}

// Apply folds in a NEW or a DESTROY from the watcher.
func (t *Table) Apply(e ct.Event, now time.Time) {
	k, ok := keyOf(e)
	if !ok {
		return
	}
	var out []Delta
	t.mu.Lock()
	switch e.Kind {
	case ct.New:
		if t.live[k] == nil {
			// The kernel only says NEW for a connection it has just created,
			// so whatever this key meant before is over.
			t.finished.del(k)
			t.evicted.del(k)
			en := t.add(k, e, now, &out)
			// Whatever the SYN carried is counted from zero. No name is
			// asked for here: the handshake has not been sent yet.
			t.advance(en, e, now, false, &out)
		}
	case ct.Destroy:
		if _, done := t.finished.get(k); done {
			break
		}
		en := t.live[k]
		if en == nil {
			if last, ok := t.evicted.get(k); ok {
				t.evicted.del(k)
				up, down := sidesOf(e, t.inbound(k))
				if up >= last[0] && down >= last[1] {
					// Pushed out while open and never readmitted: what it
					// moved since was never counted and cannot be stamped.
					// It has ended, so a dump taken before this Destroy, or
					// the same Destroy again, must not bring it back.
					t.lost += up - last[0] + down - last[1]
					t.finished.put(k, t.syncs)
					break
				}
			}
			// A connection whose NEW was lost. Its final counters are all
			// there is, so they are counted whole.
			en = t.add(k, e, now, &out)
		}
		t.advance(en, e, now, true, &out)
		t.finish(en, now, &out)
	}
	t.mu.Unlock()
	t.emit(out)
}

// Sync reads counters from a full dump of the kernel's table.
func (t *Table) Sync(dump []ct.Event, now time.Time) {
	var out []Delta
	t.mu.Lock()
	seen := make(map[key]bool, len(dump))
	for _, e := range dump {
		// The kernel answers a dump with CT_NEW messages, so a dump row
		// parses as a NEW. It is a reading of a flow's counters, never the
		// moment it was created: read as a NEW it would never be settled,
		// and a live flow would carry no name, app or country until it ended.
		e.Kind = ct.Update
		k, ok := keyOf(e)
		if !ok {
			continue
		}
		reborn := false
		if gen, done := t.finished.get(k); done {
			if gen == t.syncs {
				continue // taken before its Destroy was applied
			}
			// Back in a dump taken after its Destroy: the key was created
			// again and its NEW lost. Counted whole, as a flow of its own.
			t.finished.del(k)
			reborn = true
		}
		seen[k] = true
		en := t.live[k]
		if en == nil {
			// The eviction record is read before add, which may evict others
			// and overwrite it.
			last, wasEvicted := t.evicted.get(k)
			t.evicted.del(k)
			en = t.add(k, e, now, &out)
			up, down := en.sides(e)
			if wasEvicted && !reborn {
				if up >= last[0] && down >= last[1] {
					// Pushed out while open and back: counted from here. It
					// was counted as a flow when it first came out.
					t.lost += up - last[0] + down - last[1]
					en.lastUp, en.lastDown = up, down
					en.first = false
					en.journaled = true
				}
			} else if !reborn {
				// First met in a dump: running since before the service
				// started, or its NEW lost. The counter so far is history,
				// and counting it now would draw a spike that never happened.
				en.lastUp, en.lastDown = up, down
			}
		}
		en.missing = 0
		t.advance(en, e, now, false, &out)
	}
	for k, en := range t.live {
		if seen[k] {
			continue
		}
		// One absence is not an ending: a NEW applied after the dump was
		// taken is missing from it. Two in a row are a lost Destroy.
		en.missing++
		if en.missing >= 2 {
			t.finish(en, now, &out)
		}
	}
	t.rates = map[string]Rate{}
	var step time.Duration
	if !t.lastSync.IsZero() && now.After(t.lastSync) {
		step = now.Sub(t.lastSync)
		secs := step.Seconds()
		for dev, b := range t.acc {
			t.rates[dev] = Rate{Up: float64(b[0]) / secs, Down: float64(b[1]) / secs}
		}
	}
	t.acc = map[string]*[2]uint64{}
	t.sample(now, step)
	t.lastSync = now
	t.syncs++
	t.mu.Unlock()
	t.emit(out)
}

// add creates a live entry, making room first. The caller holds the lock.
func (t *Table) add(k key, e ct.Event, now time.Time, out *[]Delta) *entry {
	for len(t.live) >= t.o.MaxLive {
		t.evict(now, out)
	}
	en := &entry{k: k, first: true, rateAt: now}
	en.ID, en.Proto, en.Start, en.Updated, en.Count = e.ID, e.Orig.Proto, now, now, 1
	en.el = t.lru.PushBack(en)
	src, dst := t.o.IsLocal(k.src), t.o.IsLocal(k.dst)
	origSrc := netip.AddrPortFrom(k.src, k.sport)
	origDst := netip.AddrPortFrom(k.dst, k.dport)
	switch {
	case src && dst:
		en.Dir, en.Local, en.Remote = Local, origSrc, origDst
	case src:
		en.Dir, en.Local, en.Remote = Out, origSrc, origDst
	default:
		// Opened from outside. The device is whoever answers, which behind a
		// port forward is the reply's source, not the original destination
		// (the router's own address). Its upload is ct's reply direction.
		en.Dir, en.Remote, en.swap = In, origSrc, t.inbound(k)
		en.Local = origDst
		if r := addr(e.Reply.SrcIP); r.IsValid() {
			en.Local = netip.AddrPortFrom(r, e.Reply.SrcPort)
		}
	}
	en.MAC = t.o.MACOf(en.Local.Addr().String())
	t.liveSeq++
	en.seq = t.liveSeq
	t.live[k] = en
	return en
}

// evict lets the least recently updated flow go, with its held bytes, so a
// full table loses rows rather than bytes. It is not marked finished: a flow
// still open comes back in the next dump and is counted from there.
func (t *Table) evict(now time.Time, out *[]Delta) {
	front := t.lru.Front()
	if front == nil {
		return
	}
	t.examined++
	old := front.Value.(*entry)
	t.settle(old, now, true, out)
	old.Evicted = true
	t.retire(old, now)
	t.evicted.put(old.k, [2]uint64{old.lastUp, old.lastDown})
	t.dropped++
}

// inbound says whether something outside opened the flow, in which case its
// device-side counters are ct's reply direction.
func (t *Table) inbound(k key) bool { return !t.o.IsLocal(k.src) }

// sidesOf returns ct's counters as the device sees them.
func sidesOf(e ct.Event, swap bool) (up, down uint64) {
	if swap {
		return e.Down.Bytes, e.Up.Bytes
	}
	return e.Up.Bytes, e.Down.Bytes
}

// sides returns ct's counters as the device sees them.
func (en *entry) sides(e ct.Event) (up, down uint64) { return sidesOf(e, en.swap) }

// advance takes an event's counters into the flow: the delta since the last
// event is held, and settled if the flow can now be fixed.
func (t *Table) advance(en *entry, e ct.Event, now time.Time, final bool, out *[]Delta) {
	up, down := en.sides(e)
	du, dd := up-en.lastUp, down-en.lastDown
	if up < en.lastUp || down < en.lastDown {
		// A counter that falls belongs to a new connection under a reused
		// id. Both directions start again: a down counter that rose from 100
		// to 300 is 300 bytes of the new flow, not 200 more of the old one.
		du, dd = up, down
		t.restart(en, now, out)
	}
	en.lastUp, en.lastDown = up, down
	en.Up, en.Down = e.Up, e.Down
	if en.swap {
		en.Up, en.Down = e.Down, e.Up
	}
	en.Offloaded = e.Offloaded()
	if du+dd > 0 {
		en.Updated = now
		t.lru.MoveToBack(en.el)
		// Bytes are filed under the device that holds the address while
		// they move, asked again while it is unknown; never translated
		// later, when the address may belong to someone else.
		t.resolve(en)
		dev := en.device()
		b := t.acc[dev]
		if b == nil {
			b = &[2]uint64{}
			t.acc[dev] = b
		}
		b[0] += du
		b[1] += dd
	}
	en.heldUp += du
	en.heldDown += dd
	en.sinceUp += du
	en.sinceDown += dd
	if e.Kind == ct.New {
		return // too early to name; Sync and Destroy settle it
	}
	t.settle(en, now, final, out)
}

// resolve asks again whose a flow is while nobody knew: the hardware address
// of a device is often learned after its first connection opened. The caller
// holds the lock.
func (t *Table) resolve(en *entry) {
	if en.MAC == "" {
		en.MAC = t.o.MACOf(en.Local.Addr().String())
	}
}

// device is the key a flow's bytes are filed under: its hardware address,
// or its local address when none is known, as the collector files devices.
func (en *entry) device() string {
	if en.MAC != "" {
		return en.MAC
	}
	return en.Local.Addr().String()
}

// settle fixes the flow if it can be fixed, and emits whatever it holds.
func (t *Table) settle(en *entry, now time.Time, final bool, out *[]Delta) {
	if !en.fixed && !t.fix(en, now, final) {
		return
	}
	if !en.first && en.heldUp+en.heldDown == 0 {
		return
	}
	// The first delta goes out even when empty, so a rollup counts every
	// flow exactly once.
	*out = append(*out, Delta{
		At: now, MAC: en.device(), App: en.App.ID, CC: en.Geo.CC, Domain: en.Name,
		ASN: en.Geo.ASN, Org: en.Geo.Org, Up: en.heldUp, Down: en.heldDown, First: en.first,
		NameSrc: en.NameSrc,
	})
	en.first = false
	en.heldUp, en.heldDown = 0, 0
}

// fix stamps the flow with its name, place and app, once: a rollup keyed by
// app and country cannot have a flow change its mind halfway through.
func (t *Table) fix(en *entry, now time.Time, final bool) bool {
	// The capture learns a device's hardware address from its handshake,
	// which arrives after the SYN that created this row.
	t.resolve(en)
	name, via := t.o.NameOf(en.Local.Addr().String(), en.Remote.Addr().String())
	if name == "" && !final && now.Sub(en.Start) < t.o.NameWait {
		return false
	}
	en.Name, en.NameSrc = name, via
	if en.NameSrc == "" || (name == "" && via != "ech") {
		en.NameSrc = "none"
	}
	var geo enrich.ASInfo
	if en.Dir == Local {
		// Both ends are at home: it never crossed the line, so it has no
		// place, and history keeps it apart from what did.
		geo = enrich.ASInfo{CC: LANCountry}
	} else if g, ok := t.o.Geo(en.Remote.Addr()); ok {
		geo = g
	} else {
		geo = enrich.ASInfo{CC: "--"}
	}
	en.Geo = geo
	en.App = t.o.App(name, geo.Org)
	en.fixed = true
	return true
}

// finish moves a flow that ended from the live table to the journal,
// settling first so a flow that ends before its name arrives still hands
// over every held byte.
func (t *Table) finish(en *entry, now time.Time, out *[]Delta) {
	t.settle(en, now, true, out)
	t.retire(en, now)
	t.finished.put(en.k, t.syncs)
}

// retire takes a settled flow out of the live table and journals it.
func (t *Table) retire(en *entry, now time.Time) {
	// Asked once more as it leaves the live table, then kept: the journal
	// row says whose it was while it ran, and its address may go to another
	// device later.
	t.resolve(en)
	en.End = now
	// A finished flow moves nothing now, whatever it moved last.
	en.RateUp, en.RateDown = 0, 0
	delete(t.live, en.k)
	t.lru.Remove(en.el)
	if !en.journaled {
		t.j.push(en.Flow)
		en.journaled = true
	}
}

// restart turns a live row into the new connection that reused its key:
// whatever the old one held goes out under the old stamp, and the new one
// waits for its own name and is counted as a flow of its own.
func (t *Table) restart(en *entry, now time.Time, out *[]Delta) {
	t.settle(en, now, true, out)
	en.fixed, en.first = false, true
	// A new connection has no journal row yet, whatever the old one had.
	// (Evicted needs no reset: it is set only on an entry leaving the table,
	// and a readmitted flow is a fresh entry.)
	en.journaled = false
	en.Start = now
	en.Name, en.NameSrc = "", ""
	en.App, en.Geo = enrich.App{}, enrich.ASInfo{}
}

// bounded is a map capped at a fixed size that forgets its oldest key first.
// A key put again takes a new slot; the old slot no longer owns it.
type bounded[V any] struct {
	m    map[key]slotted[V]
	ring []key
	next int
}

type slotted[V any] struct {
	v    V
	slot int
}

func newBounded[V any](n int) *bounded[V] {
	return &bounded[V]{m: map[key]slotted[V]{}, ring: make([]key, n)}
}

func (b *bounded[V]) put(k key, v V) {
	if len(b.ring) == 0 {
		return
	}
	slot := b.next
	if prev := b.ring[slot]; prev != (key{}) {
		if r, ok := b.m[prev]; ok && r.slot == slot {
			delete(b.m, prev)
		}
	}
	b.ring[slot] = k
	b.m[k] = slotted[V]{v: v, slot: slot}
	b.next = (slot + 1) % len(b.ring)
}

func (b *bounded[V]) get(k key) (V, bool) {
	r, ok := b.m[k]
	return r.v, ok
}

func (b *bounded[V]) del(k key) { delete(b.m, k) }

// emit hands deltas to the sink outside the lock, so a slow sink never holds
// up the watcher or a reader.
func (t *Table) emit(out []Delta) {
	for _, d := range out {
		t.o.Sink(d)
	}
}

// Live returns a copy of the live flows, newest first.
func (t *Table) Live() []Flow {
	t.mu.Lock()
	defer t.mu.Unlock()
	ens := t.liveNewestFirst(0)
	out := make([]Flow, len(ens))
	for i, en := range ens {
		t.resolve(en)
		out[i] = en.Flow
	}
	return out
}

// liveNewestFirst lists the live entries older than cursor (0: all), newest
// first. The caller holds the lock and copies what it keeps.
func (t *Table) liveNewestFirst(cursor uint64) []*entry {
	ens := make([]*entry, 0, len(t.live))
	for _, en := range t.live {
		if cursor == 0 || en.seq < cursor {
			ens = append(ens, en)
		}
	}
	sort.Slice(ens, func(i, j int) bool { return ens[i].seq > ens[j].seq })
	return ens
}

// Rates returns each device's throughput over the last Sync interval, under
// the device each byte was filed under when it moved.
func (t *Table) Rates() map[string]Rate {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]Rate, len(t.rates))
	for k, v := range t.rates {
		out[k] = v
	}
	return out
}

// Dropped is how many live flows were pushed out to make room, so a full
// table says so rather than looking complete.
func (t *Table) Dropped() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped
}

// LostBytes is what evicted flows moved while they were out of the table.
func (t *Table) LostBytes() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lost
}

const (
	// livePoints is five minutes at the 2 s sync a watched device gets.
	livePoints = 150
	// liveDeviceCap bounds the ring in devices: a flood of made-up addresses
	// must not cost the router 150 points each.
	liveDeviceCap = 512
	// maxPointApps bounds one point: the busiest apps keep their own rate and
	// the rest are folded into other, so a device with many apps costs no
	// more than one with eight. A chart shows no more colours than that.
	maxPointApps = 8
	// liveIdle is how long a device keeps its ring with no live flow, so a
	// card left open on a quiet device still draws its last five minutes.
	liveIdle = 5 * time.Minute
	// RouterKey is the device MACOf names the router's own addresses; it
	// must equal collect.RouterKey (a test in cmd/netryx holds them
	// together). The router's traffic is shown elsewhere; it is no client.
	RouterKey = "router"
	// unknownApp is the app of a flow still waiting for its name, and
	// otherApp the app the apps past maxPointApps are folded into.
	unknownApp = "unknown"
	otherApp   = "other"
)

// maxRingBytes bounds one device's full ring: the ring itself, 150 points
// and their app lists at most maxPointApps long. App ids are not counted:
// they are the catalog's own strings, shared, not copied per point.
const maxRingBytes = int(unsafe.Sizeof(ring{})) +
	livePoints*(int(unsafe.Sizeof(point{}))+maxPointApps*int(unsafe.Sizeof(AppRate{})))

// point is a LivePoint as the ring keeps it: the time as nanoseconds, so a
// point is 40 bytes plus its list.
type point struct {
	at   int64
	step time.Duration
	apps []AppRate
}

// ring is one device's live points, oldest at head once it is full.
type ring struct {
	pts    []point
	head   int
	active time.Time // the last Sync it had a live flow
	rate   float64   // its total rate then, to choose between equals
}

func (r *ring) push(p point) {
	if len(r.pts) < livePoints {
		if len(r.pts) == cap(r.pts) {
			// Grown by hand: append would double 128 to 256 and keep room
			// for 106 points the ring never holds.
			grown := make([]point, len(r.pts), min(max(2*cap(r.pts), 8), livePoints))
			copy(grown, r.pts)
			r.pts = grown
		}
		r.pts = append(r.pts, p)
		return
	}
	r.pts[r.head] = p
	r.head = (r.head + 1) % livePoints
}

// bytes is what the ring holds in memory, by the same count as maxRingBytes.
func (r *ring) bytes() int {
	n := int(unsafe.Sizeof(*r)) + cap(r.pts)*int(unsafe.Sizeof(point{}))
	for _, p := range r.pts {
		n += cap(p.apps) * int(unsafe.Sizeof(AppRate{}))
	}
	return n
}

// compact turns one device's rates by app into a point's list: busiest
// first, at most maxPointApps long, the rest folded into other. One
// allocation, sized exactly.
func compact(byApp map[string]*[2]float64) []AppRate {
	if len(byApp) == 0 {
		return nil
	}
	all := make([]AppRate, 0, len(byApp))
	for app, r := range byApp {
		all = append(all, AppRate{App: app, Up: float32(r[0]), Down: float32(r[1])})
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i].Up+all[i].Down, all[j].Up+all[j].Down
		if a != b {
			return a > b
		}
		return all[i].App < all[j].App
	})
	if len(all) <= maxPointApps {
		return append(make([]AppRate, 0, len(all)), all...)
	}
	keep := maxPointApps - 1
	out := make([]AppRate, keep, maxPointApps)
	copy(out, all[:keep])
	rest := AppRate{App: otherApp}
	for _, a := range all[keep:] {
		rest.Up += a.Up
		rest.Down += a.Down
	}
	return append(out, rest)
}

// sample takes every live flow's rate over its own interval and appends one
// point to the ring of each device with a known MAC. It runs once per Sync,
// after the dump is applied; step is the time since the previous Sync, 0 on
// the first. The caller holds the lock.
func (t *Table) sample(now time.Time, step time.Duration) {
	byDev := map[string]map[string]*[2]float64{}
	for _, en := range t.live {
		en.RateUp, en.RateDown = 0, 0
		if secs := now.Sub(en.rateAt).Seconds(); secs > 0 {
			en.RateUp = float64(en.sinceUp) / secs
			en.RateDown = float64(en.sinceDown) / secs
		}
		if en.sinceUp > 0 || en.sinceDown > 0 {
			en.addWin(now, en.sinceUp, en.sinceDown)
		}
		en.sinceUp, en.sinceDown, en.rateAt = 0, 0, now
		if step <= 0 {
			continue // no interval yet for a point to cover
		}
		t.resolve(en)
		// The ring is read by MAC. An address nobody has named would take a
		// slot no reader asks for, and a flood of them could push out the
		// device being watched.
		if en.MAC == "" || en.MAC == RouterKey {
			continue
		}
		apps := byDev[en.MAC]
		if apps == nil {
			apps = map[string]*[2]float64{}
			byDev[en.MAC] = apps
		}
		app := en.App.ID
		if !en.fixed || app == "" {
			app = unknownApp
		}
		if en.RateUp+en.RateDown > 0 {
			r := apps[app]
			if r == nil {
				r = &[2]float64{}
				apps[app] = r
			}
			r[0] += en.RateUp
			r[1] += en.RateDown
		}
	}
	if step <= 0 {
		return
	}
	at := now.UnixNano()
	for dev, apps := range byDev {
		r := t.series[dev]
		if r == nil {
			r = &ring{}
			t.series[dev] = r
		}
		r.active, r.rate = now, 0
		for _, v := range apps {
			r.rate += v[0] + v[1]
		}
		r.push(point{at: at, step: step, apps: compact(apps)})
	}
	for dev, r := range t.series {
		if byDev[dev] != nil {
			continue
		}
		if now.Sub(r.active) >= liveIdle {
			delete(t.series, dev)
			continue
		}
		// A quiet point keeps the chart moving at the pace of the syncs.
		r.push(point{at: at, step: step})
	}
	if len(t.series) <= liveDeviceCap {
		return
	}
	// Past the cap the devices live longest ago go first; among devices live
	// now, the slowest. One sort a Sync, and only in a flood.
	devs := make([]string, 0, len(t.series))
	for dev := range t.series {
		devs = append(devs, dev)
	}
	sort.Slice(devs, func(i, j int) bool {
		a, b := t.series[devs[i]], t.series[devs[j]]
		if !a.active.Equal(b.active) {
			return a.active.After(b.active)
		}
		if a.rate != b.rate {
			return a.rate > b.rate
		}
		return strings.Compare(devs[i], devs[j]) < 0
	})
	for _, dev := range devs[liveDeviceCap:] {
		delete(t.series, dev)
	}
}

// LiveSeries returns a copy of a device's live ring, newest last, or nil for
// a device with none.
func (t *Table) LiveSeries(mac string) []LivePoint {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.series[mac]
	if r == nil {
		return nil
	}
	out := make([]LivePoint, 0, len(r.pts))
	for i := range r.pts {
		p := r.pts[(r.head+i)%len(r.pts)]
		out = append(out, LivePoint{
			At: time.Unix(0, p.at).UTC(), Step: p.step,
			Apps: append([]AppRate(nil), p.apps...),
		})
	}
	return out
}

// LiveOf returns a copy of one device's live flows, newest first. Only its
// own rows are copied and sorted: a live view polls this every 2 s, and the
// table may hold thousands of flows of other devices.
func (t *Table) LiveOf(mac string) []Flow {
	if mac == "" || mac == RouterKey {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var ens []*entry
	for _, en := range t.live {
		t.resolve(en)
		if en.MAC == mac {
			ens = append(ens, en)
		}
	}
	sort.Slice(ens, func(i, j int) bool { return ens[i].seq > ens[j].seq })
	var out []Flow
	for _, en := range ens {
		out = append(out, en.Flow)
	}
	return out
}
