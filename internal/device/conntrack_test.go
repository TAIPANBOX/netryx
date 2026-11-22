package device

import (
	"net"
	"strings"
	"testing"
)

// Lines in the shapes an OpenWrt router actually prints: a NATed TCP flow, the
// router's own UDP flow, an IPv6 flow that needs no NAT, an unreplied flow
// whose marker comes BEFORE the reply tuple, an offloaded flow, and an ICMP
// line that carries type/code instead of ports.
const sample = `ipv4     2 tcp      6 431997 ESTABLISHED src=192.168.50.10 dst=140.82.121.4 sport=51000 dport=443 packets=42 bytes=5120 src=140.82.121.4 dst=192.168.1.23 sport=443 dport=51000 packets=38 bytes=40960 [ASSURED] mark=0 use=1
ipv4     2 udp      17 29 src=192.168.1.23 dst=1.1.1.1 sport=40000 dport=853 packets=2 bytes=200 src=1.1.1.1 dst=192.168.1.23 sport=853 dport=40000 packets=2 bytes=300 mark=0 use=1
ipv6     10 tcp      6 431999 ESTABLISHED src=2a01:4b00:ab09:c000::10 dst=2606:4700::1111 sport=52000 dport=443 packets=5 bytes=600 src=2606:4700::1111 dst=2a01:4b00:ab09:c000::10 sport=443 dport=52000 packets=4 bytes=700 [ASSURED] mark=0 use=1
ipv4     2 tcp      6 118 SYN_SENT src=192.168.50.11 dst=203.0.113.9 sport=51002 dport=22 packets=3 bytes=180 [UNREPLIED] src=203.0.113.9 dst=192.168.1.23 sport=22 dport=51002 packets=0 bytes=0 mark=0 use=1
ipv4     2 tcp      6 86399 ESTABLISHED src=192.168.50.12 dst=142.250.180.14 sport=51004 dport=443 packets=900 bytes=90000 src=142.250.180.14 dst=192.168.1.23 sport=443 dport=51004 packets=1200 bytes=1400000 [OFFLOAD] mark=0 use=1
ipv4     2 icmp     1 29 src=192.168.50.13 dst=1.1.1.1 type=8 code=0 id=1234 packets=4 bytes=400 src=1.1.1.1 dst=192.168.1.23 type=0 code=0 id=1234 packets=4 bytes=400 mark=0 use=1
ipv4     2 unknown  47 598 src=10.0.0.1 dst=10.0.0.2 packets=1 bytes=100 mark=0 use=1`

func find(t *testing.T, flows []Flow, src string) Flow {
	t.Helper()
	for _, f := range flows {
		if f.SrcIP.String() == src {
			return f
		}
	}
	t.Fatalf("no flow from %s in %d flows", src, len(flows))
	return Flow{}
}

