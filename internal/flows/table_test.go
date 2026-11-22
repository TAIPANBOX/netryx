package flows

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"netryx/internal/ct"
	"netryx/internal/enrich"
)

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

const (
	phone     = "192.168.1.115"
	phoneMAC  = "4e:c2:94:9f:07:79"
	laptop    = "192.168.1.116"
	nas       = "192.168.1.20"
	youtube   = "142.250.180.14"
	elsewhere = "81.2.69.160"
)

var (
	appYouTube = enrich.App{ID: "youtube", Name: "YouTube", Category: "video"}
	appGoogle  = enrich.App{ID: "google", Name: "Google", Category: "cloud"}
)

// fakes stands in for the collector, the address databases and the rollup.
type fakes struct {
	mu     sync.Mutex
	names  map[string][2]string
	macs   map[string]string
	deltas []Delta
}

func newFakes() *fakes {
	return &fakes{names: map[string][2]string{}, macs: map[string]string{phone: phoneMAC}}
}

func (f *fakes) name(local, remote, name, via string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.names[local+">"+remote] = [2]string{name, via}
}

func (f *fakes) options() Options {
	return Options{
		MaxLive: 100, MaxJournal: 100, NameWait: 10 * time.Second,
		IsLocal: func(a netip.Addr) bool {
			return netip.MustParsePrefix("192.168.0.0/16").Contains(a)
		},
		MACOf: func(addr string) string {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.macs[addr]
		},
		NameOf: func(local, remote string) (string, string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			n := f.names[local+">"+remote]
			return n[0], n[1]
		},
		Geo: func(a netip.Addr) (enrich.ASInfo, bool) {
			switch {
			case netip.MustParsePrefix("142.250.0.0/16").Contains(a):
				return enrich.ASInfo{CC: "US", ASN: 15169, Org: "GOOGLE"}, true
			case netip.MustParsePrefix("81.2.0.0/16").Contains(a):
				return enrich.ASInfo{CC: "GB", ASN: 20712, Org: "ANDREWS-ARNOLD"}, true
			}
			return enrich.ASInfo{}, false
		},
		App: func(name, org string) enrich.App {
			switch {
			case strings.HasSuffix(strings.ToLower(name), "youtube.com"):
				return appYouTube
			case strings.Contains(org, "GOOGLE"):
				return appGoogle
			}
			return enrich.Unknown
		},
		Sink: func(d Delta) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.deltas = append(f.deltas, d)
		},
	}
}

func (f *fakes) got() []Delta {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Delta{}, f.deltas...)
}

// total is what a rollup fed by the sink would hold.
func (f *fakes) total() (up, down uint64, flows int) {
	for _, d := range f.got() {
		up += d.Up
		down += d.Down
		if d.First {
			flows++
		}
	}
	return
}

// event builds a TCP connection the way ct reports it, with no NAT: the
// reply tuple is the original one turned round.
func event(kind ct.Kind, id uint32, src string, sport uint16, dst string, dport uint16, up, down uint64) ct.Event {
	return ct.Event{
		Kind: kind, ID: id,
		Orig:  ct.Tuple{SrcIP: net.ParseIP(src), DstIP: net.ParseIP(dst), Proto: 6, SrcPort: sport, DstPort: dport},
		Reply: ct.Tuple{SrcIP: net.ParseIP(dst), DstIP: net.ParseIP(src), Proto: 6, SrcPort: dport, DstPort: sport},
		Up:    ct.Counters{Packets: up / 1000, Bytes: up},
		Down:  ct.Counters{Packets: down / 1000, Bytes: down},
	}
}

func watch(id uint32, up, down uint64) ct.Event {
	return event(ct.Update, id, phone, 51000+uint16(id), youtube, 443, up, down)
}

func live(t *testing.T, tb *Table, id uint32) Flow {
	t.Helper()
	for _, f := range tb.Live() {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("flow %d is not live: %+v", id, tb.Live())
	return Flow{}
}

func TestDirectionComesFromWhichSideIsLocal(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51000, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.New, 2, elsewhere, 40000, nas, 22, 0, 0), t0)
	tb.Apply(event(ct.New, 3, laptop, 52000, nas, 445, 0, 0), t0)
	for id, want := range map[uint32]Direction{1: Out, 2: In, 3: Local} {
		if got := live(t, tb, id).Dir; got != want {
			t.Errorf("flow %d: direction %q, want %q", id, got, want)
		}
	}
	out := live(t, tb, 1)
	if out.Local != netip.MustParseAddrPort(phone+":51000") || out.Remote != netip.MustParseAddrPort(youtube+":443") {
		t.Errorf("an outgoing flow's local end is the device that opened it: %+v", out)
	}
	if out.MAC != phoneMAC {
		t.Errorf("and it carries that device's hardware address: %q", out.MAC)
	}
}

// For a flow opened from outside, ct's original direction is the stranger
// talking to the house. A NAS answering a backup uploads, and counting that
// as its download would be a silent wrong answer on every chart.
func TestAnInboundFlowIsCountedFromTheDevicesSide(t *testing.T) {
	f := newFakes()
	f.macs[nas] = "00:11:32:aa:bb:cc"
	tb := NewTable(f.options())
	// A port forward: the stranger dials the router's WAN address and the
	// reply comes from the NAS, which is the only place the device appears.
	e := event(ct.New, 7, elsewhere, 40000, "203.0.113.5", 2222, 0, 0)
	e.Reply = ct.Tuple{SrcIP: net.ParseIP(nas), DstIP: net.ParseIP(elsewhere), Proto: 6, SrcPort: 22, DstPort: 40000}
	tb.Apply(e, t0)
	fl := live(t, tb, 7)
	if fl.Local != netip.MustParseAddrPort(nas+":22") || fl.Remote != netip.MustParseAddrPort(elsewhere+":40000") {
		t.Fatalf("the local end of an inbound flow is the device behind the forward: %+v", fl)
	}
	if fl.MAC != "00:11:32:aa:bb:cc" {
		t.Fatalf("and the hardware address is that device's: %q", fl.MAC)
	}
	e.Kind, e.Up, e.Down = ct.Destroy, ct.Counters{Bytes: 300}, ct.Counters{Bytes: 90000}
	tb.Apply(e, t0.Add(time.Second))
	up, down, _ := f.total()
	if up != 90000 || down != 300 {
		t.Fatalf("what the device sent is its upload: up %d down %d", up, down)
	}
}

// ct NEW fires on the SYN, before any ClientHello. A flow counted the moment
// it appears would be counted under no name, and the YouTube bytes would land
// in "unknown" for ever.
func TestDeltasWaitForTheName(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 1000, 9000)}, t0.Add(5*time.Second))
	if d := f.got(); len(d) != 0 {
		t.Fatalf("nothing may be emitted before the flow has a name: %+v", d)
	}
	f.name(phone, youtube, "youtube.com", "sni")
	tb.Sync([]ct.Event{watch(1, 1500, 12000)}, t0.Add(6*time.Second))
	d := f.got()
	if len(d) != 1 {
		t.Fatalf("the held bytes come out in exactly one delta: %+v", d)
	}
	if d[0].App != "youtube" || d[0].Up != 1500 || d[0].Down != 12000 || d[0].Domain != "youtube.com" {
		t.Fatalf("under the name the flow turned out to have: %+v", d[0])
	}
	if !d[0].First || d[0].MAC != phoneMAC || d[0].CC != "US" || d[0].ASN != 15169 {
		t.Fatalf("stamped with the device, the place, and as the flow's first: %+v", d[0])
	}
	if fl := live(t, tb, 1); fl.Name != "youtube.com" || fl.NameSrc != "sni" || fl.App != appYouTube {
		t.Fatalf("and the live row says the same: %+v", fl)
	}
}

