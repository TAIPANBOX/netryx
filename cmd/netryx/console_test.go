package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"netryx/internal/alerts"
	"netryx/internal/rules"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"netryx/internal/ct"
	"netryx/internal/flows"
	"netryx/internal/icons"
	"netryx/internal/inventory"
	"netryx/internal/rollup"
)

// fakeRun stands in for ubus and ping, which exist only on a router. A
// command it has no answer for fails the way ubus does for a missing object.
type fakeRun struct {
	mu    sync.Mutex
	out   map[string]string
	calls []string
}

func (f *fakeRun) called(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var got []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			got = append(got, c)
		}
	}
	return got
}

func (f *fakeRun) set(cmd, out string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.out == nil {
		f.out = map[string]string{}
	}
	f.out[cmd] = out
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, k)
	if v, ok := f.out[k]; ok {
		return []byte(v), nil
	}
	return nil, errors.New("Command failed: Not found")
}

// clock is a settable time, the way NTP sets a router's.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// houseAddrs is a router with a house bridge (IPv4, a global IPv6 prefix and
// link-local), a guest bridge, the internet line on eth0, and loopback.
func houseAddrs() (map[string][]netip.Prefix, error) {
	return map[string][]netip.Prefix{
		"br-lan": {
			netip.MustParsePrefix("192.168.50.1/24"),
			netip.MustParsePrefix("2001:db8:50::1/64"),
			netip.MustParsePrefix("fe80::1/64"),
		},
		"br-guest": {netip.MustParsePrefix("192.168.2.1/24")},
		"eth0":     {netip.MustParsePrefix("203.0.113.7/24")},
		"lo":       {netip.MustParsePrefix("127.0.0.1/8"), netip.MustParsePrefix("::1/128")},
	}, nil
}

// routes says eth0 carries the default route, which is what makes it the
// internet side. The lo line is the unreachable default every OpenWrt has.
func routes() fstest.MapFS {
	return fstest.MapFS{
		"proc/net/route": {Data: []byte("Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
			"eth0\t00000000\t017100CB\t0003\t0\t0\t10\t00000000\t0\t0\t0\n" +
			"eth0\t007100CB\t00000000\t0001\t0\t0\t10\t00FFFFFF\t0\t0\t0\n")},
		"proc/net/ipv6_route": {Data: []byte(
			"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo\n")},
	}
}

func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		Geo:    "/nonexistent/ip2asn.tsv.gz",
		Apps:   "/nonexistent/appmap.tsv",
		OUI:    "/nonexistent/oui.txt",
		State:  t.TempDir(),
		WAN:    "wan",
		Leases: "/nonexistent/dhcp.leases",
		LAN:    []string{"br-lan"},
		Run:    (&fakeRun{}).run,
		FS:     routes(),
		Addrs:  houseAddrs,
	}
}

func tuple(src, dst string, sport, dport uint16) ct.Tuple {
	return ct.Tuple{SrcIP: net.ParseIP(src), DstIP: net.ParseIP(dst), Proto: 6, SrcPort: sport, DstPort: dport}
}

// openAndClose runs one connection through the flow table: its NEW, then its
// DESTROY with the final counters, which stamps it and journals it.
func openAndClose(c *console, id uint32, src, dst string, now time.Time) flows.Flow {
	orig := tuple(src, dst, 50000+uint16(id), 443)
	reply := tuple(dst, src, 443, 50000+uint16(id))
	c.table.Apply(ct.Event{Kind: ct.New, ID: id, Orig: orig, Reply: reply}, now)
	c.table.Apply(ct.Event{Kind: ct.Destroy, ID: id, Orig: orig, Reply: reply,
		Up: ct.Counters{Packets: 10, Bytes: 1000}, Down: ct.Counters{Packets: 20, Bytes: 9000}}, now.Add(time.Second))
	rows, _ := c.table.Query(flows.Query{})
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	return flows.Flow{}
}

// A router without the downloaded data files still runs the console: the
// flows are counted and shown, with no country and no app, rather than the
// service refusing to start.
func TestMissingDataFilesDegradeNotFail(t *testing.T) {
	c, err := buildConsole(testOptions(t))
	if err != nil {
		t.Fatalf("missing data files must not stop the console: %v", err)
	}
	if c == nil || c.table == nil || c.store == nil {
		t.Fatalf("the console is not wired: %+v", c)
	}
	f := openAndClose(c, 1, "192.168.50.10", "1.1.1.1", time.Now())
	if f.ID != 1 {
		t.Fatal("the connection never reached the flow table")
	}
	if f.Geo.CC != "--" {
		t.Errorf("without the address table the country is unknown, got %q", f.Geo.CC)
	}
	if f.App.Name != "Unknown" {
		t.Errorf("without the app map the app is Unknown, got %q", f.App.Name)
	}
	rec := httptest.NewRecorder()
	c.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/flows", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("the API still answers: %d %s", rec.Code, rec.Body)
	}
}

// With the data files present, a flow is named from them: the country and
// organisation from the address table, and the app from the organisation
// rule when the connection carried no name.
func TestDataFilesNameTheFlows(t *testing.T) {
	o := testOptions(t)
	dir := t.TempDir()
	o.Geo = filepath.Join(dir, "ip2asn.tsv")
	o.Apps = filepath.Join(dir, "appmap.tsv")
	write(t, o.Geo, "1.1.1.0\t1.1.1.255\t13335\tUS\tCLOUDFLARENET\n")
	write(t, o.Apps, "o\tcloudflare\tcloudflare\tCloudflare\tcdn\n")
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	f := openAndClose(c, 2, "192.168.50.10", "1.1.1.1", time.Now())
	if f.Geo.CC != "US" || f.Geo.Org != "CLOUDFLARENET" {
		t.Errorf("the address table places it: %+v", f.Geo)
	}
	if f.App.ID != "cloudflare" {
		t.Errorf("the organisation rule names the app: %+v", f.App)
	}
}

func addDelta(s *rollup.Store, at time.Time) {
	s.Add(flows.Delta{At: at, MAC: "02:00:00:00:00:01", App: "web", CC: "US", Up: 100, Down: 900, First: true})
}

func bytesOf(s *rollup.Store, from, to time.Time) uint64 {
	var n uint64
	for _, r := range s.Top(rollup.Filter{MAC: "02:00:00:00:00:01"}, "app", from, to, 0) {
		n += r.Bytes.Up + r.Bytes.Down
	}
	return n
}

// Stopping the service writes what it counted, including the hour still
// open, so the next start shows it.
func TestStateIsSavedOnShutdown(t *testing.T) {
	o := testOptions(t)
	clk := &clock{t: time.Date(2026, 11, 20, 10, 30, 0, 0, time.UTC)}
	o.Now = clk.Now
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	addDelta(c.store, clk.Now())
	if err := c.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(o.State, "history", "hours", "*.gob"))
	if len(files) == 0 {
		t.Fatalf("nothing was written under %s", o.State)
	}
	clk.Set(clk.Now().Add(time.Minute))
	again, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	day := clk.Now().Truncate(24 * time.Hour)
	if got := bytesOf(again.store, day, day.Add(24*time.Hour)); got != 1000 {
		t.Errorf("the next start reads back the 1000 bytes counted, got %d", got)
	}
}

// A router without a clock starts before NTP. History written when the clock
// was right lies in its future then and is set aside; once the clock moves
// past it, the next tick loads it.
func TestHistoryWrittenBeforeTheClockWasSetIsLoadedOnceItIs(t *testing.T) {
	o := testOptions(t)
	real := time.Date(2026, 11, 20, 13, 30, 0, 0, time.UTC)
	before := &clock{t: real}
	prev := rollup.NewStore(rollup.Options{Now: before.Now})
	addDelta(prev, real)
	if err := rollup.SaveOpen(prev, filepath.Join(o.State, "history")); err != nil {
		t.Fatal(err)
	}

	clk := &clock{t: real.Add(-3 * time.Hour)} // booted with a stale clock
	o.Now = clk.Now
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	day := real.Truncate(24 * time.Hour)
	if got := bytesOf(c.store, day, day.Add(24*time.Hour)); got != 0 {
		t.Fatalf("a file later than the clock must not load yet, got %d bytes", got)
	}
	clk.Set(clk.Now().Add(time.Minute))
	c.hist.tick(clk.Now())
	if got := bytesOf(c.store, day, day.Add(24*time.Hour)); got != 0 {
		t.Fatalf("the clock has not reached the file yet, got %d bytes", got)
	}
	clk.Set(real.Add(2 * time.Minute)) // NTP
	c.hist.tick(clk.Now())
	if got := bytesOf(c.store, day, day.Add(24*time.Hour)); got != 1000 {
		t.Errorf("once the clock is right the history loads: want 1000 bytes, got %d", got)
	}
}

// Closed hours reach the flash once an hour while the service runs, not only
// at a stop, so a power cut loses at most the hour in progress.
func TestClosedHoursAreWrittenWhileRunning(t *testing.T) {
	o := testOptions(t)
	start := time.Date(2026, 11, 20, 10, 30, 0, 0, time.UTC)
	clk := &clock{t: start}
	o.Now = clk.Now
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	addDelta(c.store, start)
	hours := func() int {
		files, _ := filepath.Glob(filepath.Join(o.State, "history", "hours", "*.gob"))
		return len(files)
	}
	clk.Set(start.Add(20 * time.Minute)) // 10:50, the hour is still open
	c.hist.tick(clk.Now())
	if n := hours(); n != 0 {
		t.Fatalf("an open hour is not written by the tick, got %d files", n)
	}
	// The first tick after the 10:00 hour closes writes it, not a tick an
	// hour after the last write: otherwise a cut just before that write
	// loses two hours.
	clk.Set(start.Add(40 * time.Minute)) // 11:10
	c.hist.tick(clk.Now())
	ten := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	name := filepath.Join(o.State, "history", "hours", strconv.FormatInt(ten.Unix()/3600, 10)+".gob")
	if _, err := os.Stat(name); err != nil {
		t.Errorf("at 11:10 the closed 10:00 hour is on flash as %s: %v", filepath.Base(name), err)
	}
	if n := hours(); n != 1 {
		t.Errorf("and it is the only file, got %d", n)
	}
	addDelta(c.store, clk.Now())
	clk.Set(time.Date(2026, 11, 20, 12, 5, 0, 0, time.UTC))
	c.hist.tick(clk.Now())
	if n := hours(); n != 2 {
		t.Errorf("at 12:05 the 11:00 hour follows, got %d files", n)
	}
}

