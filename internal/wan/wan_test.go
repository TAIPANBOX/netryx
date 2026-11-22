package wan

import (
	"math"
	"testing"
	"testing/fstest"
	"time"
)

// ubusFixture is the shape of `ubus call network.interface.wan status` on OpenWrt 25.12.
const ubusFixture = `{
	"up": true,
	"pending": false,
	"available": true,
	"autostart": true,
	"dynamic": false,
	"proto": "dhcp",
	"device": "eth0",
	"l3_device": "eth0",
	"uptime": 1040,
	"ipv4-address": [{"address": "203.0.113.24", "mask": 24}],
	"ipv6-address": [],
	"route": [{"target": "0.0.0.0", "mask": 0, "nexthop": "203.0.113.1", "source": "203.0.113.24/32"}],
	"dns-server": ["192.0.2.1"],
	"dns-search": [],
	"neighbors": []
}`

func TestUbusInterfaceStatus(t *testing.T) {
	st, err := ParseUbusInterface([]byte(ubusFixture))
	if err != nil {
		t.Fatalf("ParseUbusInterface: %v", err)
	}
	if !st.Up {
		t.Errorf("Up = false, want true")
	}
	if st.Device != "eth0" {
		t.Errorf("Device = %q, want eth0", st.Device)
	}
	if st.IPv4 != "203.0.113.24" {
		t.Errorf("IPv4 = %q, want 203.0.113.24", st.IPv4)
	}
	if st.Uptime != 1040*time.Second {
		t.Errorf("Uptime = %v, want 17m20s", st.Uptime)
	}
}

func TestUbusInterfaceRejectsGarbage(t *testing.T) {
	if _, err := ParseUbusInterface([]byte("not json")); err == nil {
		t.Fatal("ParseUbusInterface accepted non-JSON input")
	}
}

func TestUbusEmptyIPv4LeavesAddressBlank(t *testing.T) {
	st, err := ParseUbusInterface([]byte(`{"up":false,"device":"eth0","uptime":0,"ipv4-address":[]}`))
	if err != nil {
		t.Fatalf("ParseUbusInterface: %v", err)
	}
	if st.IPv4 != "" {
		t.Errorf("IPv4 = %q, want empty while the line has no address", st.IPv4)
	}
	if st.Up {
		t.Errorf("Up = true, want false")
	}
}

func TestUbusMissingL3DeviceFallsBackToDevice(t *testing.T) {
	st, err := ParseUbusInterface([]byte(`{"up":true,"device":"pppoe-wan","uptime":5,"ipv4-address":[{"address":"198.51.100.7"}]}`))
	if err != nil {
		t.Fatalf("ParseUbusInterface: %v", err)
	}
	if st.Device != "pppoe-wan" {
		t.Errorf("Device = %q, want pppoe-wan from the configured device", st.Device)
	}
}

func TestPingFormats(t *testing.T) {
	cases := []struct {
		name string
		out  string
		ms   float64
		ok   bool
	}{
		{
			name: "busybox reply",
			out: "PING 1.1.1.1 (1.1.1.1): 56 data bytes\n" +
				"64 bytes from 1.1.1.1: seq=0 ttl=57 time=9.812 ms\n\n" +
				"--- 1.1.1.1 ping statistics ---\n" +
				"1 packets transmitted, 1 packets received, 0% packet loss\n" +
				"round-trip min/avg/max = 9.812/9.812/9.812 ms\n",
			ms: 9.812, ok: true,
		},
		{
			name: "iputils reply",
			out: "PING 1.1.1.1 (1.1.1.1) 56(84) bytes of data.\n" +
				"64 bytes from 1.1.1.1: icmp_seq=1 ttl=57 time=9.81 ms\n\n" +
				"--- 1.1.1.1 ping statistics ---\n" +
				"1 packets transmitted, 1 received, 0% packet loss, time 0ms\n",
			ms: 9.81, ok: true,
		},
		{
			name: "sub-millisecond reply keeps its fraction",
			out:  "64 bytes from 192.168.1.1: seq=0 ttl=64 time=0.5 ms\n",
			ms:   0.5, ok: true,
		},
		{
			name: "timeout has no time field",
			out: "PING 1.1.1.1 (1.1.1.1): 56 data bytes\n\n" +
				"--- 1.1.1.1 ping statistics ---\n" +
				"1 packets transmitted, 0 packets received, 100% packet loss\n",
			ms: 0, ok: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ms, ok := ParsePing(c.out)
			if ok != c.ok || ms != c.ms {
				t.Errorf("ParsePing = (%v, %v), want (%v, %v)", ms, ok, c.ms, c.ok)
			}
		})
	}
}