func TestUnnamedFlowsAreEmittedAfterTheWait(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 300, 400)}, t0.Add(5*time.Second))
	tb.Sync([]ct.Event{watch(1, 700, 800)}, t0.Add(11*time.Second))
	d := f.got()
	if len(d) != 1 {
		t.Fatalf("a flow that never gets a name is not held for ever: %+v", d)
	}
	if d[0].App != "google" || d[0].Domain != "" || d[0].Up != 700 || d[0].Down != 800 || !d[0].First {
		t.Fatalf("it goes out under its network's owner, with no domain: %+v", d[0])
	}
	if fl := live(t, tb, 1); fl.NameSrc != "none" {
		t.Fatalf("and it says it was not named: %+v", fl)
	}
}

// A flow that ends before the name window closes still moved its bytes. Losing
// the held sum on Destroy is the quietest way to undercount a whole house of
// short connections.
func TestHeldBytesAreNotLostWhenAFlowEndsUnnamed(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 1000, 2000)}, t0.Add(2*time.Second))
	end := watch(1, 1200, 2500)
	end.Kind = ct.Destroy
	tb.Apply(end, t0.Add(3*time.Second))
	up, down, flows := f.total()
	if up != 1200 || down != 2500 || flows != 1 {
		t.Fatalf("every held byte comes out when the flow ends: up %d down %d flows %d", up, down, flows)
	}
	if len(tb.Live()) != 0 {
		t.Fatalf("and the flow is no longer live: %+v", tb.Live())
	}
}

func TestCountersNeverCountTwice(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 100, 100)}, t0.Add(5*time.Second))
	tb.Sync([]ct.Event{watch(1, 300, 500)}, t0.Add(10*time.Second))
	end := watch(1, 300, 500)
	end.Kind = ct.Destroy
	tb.Apply(end, t0.Add(11*time.Second))
	up, down, flows := f.total()
	if up != 300 || down != 500 {
		t.Fatalf("the totals are the final counters, not their sum: up %d down %d", up, down)
	}
	if flows != 1 {
		t.Fatalf("one flow is counted once: %d", flows)
	}
}

func TestACounterThatFallsIsANewFlow(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 5000, 100)}, t0.Add(5*time.Second))
	tb.Sync([]ct.Event{watch(1, 200, 300)}, t0.Add(10*time.Second))
	d := f.got()
	if len(d) != 2 {
		t.Fatalf("two syncs with movement are two deltas: %+v", d)
	}
	// Both directions start again: the down counter rising from 100 to 300
	// is not 200 more bytes of the old flow, it is 300 bytes of a new one.
	if d[1].Up != 200 || d[1].Down != 300 {
		t.Fatalf("a counter that falls is a new flow's whole value: %+v", d[1])
	}
}

// A dump is taken a moment before it is applied. A Destroy that lands in
// between must not let the dump bring the flow back as a new one, which would
// count it twice and leave a ghost in the live table.
func TestADumpThatRacesADestroyDoesNotBringTheFlowBack(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 100, 100)}, t0.Add(5*time.Second))
	end := watch(1, 300, 300)
	end.Kind = ct.Destroy
	tb.Apply(end, t0.Add(9*time.Second))
	tb.Sync([]ct.Event{watch(1, 200, 200)}, t0.Add(10*time.Second))
	if l := tb.Live(); len(l) != 0 {
		t.Fatalf("a destroyed flow stays destroyed: %+v", l)
	}
	if up, down, flows := f.total(); up != 300 || down != 300 || flows != 1 {
		t.Fatalf("and its bytes and its count are what Destroy said: up %d down %d flows %d", up, down, flows)
	}
	if rows, _ := tb.Query(Query{}); len(rows) != 1 {
		t.Fatalf("and it is journaled once: %+v", rows)
	}
}

// A flow the table first meets in a dump has been running since before the
// service started, or its NEW was lost. Its counter is history: counting it as
// the last five seconds would draw a spike that never happened.
func TestAFlowFirstSeenInADumpCountsFromThere(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	tb := NewTable(f.options())
	tb.Sync([]ct.Event{watch(1, 50000, 900000)}, t0)
	tb.Sync([]ct.Event{watch(1, 51000, 905000)}, t0.Add(5*time.Second))
	if up, down, flows := f.total(); up != 1000 || down != 5000 || flows != 1 {
		t.Fatalf("only what moved while watched is counted: up %d down %d flows %d", up, down, flows)
	}
}

// A flow whose Destroy was lost would otherwise stay live for ever. One
// missing dump is not enough: a NEW applied after the dump was taken is
// missing from it and is very much alive.
func TestOnlyAFlowMissingFromTwoDumpsHasEnded(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 100, 200)}, t0.Add(5*time.Second))
	tb.Sync(nil, t0.Add(10*time.Second))
	if len(tb.Live()) != 1 {
		t.Fatalf("one missing dump is not an ending: %+v", tb.Live())
	}
	tb.Sync(nil, t0.Add(15*time.Second))
	if len(tb.Live()) != 0 {
		t.Fatalf("two are: %+v", tb.Live())
	}
	if up, down, flows := f.total(); up != 100 || down != 200 || flows != 1 {
		t.Fatalf("and what it held comes out: up %d down %d flows %d", up, down, flows)
	}
}

func TestTheLiveTableIsBounded(t *testing.T) {
	f := newFakes()
	o := f.options()
	o.MaxLive = 3
	tb := NewTable(o)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 4000, 8000)}, t0.Add(time.Second))
	for id := uint32(2); id <= 5; id++ {
		tb.Apply(event(ct.New, id, phone, 51000+uint16(id), youtube, 443, 0, 0), t0.Add(time.Duration(id)*time.Second))
	}
	l := tb.Live()
	if len(l) != 3 {
		t.Fatalf("the live table holds what it is allowed to: %d", len(l))
	}
	for _, fl := range l {
		if fl.ID < 3 {
			t.Fatalf("the least recently updated flows are the ones let go: %+v", l)
		}
	}
	rows, _ := tb.Query(Query{})
	if len(rows) != 2 || rows[0].ID != 2 || rows[1].ID != 1 {
		t.Fatalf("and they go to the journal, not nowhere: %+v", rows)
	}
	if up, down, _ := f.total(); up != 4000 || down != 8000 {
		t.Fatalf("with the bytes they were holding: up %d down %d", up, down)
	}
	if tb.Dropped() != 2 {
		t.Fatalf("and the table says how many it pushed out: %d", tb.Dropped())
	}
}

func TestRatesArePerDevicePerSecond(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.New, 2, phone, 51002, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 100, 1000), watch(2, 0, 0)}, t0.Add(5*time.Second))
	tb.Sync([]ct.Event{watch(1, 600, 4000), watch(2, 0, 2000)}, t0.Add(10*time.Second))
	r, ok := tb.Rates()[phoneMAC]
	if !ok {
		t.Fatalf("the device has a rate: %+v", tb.Rates())
	}
	if r.Down != 1000 || r.Up != 100 {
		t.Fatalf("5000 bytes down and 500 up over 5 s, across both flows: %+v", r)
	}
}