// After the stop has written the history, a tick that is already running
// must not write again: SaveClosed after SaveOpen would race the exit.
func TestNothingIsSavedAfterShutdown(t *testing.T) {
	o := testOptions(t)
	start := time.Date(2026, 11, 20, 10, 30, 0, 0, time.UTC)
	clk := &clock{t: start}
	o.Now = clk.Now
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	addDelta(c.store, start)
	if err := c.shutdown(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(o.State, "history")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	clk.Set(start.Add(2 * time.Hour))
	c.hist.tick(clk.Now())
	if _, err := os.Stat(dir); err == nil {
		t.Error("a tick after the shutdown wrote the history again")
	}
}

// A file that stays in the future is reported once, not on every tick.
func TestHistoryErrorsAreLoggedWhenTheyChange(t *testing.T) {
	o := testOptions(t)
	real := time.Date(2026, 11, 20, 13, 30, 0, 0, time.UTC)
	prev := rollup.NewStore(rollup.Options{Now: func() time.Time { return real }})
	addDelta(prev, real)
	if err := rollup.SaveOpen(prev, filepath.Join(o.State, "history")); err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: real.Add(-3 * time.Hour)}
	o.Now = clk.Now
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	c.hist.logf = func(f string, a ...any) { said = append(said, f) }
	for i := 1; i <= 3; i++ {
		clk.Set(real.Add(-3*time.Hour + time.Duration(i)*time.Minute))
		c.hist.tick(clk.Now())
	}
	if len(said) != 0 {
		t.Errorf("the same file still in the future is not news: %v", said)
	}
}