func TestProberStats(t *testing.T) {
	var p Prober
	p.Add(10, true)
	p.Add(0, false)
	p.Add(14, true)
	p.Add(11, true)

	latency, jitter, loss, ok := p.Stats()
	if !ok {
		t.Fatal("ok = false, want true with three successful probes")
	}
	if want := 35.0 / 3; math.Abs(latency-want) > 1e-9 {
		t.Errorf("latency = %v, want %v (mean of 10, 14, 11)", latency, want)
	}
	// Successful latencies 10, 14, 11 give differences 4 and 3; the failed probe is skipped.
	if jitter != 3.5 {
		t.Errorf("jitter = %v, want 3.5", jitter)
	}
	if loss != 25 {
		t.Errorf("loss = %v, want 25 (1 of 4 failed)", loss)
	}
}

func TestLatencyIsTheMeanOfTheWindow(t *testing.T) {
	var p Prober
	p.Add(10, true)
	p.Add(20, true)
	p.Add(30, true)
	p.Add(0, false)

	latency, _, _, ok := p.Stats()
	if !ok {
		t.Fatal("ok = false, want true")
	}
	// The latest sample is 30; the tile must show the window mean, 20, next to the loss figure.
	if latency != 20 {
		t.Errorf("latency = %v, want 20 (mean of the successful probes in the window)", latency)
	}
}

func TestADeadLineReportsNoLatency(t *testing.T) {
	var p Prober
	for i := 0; i < 5; i++ {
		p.Add(0, false)
	}

	latency, jitter, loss, ok := p.Stats()
	if ok {
		t.Error("ok = true on a window with no successful probe, want false")
	}
	if latency != 0 || jitter != 0 {
		t.Errorf("latency, jitter = (%v, %v), want (0, 0) when ok is false", latency, jitter)
	}
	// Loss is still a real number on a dead line: it is 100 percent.
	if loss != 100 {
		t.Errorf("loss = %v, want 100", loss)
	}
}

func TestProberForgetsResultsBeyondThirty(t *testing.T) {
	var p Prober
	for i := 0; i < 30; i++ {
		p.Add(0, false)
	}
	for i := 0; i < 30; i++ {
		p.Add(5, true)
	}
	_, _, loss, _ := p.Stats()
	if loss != 0 {
		t.Errorf("loss = %v, want 0 once the 30 failures left the window", loss)
	}
}

func TestProberWithoutSamplesReportsZero(t *testing.T) {
	var p Prober
	latency, jitter, loss, ok := p.Stats()
	if ok || latency != 0 || jitter != 0 || loss != 0 {
		t.Errorf("Stats on empty prober = (%v, %v, %v, %v), want zeros and ok false", latency, jitter, loss, ok)
	}
}

const (
	rxPath = "sys/class/net/eth0/statistics/rx_bytes"
	txPath = "sys/class/net/eth0/statistics/tx_bytes"
)

func counterFS(rx, tx string) fstest.MapFS {
	return fstest.MapFS{
		rxPath: {Data: []byte(rx)},
		txPath: {Data: []byte(tx)},
	}
}