// Every table is read by HTTP while the watcher and the ticker write it.
func TestTheTableIsSafeToReadWhileItIsWritten(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	tb := NewTable(f.options())
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := uint32(1); i <= 200; i++ {
			tb.Apply(event(ct.New, i, phone, uint16(i), youtube, 443, 0, 0), t0)
			e := watch(i, 10, 10)
			e.Kind = ct.Destroy
			tb.Apply(e, t0.Add(time.Second))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			tb.Sync([]ct.Event{watch(uint32(i), 5, 5)}, t0.Add(time.Duration(i)*time.Second))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			tb.Live()
			tb.Rates()
			tb.Query(Query{Text: "you"})
		}
	}()
	wg.Wait()
}

// The service may start before the databases have loaded. A table with
// nothing wired still counts, under the device's address and "unknown".
func TestATableWithNothingWiredStillCounts(t *testing.T) {
	var got []Delta
	tb := NewTable(Options{Sink: func(d Delta) { got = append(got, d) }})
	tb.Apply(event(ct.New, 1, laptop, 51001, youtube, 443, 0, 0), t0)
	e := event(ct.Update, 1, laptop, 51001, youtube, 443, 100, 200)
	e.Status = 1 << 14 // handed to the flow table: counters arrive in bursts
	tb.Sync([]ct.Event{e}, t0.Add(11*time.Second))
	if len(got) != 1 || got[0].MAC != laptop || got[0].App != "unknown" || got[0].CC != "--" || got[0].Up != 100 {
		t.Fatalf("a device with no known hardware address is filed under its address: %+v", got)
	}
	if fl := live(t, tb, 1); fl.Dir != Out || !fl.Offloaded {
		t.Fatalf("private addresses are local by default, and an offloaded flow says so: %+v", fl)
	}
}

// A NEW lost to a full socket buffer leaves only the Destroy, and its final
// counters are the whole flow. A second Destroy for the same flow is nothing.
func TestADestroyForAnUnseenFlowCountsItWholeOnce(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	end := watch(1, 700, 900)
	end.Kind = ct.Destroy
	tb.Apply(end, t0)
	tb.Apply(end, t0.Add(time.Second))
	if up, down, flows := f.total(); up != 700 || down != 900 || flows != 1 {
		t.Fatalf("counted whole, and once: up %d down %d flows %d", up, down, flows)
	}
	if rows, _ := tb.Query(Query{}); len(rows) != 1 {
		t.Fatalf("and journaled once: %+v", rows)
	}
}

// A connection that never got an answer moved nothing, and is still a flow:
// the rollup counts flows from the First delta, so it must come out once.
func TestAFlowThatMovedNothingIsStillCountedOnce(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	end := watch(1, 0, 0)
	end.Kind = ct.Destroy
	tb.Apply(end, t0.Add(3*time.Second))
	d := f.got()
	if len(d) != 1 || !d[0].First || d[0].Up != 0 || d[0].Down != 0 {
		t.Fatalf("one empty first delta: %+v", d)
	}
}

// ct frees an entry and a periodic sender (NTP, a WireGuard handshake) opens
// it again with the same id and tuple. The second flow is real traffic.
func TestAKeyReusedAfterDestroyIsANewFlow(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 100, 100)}, t0.Add(5*time.Second))
	end := watch(1, 300, 300)
	end.Kind = ct.Destroy
	tb.Apply(end, t0.Add(6*time.Second))
	// No dump between: the first one after the Destroy is the one that could
	// be stale, and the NEW is what says this row is not.
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0.Add(8*time.Second))
	tb.Sync([]ct.Event{watch(1, 50, 70)}, t0.Add(25*time.Second))
	if len(tb.Live()) != 1 {
		t.Fatalf("the second flow is live: %+v", tb.Live())
	}
	if up, down, flows := f.total(); up != 350 || down != 370 || flows != 2 {
		t.Fatalf("and its bytes and its count are added: up %d down %d flows %d", up, down, flows)
	}
	end2 := watch(1, 60, 80)
	end2.Kind = ct.Destroy
	tb.Apply(end2, t0.Add(26*time.Second))
	if up, down, _ := f.total(); up != 360 || down != 380 || len(tb.Live()) != 0 {
		t.Fatalf("and its own Destroy is not mistaken for the first one's: up %d down %d live %d", up, down, len(tb.Live()))
	}
}

// A dump taken a moment before a Destroy carries the counters it had then,
// which can equal the final ones. That row is the old flow, not a new one.
func TestAStaleDumpRowForAFinishedKeyIsStillIgnored(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	end := watch(1, 300, 300)
	end.Kind = ct.Destroy
	tb.Apply(end, t0.Add(4*time.Second))
	tb.Sync([]ct.Event{watch(1, 300, 300)}, t0.Add(5*time.Second))
	if len(tb.Live()) != 0 {
		t.Fatalf("a stale row does not bring the flow back: %+v", tb.Live())
	}
	if up, down, flows := f.total(); up != 300 || down != 300 || flows != 1 {
		t.Fatalf("and adds nothing: up %d down %d flows %d", up, down, flows)
	}
}

// When the NEW for a re-created entry is lost, the key is back in the kernel
// with no event to say so. Only the first dump after a Destroy can be stale;
// a later one that still holds the key holds a new flow.
func TestAFinishedKeyBackInALaterDumpIsANewFlow(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	end := watch(1, 300, 300)
	end.Kind = ct.Destroy
	tb.Apply(end, t0.Add(4*time.Second))
	tb.Sync([]ct.Event{watch(1, 200, 200)}, t0.Add(5*time.Second))
	tb.Sync([]ct.Event{watch(1, 40, 60)}, t0.Add(10*time.Second))
	if len(tb.Live()) != 1 {
		t.Fatalf("the re-created flow is live: %+v", tb.Live())
	}
	if up, down, flows := f.total(); up != 340 || down != 360 || flows != 2 {
		t.Fatalf("and counted whole, as a flow of its own: up %d down %d flows %d", up, down, flows)
	}
}

// Every NEW into a full table evicts one flow. A scan of the whole table per
// eviction under the shared lock is what a busy house would turn into a stall.
func TestEvictionIsConstantTime(t *testing.T) {
	for _, max := range []int{1000, 16000} {
		f := newFakes()
		o := f.options()
		o.MaxLive, o.MaxJournal = max, 10
		tb := NewTable(o)
		for i := 0; i < max; i++ {
			tb.Apply(event(ct.New, uint32(i+1), phone, 1, youtube, 443, 0, 0), t0)
		}
		tb.mu.Lock()
		tb.examined = 0
		tb.mu.Unlock()
		for i := 0; i < 100; i++ {
			tb.Apply(event(ct.New, uint32(max+i+1), phone, 1, youtube, 443, 0, 0), t0.Add(time.Second))
		}
		tb.mu.Lock()
		n := tb.examined
		tb.mu.Unlock()
		if n > 100 {
			t.Fatalf("MaxLive %d: 100 evictions looked at %d entries", max, n)
		}
	}
}