// The flow table decides who opened a connection by which end is at home.
// Home is every network the router serves, the IPv6 prefix included, and the
// router's own addresses; the subnet of the internet line is not.
func TestTheRoutersOwnAndLANAddressesAreLocal(t *testing.T) {
	c, err := buildConsole(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	for a, want := range map[string]bool{
		"192.168.50.10":         true,  // a device on the house bridge
		"::ffff:192.168.50.10":  true,  // the same, as a dual-stack socket writes it
		"2001:db8:50::abcd":     true,  // its global IPv6 address
		"192.168.2.5":           true,  // the guest bridge is still home
		"203.0.113.7":           true,  // the router's own WAN address
		"fe80::1234":            true,  // link-local never leaves the link
		"203.0.113.1":           false, // the ISP's gateway on the WAN subnet
		"1.1.1.1":               false,
		"2606:4700:4700::1111":  false,
		"2001:db8:51::1":        false, // next to the house prefix, not in it
		"100.64.0.1":            false,
		"10.166.170.1":          false, // private, but not one of ours
		"fd7a:115c:a1e0::53":    false,
		"2001:db8:50:ffff::abc": false,
	} {
		if got := c.local.IsLocal(netip.MustParseAddr(a)); got != want {
			t.Errorf("IsLocal(%s) = %v, want %v", a, got, want)
		}
	}
	// And the table uses it: a phone's global IPv6 address opening a
	// connection out is outbound, not something the internet opened.
	f := openAndClose(c, 3, "2001:db8:50::abcd", "2606:4700:4700::1111", time.Now())
	if f.Dir != flows.Out {
		t.Errorf("a connection from the house prefix is outbound, got %q", f.Dir)
	}
}

const wanStatus = `{"up": true, "uptime": 4992, "l3_device": "eth0", "device": "eth0",
 "ipv4-address": [{"address": "203.0.113.7", "mask": 24}], "ipv6-address": []}`

const wan6Status = `{"up": true, "uptime": 4990, "l3_device": "eth0", "device": "eth0",
 "ipv4-address": [], "ipv6-address": [{"address": "2001:db8:ffff::2", "mask": 64}]}`

const pingOK = "PING 1.1.1.1 (1.1.1.1): 56 data bytes\n64 bytes from 1.1.1.1: seq=0 ttl=54 time=11.276 ms\n\n" +
	"--- 1.1.1.1 ping statistics ---\n1 packets transmitted, 1 packets received, 0% packet loss\n"

const pingLost = "PING 1.1.1.1 (1.1.1.1): 56 data bytes\n\n--- 1.1.1.1 ping statistics ---\n" +
	"1 packets transmitted, 0 packets received, 100% packet loss\n"

func overviewWAN(t *testing.T, c *console) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	c.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/overview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		WAN map[string]any `json:"wan"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.WAN
}

// The internet tile shows latency only once a probe has replied: before that
// it is no data, not 0 ms. Addresses come from both ubus interfaces and the
// rate from the line's counters.
func TestTheWANTileGetsLatencyFromTheProber(t *testing.T) {
	o := testOptions(t)
	run := &fakeRun{}
	run.set("ubus call network.interface.wan status", wanStatus)
	run.set("ubus call network.interface.wan6 status", wan6Status)
	run.set("ping -c 1 -W 1 -- 1.1.1.1", pingLost)
	o.Run = run.run
	o.PingTarget = "1.1.1.1"
	fsys := routes()
	fsys["sys/class/net/eth0/statistics/rx_bytes"] = &fstest.MapFile{Data: []byte("1000\n")}
	fsys["sys/class/net/eth0/statistics/tx_bytes"] = &fstest.MapFile{Data: []byte("500\n")}
	o.FS = fsys
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	if w := overviewWAN(t, c); w["latencyMs"] != nil {
		t.Fatalf("no probe has run, so latency is no data: %v", w["latencyMs"])
	}
	now := time.Now()
	c.wan.tick(context.Background(), now)
	w := overviewWAN(t, c)
	if w["latencyMs"] != nil {
		t.Errorf("the only probe was lost, so latency is still no data: %v", w["latencyMs"])
	}
	if w["up"] != true || w["ipv4"] != "203.0.113.7" || w["ipv6"] != "2001:db8:ffff::2" || w["device"] != "eth0" {
		t.Errorf("the line's state comes from wan and wan6: %v", w)
	}
	run.set("ping -c 1 -W 1 -- 1.1.1.1", pingOK)
	fsys["sys/class/net/eth0/statistics/rx_bytes"] = &fstest.MapFile{Data: []byte("21000\n")}
	fsys["sys/class/net/eth0/statistics/tx_bytes"] = &fstest.MapFile{Data: []byte("5500\n")}
	c.wan.tick(context.Background(), now.Add(10*time.Second))
	w = overviewWAN(t, c)
	if w["latencyMs"] != 11.276 {
		t.Errorf("one probe replied in 11.276 ms: %v", w["latencyMs"])
	}
	if w["lossPct"] != 50.0 {
		t.Errorf("one of two probes was lost: %v", w["lossPct"])
	}
	if w["rxRate"] != 2000.0 || w["txRate"] != 500.0 {
		t.Errorf("20000 and 5000 bytes over 10 s: rx=%v tx=%v", w["rxRate"], w["txRate"])
	}
}

// The Speed now tile is the line's average over the window, from every
// reading of its counters, not the last 10 s alone.
func TestTheWANTileAveragesTheLineOverTheWindow(t *testing.T) {
	o := testOptions(t)
	run := &fakeRun{}
	run.set("ubus call network.interface.wan status", wanStatus)
	o.Run = run.run
	fsys := routes()
	o.FS = fsys
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, b := range []string{"0", "10000", "40000"} {
		fsys["sys/class/net/eth0/statistics/rx_bytes"] = &fstest.MapFile{Data: []byte(b + "\n")}
		fsys["sys/class/net/eth0/statistics/tx_bytes"] = &fstest.MapFile{Data: []byte("0\n")}
		c.wan.tick(context.Background(), now.Add(time.Duration(i)*10*time.Second))
	}
	w := overviewWAN(t, c)
	avg, _ := w["avg"].(map[string]any)
	// 10000 then 30000 bytes over 20 s: the last reading says 3000 B/s, the window 2000.
	if w["rxRate"] != 3000.0 || avg == nil || avg["rx"] != 2000.0 || avg["sec"] != 20.0 {
		t.Fatalf("wan %v, want the last reading 3000 and the window's average 2000 over 20 s", w)
	}
}

// brforward is the kernel's binary forwarding table: 6 bytes of address, the
// port number, a local flag, the age, and the port's high byte.
func fdbEntry(mac string, port uint16, local bool) []byte {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		panic(err)
	}
	b := make([]byte, 16)
	copy(b, hw)
	b[6] = byte(port)
	if local {
		b[7] = 1
	}
	binary.LittleEndian.PutUint32(b[8:], 100)
	b[12] = byte(port >> 8)
	return b
}

// A device on a cable is shown on the port it sits behind, a Wi-Fi client
// with its network's name, and the router's own addresses are not devices.
func TestClientsShowTheirPortOrTheirNetwork(t *testing.T) {
	o := testOptions(t)
	run := &fakeRun{}
	run.set("ubus list hostapd.*", "hostapd.phy0-ap0\n")
	run.set("ubus call hostapd.phy0-ap0 get_clients", `{"freq": 5180, "clients": {"AA:BB:CC:00:00:01": {"signal": -58}}}`)
	run.set("ubus call hostapd.phy0-ap0 get_status", `{"status": "ENABLED", "ssid": "Home", "freq": 5180}`)
	o.Run = run.run
	var fdb []byte
	for _, e := range []struct {
		mac   string
		port  uint16
		local bool
	}{
		{"8c:79:f5:00:00:02", 1, false},     // on a cable behind lan1
		{"8c:79:f5:00:00:09", 0x101, false}, // behind port 257: the high byte counts
		{"94:83:c4:a3:57:23", 1, true},      // the bridge's own address
		// hostapd lists it, so it is on Wi-Fi even though its last frame
		// came in on a cable (a client moving to a wired access point).
		{"aa:bb:cc:00:00:01", 1, false},
		{"aa:bb:cc:00:00:03", 3, false}, // behind a second radio hostapd did not list
		{"aa:bb:cc:00:00:04", 2, false}, // behind the access point, not yet a client
	} {
		fdb = append(fdb, fdbEntry(e.mac, e.port, e.local)...)
	}
	fsys := routes()
	fsys["sys/class/net/br-lan/brforward"] = &fstest.MapFile{Data: fdb}
	fsys["sys/class/net/br-lan/brif/lan1/port_no"] = &fstest.MapFile{Data: []byte("0x1\n")}
	fsys["sys/class/net/br-lan/brif/lan9/port_no"] = &fstest.MapFile{Data: []byte("0x101\n")}
	fsys["sys/class/net/br-lan/brif/phy0-ap0/port_no"] = &fstest.MapFile{Data: []byte("0x2\n")}
	fsys["sys/class/net/br-lan/brif/phy1-ap0/port_no"] = &fstest.MapFile{Data: []byte("0x3\n")}
	fsys["sys/class/net/phy1-ap0/phy80211/name"] = &fstest.MapFile{Data: []byte("phy1\n")}
	o.FS = fsys
	o.Leases = filepath.Join(t.TempDir(), "dhcp.leases")
	write(t, o.Leases, "1789549116 8c:79:f5:00:00:02 192.168.50.20 desk-pc *\n")
	write(t, filepath.Join(o.State, "labels.json"), `{"AA:BB:CC:00:00:01": {"name": "Kitchen tablet", "group": "family"}}`)
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]int{}
	devs := c.inv.Devices()
	for i, d := range devs {
		by[d.MAC] = i
	}
	i, ok := by["8c:79:f5:00:00:02"]
	if !ok {
		t.Fatalf("the wired device is missing: %+v", devs)
	}
	if l := devs[i].Link; l.Kind != "wired" || l.Iface != "lan1" {
		t.Errorf("the wired device sits behind lan1: %+v", l)
	}
	if devs[i].Hostname != "desk-pc" || len(devs[i].IPv4) != 1 || devs[i].IPv4[0] != "192.168.50.20" {
		t.Errorf("its lease names it and gives its address: %+v", devs[i])
	}
	i, ok = by["aa:bb:cc:00:00:01"]
	if !ok {
		t.Fatalf("the Wi-Fi client is missing: %+v", devs)
	}
	if l := devs[i].Link; l.Kind != "wifi" || l.SSID != "Home" || l.Band != "5 GHz" || l.Signal != -58 {
		t.Errorf("the Wi-Fi client carries its network: %+v", l)
	}
	if devs[i].Name != "Kitchen tablet" || devs[i].Group != "family" {
		t.Errorf("the label the household typed names it: %+v", devs[i])
	}
	if _, ok := by["94:83:c4:a3:57:23"]; ok {
		t.Error("the bridge's own address is not a client")
	}
	if i, ok := by["8c:79:f5:00:00:09"]; !ok || devs[i].Link.Iface != "lan9" {
		t.Errorf("a port numbered above 255 is still found: %+v", devs)
	}
	// A radio port is never a cable, whether hostapd names it or the kernel
	// marks it as a wireless interface.
	for _, m := range []string{"aa:bb:cc:00:00:03", "aa:bb:cc:00:00:04"} {
		if i, ok := by[m]; ok && devs[i].Link.Kind == "wired" {
			t.Errorf("%s sits behind a radio and is called wired: %+v", m, devs[i].Link)
		}
	}
}

// The page at / is the console, with its own rules about what it may load,
// and the API sits under /api/.
func TestTheConsolePageAndItsAPIAreServed(t *testing.T) {
	c, err := buildConsole(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	h := c.handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "default-src 'self'") {
		t.Errorf("/ is the console page with its CSP, got %q", csp)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/devices", nil))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("/api/devices is the console API: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

const wanStatusWithGateway = `{"up": true, "uptime": 4992, "l3_device": "eth0", "device": "eth0",
 "ipv4-address": [{"address": "203.0.113.7", "mask": 24}], "ipv6-address": [],
 "route": [{"target": "203.0.113.0", "mask": 24, "nexthop": "0.0.0.0"},
           {"target": "0.0.0.0", "mask": 0, "nexthop": "203.0.113.1", "source": "203.0.113.7/32"}]}`

// With nothing configured the probe goes to the line's own gateway, so no
// third party is contacted; a configured internet target takes its place.
func TestTheProbeTargetsTheGatewayByDefault(t *testing.T) {
	o := testOptions(t)
	run := &fakeRun{}
	run.set("ubus call network.interface.wan status", wanStatusWithGateway)
	run.set("ping -c 1 -W 1 -- 203.0.113.1", pingOK)
	o.Run = run.run
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	c.wan.tick(context.Background(), time.Now())
	if got := run.called("ping"); len(got) != 1 || got[0] != "ping -c 1 -W 1 -- 203.0.113.1" {
		t.Errorf("one probe, to the gateway: %v", got)
	}
	if w := overviewWAN(t, c); w["latencyMs"] != 11.276 {
		t.Errorf("the gateway's reply is the latency: %v", w["latencyMs"])
	}
	// A default route with no next hop (a point-to-point line) names no
	// gateway, and a route that is not the default is not one either.
	for js, want := range map[string]string{
		`{"route": [{"target": "0.0.0.0", "mask": 0, "nexthop": "0.0.0.0"}]}`:   "",
		`{"route": [{"target": "10.0.0.0", "mask": 8, "nexthop": "10.0.0.1"}]}`: "",
		`{"route": [{"target": "0.0.0.0", "mask": 0, "nexthop": "10.0.0.1"}]}`:  "10.0.0.1",
		`not json`: "",
	} {
		if got := gatewayOf([]byte(js)); got != want {
			t.Errorf("gatewayOf(%s) = %q, want %q", js, got, want)
		}
	}

	o.PingTarget = "one.one.one.one"
	run = &fakeRun{}
	run.set("ubus call network.interface.wan status", wanStatusWithGateway)
	o.Run = run.run
	c, err = buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	c.wan.tick(context.Background(), time.Now())
	if got := run.called("ping"); len(got) != 1 || got[0] != "ping -c 1 -W 1 -- one.one.one.one" {
		t.Errorf("a configured target replaces the gateway: %v", got)
	}
}

// The target comes from a config file and ends up on a command line: it
// must be an address or a host name, and never be read as an option.
func TestAProbeTargetCannotBeAnOption(t *testing.T) {
	for target, want := range map[string]bool{
		"1.1.1.1": true, "2606:4700:4700::1111": true, "one.one.one.one": true, "gw.lan.": true,
		"-f": false, "-s65000": false, "--": false, "a b": false, "a..b": false, "x;reboot": false,
		"-host.example": false, "": false,
	} {
		if got := validTarget(target); got != want {
			t.Errorf("validTarget(%q) = %v, want %v", target, got, want)
		}
	}
	o := testOptions(t)
	run := &fakeRun{}
	run.set("ubus call network.interface.wan status", wanStatusWithGateway)
	o.Run = run.run
	o.PingTarget = "-f"
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	c.wan.tick(context.Background(), time.Now())
	got := run.called("ping")
	if len(got) != 1 || got[0] != "ping -c 1 -W 1 -- 203.0.113.1" {
		t.Errorf("an option-shaped target is refused and the gateway probed instead: %v", got)
	}
}

// A page elsewhere that points its own name at the router (DNS rebinding)
// arrives with that name in Host, and is refused before any route runs.
func TestAForeignHostIsRefused(t *testing.T) {
	o := testOptions(t)
	o.Listen = "192.0.2.10:8788"
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	h := c.guard(c.handler())
	for host, want := range map[string]int{
		"evil.example":        http.StatusMisdirectedRequest,
		"evil.example:8788":   http.StatusMisdirectedRequest,
		"192.168.50.10:8788":  http.StatusMisdirectedRequest, // a device, not the router
		"203.0.113.7:8788":    http.StatusMisdirectedRequest, // the router, but on the WAN
		"localhost:8788":      http.StatusOK,
		"LOCALHOST.":          http.StatusOK,
		"127.0.0.1":           http.StatusOK,
		"[::1]:8788":          http.StatusOK,
		"192.0.2.10:8788":     http.StatusOK, // the listen address
		"192.168.50.1:8788":   http.StatusOK, // the router on the house bridge
		"[2001:db8:50::1]:80": http.StatusOK,
		"192.168.2.1":         http.StatusOK, // and on the guest bridge
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
		r.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("Host %q: %d, want %d", host, rec.Code, want)
		}
		if want != http.StatusOK && strings.Contains(rec.Body.String(), "evil") {
			t.Errorf("the refusal echoes the host back: %q", rec.Body)
		}
	}
	// The listen address is compared as an address, and a listen name as a name.
	for listen, host := range map[string]string{
		"[2001:db8::a]:8788": "[2001:db8:0:0::a]:8788",
		"netryx.home:8788":   "NetRyx.Home",
	} {
		o.Listen = listen
		c, err := buildConsole(o)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
		r.Host = host
		rec := httptest.NewRecorder()
		c.guard(c.handler()).ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Errorf("listening on %s, Host %q: %d", listen, host, rec.Code)
		}
	}
}

// The router's own host name, alone and in its local domain, reaches the
// console; a name that only ends in the domain, another host name, or any
// name when the router has none does not.
func TestTheRoutersOwnNameReachesTheConsole(t *testing.T) {
	code := func(t *testing.T, hostname, domain string, uciFails bool, host string) int {
		t.Helper()
		o := testOptions(t)
		o.Listen = "192.0.2.10:8788"
		fs := routes()
		fs["proc/sys/kernel/hostname"] = &fstest.MapFile{Data: []byte(hostname)}
		fr := &fakeRun{}
		if !uciFails {
			fr.set("uci -q get dhcp.@dnsmasq[0].domain", domain)
		}
		o.FS, o.Run = fs, fr.run
		c, err := buildConsole(o)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
		r.Host = host
		rec := httptest.NewRecorder()
		c.guard(c.handler()).ServeHTTP(rec, r)
		return rec.Code
	}
	ok, refused := http.StatusOK, http.StatusMisdirectedRequest
	for host, want := range map[string]int{
		"flint2":            ok,
		"flint2.lan":        ok,
		"FLINT2.LAN":        ok,
		"Flint2:8788":       ok,
		"flint2.lan:80":     ok,
		"flint2.lan.":       ok, // a trailing dot is the same name, as for localhost
		"flint2.":           ok,
		"evil.lan":          refused,
		"flint2.evil.com":   refused,
		"flint2.lan.evil":   refused,
		"xflint2":           refused,
		"flint22.lan":       refused,
		"flint2.lan.lan":    refused,
		"other":             refused,
		"lan":               refused,
		".lan":              refused,
		"flint2.home.arpa":  refused, // the domain is lan here
		"evil.example:8788": refused,
	} {
		if got := code(t, "flint2\n", "lan\n", false, host); got != want {
			t.Errorf("Host %q: %d, want %d", host, got, want)
		}
	}
	// dnsmasq's domain is read, and lan is the OpenWrt default when it cannot be.
	if got := code(t, "flint2\n", "home.arpa\n", false, "flint2.home.arpa"); got != ok {
		t.Errorf("the configured domain: %d", got)
	}
	if got := code(t, "flint2\n", "home.arpa\n", false, "flint2.lan"); got != refused {
		t.Errorf("the default domain beside a configured one: %d", got)
	}
	if got := code(t, "flint2\n", "", true, "flint2.lan"); got != ok {
		t.Errorf("no uci answer means lan: %d", got)
	}
	// A router with no host name admits no name it was not admitted by.
	for _, host := range []string{"", ".lan", "lan", "localhost.lan", "evil.lan", "x"} {
		if got := code(t, "\n", "lan\n", false, host); got != refused {
			t.Errorf("empty host name, Host %q: %d", host, got)
		}
	}
	// A host name that is not a valid name is not admitted.
	if got := code(t, "a b\n", "lan\n", false, "a b"); got != refused {
		t.Errorf("an invalid host name admitted: %d", got)
	}
}

// The local domain is read from uci once for a host name, not at every
// 10 s tick.
func TestTheLocalDomainIsNotReadEveryTick(t *testing.T) {
	o := testOptions(t)
	fs := routes()
	fs["proc/sys/kernel/hostname"] = &fstest.MapFile{Data: []byte("flint2\n")}
	fr := &fakeRun{}
	fr.set("uci -q get dhcp.@dnsmasq[0].domain", "lan\n")
	o.FS, o.Run = fs, fr.run
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		c.local.refresh()
	}
	if n := len(fr.called("uci -q get dhcp.@dnsmasq[0].domain")); n != 1 {
		t.Errorf("uci asked %d times for the domain over 6 refreshes, want 1", n)
	}
}

// A command that floods its output cannot fill the router's memory.
func TestExecRunCapsItsOutput(t *testing.T) {
	out, err := execRun(context.Background(), "sh", "-c", "head -c 3000000 /dev/zero")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1<<20 {
		t.Errorf("output is capped at 1 MiB, got %d bytes", len(out))
	}
	out, _ = execRun(context.Background(), "sh", "-c", "printf ok")
	if string(out) != "ok" {
		t.Errorf("a short answer comes through whole: %q", out)
	}
}

// A command that leaves a child holding its output open must not hold the
// caller past its deadline: WaitDelay closes the pipe 2 s after it, and what
// was printed before is kept.
func TestAHungChildIsCutOffAfterItsDeadline(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	began := time.Now()
	out, _ := execRun(ctx, "sh", "-c", "sleep 8 & echo hi")
	if took := time.Since(began); took >= 4*time.Second {
		t.Errorf("a 1 s deadline held the caller for %v", took)
	}
	if string(out) != "hi\n" {
		t.Errorf("what it printed before is kept: %q", out)
	}
}

// A live poll is the client card open in a browser. While one came in the
// last 10 s the connection table is read every 2 s; once the polls stop, the
// next wait is the configured 5 s again. One loop takes every dump, so the
// table still sees them one at a time.
func TestSyncSlowsDownWhenNobodyWatches(t *testing.T) {
	o := testOptions(t)
	clk := &clock{t: noon}
	o.Now = clk.Now
	o.Leases = filepath.Join(t.TempDir(), "dhcp.leases")
	write(t, o.Leases, "1893456000 8c:79:f5:00:00:02 192.168.50.20 desk-pc *\n")
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	asked := make(chan time.Duration)
	tick := make(chan time.Time)
	c.after = func(d time.Duration) <-chan time.Time { asked <- d; return tick }
	var dumps atomic.Int32
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { c.syncLoop(stop, func() { dumps.Add(1) }); close(done) }()
	next := func(why string, want time.Duration) {
		t.Helper()
		select {
		case d := <-asked:
			if d != want {
				t.Errorf("%s: the loop waits %v, want %v", why, d, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: the loop never asked to wait", why)
		}
	}
	get := func(path string) {
		t.Helper()
		rec := httptest.NewRecorder()
		c.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	next("nobody has opened a card", 5*time.Second)

	// The first poll wakes the loop: live mode starts at once, not after the
	// 5 s wait already under way.
	get("/api/devices/8c:79:f5:00:00:02/live")
	next("a live card polled just now", 2*time.Second)
	// A poll while already watching does not wake it again.
	get("/api/devices/8c:79:f5:00:00:02/live")
	select {
	case d := <-asked:
		t.Fatalf("a second poll woke the loop again (asked %v)", d)
	case <-time.After(50 * time.Millisecond):
	}

	clk.Set(noon.Add(9 * time.Second))
	tick <- clk.Now()
	next("9 s after the last poll", 2*time.Second)

	clk.Set(noon.Add(11 * time.Second))
	tick <- clk.Now()
	next("11 s after the last poll, the card was closed", 5*time.Second)

	get("/api/devices/8c:79:f5:00:00:02")
	tick <- clk.Now()
	next("the plain card is not a live poll", 5*time.Second)

	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop did not end with stop")
	}
	if n := dumps.Load(); n != 5 {
		t.Errorf("one dump per turn of the loop, 5 turns: %d", n)
	}

	// A sync configured faster than the live one is never slowed down.
	o.Sync = time.Second
	fast, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	fast.livePolled()
	if d := fast.syncInterval(clk.Now()); d != time.Second {
		t.Errorf("a 1 s sync stays 1 s while someone watches: %v", d)
	}
}

// pngIcon is a real 16x16 PNG: the fetcher keeps and serves only images
// whose header it can read.
var pngIcon = func() string {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		panic(err)
	}
	return b.String()
}()

// A fetched favicon is served at /icons/<id> from <state>/icons and from
// nowhere else, with the type it was stored as and a day's caching. The icon
// pack and its manifest come from the page built into the binary, never from
// files someone left in the state directory. Every route sits behind the
// Host check.
func TestFetchedIconsAreServedOnlyFromTheStateDir(t *testing.T) {
	o := testOptions(t)
	dir := filepath.Join(o.State, "icons")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "brand.png"), pngIcon)
	write(t, filepath.Join(dir, "news.site.png"), pngIcon) // an id with a dot
	write(t, filepath.Join(dir, "a..b.png"), pngIcon)
	// Things that are not fetched icons, in and next to the icon directory.
	write(t, filepath.Join(o.State, "secret.png"), pngIcon)
	write(t, filepath.Join(dir, "manifest.json"), `{"planted": {"kind": "svg"}}`)
	write(t, filepath.Join(dir, "planted.svg"), `<svg><script>alert(1)</script></svg>`)
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	h := c.guard(c.handler())
	do := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		r.Host = "localhost:8788"
		h.ServeHTTP(rec, r)
		return rec
	}

	rec := do("/icons/brand")
	if rec.Code != http.StatusOK || rec.Body.String() != pngIcon {
		t.Fatalf("the fetched icon is served whole: %d %q", rec.Code, rec.Body)
	}
	for k, want := range map[string]string{
		"Content-Type":           "image/png",
		"Cache-Control":          "max-age=86400",
		"X-Content-Type-Options": "nosniff",
		// The magic is checked only as a prefix: a polyglot must never be
		// sniffed or rendered as a document.
		"Content-Security-Policy": "default-src 'none'",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}

	// A dotted id is still a fetched icon, not a file of the page.
	if rec := do("/icons/news.site"); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("/icons/news.site reaches the fetcher: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	for _, p := range []string{
		"/icons/a..b",           // no ".." in an id, whatever is on disk
		"/icons/secret",         // a file beside the icon directory
		"/icons/missing",        // nothing was fetched for it
		"/icons/brand.png",      // the stored name is not the route
		"/icons/..%2Fsecret",    // an escaped separator
		"/icons/Brand",          // not an id
		"/icons/manifest",       // not a fetched icon
		"/icons/sub/brand",      // no directories
		"/icons/.brand.123.tmp", // a fetch still being written
		"/icons/failures.json",  // the fetcher's own records
		"/icons/" + strings.Repeat("a", 70),
	} {
		rec := do(p)
		if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "PNG") {
			t.Errorf("%s must not be served: %d %q", p, rec.Code, rec.Body)
		}
	}
	for _, p := range []string{"/icons/manifest.json", "/icons/planted.svg", "/icons/youtube.svg", "/icons/news.site.svg"} {
		rec := do(p)
		if strings.Contains(rec.Body.String(), "planted") || strings.Contains(rec.Body.String(), "script") {
			t.Errorf("%s came from the state directory, not from the built page: %q", p, rec.Body)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "default-src 'self'") {
			t.Errorf("%s is served by the page handler, with its CSP: %q", p, csp)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/icons/brand", nil)
	r.Host = "evil.example"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("a foreign Host gets no icon: %d", rec.Code)
	}
}

// brandServer is a brand's site with an icon, and a client that reaches it
// under the brand's name. It counts what was asked of it.
func brandServer(t *testing.T) (*http.Client, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/apple-touch-icon.png" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(pngIcon))
	}))
	t.Cleanup(srv.Close)
	cl := srv.Client()
	tr := cl.Transport.(*http.Transport).Clone()
	addr := srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	cl.Transport = tr
	cl.Timeout = 5 * time.Second
	return cl, &hits
}

// fetch_icons is on by default and the config says what it does. With it on,
// the router fetches each brand's icon once its clock looks set; with it off,
// nothing is fetched, and the icons already kept are still served.
func TestIconFetchingCanBeTurnedOff(t *testing.T) {
	cl, hits := brandServer(t)
	o := testOptions(t)
	o.Meta = filepath.Join(t.TempDir(), "apps-meta.json")
	write(t, o.Meta, `{
		"brand":   {"name": "Brand", "icon": "fav", "domain": "example.com"},
		"logo":    {"name": "Logo", "icon": "si", "slug": "logo"},
		"plain":   {"name": "Plain", "icon": ""}
	}`)
	o.IconClient = cl
	clk := &clock{t: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)} // before NTP
	o.Now = clk.Now
	quick := func(time.Duration) <-chan time.Time { return time.After(time.Millisecond) }

	o.FetchIcons = false
	off, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	off.after = quick
	clk.Set(noon)
	stop := make(chan struct{})
	defer close(stop)
	if off.startIcons(stop) {
		t.Error("with fetch_icons 0 no fetching starts")
	}
	time.Sleep(100 * time.Millisecond)
	if n := hits.Load(); n != 0 {
		t.Fatalf("with fetch_icons 0 the router contacted the brand %d times", n)
	}

	clk.Set(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	o.FetchIcons = true
	on, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	on.after = quick
	if !on.startIcons(stop) {
		t.Fatal("with fetch_icons 1 fetching starts")
	}
	time.Sleep(100 * time.Millisecond)
	if n := hits.Load(); n != 0 {
		t.Fatalf("a clock that is not set yet would fail every certificate: %d requests", n)
	}
	clk.Set(noon) // NTP
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, _, ok := on.icons.Open("brand"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the brand's icon was never kept (%d requests)", hits.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := hits.Load(); n != 1 {
		t.Errorf("one request for one fav app, the si and plain ones are not fetched: %d", n)
	}
	if rec := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		off.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/icons/brand", nil))
		return rec
	}(); rec.Code != http.StatusOK {
		t.Errorf("an icon already kept is served with fetching off: %d", rec.Code)
	}

	cfg, err := os.ReadFile("../../package/netryx/files/netryx.config")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "option fetch_icons '1'") {
		t.Error("the config carries fetch_icons, on")
	}
}

// loopProbe runs the icon loop with its waits and its fetches in the test's
// hands: every wait it asks for arrives on asked and returns when the test
// ticks; every fetch arrives on fetched with the time its context leaves,
// and answers with the next of results.
type loopProbe struct {
	asked   chan time.Duration
	tick    chan time.Time
	fetched chan fetchCall
}

type fetchCall struct {
	id   string
	left time.Duration
}

func probeIcons(t *testing.T, c *console, results func(id string) error) *loopProbe {
	t.Helper()
	p := &loopProbe{asked: make(chan time.Duration), tick: make(chan time.Time), fetched: make(chan fetchCall)}
	c.after = func(d time.Duration) <-chan time.Time { p.asked <- d; return p.tick }
	c.ensure = func(ctx context.Context, id, domain string) error {
		var left time.Duration
		if dl, ok := ctx.Deadline(); ok {
			left = time.Until(dl)
		}
		p.fetched <- fetchCall{id, left}
		return results(id)
	}
	return p
}

func (p *loopProbe) wait(t *testing.T, why string, want time.Duration) {
	t.Helper()
	select {
	case d := <-p.asked:
		if d != want {
			t.Fatalf("%s: the loop waits %v, want %v", why, d, want)
		}
	case c := <-p.fetched:
		t.Fatalf("%s: the loop fetched %s instead of waiting %v", why, c.id, want)
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: the loop never asked to wait %v", why, want)
	}
}

func (p *loopProbe) fetch(t *testing.T, want string) {
	t.Helper()
	select {
	case c := <-p.fetched:
		if c.id != want {
			t.Fatalf("fetched %s, want %s", c.id, want)
		}
		if c.left <= 9*time.Second || c.left > 10*time.Second {
			t.Errorf("%s: each fetch gets 10 s, its context leaves %v", want, c.left)
		}
	case d := <-p.asked:
		t.Fatalf("waited %v instead of fetching %s", d, want)
	case <-time.After(2 * time.Second):
		t.Fatalf("%s was never fetched", want)
	}
}

func iconConsole(t *testing.T, clk *clock, ids ...string) *console {
	t.Helper()
	o := testOptions(t)
	o.Now = clk.Now
	o.FetchIcons = true
	o.Meta = filepath.Join(t.TempDir(), "apps-meta.json")
	var b strings.Builder
	b.WriteString("{")
	for i, id := range ids {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"` + id + `": {"icon": "fav", "domain": "` + id + `.example"}`)
	}
	b.WriteString("}")
	write(t, o.Meta, b.String())
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The loop waits 2 minutes after the start (a booting router's line may not
// be up yet), re-checks a clock that is not set yet every minute, gives each
// site 10 s and pauses 2 s between sites it actually asks, and makes a pass a
// day. An icon already kept and a site whose failure still holds cost no
// request and no pause.
func TestTheIconLoopKeepsItsPace(t *testing.T) {
	clk := &clock{t: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}
	c := iconConsole(t, clk, "a", "b", "c", "d", "e")
	dir := filepath.Join(c.o.State, "icons")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "d.png"), pngIcon)
	write(t, filepath.Join(dir, "failures.json"), `{"b": `+strconv.FormatInt(noon.Add(-time.Hour).Unix(), 10)+`}`)
	p := probeIcons(t, c, func(string) error { return nil })
	stop := make(chan struct{})
	defer close(stop)
	if !c.startIcons(stop) {
		t.Fatal("fetching is on")
	}
	p.wait(t, "right after the start", 2*time.Minute)
	p.tick <- clk.Now()
	p.wait(t, "the clock is not set yet", time.Minute)
	clk.Set(noon)
	p.tick <- clk.Now()
	p.fetch(t, "a")
	p.wait(t, "between two sites", 2*time.Second)
	p.tick <- clk.Now()
	p.fetch(t, "c") // b's failure holds, without a pause for it
	p.wait(t, "between two sites", 2*time.Second)
	p.tick <- clk.Now()
	p.fetch(t, "e") // d is kept
	p.wait(t, "the next pass", 24*time.Hour)
}