func TestCounterRates(t *testing.T) {
	fsys := counterFS("1000\n", "2000\n")
	c := NewCounter(fsys, "eth0")
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	rx, tx, err := c.Sample(t0)
	if err != nil || rx != 0 || tx != 0 {
		t.Fatalf("first Sample = (%v, %v, %v), want zero rates and no error", rx, tx, err)
	}

	fsys[rxPath] = &fstest.MapFile{Data: []byte("6000\n")}
	fsys[txPath] = &fstest.MapFile{Data: []byte("2500\n")}
	rx, tx, err = c.Sample(t0.Add(5 * time.Second))
	if err != nil {
		t.Fatalf("second Sample: %v", err)
	}
	if rx != 1000 {
		t.Errorf("rx rate = %v, want 1000 B/s ((6000-1000)/5s)", rx)
	}
	if tx != 100 {
		t.Errorf("tx rate = %v, want 100 B/s ((2500-2000)/5s)", tx)
	}
}

// TestA32BitCounterWrapKeepsTheRate: OpenWrt on mips and arm32 exposes 32-bit
// statistics, so a counter sitting at 2^32-1000 that reads 3000 after the
// wrap moved 4000 bytes, not a reset.
func TestA32BitCounterWrapKeepsTheRate(t *testing.T) {
	fsys := counterFS("4294966296\n", "0\n") // 2^32 - 1000
	c := newCounter(fsys, "eth0", true)
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	if _, _, err := c.Sample(t0); err != nil {
		t.Fatalf("first Sample: %v", err)
	}

	fsys[rxPath] = &fstest.MapFile{Data: []byte("3000\n")}
	rx, _, err := c.Sample(t0.Add(2 * time.Second))
	if err != nil {
		t.Fatalf("wrap Sample: %v", err)
	}
	if rx != 2000 {
		t.Errorf("rx rate across a 32-bit wrap = %v, want 2000 B/s (4000 bytes over 2s)", rx)
	}
}

// TestA64BitCounterResetIsNotAWrap: on a 64-bit build the counter is 64 bits
// wide, so a drop from 1676517 to 109977 is an interface recreated, not a
// wrap. Read as a 32-bit wrap it would report about 429 MB/s of traffic that
// never happened.
func TestA64BitCounterResetIsNotAWrap(t *testing.T) {
	fsys := counterFS("1676517\n", "0\n")
	c := newCounter(fsys, "eth0", false)
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	if _, _, err := c.Sample(t0); err != nil {
		t.Fatalf("first Sample: %v", err)
	}

	fsys[rxPath] = &fstest.MapFile{Data: []byte("109977\n")}
	rx, _, err := c.Sample(t0.Add(10 * time.Second))
	if err != nil {
		t.Fatalf("reset Sample: %v", err)
	}
	if rx != 0 {
		t.Errorf("rx rate = %v, want 0: on a 64-bit build a drop is a reset", rx)
	}
}

// TestAnImplausibleWrapIsAReset: a drop from a 64-bit-sized value can look
// like a 32-bit wrap, but a 2^32 span in 0.1 s is 2.9 GB/s, above 10 Gbit/s,
// so it has to be a reset.
func TestAnImplausibleWrapIsAReset(t *testing.T) {
	fsys := counterFS("4000000000\n", "0\n")
	c := newCounter(fsys, "eth0", true)
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	if _, _, err := c.Sample(t0); err != nil {
		t.Fatalf("first Sample: %v", err)
	}

	fsys[rxPath] = &fstest.MapFile{Data: []byte("1000\n")}
	rx, _, err := c.Sample(t0.Add(100 * time.Millisecond))
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if rx != 0 {
		t.Errorf("rx rate = %v, want 0: an implausible wrap is a reset", rx)
	}
}

func TestCounterReportsMissingInterface(t *testing.T) {
	c := NewCounter(fstest.MapFS{}, "eth9")
	if _, _, err := c.Sample(time.Now()); err == nil {
		t.Fatal("Sample on a missing interface returned no error")
	}
}