func TestAnEvictedFlowIsMarkedInTheJournal(t *testing.T) {
	f := newFakes()
	o := f.options()
	o.MaxLive = 1
	tb := NewTable(o)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.New, 2, phone, 51002, youtube, 443, 0, 0), t0.Add(3*time.Second))
	end := watch(2, 10, 10)
	end.Kind = ct.Destroy
	tb.Apply(end, t0.Add(4*time.Second))
	rows, _ := tb.Query(Query{})
	if len(rows) != 2 || rows[1].ID != 1 || !rows[1].Evicted || !rows[1].End.Equal(t0.Add(3*time.Second)) {
		t.Fatalf("a flow pushed out says so, with when: %+v", rows)
	}
	if rows[0].Evicted {
		t.Fatalf("a flow that ended is not marked evicted: %+v", rows[0])
	}
}

// A flow pushed out while open comes back in the next dump. Its bytes from
// then on are counted; the ones it moved while out are lost and said to be,
// and nothing is counted twice, the flow included.
func TestAnEvictedFlowThatReturnsIsCountedFromWhereItCameBack(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	o := f.options()
	o.MaxLive = 2
	tb := NewTable(o)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.New, 2, phone, 51002, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 1000, 2000), watch(2, 10, 10)}, t0.Add(5*time.Second))
	tb.Apply(event(ct.New, 3, phone, 51003, youtube, 443, 0, 0), t0.Add(6*time.Second)) // pushes 1 out
	end := watch(3, 0, 0)
	end.Kind = ct.Destroy
	tb.Apply(end, t0.Add(7*time.Second))
	tb.Sync([]ct.Event{watch(1, 1500, 2600), watch(2, 10, 10)}, t0.Add(10*time.Second))
	tb.Sync([]ct.Event{watch(1, 1800, 3000), watch(2, 10, 10)}, t0.Add(15*time.Second))
	if up, down, flows := f.total(); up != 1000+10+300 || down != 2000+10+400 || flows != 3 {
		t.Fatalf("counted up to eviction and from its return, once: up %d down %d flows %d", up, down, flows)
	}
	if lost := tb.LostBytes(); lost != 500+600 {
		t.Fatalf("the gap is lost and counted as lost: %d", lost)
	}
	if fl := live(t, tb, 1); fl.Name != "youtube.com" {
		t.Fatalf("and it is live again: %+v", fl)
	}
}

// A counter that falls is a new connection under the old key. It is named
// afresh: inheriting the old flow's app would file its bytes wrongly, and not
// marking it first would undercount flows.
func TestAFlowUnderAReusedIDIsNamedAfresh(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 5000, 9000)}, t0.Add(5*time.Second))
	f.name(phone, youtube, "", "")
	tb.Sync([]ct.Event{watch(1, 200, 300)}, t0.Add(30*time.Second))
	if d := f.got(); len(d) != 1 {
		t.Fatalf("the new flow waits for its own name: %+v", d)
	}
	f.name(phone, youtube, "maps.google.com", "sni")
	tb.Sync([]ct.Event{watch(1, 400, 500)}, t0.Add(35*time.Second))
	d := f.got()
	if len(d) != 2 || d[1].App != "google" || d[1].Domain != "maps.google.com" || !d[1].First || d[1].Up != 400 || d[1].Down != 500 {
		t.Fatalf("and comes out under it, as a flow of its own: %+v", d)
	}
	if fl := live(t, tb, 1); fl.Name != "maps.google.com" || !fl.Start.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("the live row is the new flow: %+v", fl)
	}
}

// The flow that is moving bytes is the one a person is watching. The idle
// one goes first.
func TestAFlowThatMovesIsNotTheOneEvicted(t *testing.T) {
	f := newFakes()
	o := f.options()
	o.MaxLive = 2
	tb := NewTable(o)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.New, 2, phone, 51002, youtube, 443, 0, 0), t0.Add(time.Second))
	tb.Sync([]ct.Event{watch(1, 100, 100), watch(2, 0, 0)}, t0.Add(2*time.Second))
	tb.Apply(event(ct.New, 3, phone, 51003, youtube, 443, 0, 0), t0.Add(3*time.Second))
	live(t, tb, 1)
	live(t, tb, 3)
}

// A flow pushed out and then destroyed before any dump brought it back: its
// row is already in the journal and its first delta already out, so the
// Destroy only says how much was lost while it was out.
func TestADestroyForAnEvictedFlowAddsOnlyToLost(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	o := f.options()
	o.MaxLive = 1
	tb := NewTable(o)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 100, 100)}, t0.Add(5*time.Second))
	tb.Apply(event(ct.New, 2, phone, 51002, youtube, 443, 0, 0), t0.Add(6*time.Second))
	end := watch(1, 150, 170)
	end.Kind = ct.Destroy
	tb.Apply(end, t0.Add(7*time.Second))
	if up, down, flows := f.total(); up != 100 || down != 100 || flows != 1 {
		t.Fatalf("nothing is counted twice: up %d down %d flows %d", up, down, flows)
	}
	if lost := tb.LostBytes(); lost != 50+70 {
		t.Fatalf("the gap is counted as lost: %d", lost)
	}
	if rows, _ := tb.Query(Query{}); len(rows) != 1 || !rows[0].Evicted {
		t.Fatalf("and the journal keeps its one row: %+v", rows)
	}
	live(t, tb, 2)
}

// An unnamed flow still holding bytes when its key is reused: those bytes are
// the old flow's, and go out under its stamp before the new one starts.
func TestWhatTheOldFlowHeldIsNotLostWhenItsKeyIsReused(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 1000, 2000)}, t0.Add(2*time.Second))
	tb.Sync([]ct.Event{watch(1, 100, 100)}, t0.Add(4*time.Second))
	d := f.got()
	if len(d) != 1 || d[0].Up != 1000 || d[0].Down != 2000 || !d[0].First {
		t.Fatalf("the old flow's held bytes come out, and only those: %+v", d)
	}
}

// A flow pushed out, then destroyed, then listed by a dump taken before that
// Destroy: the row is stale, and must not bring the flow back as a new one.
// A late duplicate Destroy is nothing too.
func TestAStaleRowAfterAnEvictedFlowEndsDoesNotBringItBack(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	o := f.options()
	o.MaxLive = 2
	tb := NewTable(o)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.New, 2, phone, 51002, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 100, 100), watch(2, 10, 10)}, t0.Add(5*time.Second))
	tb.Apply(event(ct.New, 3, phone, 51003, youtube, 443, 0, 0), t0.Add(6*time.Second)) // pushes 1 out
	end := watch(1, 150, 170)
	end.Kind = ct.Destroy
	tb.Apply(end, t0.Add(7*time.Second))
	tb.Sync([]ct.Event{watch(1, 140, 160), watch(2, 10, 10), watch(3, 0, 0)}, t0.Add(8*time.Second))
	tb.Apply(end, t0.Add(9*time.Second))
	if _, _, flows := f.total(); flows != 3 {
		t.Fatalf("three connections are three flows: %d", flows)
	}
	for _, fl := range tb.Live() {
		if fl.ID == 1 {
			t.Fatalf("the ended flow is not live again: %+v", tb.Live())
		}
	}
	rows, _ := tb.Query(Query{})
	n := 0
	for _, r := range rows {
		if r.ID == 1 {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("and it has one journal row: %+v", rows)
	}
	if lost := tb.LostBytes(); lost != 50+70 {
		t.Fatalf("the gap is counted once: %d", lost)
	}
}