// A router whose line is not up yet, or is down, gets network errors from
// every site. They are not the sites' failures, so they are not recorded,
// and three in a row end the pass rather than walking the whole list; a
// success in between starts the count again.
func TestABootWithoutTheInternetCostsNoIcons(t *testing.T) {
	clk := &clock{t: noon}
	c := iconConsole(t, clk, "a", "b", "c", "d", "e")
	offline := fmt.Errorf("%w: lookup: no such host", icons.ErrNetwork)
	var mu sync.Mutex
	script := []error{offline, offline, nil, offline, offline, offline, offline, offline}
	p := probeIcons(t, c, func(string) error {
		mu.Lock()
		defer mu.Unlock()
		err := script[0]
		script = script[1:]
		return err
	})
	stop := make(chan struct{})
	defer close(stop)
	c.startIcons(stop)
	p.wait(t, "right after the start", 2*time.Minute)
	p.tick <- clk.Now()
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		if i > 0 {
			p.wait(t, "between two sites", 2*time.Second)
			p.tick <- clk.Now()
		}
		p.fetch(t, id)
	}
	p.wait(t, "two errors, a success, two errors: the pass went to the end", 24*time.Hour)
	p.tick <- clk.Now()
	p.fetch(t, "a")
	p.wait(t, "between two sites", 2*time.Second)
	p.tick <- clk.Now()
	p.fetch(t, "b")
	p.wait(t, "between two sites", 2*time.Second)
	p.tick <- clk.Now()
	p.fetch(t, "c")
	p.wait(t, "three network errors in a row end the pass", 24*time.Hour)
	if m, _ := os.ReadFile(filepath.Join(c.o.State, "icons", "failures.json")); len(m) != 0 {
		t.Errorf("no network error is recorded: %s", m)
	}
}