func TestParseConntrackFindsThePreNATSource(t *testing.T) {
	flows, st, err := ParseConntrack(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 6 {
		t.Fatalf("expected the six transport flows, got %d", len(flows))
	}
	if st.AcctOff {
		t.Fatal("these lines carry byte counters, so accounting is on")
	}
	f := find(t, flows, "192.168.50.10")
	if !f.NATIP.Equal(net.ParseIP("192.168.1.23")) {
		t.Fatalf("NAT address not recovered from the reply: got %v", f.NATIP)
	}
	if f.DstPort != 443 || f.Proto != "tcp" || !f.Assured {
		t.Fatalf("flow decoded wrong: %+v", f)
	}

	// The two directions must stay apart: one total would hide an upload.
	if f.Up.Packets != 42 || f.Up.Bytes != 5120 || f.Down.Packets != 38 || f.Down.Bytes != 40960 {
		t.Fatalf("directions mixed up: up %+v down %+v", f.Up, f.Down)
	}
	if f.TotalBytes() != 46080 {
		t.Fatalf("total wrong: %d", f.TotalBytes())
	}

	// the router's own traffic is not NATed
	if own := find(t, flows, "192.168.1.23"); own.NATIP != nil {
		t.Fatalf("the router's own flow must not look NATed: %+v", own)
	}
	// IPv6 needs no NAT and the device is visible in the source
	if v6 := find(t, flows, "2a01:4b00:ab09:c000::10"); v6.NATIP != nil || v6.Up.Bytes != 600 {
		t.Fatalf("IPv6 flow decoded wrong: %+v", v6)
	}
}

// [UNREPLIED] arrives before the reply tuple, which is where a naive parser
// loses track of which half it is reading.
func TestUnrepliedMarkerBeforeTheReplyTuple(t *testing.T) {
	flows, _, err := ParseConntrack(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	f := find(t, flows, "192.168.50.11")
	if !f.Unreplied {
		t.Fatal("the flow is marked UNREPLIED on the wire")
	}
	if f.Up.Packets != 3 || f.Down.Packets != 0 {
		t.Fatalf("an unreplied flow has traffic one way only: up %+v down %+v", f.Up, f.Down)
	}
	if !f.NATIP.Equal(net.ParseIP("192.168.1.23")) {
		t.Fatalf("the reply tuple still names the NAT address: %v", f.NATIP)
	}
	if f.DstPort != 22 {
		t.Fatalf("destination port lost: %d", f.DstPort)
	}
}

// An offloaded flow is counted in bursts by the flow table or the hardware, so
// its figures are approximate and the product has to know which ones they are.
func TestOffloadedFlowIsMarked(t *testing.T) {
	flows, _, err := ParseConntrack(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	f := find(t, flows, "192.168.50.12")
	if !f.Offloaded {
		t.Fatal("[OFFLOAD] on the line must reach the product")
	}
	if f.Down.Bytes != 1400000 {
		t.Fatalf("offloaded counters still have to be read: %+v", f.Down)
	}
	if find(t, flows, "192.168.50.10").Offloaded {
		t.Fatal("a flow without the marker must not be called offloaded")
	}
}

// An ICMP line carries type/code instead of ports.
func TestICMPLineDoesNotBecomeAPort(t *testing.T) {
	flows, _, err := ParseConntrack(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	f := find(t, flows, "192.168.50.13")
	if f.Proto != "icmp" || f.SrcPort != 0 || f.DstPort != 0 {
		t.Fatalf("icmp decoded as if it had ports: %+v", f)
	}
	if f.Up.Bytes != 400 || f.Down.Bytes != 400 {
		t.Fatalf("icmp counters lost: %+v %+v", f.Up, f.Down)
	}
}

// With net.netfilter.nf_conntrack_acct=0 the kernel prints no counters at all.
// Reporting that as "0 bytes" turns a broken measurement into a quiet network,
// which is the worst of both.
func TestAccountingOffIsReportedNotAveraged(t *testing.T) {
	const noAcct = `ipv4     2 tcp      6 431997 ESTABLISHED src=192.168.50.10 dst=140.82.121.4 sport=51000 dport=443 src=140.82.121.4 dst=192.168.1.23 sport=443 dport=51000 [ASSURED] mark=0 use=1`
	flows, st, err := ParseConntrack(strings.NewReader(noAcct))
	if err != nil {
		t.Fatal(err)
	}
	if !st.AcctOff {
		t.Fatal("a table with no byte counters at all means accounting is off")
	}
	if len(flows) != 1 {
		t.Fatalf("the flow itself is still usable: %d", len(flows))
	}
	f := flows[0]
	if !f.NATIP.Equal(net.ParseIP("192.168.1.23")) {
		t.Fatalf("without counters the tuples must still be told apart: %+v", f)
	}
	if f.DstPort != 443 {
		t.Fatalf("ports lost when there are no counters: %+v", f)
	}
}

func TestParseLeasesAndNames(t *testing.T) {
	leases := ParseLeases(strings.NewReader(
		"1789500000 aa:bb:cc:dd:ee:ff 192.168.50.10 iPhone-guest 01:aa:bb:cc:dd:ee:ff\n" +
			"1789500000 11:22:33:44:55:66 192.168.50.11 * *\n" +
			"broken line\n"))
	if len(leases) != 2 {
		t.Fatalf("expected two leases, got %d", len(leases))
	}
	tab := NewTable(leases)
	if got := tab.Name(net.ParseIP("192.168.50.10")); got != "iPhone-guest" {
		t.Fatalf("name lost: %q", got)
	}
	if got := tab.Name(net.ParseIP("192.168.50.11")); got != "11:22:33:44:55:66" {
		t.Fatalf("a nameless lease should fall back to its MAC: %q", got)
	}
	if got := tab.Name(net.ParseIP("192.168.50.99")); got != "192.168.50.99" {
		t.Fatalf("an unknown address should read as itself: %q", got)
	}
}