// A house with more open flows than the table holds churns: every dump
// readmits some and pushes others out. Each connection is journaled once, or
// the churn would flush the journal within minutes. Six flows against a table
// of two keeps four out at once, more than the table's own size.
func TestAFlowEvictedRepeatedlyHasOneJournalRow(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	o := f.options()
	o.MaxLive, o.MaxJournal = 2, 1000
	tb := NewTable(o)
	for i := uint64(1); i <= 6; i++ {
		var dump []ct.Event
		for id := uint32(1); id <= 6; id++ {
			dump = append(dump, watch(id, i*100, i*100))
		}
		tb.Sync(dump, t0.Add(time.Duration(i)*5*time.Second))
	}
	for id := uint32(1); id <= 6; id++ {
		end := watch(id, 1000, 1000)
		end.Kind = ct.Destroy
		tb.Apply(end, t0.Add(time.Minute))
	}
	rows, _ := tb.Query(Query{Limit: 500})
	per := map[uint32]int{}
	for _, r := range rows {
		per[r.ID]++
	}
	for id := uint32(1); id <= 6; id++ {
		if per[id] != 1 {
			t.Fatalf("each connection has one journal row: %v (%d rows)", per, len(rows))
		}
	}
	if _, _, flows := f.total(); flows != 6 {
		t.Fatalf("and is counted as one flow: %d", flows)
	}
}

// With the eviction memory full, readmitting a flow evicts another, whose
// record takes the oldest slot. If that slot is the returning flow's own, its
// record must have been read first, or it comes back as a stranger.
func TestAReturningFlowIsRecognisedWhenTheEvictionMemoryIsFull(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	o := f.options()
	o.MaxLive = 1 // the eviction memory holds four
	tb := NewTable(o)
	for id := uint32(1); id <= 5; id++ {
		tb.Apply(event(ct.New, id, phone, 51000+uint16(id), youtube, 443, 0, 0), t0.Add(time.Duration(id)*time.Second))
	}
	tb.Sync([]ct.Event{watch(1, 500, 500)}, t0.Add(10*time.Second))
	if _, _, flows := f.total(); flows != 5 {
		t.Fatalf("five connections are five flows: %d", flows)
	}
	if lost := tb.LostBytes(); lost != 1000 {
		t.Fatalf("what flow 1 moved while out is counted as lost: %d", lost)
	}
	rows, _ := tb.Query(Query{})
	n := 0
	for _, r := range rows {
		if r.ID == 1 {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("and it keeps its one journal row: %+v", rows)
	}
}

// A flow pushed out and readmitted already has its journal row. A new
// connection under the same key, seen by its falling counter, is another
// connection and gets a row of its own.
func TestANewConnectionOnAReturnedFlowsKeyGetsItsOwnJournalRow(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "youtube.com", "sni")
	o := f.options()
	o.MaxLive = 1
	tb := NewTable(o)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.New, 2, phone, 51002, youtube, 443, 0, 0), t0.Add(time.Second))
	tb.Sync([]ct.Event{watch(1, 500, 500)}, t0.Add(5*time.Second))
	tb.Sync([]ct.Event{watch(1, 10, 10)}, t0.Add(10*time.Second))
	end := watch(1, 20, 20)
	end.Kind = ct.Destroy
	tb.Apply(end, t0.Add(11*time.Second))
	rows, _ := tb.Query(Query{})
	n := 0
	for _, r := range rows {
		if r.ID == 1 {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("two connections under key 1 are two journal rows: %+v", rows)
	}
	if rows[0].ID != 1 || rows[0].Evicted {
		t.Fatalf("and the new one's row does not claim it was pushed out: %+v", rows[0])
	}
	if _, _, flows := f.total(); flows != 3 {
		t.Fatalf("and three flows in all: %d", flows)
	}
}

// The console shows how each domain was named; the rollup can only keep what
// the delta carries.
func TestADeltaSaysHowItsFlowWasNamed(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	f.name(phone, youtube, "youtube.com", "quic")
	tb.Sync([]ct.Event{watch(1, 100, 1000)}, t0.Add(time.Second))
	tb.Sync([]ct.Event{watch(1, 200, 3000)}, t0.Add(2*time.Second))
	d := f.got()
	if len(d) != 2 || d[0].NameSrc != "quic" || d[1].NameSrc != "quic" {
		t.Fatalf("every delta of a named flow carries how it was named: %+v", d)
	}
	tb.Apply(event(ct.New, 2, phone, 51002, elsewhere, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 200, 3000), watch(2, 5, 5)}, t0.Add(11*time.Second))
	for _, x := range f.got() {
		if x.Domain == "" && x.NameSrc != "none" {
			t.Fatalf("an unnamed flow says so: %+v", x)
		}
	}
}

// A flow with both ends at home never crossed the internet line. It keeps its
// app but carries the country "LAN" and no organisation, so history can keep
// it apart from WAN traffic; no address table is asked about a house address.
func TestALocalFlowIsMarkedLAN(t *testing.T) {
	f := newFakes()
	o := f.options()
	asked := 0
	geo := o.Geo
	o.Geo = func(a netip.Addr) (enrich.ASInfo, bool) { asked++; return geo(a) }
	tb := NewTable(o)
	f.name(phone, nas, "nas.youtube.com", "sni")
	tb.Apply(event(ct.New, 1, phone, 51001, nas, 445, 0, 0), t0)
	tb.Apply(event(ct.Destroy, 1, phone, 51001, nas, 445, 100, 900), t0.Add(time.Second))
	ds := f.got()
	if len(ds) == 0 || ds[0].CC != LANCountry || ds[0].Org != "" || ds[0].ASN != 0 {
		t.Fatalf("a local delta is LAN, with no organisation: %+v", ds)
	}
	if ds[0].App != appYouTube.ID {
		t.Fatalf("it keeps its app: %+v", ds[0])
	}
	if asked != 0 {
		t.Fatalf("no address table is asked about a house address: %d", asked)
	}
	rows, _ := tb.Query(Query{})
	if len(rows) != 1 || rows[0].Geo.CC != LANCountry {
		t.Fatalf("the journal row says LAN too: %+v", rows)
	}
}

// Loopback never leaves the router: an SSH tunnel polling the console from
// 127.0.0.1 is not traffic of anybody's, and must not become a client.
func TestLoopbackIsNotTraffic(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, "127.0.0.1", 40000, "127.0.0.1", 8788, 0, 0), t0)
	tb.Sync([]ct.Event{event(ct.Update, 1, "127.0.0.1", 40000, "127.0.0.1", 8788, 500, 5000)}, t0.Add(5*time.Second))
	tb.Sync([]ct.Event{event(ct.Update, 1, "127.0.0.1", 40000, "127.0.0.1", 8788, 900, 9000)}, t0.Add(10*time.Second))
	tb.Apply(event(ct.Destroy, 2, "::1", 40001, "::1", 8788, 10, 10), t0.Add(11*time.Second))
	tb.Apply(event(ct.Destroy, 1, "127.0.0.1", 40000, "127.0.0.1", 8788, 1000, 10000), t0.Add(12*time.Second))
	if ds := f.got(); len(ds) != 0 {
		t.Fatalf("loopback reached the rollup: %+v", ds)
	}
	if r := tb.Rates(); len(r) != 0 {
		t.Fatalf("loopback has a rate: %+v", r)
	}
	if rows, _ := tb.Query(Query{}); len(rows) != 0 || len(tb.Live()) != 0 {
		t.Fatalf("loopback is a flow: %+v %+v", rows, tb.Live())
	}
}