// The init script turns the UCI settings into serve's command line. It is
// run here for real, with procd and uci stood in, and its command line is
// parsed by serve's own flag set: a value the flag package does not take
// where it is written stops the parse, and every option after it would be
// ignored without a word.
func TestTheInitCommandLineIsParsedAsWritten(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	argv, _, env := runInit(t, state,
		"UCI_interface=br-lan br-guest", "UCI_mode=forward", "UCI_listen=192.168.50.1:8080",
		"UCI_log_group=9", "UCI_capture=0", "UCI_passive_dns=0", "UCI_fetch_icons=0",
		"UCI_geo=/g.tsv.gz", "UCI_apps=/a.tsv", "UCI_oui=/o.txt", "UCI_meta=/m.json",
		"UCI_state="+state, "UCI_wan=wan9", "UCI_ping_target=gw.lan", "UCI_zonename=Europe/Kyiv",
		"UCI_cert=/etc/c.crt", "UCI_key=/etc/c.key", "UCI_metrics_token=scrape-tok",
	)
	if len(argv) < 2 || argv[0] != "/usr/sbin/netryx" || argv[1] != "serve" {
		t.Fatalf("the command is netryx serve: %q", argv)
	}
	// The scrape token goes in the service's environment, which only root
	// reads, never on the command line, which every user's ps shows.
	for _, a := range argv {
		if strings.Contains(a, "scrape-tok") || strings.HasPrefix(a, "-metrics-token") {
			t.Errorf("the scrape token is on the command line: %q", a)
		}
	}
	tokenEnv := ""
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "NETRYX_METRICS_TOKEN="); ok {
			tokenEnv = v
			t.Setenv("NETRYX_METRICS_TOKEN", v)
		}
	}
	if tokenEnv != "scrape-tok" {
		t.Errorf("procd is given the token in the environment: %q", env)
	}
	f, err := parseArgs("serve", argv[2:], flag.ContinueOnError)
	if err != nil {
		t.Fatalf("serve refuses the init's command line %q: %v", argv, err)
	}
	if v, set := os.LookupEnv("NETRYX_METRICS_TOKEN"); set {
		t.Errorf("serve leaves the token in its environment for its children: %q", v)
	}

	// Every option the shipped config names reaches the command line under
	// its flag, so an option the init reads and then drops cannot ship again.
	uciFlag := map[string]string{
		"interface": "watch-iface", "mode": "mode", "listen": "addr", "log_group": "group",
		"capture": "capture", "passive_dns": "dns", "geo": "geo", "apps": "apps", "oui": "oui",
		"meta": "meta", "fetch_icons": "fetch-icons", "state": "state", "wan": "wan", "ping_target": "ping",
		"cert": "cert", "key": "key", "http_redirect": "http-redirect",
	}
	// Options that reach serve through procd's environment instead.
	uciEnv := map[string]string{"metrics_token": "NETRYX_METRICS_TOKEN", "memory_limit": "GOMEMLIMIT"}
	conf, err := os.ReadFile("../../package/netryx/files/netryx.config")
	if err != nil {
		t.Fatal(err)
	}
	passed := map[string]bool{}
	for _, a := range argv[2:] {
		if name, _, ok := strings.Cut(strings.TrimPrefix(a, "-"), "="); ok {
			passed[name] = true
		}
	}
	options := 0
	for _, line := range strings.Split(string(conf), "\n") {
		w := strings.Fields(line)
		if len(w) < 2 || (w[0] != "option" && w[0] != "list") {
			continue
		}
		options++
		if name, ok := uciEnv[w[1]]; ok {
			if !strings.Contains(strings.Join(env, "\n"), name+"=") {
				t.Errorf("netryx.config names option %q, and the init never sets %s", w[1], name)
			}
			continue
		}
		fl, ok := uciFlag[w[1]]
		if !ok {
			t.Errorf("netryx.config names option %q, which no flag of serve carries", w[1])
			continue
		}
		if !passed[fl] {
			t.Errorf("netryx.config names option %q, and the init never passes -%s", w[1], fl)
		}
	}
	if options == 0 {
		t.Fatal("measured nothing: no option read from netryx.config")
	}
	for name, got := range map[string][2]any{
		"watch-iface":   {*f.watchIf, "br-lan,br-guest"},
		"mode":          {*f.watchMode, "forward"},
		"addr":          {*f.addr, "192.168.50.1:8080"},
		"group":         {*f.group, uint(9)},
		"capture":       {*f.capture, false},
		"dns":           {*f.passiveDNS, false},
		"fetch-icons":   {*f.fetchIcons, false},
		"geo":           {*f.geoPath, "/g.tsv.gz"},
		"apps":          {*f.appsPath, "/a.tsv"},
		"oui":           {*f.ouiPath, "/o.txt"},
		"meta":          {*f.metaPath, "/m.json"},
		"state":         {*f.stateDir, state},
		"wan":           {*f.wanIface, "wan9"},
		"ping":          {*f.pingTarget, "gw.lan"},
		"zone":          {*f.zone, "Europe/Kyiv"},
		"cert":          {*f.cert, "/etc/c.crt"},
		"key":           {*f.key, "/etc/c.key"},
		"metrics-token": {*f.metricsToken, "scrape-tok"},
	} {
		if got[0] != got[1] {
			t.Errorf("-%s: serve read %v, the config said %v", name, got[0], got[1])
		}
	}

	// The group read from the config is the group the rule copies to.
	if g := ruleFor(*f.watchMode, []string{"br-lan"}, *f.passiveDNS, uint16(*f.group)).Group; g != 9 {
		t.Errorf("the rule copies to group %d; the config said 9", g)
	}

	// A group the kernel cannot have is refused, not wrapped into another.
	if _, err := parseArgs("serve", []string{"-group=70000"}, flag.ContinueOnError); err == nil {
		t.Error("-group=70000 was taken")
	}

	// The form that stopped the parse is refused, naming what was left over.
	_, err = parseArgs("serve", []string{"-capture", "false", "-fetch-icons=false"}, flag.ContinueOnError)
	if err == nil || !strings.Contains(err.Error(), "false") {
		t.Errorf("a stray word on serve's command line is an error that names it: %v", err)
	}
}