// A DHCP discover (0.0.0.0 to 255.255.255.255), a limited broadcast and
// multicast have no device and no remote: they never become a row, a rate or
// a key of their own, as 255.255.255.255 once did in the console.
func TestBroadcastAndMulticastAreNotTraffic(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	for i, fl := range []struct {
		src   string
		sport uint16
		dst   string
		dport uint16
	}{
		{"0.0.0.0", 68, "255.255.255.255", 67},
		{laptop, 40100, "255.255.255.255", 9},
		{laptop, 5353, "224.0.0.251", 5353},
		{"fe80::1", 546, "ff02::1:2", 547},
	} {
		id := uint32(10 + i)
		tb.Apply(event(ct.New, id, fl.src, fl.sport, fl.dst, fl.dport, 0, 0), t0)
		tb.Sync([]ct.Event{event(ct.Update, id, fl.src, fl.sport, fl.dst, fl.dport, 300, 300)}, t0.Add(5*time.Second))
		tb.Apply(event(ct.Destroy, id, fl.src, fl.sport, fl.dst, fl.dport, 600, 600), t0.Add(6*time.Second))
	}
	if ds := f.got(); len(ds) != 0 {
		t.Fatalf("broadcast or multicast reached the rollup: %+v", ds)
	}
	if r := tb.Rates(); len(r) != 0 {
		t.Fatalf("broadcast or multicast has a rate: %+v", r)
	}
	if rows, _ := tb.Query(Query{}); len(rows) != 0 || len(tb.Live()) != 0 {
		t.Fatalf("broadcast or multicast is a flow: %+v %+v", rows, tb.Live())
	}
}

// The device a flow's bytes belong to is asked again whenever it is still
// unknown: an address whose hardware address is learned after the flow
// opened has its rate under that hardware address, never under the address.
func TestARateFollowsAnAddressLearnedLater(t *testing.T) {
	const laptopMAC = "aa:bb:cc:00:01:16"
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, laptop, 51001, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.New, 2, laptop, 51002, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{event(ct.Update, 1, laptop, 51001, youtube, 443, 0, 0), event(ct.Update, 2, laptop, 51002, youtube, 443, 0, 0)}, t0.Add(5*time.Second))
	// Learned between two dumps: the next interval's bytes are the laptop's.
	f.mu.Lock()
	f.macs[laptop] = laptopMAC
	f.mu.Unlock()
	tb.Sync([]ct.Event{event(ct.Update, 1, laptop, 51001, youtube, 443, 500, 5000), event(ct.Update, 2, laptop, 51002, youtube, 443, 0, 0)}, t0.Add(10*time.Second))
	r := tb.Rates()
	if r[laptopMAC].Down != 1000 || len(r) != 1 {
		t.Fatalf("the rate is the laptop's, not its address's: %+v", r)
	}

	// Learned only after the dump: the rate already taken stays where its
	// bytes were filed, and the live row says whose the flow is now.
	const nasMAC = "aa:bb:cc:00:00:20"
	tb.Apply(event(ct.New, 3, nas, 51003, youtube, 443, 0, 0), t0.Add(10*time.Second))
	tb.Sync([]ct.Event{event(ct.Update, 3, nas, 51003, youtube, 443, 0, 0)}, t0.Add(15*time.Second))
	tb.Sync([]ct.Event{event(ct.Update, 3, nas, 51003, youtube, 443, 0, 2500)}, t0.Add(20*time.Second))
	f.mu.Lock()
	f.macs[nas] = nasMAC
	f.mu.Unlock()
	if r := tb.Rates(); r[nas].Down != 500 || r[nasMAC] != (Rate{}) {
		t.Fatalf("a rate is never moved to a device learned after its bytes moved: %+v", r)
	}
	if fl := live(t, tb, 3); fl.MAC != nasMAC {
		t.Fatalf("the live row says whose it is too: %+v", fl)
	}
}

// The journal keeps 50 000 finished flows by default: about 18 MB at the
// 360 bytes a row costs, on a router with 128 MB.
func TestTheJournalDefaultsToFiftyThousandRows(t *testing.T) {
	if n := len(NewTable(Options{}).j.ring); n != 50_000 {
		t.Fatalf("journal default: %d", n)
	}
}

// A finished flow belongs to whoever held its address while it ran. Its
// device is asked once more when it ends and then kept: an address the DHCP
// server gives to another device later must not hand that device the old
// traffic, so an unknown stays unknown.
func TestAFinishedFlowIsNotGivenToTheAddressesNextOwner(t *testing.T) {
	const reused = "192.168.50.20"
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, reused, 51001, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.Destroy, 1, reused, 51001, youtube, 443, 100, 900), t0.Add(time.Second))
	f.mu.Lock()
	f.macs[reused] = "aa:bb:cc:00:00:99" // the address's next owner
	f.mu.Unlock()
	rows, _ := tb.Query(Query{})
	if len(rows) != 1 || rows[0].MAC != "" {
		t.Fatalf("the finished flow went to the address's next owner: %+v", rows)
	}
	if rows, _ := tb.Query(Query{MAC: "aa:bb:cc:00:00:99"}); len(rows) != 0 {
		t.Fatalf("a search for the new owner finds the old flow: %+v", rows)
	}
}

// Bytes belong to the device that held the address while they moved. An
// address learned as MAC1 after its flow opened files the next bytes under
// MAC1; when the address later passes to MAC2, MAC1 keeps them and MAC2 has
// none.
func TestBytesAreFiledUnderTheDeviceThatMovedThem(t *testing.T) {
	const a, mac1, mac2 = "192.168.1.40", "aa:bb:cc:00:00:01", "aa:bb:cc:00:00:02"
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, a, 51001, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{event(ct.Update, 1, a, 51001, youtube, 443, 0, 0)}, t0.Add(5*time.Second))
	f.mu.Lock()
	f.macs[a] = mac1
	f.mu.Unlock()
	tb.Sync([]ct.Event{event(ct.Update, 1, a, 51001, youtube, 443, 0, 5000)}, t0.Add(10*time.Second))
	f.mu.Lock()
	f.macs[a] = mac2 // the address passes to another device
	f.mu.Unlock()
	r := tb.Rates()
	if r[mac1].Down != 1000 || r[mac2] != (Rate{}) {
		t.Fatalf("the bytes are MAC1's, who moved them: %+v", r)
	}
}