// The Go heap is held by a soft limit from the config, passed in procd's
// environment as GOMEMLIMIT: 160MiB when the option is absent, the value
// written when it is a whole number above zero with B or KiB (at most 15
// digits), MiB (at most 12) or GiB (at most 9), so it stays under the
// runtime's int64 of bytes, and nothing when it is empty (no limit) or
// malformed (logged: a value the runtime cannot parse stops the service at
// start, and a zero would keep the collector running without pause). procd
// keeps only the last env of an instance, so the limit and the scrape token
// travel together.
func TestTheMemoryLimitReachesTheServiceAsGOMEMLIMIT(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	limit := func(env []string) (string, int) {
		v, n := "", 0
		for _, e := range env {
			if s, ok := strings.CutPrefix(e, "GOMEMLIMIT="); ok {
				v, n = s, n+1
			}
		}
		return v, n
	}
	logged := func(ev []string) string {
		for _, e := range ev {
			if strings.HasPrefix(e, "logger ") && strings.Contains(e, "memory_limit") {
				return e
			}
		}
		return ""
	}
	for _, c := range []struct {
		name, want string
		env        []string
		log        bool
	}{
		{"absent", "160MiB", nil, false},
		{"set", "96MiB", []string{"UCI_memory_limit=96MiB"}, false},
		{"bytes", "123456789B", []string{"UCI_memory_limit=123456789B"}, false},
		{"KiB", "200000KiB", []string{"UCI_memory_limit=200000KiB"}, false},
		{"GiB", "1GiB", []string{"UCI_memory_limit=1GiB"}, false},
		{"empty", "", []string{"UCI_memory_limit="}, false},
		{"MB is not a Go unit", "", []string{"UCI_memory_limit=160MB"}, true},
		{"no number", "", []string{"UCI_memory_limit=MiB"}, true},
		{"no unit", "", []string{"UCI_memory_limit=160"}, true},
		{"negative", "", []string{"UCI_memory_limit=-1GiB"}, true},
		{"a word", "", []string{"UCI_memory_limit=lots"}, true},
		{"two words", "", []string{"UCI_memory_limit=160MiB 1GiB"}, true},
		{"fifteen digits", "999999999999999B", []string{"UCI_memory_limit=999999999999999B"}, false},
		{"sixteen digits", "", []string{"UCI_memory_limit=1000000000000000B"}, true},
		{"zero", "", []string{"UCI_memory_limit=0MiB"}, true},
		{"zeros", "", []string{"UCI_memory_limit=000B"}, true},
		{"KiB at its cap", "999999999999999KiB", []string{"UCI_memory_limit=999999999999999KiB"}, false},
		{"KiB over its cap", "", []string{"UCI_memory_limit=1000000000000000KiB"}, true},
		{"MiB at its cap", "999999999999MiB", []string{"UCI_memory_limit=999999999999MiB"}, false},
		{"MiB over its cap", "", []string{"UCI_memory_limit=9999999999999MiB"}, true},
		{"GiB at its cap", "999999999GiB", []string{"UCI_memory_limit=999999999GiB"}, false},
		{"GiB over its cap", "", []string{"UCI_memory_limit=9999999999GiB"}, true},
	} {
		_, ev, env := runInit(t, state, append([]string{"UCI_state=" + state}, c.env...)...)
		got, n := limit(env)
		if got != c.want || (c.want != "" && n != 1) || (c.want == "" && n != 0) {
			t.Errorf("%s: procd's env carries GOMEMLIMIT %q (%d times), want %q: %q", c.name, got, n, c.want, env)
		}
		if l := logged(ev); (l != "") != c.log {
			t.Errorf("%s: logged %v, want %v: %q", c.name, l != "", c.log, ev)
		}
	}

	// With the scrape token set as well, both reach the one env procd keeps.
	_, _, env := runInit(t, state, "UCI_state="+state, "UCI_metrics_token=scrape-tok", "UCI_memory_limit=96MiB")
	if got, _ := limit(env); got != "96MiB" || !strings.Contains(strings.Join(env, "\n"), "NETRYX_METRICS_TOKEN=scrape-tok") {
		t.Errorf("the limit and the scrape token are both in procd's env: %q", env)
	}
}

// runInit runs start_service of the init script with procd, uci, nft and
// logger stood in, and returns serve's command line and a log of what the
// script did, in order: each nft call with the TZ it ran under, each logger
// line, and "instance" where procd's instance was opened.
func runInit(t *testing.T, state string, env ...string) (argv, events, procEnv []string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	const initPath = "../../package/netryx/files/netryx.init"
	// Read here as well as by sh, so the test cache sees the script change.
	if _, err := os.ReadFile(initPath); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "argv")
	evlog := filepath.Join(dir, "events")
	harness := `
unset TZ
config_load() { :; }
config_get() { eval "_v=\${UCI_$3-\$4}"; eval "$1=\$_v"; }
config_get_bool() {
	config_get "$@"; eval "_b=\$$1"
	case "$_b" in 1|on|true|yes|enabled) eval "$1=1" ;; *) eval "$1=0" ;; esac
}
uci() {
	case "$*" in
	"-q get system.@system[0].zonename") [ -n "$UCI_zonename" ] && printf '%s\n' "$UCI_zonename" ;;
	*) return 1 ;;
	esac
}
nft() {
	printf 'nft %s TZ=%s\n' "$*" "${TZ-}" >>"$EVENTS"
	case "$*" in
	"list table inet netryx_rules") [ -n "$TABLE_PRESENT" ] ;;
	"-f "*) [ "$2" != "$NFT_FAILS" ] || { echo "Error: syntax error" >&2; return 1; } ;;
	esac
}
logger() { printf 'logger %s\n' "$*" >>"$EVENTS"; }
procd_open_instance() { echo instance >>"$EVENTS"; }
procd_close_instance() { :; }
procd_set_param() {
	if [ "$1" = env ]; then
		# procd keeps the last env given to an instance, so a second call
		# replaces the first, as it does on the router.
		shift
		: >"$OUT.env"
		for a in "$@"; do printf '%s\n' "$a" >>"$OUT.env"; done
		return 0
	fi
	[ "$1" = command ] || return 0
	shift
	: >"$OUT"
	for a in "$@"; do printf '%s\n' "$a" >>"$OUT"; done
}
modprobe() { :; }
sysctl() { :; }
. "$INIT"
start_service
`
	cmd := exec.Command("sh", "-c", harness)
	cmd.Env = append(append(os.Environ(), "INIT="+initPath, "OUT="+out, "EVENTS="+evlog), env...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the init script did not run: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	argv = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if ev, err := os.ReadFile(evlog); err == nil {
		events = strings.Split(strings.TrimSuffix(string(ev), "\n"), "\n")
	}
	if pe, err := os.ReadFile(out + ".env"); err == nil {
		procEnv = strings.Split(strings.TrimSuffix(string(pe), "\n"), "\n")
	}
	return argv, events, procEnv
}