// A flow's rate is the bytes it moved since its own previous sample over the
// time between the two, so a flow that opened halfway through an interval is
// not diluted by the half it did not exist for, and a flow first met in a dump
// has no rate yet.
func TestAFlowKnowsItsCurrentRate(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Sync(nil, t0)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0.Add(3*time.Second))
	tb.Sync([]ct.Event{watch(1, 1000, 4000), watch(2, 10000, 10000)}, t0.Add(5*time.Second))
	if got := live(t, tb, 1); got.RateUp != 500 || got.RateDown != 2000 {
		t.Fatalf("1000 up and 4000 down in the 2 s since it opened: up %v down %v", got.RateUp, got.RateDown)
	}
	if got := live(t, tb, 2); got.RateUp != 0 || got.RateDown != 0 {
		t.Fatalf("a flow first met in a dump has no rate yet: up %v down %v", got.RateUp, got.RateDown)
	}
	tb.Sync([]ct.Event{watch(1, 1000, 4000), watch(2, 10200, 10600)}, t0.Add(7*time.Second))
	if got := live(t, tb, 1); got.RateUp != 0 || got.RateDown != 0 {
		t.Fatalf("a flow that moved nothing has no rate: up %v down %v", got.RateUp, got.RateDown)
	}
	if got := live(t, tb, 2); got.RateUp != 100 || got.RateDown != 300 {
		t.Fatalf("200 up and 600 down over 2 s: up %v down %v", got.RateUp, got.RateDown)
	}
	tb.Apply(event(ct.Destroy, 2, phone, 51002, youtube, 443, 10200, 10600), t0.Add(8*time.Second))
	rows, _ := tb.Query(Query{})
	for _, r := range rows {
		if r.RateUp != 0 || r.RateDown != 0 {
			t.Fatalf("a finished flow moves nothing now: %+v", r)
		}
	}
}

// The live ring keeps one point a Sync for each device with live flows: its
// rate per app, at most 150 points (five minutes at the 2 s sync), each with
// the step since the Sync before it. The router and loopback are no device,
// and a device with no live flows for five minutes is let go.
func TestTheLiveRingKeepsFiveMinutesPerDevice(t *testing.T) {
	const routerIP = "192.168.1.1"
	f := newFakes()
	f.macs[routerIP] = "router"
	f.name(phone, youtube, "www.youtube.com", "sni")
	tb := NewTable(f.options())
	now := t0
	tb.Sync(nil, now)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), now)
	tb.Apply(event(ct.New, 3, routerIP, 40000, elsewhere, 443, 0, 0), now)
	tb.Apply(event(ct.New, 4, "127.0.0.1", 40001, "127.0.0.1", 8080, 0, 0), now)
	var down uint64
	for i := 0; i < 200; i++ {
		now = now.Add(2 * time.Second)
		down += 2000
		dump := []ct.Event{
			watch(1, down/10, down),
			event(ct.Update, 3, routerIP, 40000, elsewhere, 443, down, down),
			event(ct.Update, 4, "127.0.0.1", 40001, "127.0.0.1", 8080, down, down),
		}
		if i == 199 {
			// A flow still waiting for its name is counted, under unknown.
			tb.Apply(event(ct.New, 2, phone, 51002, elsewhere, 443, 0, 0), now.Add(-time.Second))
			dump = append(dump, event(ct.Update, 2, phone, 51002, elsewhere, 443, 300, 600))
		}
		tb.Sync(dump, now)
	}
	pts := tb.LiveSeries(phoneMAC)
	if len(pts) != 150 {
		t.Fatalf("the ring holds 150 points, not %d", len(pts))
	}
	last := pts[len(pts)-1]
	if !last.At.Equal(now) || !pts[0].At.Equal(now.Add(-149*2*time.Second)) {
		t.Fatalf("newest last, five minutes back: %v .. %v", pts[0].At, last.At)
	}
	if last.Step != 2*time.Second {
		t.Fatalf("each point says the step it covers: %v", last.Step)
	}
	up, dn := pointRates(last)
	if dn["youtube"] != 1000 || up["youtube"] != 100 {
		t.Fatalf("2000 bytes down every 2 s is 1000 B/s for the app: %+v", last)
	}
	if dn["unknown"] != 600 || up["unknown"] != 300 {
		t.Fatalf("an unnamed flow is counted under unknown at its own rate: %+v", last)
	}
	for i := 1; i < len(pts); i++ {
		if !pts[i].At.After(pts[i-1].At) {
			t.Fatalf("points are in time order: %v then %v", pts[i-1].At, pts[i].At)
		}
	}
	for _, dev := range []string{"router", routerIP, "127.0.0.1"} {
		if got := tb.LiveSeries(dev); got != nil {
			t.Fatalf("%s is not a device: %d points", dev, len(got))
		}
	}
	// What a caller gets is a copy.
	last.Apps[0].Down = -1
	if _, d := pointRates(tb.LiveSeries(phoneMAC)[149]); d["youtube"] != 1000 {
		t.Fatal("LiveSeries hands out the ring's own maps")
	}

	// The interval switches to 5 s: the point says so.
	now = now.Add(5 * time.Second)
	tb.Sync([]ct.Event{watch(1, down/10, down+5000)}, now)
	if pts := tb.LiveSeries(phoneMAC); pts[len(pts)-1].Step != 5*time.Second || downOf(pts[len(pts)-1], "youtube") != 1000 {
		t.Fatalf("a 5 s point after 2 s ones: %+v", pts[len(pts)-1])
	}

	// The flows end; the device keeps its ring for five minutes, then goes.
	tb.Apply(event(ct.Destroy, 1, phone, 51001, youtube, 443, down/10, down+5000), now)
	tb.Apply(event(ct.Destroy, 2, phone, 51002, elsewhere, 443, 300, 600), now)
	quiet := now
	for now.Sub(quiet) < 4*time.Minute {
		now = now.Add(5 * time.Second)
		tb.Sync(nil, now)
	}
	if pts := tb.LiveSeries(phoneMAC); len(pts) == 0 || len(pts[len(pts)-1].Apps) != 0 {
		t.Fatalf("four quiet minutes keep the ring, with empty points: %d", len(pts))
	}
	for now.Sub(quiet) <= 5*time.Minute {
		now = now.Add(5 * time.Second)
		tb.Sync(nil, now)
	}
	if pts := tb.LiveSeries(phoneMAC); pts != nil {
		t.Fatalf("five quiet minutes let the device go: %d points", len(pts))
	}
}

// A flood of devices cannot grow the ring past 512 devices: the one whose
// flows were live longest ago goes first.
func TestTheLiveRingIsBoundedInDevices(t *testing.T) {
	f := newFakes()
	o := f.options()
	o.MaxLive = 2000
	tb := NewTable(o)
	ip := func(i int) string { return "192.168." + strconv.Itoa(10+i/200) + "." + strconv.Itoa(1+i%200) }
	mac := func(i int) string { return "02:00:00:00:" + strconv.Itoa(10+i/100) + ":" + strconv.Itoa(10+i%100) }
	for i := 0; i < 522; i++ {
		f.macs[ip(i)] = mac(i)
	}
	ev := func(kind ct.Kind, i int) ct.Event {
		return event(kind, uint32(i+1), ip(i), 40000, youtube, 443, 100, 100)
	}
	tb.Sync(nil, t0)
	var dump []ct.Event
	for i := 0; i < 512; i++ {
		dump = append(dump, ev(ct.Update, i))
	}
	tb.Sync(dump, t0.Add(2*time.Second))
	for i := 0; i < 10; i++ {
		tb.Apply(ev(ct.Destroy, i), t0.Add(3*time.Second))
	}
	dump = dump[10:]
	for i := 512; i < 522; i++ {
		dump = append(dump, ev(ct.Update, i))
	}
	tb.Sync(dump, t0.Add(4*time.Second))
	if n := tb.liveDevices(); n != 512 {
		t.Fatalf("at most 512 devices, not %d", n)
	}
	for i := 0; i < 10; i++ {
		if tb.LiveSeries(mac(i)) != nil {
			t.Fatalf("device %d was live longest ago and goes first", i)
		}
	}
	for _, i := range []int{10, 511, 512, 521} {
		if tb.LiveSeries(mac(i)) == nil {
			t.Fatalf("device %d is live now and stays", i)
		}
	}
}

// liveDevices is how many devices the live ring holds.
func (t *Table) liveDevices() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.series)
}

// pointRates reads a live point as maps, up then down, by app.
func pointRates(p LivePoint) (up, down map[string]float64) {
	up, down = map[string]float64{}, map[string]float64{}
	for _, a := range p.Apps {
		up[a.App] += float64(a.Up)
		down[a.App] += float64(a.Down)
	}
	return up, down
}

func downOf(p LivePoint, app string) float64 {
	_, d := pointRates(p)
	return d[app]
}

// The ring is read by MAC, so only a device whose hardware address is known
// gets one: an address nobody has named yet would take a slot no reader
// asks for, and split the device's history once its MAC is learned.
func TestOnlyKnownDevicesGetALiveRing(t *testing.T) {
	const laptopMAC = "4e:c2:94:9f:07:80"
	f := newFakes()
	tb := NewTable(f.options())
	tb.Sync(nil, t0)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.New, 2, laptop, 51002, youtube, 443, 0, 0), t0)
	tb.Sync([]ct.Event{watch(1, 100, 1000), event(ct.Update, 2, laptop, 51002, youtube, 443, 100, 1000)}, t0.Add(2*time.Second))
	if tb.LiveSeries(laptop) != nil || tb.liveDevices() != 1 || tb.LiveSeries(phoneMAC) == nil {
		t.Fatalf("only the phone, whose MAC is known, has a ring: %d devices", tb.liveDevices())
	}
	f.mu.Lock()
	f.macs[laptop] = laptopMAC
	f.mu.Unlock()
	tb.Sync([]ct.Event{watch(1, 200, 2000), event(ct.Update, 2, laptop, 51002, youtube, 443, 200, 2000)}, t0.Add(4*time.Second))
	if pts := tb.LiveSeries(laptopMAC); len(pts) != 1 || downOf(pts[0], "unknown") != 500 {
		t.Fatalf("once its MAC is known the laptop's ring starts under it: %+v", pts)
	}
}

// Hundreds of addresses nobody has named, all faster than the device being
// watched, never push its ring out.
func TestAFloodOfUnknownAddressesCannotEvictAWatchedDevice(t *testing.T) {
	f := newFakes()
	o := f.options()
	o.MaxLive = 2000
	tb := NewTable(o)
	ip := func(i int) string { return "192.168." + strconv.Itoa(10+i/200) + "." + strconv.Itoa(1+i%200) }
	tb.Sync(nil, t0)
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	for i := 0; i < 600; i++ {
		tb.Apply(event(ct.New, uint32(100+i), ip(i), 40000, youtube, 443, 0, 0), t0)
	}
	for s := 1; s <= 3; s++ {
		now := t0.Add(time.Duration(2*s) * time.Second)
		dump := []ct.Event{watch(1, uint64(s), uint64(2*s))}
		for i := 0; i < 600; i++ {
			b := uint64(s) * 1_000_000
			dump = append(dump, event(ct.Update, uint32(100+i), ip(i), 40000, youtube, 443, b, b))
		}
		tb.Sync(dump, now)
	}
	if pts := tb.LiveSeries(phoneMAC); len(pts) != 3 {
		t.Fatalf("the phone keeps every point: %d", len(pts))
	}
}

// A point is a short list, not two maps: at most maxPointApps apps, the
// busiest, the rest folded into other without losing a byte per second, and
// a ring's whole size is held to a bound fixed in advance.
func TestALivePointIsBoundedInBytes(t *testing.T) {
	f := newFakes()
	o := f.options()
	o.App = func(name, _ string) enrich.App { return enrich.App{ID: name} }
	tb := NewTable(o)
	remote := func(i int) string { return "81.2.69." + strconv.Itoa(1+i) }
	for i := 0; i < 20; i++ {
		f.name(phone, remote(i), "app"+strconv.Itoa(i), "sni")
	}
	tb.Sync(nil, t0)
	for i := 0; i < 20; i++ {
		tb.Apply(event(ct.New, uint32(1+i), phone, uint16(50000+i), remote(i), 443, 0, 0), t0)
	}
	for s := 1; s <= 160; s++ {
		var dump []ct.Event
		for i := 0; i < 20; i++ {
			// App i moves (i+1) * 100 bytes down a second.
			b := uint64(s) * uint64(i+1) * 200
			dump = append(dump, event(ct.Update, uint32(1+i), phone, uint16(50000+i), remote(i), 443, b/10, b))
		}
		tb.Sync(dump, t0.Add(time.Duration(2*s)*time.Second))
	}
	pts := tb.LiveSeries(phoneMAC)
	for _, p := range pts {
		if len(p.Apps) > maxPointApps {
			t.Fatalf("a point holds at most %d apps, not %d", maxPointApps, len(p.Apps))
		}
	}
	up, down := pointRates(pts[len(pts)-1])
	for i := 13; i < 20; i++ {
		if down["app"+strconv.Itoa(i)] != float64(i+1)*100 {
			t.Fatalf("the busiest apps keep their own rate: %v", down)
		}
	}
	if down["other"] != (1+2+3+4+5+6+7+8+9+10+11+12+13)*100 {
		t.Fatalf("the rest is folded into other, none lost: %v", down["other"])
	}
	var totalUp float64
	for _, v := range up {
		totalUp += v
	}
	if totalUp != 2100 {
		t.Fatalf("every byte per second up is still there: %v", totalUp)
	}
	tb.mu.Lock()
	got := tb.series[phoneMAC].bytes()
	tb.mu.Unlock()
	if got > maxRingBytes {
		t.Fatalf("a full ring is %d bytes, over its bound of %d", got, maxRingBytes)
	}
	if maxRingBytes*liveDeviceCap > 18<<20 {
		t.Fatalf("512 full rings fit in 18 MiB: %d", maxRingBytes*liveDeviceCap)
	}
}

// LiveOf hands out one device's live flows, copied, oldest last, without
// copying the rest of the table.
func TestLiveOfIsOneDevicesFlows(t *testing.T) {
	f := newFakes()
	tb := NewTable(f.options())
	tb.Apply(event(ct.New, 1, phone, 51001, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.New, 2, laptop, 51002, youtube, 443, 0, 0), t0)
	tb.Apply(event(ct.New, 3, phone, 51003, elsewhere, 443, 0, 0), t0.Add(time.Second))
	got := tb.LiveOf(phoneMAC)
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 1 {
		t.Fatalf("the phone's two flows, newest first: %+v", got)
	}
	got[0].Name = "changed"
	if tb.LiveOf(phoneMAC)[0].Name == "changed" {
		t.Fatal("LiveOf hands out the table's own rows")
	}
	if tb.LiveOf("") != nil || tb.LiveOf(RouterKey) != nil {
		t.Fatal("no MAC is no device")
	}
}