// The rules outlive netryx: the init loads the last applied ruleset and the
// saved app sets with TZ=UTC before the service starts, rules first, so they
// hold from boot even if netryx never starts. A table already in the kernel
// (a restart, a reload) is left alone, a missing file is skipped, and a file
// that does not load is logged and does not keep netryx from starting.
func TestSetsAreSavedAndLoadedAtBoot(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	rules, sets := filepath.Join(state, "rules.nft"), filepath.Join(state, "sets.nft")
	for _, f := range []string{rules, sets} {
		if err := os.WriteFile(f, []byte("# x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := []string{"UCI_state=" + state}

	argv, ev, _ := runInit(t, state, base...)
	want := []string{
		"nft list table inet netryx_rules TZ=",
		"nft -f " + rules + " TZ=UTC",
		"nft -f " + sets + " TZ=UTC",
		"instance",
	}
	if strings.Join(ev, "\n") != strings.Join(want, "\n") {
		t.Fatalf("at boot the rules, then the sets, are loaded before the instance:\n got %q\nwant %q", ev, want)
	}
	f, err := parseArgs("serve", argv[2:], flag.ContinueOnError)
	if err != nil {
		t.Fatal(err)
	}
	if *f.zone != "UTC" {
		t.Fatalf("with no zone in the system config serve is told UTC, got %q", *f.zone)
	}

	_, ev, _ = runInit(t, state, append(base, "TABLE_PRESENT=1")...)
	if strings.Join(ev, "\n") != "nft list table inet netryx_rules TZ=\ninstance" {
		t.Fatalf("a table already in the kernel is not loaded over: %q", ev)
	}

	if err := os.Remove(sets); err != nil {
		t.Fatal(err)
	}
	_, ev, _ = runInit(t, state, append(base, "NFT_FAILS="+rules)...)
	if len(ev) != 4 || ev[1] != "nft -f "+rules+" TZ=UTC" || !strings.HasPrefix(ev[2], "logger ") ||
		!strings.Contains(ev[2], "syntax error") || ev[3] != "instance" {
		t.Fatalf("a missing file is skipped, a failing one logged, and the service still starts: %q", ev)
	}
}

// Isolation keeps a device from every network the router serves: the rules
// get the same list IsLocal answers from, and never the ISP's side.
func TestTheRulesKeepIsolatedDevicesFromEveryHouseNetwork(t *testing.T) {
	c, err := buildConsole(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range c.local.Prefixes() {
		got[p.String()] = true
	}
	for _, want := range []string{"192.168.50.0/24", "192.168.2.0/24", "2001:db8:50::/64"} {
		if !got[want] {
			t.Errorf("Prefixes() lacks %s: %v", want, got)
		}
	}
	for p := range got {
		if netip.MustParsePrefix(p).Contains(netip.MustParseAddr("203.0.113.1")) {
			t.Errorf("Prefixes() holds the WAN subnet %s", p)
		}
	}
}

type fakeInv []inventory.Device

func (f fakeInv) Devices() []inventory.Device { return f }

// The quarantine's baseline is every MAC the house has known: the inventory
// (wire, Wi-Fi, leases, labels) and the history's devices of the last week,
// never a service row of the history.
func TestTheQuarantineSeesTheInventoryAndTheWeeksHistory(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	inv := fakeInv{{MAC: "02:4e:58:00:00:01", Hostname: "tv", Last: now}}
	hist := func(from, to time.Time) []string {
		if to.Sub(from) != 7*24*time.Hour {
			t.Errorf("history asked for %v; want a week", to.Sub(from))
		}
		return []string{"02:4e:58:00:00:02", "other", "router", "02:4e:58:00:00:01"}
	}
	got := map[string]string{}
	for _, s := range houseSeen(inv, hist, func(inventory.Device) bool { return true }, now) {
		got[s.MAC] = s.Hostname
	}
	if len(got) != 2 || got["02:4e:58:00:00:01"] != "tv" {
		t.Fatalf("seen = %v; want the tv (with its hostname) and the laptop from history", got)
	}
	if _, ok := got["02:4e:58:00:00:02"]; !ok {
		t.Fatalf("the history's laptop is missing: %v", got)
	}
}

// A port is on a hardware switch when the kernel gives it a switch id; a veth
// or a software bridge port reads none (docs/stage2c-measured.md, section 6).
func TestAPortOnAHardwareSwitchIsToldApart(t *testing.T) {
	fsys := fstest.MapFS{
		"sys/class/net/lan2/phys_switch_id":     {Data: []byte("00000000\n")},
		"sys/class/net/v-camera/phys_switch_id": {Data: []byte("")},
	}
	if !onHardwareSwitch(fsys, "lan2") {
		t.Error("lan2 has a switch id")
	}
	for _, p := range []string{"v-camera", "nothere", "../etc"} {
		if onHardwareSwitch(fsys, p) {
			t.Errorf("%s is not on a hardware switch", p)
		}
	}
	w := readWireless(fstest.MapFS{"etc/config/wireless": {Data: []byte("config wifi-iface 'a'\n\toption ssid 'Things'\n\toption isolate '1'\n")}})
	if len(w) != 1 || !w[0].Isolate || w[0].SSID != "Things" {
		t.Fatalf("readWireless = %+v", w)
	}
	if w := readWireless(fstest.MapFS{}); w != nil {
		t.Fatalf("no wireless config should read as none: %+v", w)
	}
}

// A try copy becomes one alert naming the device and the country the address
// belongs to.
func TestATryNamesTheDeviceAndTheCountry(t *testing.T) {
	var got []alerts.Alert
	h := tryHandler(func(ip netip.Addr) string {
		if ip == netip.MustParseAddr("5.8.0.1") {
			return "RU"
		}
		return "--"
	}, func(a alerts.Alert) { got = append(got, a) })
	h("blockru", "02:4E:58:00:00:02", net.ParseIP("5.8.0.1"))
	h("blockru", "", net.ParseIP("5.8.0.1"))
	h("blockru", "02:4e:58:00:00:02", nil)
	if len(got) != 1 || got[0].Kind != alerts.BlockedCountry || got[0].MAC != "02:4e:58:00:00:02" || got[0].Country != "RU" {
		t.Fatalf("alerts = %+v; want one try towards RU by the laptop", got)
	}
}

// The upload check reads the history: the last closed hour against the week
// before it, for a device with a day of history.
func TestUnusualUploadsAreFoundInTheHistory(t *testing.T) {
	end := time.Date(2026, 11, 21, 12, 30, 0, 0, time.UTC)
	now := end
	// The history takes a delta only while its hour is open, so the clock
	// walks forward with the deltas, as it does on a router.
	st := rollup.NewStore(rollup.Options{Now: func() time.Time { return now }})
	cam, tv := "02:4e:58:00:00:04", "02:4e:58:00:00:01"
	for h := 48; h >= 2; h-- {
		at := end.Truncate(time.Hour).Add(-time.Duration(h) * time.Hour)
		now = at.Add(time.Second)
		st.Add(flows.Delta{At: at, MAC: cam, App: "unknown", CC: "US", Up: 10e6, Down: 1e6})
		st.Add(flows.Delta{At: at, MAC: tv, App: "netflix", CC: "US", Up: 1e6, Down: 900e6})
	}
	last := end.Truncate(time.Hour).Add(-time.Hour)
	now = last.Add(time.Minute)
	st.Add(flows.Delta{At: last.Add(time.Minute), MAC: cam, App: "unknown", CC: "US", Up: 500e6})
	st.Add(flows.Delta{At: last.Add(time.Minute), MAC: tv, App: "netflix", CC: "US", Up: 2e6, Down: 900e6})
	now = end
	var found []string
	checkUploads(st, now, func(mac string, up uint64) { found = append(found, fmt.Sprintf("%s %d", mac, up)) })
	if len(found) != 1 || found[0] != cam+" 500000000" {
		t.Fatalf("found = %v; want only the camera's 500 MB hour", found)
	}
}

// With the capture off nobody reads the log group, so the rules copy no tries.
func TestTheRulesCopyTriesOnlyWhileTheCaptureReads(t *testing.T) {
	if logGroupFor(false, 7) != 0 || logGroupFor(true, 7) != 7 {
		t.Fatalf("logGroupFor(false, 7) = %d, (true, 7) = %d; want 0 and 7", logGroupFor(false, 7), logGroupFor(true, 7))
	}
}

// Found in the stage's live run: the neighbour table also holds machines on
// the WAN side, and a quarantine that saw them told the owner of a "new
// device" that is not in the house. Only a device on a house port or with a
// house address is seen.
func TestTheQuarantineSeesOnlyTheHouse(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	inv := fakeInv{
		{MAC: "02:4e:58:00:00:01", IPv4: []string{"192.168.50.211"}},
		{MAC: "10:66:6a:a1:19:2e", IPv4: []string{"10.166.170.1"}, IPv6: []string{"fe80::1266:6aff:fea1:192e"}},
		{MAC: "02:4e:58:00:00:07", Link: inventory.Link{Kind: "wifi", SSID: "Home"}},
	}
	local := func(a netip.Addr) bool {
		return a.IsLinkLocalUnicast() || netip.MustParsePrefix("192.168.50.0/24").Contains(a)
	}
	got := map[string]bool{}
	for _, s := range houseSeen(inv, nil, inHouse(local), now) {
		got[s.MAC] = true
	}
	if !got["02:4e:58:00:00:01"] || !got["02:4e:58:00:00:07"] || got["10:66:6a:a1:19:2e"] {
		t.Fatalf("seen = %v; want the tv and the Wi-Fi device, never the WAN neighbour", got)
	}
}

// Final review I6: labels.json names are part of the baseline too.
func TestLabelledDevicesAreKnown(t *testing.T) {
	got := knownMACs([]rules.Seen{{MAC: "02:4e:58:00:00:01"}}, map[string]inventory.Label{"02:4E:58:00:00:09": {Name: "NAS"}, "nonsense": {}})
	want := map[string]bool{"02:4e:58:00:00:01": true, "02:4e:58:00:00:09": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("known = %v; want the seen device and the labelled one", got)
	}
}

// Nothing is sent to Telegram before the owner's alert settings are read: the
// defaults say telegram, and on a router with Hermes's bot the first round
// would send before `channel 'console'` is seen.
func TestNoAlertIsSentBeforeTheSettingsAreRead(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	st := alerts.NewStore(filepath.Join(t.TempDir(), "alerts.json"), func() time.Time { return now }, time.UTC)
	st.Raise(alerts.Alert{Kind: alerts.Upload, MAC: "02:4e:58:00:00:04", Bytes: 500e6})
	c := &console{alerts: st}
	sends := 0
	send := func(context.Context, alerts.Bot, string) error { sends++; return nil }
	bot := func() (alerts.Bot, string) { return alerts.Bot{TokenFile: "/x", Chat: "1"}, "" }
	var set rules.Alerts
	read := false
	settings := func() (rules.Alerts, bool) { return set, read }
	unsent := func() int { return len(st.Unsent()) }

	c.deliverRound(context.Background(), settings, bot, send)
	if sends != 0 || unsent() != 1 {
		t.Fatalf("before the settings are read: %d sends, %d unsent; want 0 and 1", sends, unsent())
	}
	set, read = rules.DefaultAlerts, true
	set.Channel = "console"
	st.Raise(alerts.Alert{Kind: alerts.Upload, MAC: "02:4e:58:00:00:05", Bytes: 500e6})
	c.deliverRound(context.Background(), settings, bot, send)
	if sends != 0 {
		t.Fatalf("on the console channel: %d sends; want 0", sends)
	}
	st.Raise(alerts.Alert{Kind: alerts.Upload, MAC: "02:4e:58:00:00:06", Bytes: 500e6})
	set.Channel = "telegram"
	c.deliverRound(context.Background(), settings, bot, send)
	if sends != 1 {
		t.Fatalf("on the telegram channel: %d sends; want 1", sends)
	}
}

// The hourly upload check raises nothing before the owner's settings are
// read (the defaults have uploads on), one alert once they are read with
// uploads on, and none with uploads off.
func TestNoUploadAlertIsRaisedBeforeTheSettingsAreRead(t *testing.T) {
	end := time.Date(2026, 11, 21, 12, 30, 0, 0, time.UTC)
	now := end
	st := rollup.NewStore(rollup.Options{Now: func() time.Time { return now }})
	cam := "02:4e:58:00:00:04"
	for h := 48; h >= 2; h-- {
		at := end.Truncate(time.Hour).Add(-time.Duration(h) * time.Hour)
		now = at.Add(time.Second)
		st.Add(flows.Delta{At: at, MAC: cam, App: "unknown", CC: "US", Up: 10e6, Down: 1e6})
	}
	last := end.Truncate(time.Hour).Add(-time.Hour)
	now = last.Add(time.Minute)
	st.Add(flows.Delta{At: last.Add(time.Minute), MAC: cam, App: "unknown", CC: "US", Up: 500e6})
	now = end
	set := rules.DefaultAlerts
	read := false
	settings := func() (rules.Alerts, bool) { return set, read }
	raised := 0
	raise := func(string, uint64) { raised++ }
	uploadRound(settings, st, now, raise)
	if raised != 0 {
		t.Fatalf("before the settings are read: %d raised; want 0", raised)
	}
	read, set.Upload = true, true
	uploadRound(settings, st, now, raise)
	if raised != 1 {
		t.Fatalf("read, uploads on: %d raised; want 1", raised)
	}
	set.Upload = false
	uploadRound(settings, st, now, raise)
	if raised != 1 {
		t.Fatalf("read, uploads off: %d raised; want still 1", raised)
	}
}

// The minutes are kept in RAM across a restart of netryx: the stop writes
// them to the minutes file, the next start reads them and takes the file
// away, so a crash later leaves nothing stale behind.
func TestTheMinutesAreKeptInRAMAcrossARestart(t *testing.T) {
	o := testOptions(t)
	o.Minutes = filepath.Join(t.TempDir(), "netryx", "minutes.json")
	clk := &clock{t: time.Date(2026, 11, 20, 10, 30, 0, 0, time.UTC)}
	o.Now = clk.Now
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	addDelta(c.store, clk.Now())
	if err := c.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	fi, err := os.Stat(o.Minutes)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the minutes file: %v %v", fi, err)
	}
	clk.Set(clk.Now().Add(time.Minute))
	again, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := again.store.Recent(rollup.Filter{}, "mac", 10*time.Minute)
	var sum uint64
	for _, r := range rows {
		sum += r.Bytes.Down + r.Bytes.Up
	}
	if sum != 1000 {
		t.Errorf("the last minutes after a restart hold %d bytes, want 1000", sum)
	}
	if _, err := os.Stat(o.Minutes); !os.IsNotExist(err) {
		t.Errorf("the file read at the start is still there: %v", err)
	}
}

// The day's file is written by the first tick after the day has closed, the
// tick that writes the closed day of the history, into <state>/daily; a tick
// before midnight writes none.
func TestTheDailyFileIsWrittenByTheFirstTickAfterTheDayCloses(t *testing.T) {
	o := testOptions(t)
	start := time.Date(2026, 11, 20, 10, 30, 0, 0, time.UTC)
	clk := &clock{t: start}
	o.Now = clk.Now
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	addDelta(c.store, start)
	file := filepath.Join(o.State, "daily", "2026-11-20.day")
	for _, at := range []time.Time{
		time.Date(2026, 11, 20, 23, 30, 0, 0, time.UTC),
		time.Date(2026, 11, 21, 0, 0, 30, 0, time.UTC),
	} {
		clk.Set(at)
		c.hist.tick(at)
		if _, err := os.Stat(file); err == nil {
			t.Fatalf("at %v the open day is on flash", at)
		}
	}
	at := time.Date(2026, 11, 21, 0, 2, 30, 0, time.UTC)
	clk.Set(at)
	c.hist.tick(at)
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the first tick after the day closed did not write it: %v", err)
	}
	days := c.daily.Days([6]byte{0x02, 0, 0, 0, 0, 0x01}, at)
	if len(days) == 0 {
		t.Fatal("no days")
	}
	last := days[len(days)-1]
	if !last.Known || last.Mask != 1<<10 || last.Down[10] != 1024 || last.Up[10] != 1024 {
		t.Errorf("the day as read: known %v mask %024b down %d up %d", last.Known, last.Mask, last.Down[10], last.Up[10])
	}
}
