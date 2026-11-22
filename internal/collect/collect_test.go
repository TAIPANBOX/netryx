package collect

import (
	"encoding/binary"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"netryx/internal/ct"
	"netryx/internal/export"
	"netryx/internal/nflog"
)

// tlsHello builds a packet the way it arrives from the log group: an IPv4
// header, a TCP header with options, and a ClientHello carrying one name.
func tlsHello(t *testing.T, src, dst, name string) nflog.Packet {
	return buildHello(t, src, dst, name, false)
}

// echHello is the shape that decides whether this tool still works in a few
// years: the name is there, encrypted, and the one in the clear is a decoy.
func echHello(t *testing.T, src, dst, decoy string) nflog.Packet {
	return buildHello(t, src, dst, decoy, true)
}

func buildHello(t *testing.T, src, dst, name string, ech bool) nflog.Packet {
	t.Helper()
	ext := []byte{0, 0} // server_name extension
	sn := append([]byte{0}, byte(len(name)>>8), byte(len(name)))
	sn = append(sn, []byte(name)...)
	list := append([]byte{byte(len(sn) >> 8), byte(len(sn))}, sn...)
	ext = append(ext, byte(len(list)>>8), byte(len(list)))
	ext = append(ext, list...)

	if ech {
		// encrypted_client_hello. Its body is not parsed by anything here;
		// its presence is the fact, and the name in the clear beside it is a
		// decoy the whole internet shares.
		body := []byte{0, 0, 1, 2, 3, 4}
		ext = append(ext, 0xfe, 0x0d, byte(len(body)>>8), byte(len(body)))
		ext = append(ext, body...)
	}

	hello := []byte{3, 3}                      // client version
	hello = append(hello, make([]byte, 32)...) // random
	hello = append(hello, 0)                   // session id
	hello = append(hello, 0, 2, 0x13, 0x01)    // cipher suites
	hello = append(hello, 1, 0)                // compression
	hello = append(hello, byte(len(ext)>>8), byte(len(ext)))
	hello = append(hello, ext...)

	hs := append([]byte{1, byte(len(hello) >> 16), byte(len(hello) >> 8), byte(len(hello))}, hello...)
	rec := append([]byte{0x16, 3, 1, byte(len(hs) >> 8), byte(len(hs))}, hs...)

	const tcpHdr = 32
	pkt := make([]byte, 20+tcpHdr+len(rec))
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	pkt[9] = 6
	copy(pkt[12:16], net.ParseIP(src).To4())
	copy(pkt[16:20], net.ParseIP(dst).To4())
	binary.BigEndian.PutUint16(pkt[20:22], 51000)
	binary.BigEndian.PutUint16(pkt[22:24], 443)
	pkt[32] = 0x80
	copy(pkt[20+tcpHdr:], rec)
	return nflog.Packet{Payload: pkt}
}

func TestADeviceIsTheMACAndTheNamesItAsksFor(t *testing.T) {
	c := New()
	now := time.Now()
	p := tlsHello(t, "192.168.1.115", "1.1.1.1", "api.telegram.org")
	p.HWAddr = "4e:c2:94:9f:07:79"
	c.Observe(p, now)
	c.Observe(p, now.Add(time.Second))
	q := tlsHello(t, "192.168.1.115", "9.9.9.9", "discord.com")
	q.HWAddr = "4e:c2:94:9f:07:79"
	c.Observe(q, now.Add(2*time.Second))

	devs := c.Devices()
	if len(devs) != 1 {
		t.Fatalf("one MAC is one device whatever it asked for: %d", len(devs))
	}
	d := devs[0]
	if d.Handshakes != 3 {
		t.Fatalf("every start of a connection counts: %d", d.Handshakes)
	}
	top := d.TopSites(10)
	if len(top) != 2 || top[0].Name != "api.telegram.org" || top[0].Count != 2 {
		t.Fatalf("names must be counted and ordered: %+v", top)
	}
	if c.MACOf("192.168.1.115") != "4e:c2:94:9f:07:79" {
		t.Fatal("the address must lead back to the hardware, or nothing can be joined")
	}

	// A second address on the same hardware is the same device. This is the
	// whole reason for keying by MAC: an IPv6 privacy address rotates twice a
	// day and would otherwise become a new device each time.
	r := tlsHello(t, "192.168.1.200", "1.1.1.1", "api.telegram.org")
	r.HWAddr = "4e:c2:94:9f:07:79"
	c.Observe(r, now.Add(3*time.Second))
	if devs := c.Devices(); len(devs) != 1 || len(devs[0].Addrs) != 2 {
		t.Fatalf("a device that changed address is still one device: %+v", devs)
	}
}

func TestAnEncryptedNameIsItsOwnNumber(t *testing.T) {
	c := New()
	now := time.Now()
	p := echHello(t, "192.168.1.115", "1.1.1.1", "cloudflare-ech.com")
	p.HWAddr = "4e:c2:94:9f:07:79"
	c.Observe(p, now)
	d := c.Devices()[0]
	if d.Hidden != 1 {
		t.Fatalf("a name hidden by the client is a fact worth counting: %+v", d)
	}
	if len(d.Sites) != 0 {
		// The outer name under ECH is a decoy every such site shares. Filing
		// it as a destination would fill the table with one fake name and
		// hide how much of the house cannot be named at all.
		t.Fatalf("the decoy must never be recorded as a destination: %+v", d.Sites)
	}
	if d.Unnamed != 0 {
		t.Fatalf("encrypted is not the same as unreadable: %+v", d)
	}
}

func TestWhatCannotBeNamedIsCountedRatherThanDropped(t *testing.T) {
	c := New()
	c.PendingFor = 10 * time.Millisecond
	now := time.Now()
	// A handshake whose name is not in this packet. It is held first, because
	// with post-quantum key shares the name is usually in the next segment,
	// and counting it as unnamed straight away would count the same handshake
	// twice when that segment arrives.
	p := nflog.Packet{HWAddr: "aa:bb:cc:dd:ee:ff", Payload: tlsHello(t, "192.168.1.9", "1.1.1.1", "x.example").Payload[:60]}
	c.Observe(p, now)
	d := c.Devices()[0]
	if d.Handshakes != 1 || d.Unnamed != 0 {
		t.Fatalf("a handshake is counted once, and not yet given up on: %+v", d)
	}

	// When the rest never comes, it is counted: a handshake seen and never
	// read is exactly what "could not be named" means, and losing that number
	// would make the page look better than the network is.
	other := nflog.Packet{HWAddr: "11:22:33:44:55:66", Payload: tlsHello(t, "192.168.1.10", "1.1.1.1", "y.example").Payload[:60]}
	c.Observe(other, now.Add(time.Second))
	for _, dev := range c.Devices() {
		if dev.MAC == "aa:bb:cc:dd:ee:ff" && dev.Unnamed != 1 {
			t.Fatalf("an unreadable start must be counted once it is given up on: %+v", dev)
		}
	}
	if len(c.pending) > 1 {
		t.Fatalf("and it must not be held for ever: %d", len(c.pending))
	}
}

func TestTheTableCannotGrowWithoutLimit(t *testing.T) {
	c := New()
	c.MaxDevices, c.MaxSites = 2, 2
	now := time.Now()
	for i := 0; i < 5; i++ {
		p := tlsHello(t, "192.168.1.115", "1.1.1.1", "a.example")
		p.HWAddr = net.HardwareAddr{0, 0, 0, 0, 0, byte(i)}.String()
		c.Observe(p, now)
	}
	if len(c.Devices()) != 2 {
		t.Fatalf("the cap must hold: %d", len(c.Devices()))
	}
	if c.Dropped != 3 {
		t.Fatalf("and say how much it refused: %d", c.Dropped)
	}
	for _, n := range []string{"b.example", "c.example", "d.example"} {
		p := tlsHello(t, "192.168.1.115", "1.1.1.1", n)
		p.HWAddr = "00:00:00:00:00:00"
		c.Observe(p, now)
	}
	if got := len(c.Devices()[0].Sites); got > 2 {
		t.Fatalf("one device may not fill the router's memory on its own: %d", got)
	}
}

// The connection table counts bytes per address and the capture names
// services per MAC. Joining them wrongly is how one phone becomes six rows.
func TestOneDeviceNotOneRowPerAddress(t *testing.T) {
	counted := []export.Device{
		{Name: "192.168.1.115", Addr: "192.168.1.115", Flows: 2, UpBytes: 100, DownBytes: 900},
		{Name: "2a01::1", Addr: "2a01::1", Flows: 1, UpBytes: 10, DownBytes: 90},
		{Name: "192.168.1.50", Addr: "192.168.1.50", Flows: 1, UpBytes: 5, DownBytes: 5},
	}
	live := []Device{{
		MAC: "4e:c2:94:9f:07:79", Addrs: []string{"192.168.1.115", "2a01::1"},
		Handshakes: 3, Hidden: 1,
		Sites: map[string]*Site{"api.telegram.org": {Name: "api.telegram.org", Count: 3}},
	}}
	macOf := func(a string) string {
		if a == "192.168.1.115" || a == "2a01::1" {
			return "4e:c2:94:9f:07:79"
		}
		return ""
	}
	got := Merge(counted, live, macOf)
	if len(got) != 2 {
		t.Fatalf("two addresses of one phone are one row: %+v", got)
	}
	var phone *export.Device
	for i := range got {
		if got[i].MAC == "4e:c2:94:9f:07:79" {
			phone = &got[i]
		}
	}
	if phone == nil {
		t.Fatalf("the joined row must carry the hardware address: %+v", got)
	}
	if phone.UpBytes != 110 || phone.DownBytes != 990 || phone.Flows != 3 {
		t.Fatalf("volumes must add up across the addresses: %+v", phone)
	}
	if len(phone.Sites) != 1 || phone.Sites[0] != "api.telegram.org" || phone.Handshakes != 3 || phone.Hidden != 1 {
		t.Fatalf("what the capture learned must reach the row: %+v", phone)
	}

	// A device the capture saw but the connection table has no open flow for
	// belongs in the table with no volume, not missing.
	got = Merge(nil, live, func(string) string { return "" })
	if len(got) != 1 || got[0].MAC != "4e:c2:94:9f:07:79" || got[0].UpBytes != 0 {
		t.Fatalf("a device with no open flows is still a device: %+v", got)
	}
}

// udp4 builds an IPv4/UDP packet the way it arrives from the log group.
func udp4(t *testing.T, src, dst string, sport, dport uint16, payload []byte) nflog.Packet {
	t.Helper()
	pkt := make([]byte, 20+8+len(payload))
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	pkt[9] = 17
	copy(pkt[12:16], net.ParseIP(src).To4())
	copy(pkt[16:20], net.ParseIP(dst).To4())
	binary.BigEndian.PutUint16(pkt[20:22], sport)
	binary.BigEndian.PutUint16(pkt[22:24], dport)
	binary.BigEndian.PutUint16(pkt[24:26], uint16(8+len(payload)))
	copy(pkt[28:], payload)
	return nflog.Packet{Payload: pkt}
}

// dnsQuery and dnsAnswer are the two halves of what the router's own resolver
// sees, in the clear, on the LAN side.
func dnsQuery(name string) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[0:2], 0x4242)
	binary.BigEndian.PutUint16(b[2:4], 0x0100)
	binary.BigEndian.PutUint16(b[4:6], 1)
	for _, l := range strings.Split(name, ".") {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0, 0, 1, 0, 1)
}

func dnsAnswer(name, ip string) []byte {
	b := dnsQuery(name)
	binary.BigEndian.PutUint16(b[2:4], 0x8180)
	binary.BigEndian.PutUint16(b[6:8], 1)
	b = append(b, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 1, 44, 0, 4)
	return append(b, net.ParseIP(ip).To4()...)
}

// This is the signal the plan threw away and the review put back: on the LAN
// side the devices ask the router's resolver in the clear, so a name exists
// even when the handshake hides it.
func TestTheResolverNamesWhatTheHandshakeHides(t *testing.T) {
	c := New()
	now := time.Now()

	q := udp4(t, "192.168.1.115", "192.168.1.1", 40000, 53, dnsQuery("chat.openai.com"))
	q.HWAddr = "4e:c2:94:9f:07:79"
	c.Observe(q, now)

	d := c.Devices()[0]
	sites := d.TopSites(5)
	if len(sites) != 1 || sites[0].Name != "chat.openai.com" || sites[0].Via != "dns" {
		t.Fatalf("a question is a destination, and it must say how it was learned: %+v", sites)
	}
	if d.Handshakes != 0 {
		t.Fatalf("a question is not a handshake: %+v", d)
	}

	// The answer comes back from the router itself, so it carries no device,
	// but it names the address for everyone.
	a := udp4(t, "192.168.1.1", "192.168.1.115", 53, 40000, dnsAnswer("chat.openai.com", "104.18.32.47"))
	c.Observe(a, now)

	// Now a handshake to that address with the name encrypted. Before this,
	// it was one more "hidden" with nothing to show; now it has a name, and
	// the row says the name came from the resolver rather than the wire.
	h := echHello(t, "192.168.1.115", "104.18.32.47", "cloudflare-ech.com")
	h.HWAddr = "4e:c2:94:9f:07:79"
	c.Observe(h, now)

	d = c.Devices()[0]
	var got *Site
	for _, s := range d.TopSites(9) {
		if s.Name == "chat.openai.com" {
			cp := s
			got = &cp
		}
	}
	if got == nil || got.Count != 2 {
		t.Fatalf("the hidden handshake must land on the name the resolver gave: %+v", d.TopSites(9))
	}
	if d.Hidden != 1 {
		t.Fatalf("and it is still counted as hidden, because the client did hide it: %+v", d)
	}
	if len(d.Sites) != 1 {
		t.Fatalf("the decoy name must not appear beside it: %+v", d.Sites)
	}
}

func TestANameSeenBothWaysSaysSo(t *testing.T) {
	c := New()
	now := time.Now()
	q := udp4(t, "192.168.1.115", "192.168.1.1", 40000, 53, dnsQuery("api.telegram.org"))
	q.HWAddr = "aa:bb:cc:00:11:22"
	c.Observe(q, now)
	h := tlsHello(t, "192.168.1.115", "149.154.167.99", "api.telegram.org")
	h.HWAddr = "aa:bb:cc:00:11:22"
	c.Observe(h, now)
	s := c.Devices()[0].TopSites(3)
	if len(s) != 1 || s[0].Via != "tls+dns" {
		t.Fatalf("a name seen in the question and in the handshake is the strongest there is: %+v", s)
	}
}

func TestTheNameTableIsCappedToo(t *testing.T) {
	c := New()
	c.MaxNames = 2
	now := time.Now()
	for _, ip := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"} {
		c.Observe(udp4(t, "192.168.1.1", "192.168.1.115", 53, 40000, dnsAnswer("x.example", ip)), now)
	}
	if len(c.names) != 2 {
		t.Fatalf("the resolver can name more addresses than a router has memory: %d", len(c.names))
	}
	if c.Dropped == 0 {
		t.Fatal("and the table must say it refused some")
	}
}

// Not everything has a hardware address. The router's own traffic, loopback,
// a tunnel: all of them are real rows with real names, and dropping them
// because they have no MAC means the router can never report on itself.
func TestADeviceWithNoHardwareAddressIsStillADevice(t *testing.T) {
	counted := []export.Device{
		{Name: "192.168.1.24", Addr: "192.168.1.24", Flows: 4, UpBytes: 500},
		{Name: "127.0.0.1", Addr: "127.0.0.1", Flows: 1},
	}
	live := []Device{
		{Addrs: []string{"127.0.0.1"}, Sites: map[string]*Site{"openwrt.org": {Name: "openwrt.org", Count: 1}}},
		{Addrs: []string{"10.9.9.9"}, Handshakes: 2, Sites: map[string]*Site{"tunnel.example": {Name: "tunnel.example", Count: 2}}},
	}
	got := Merge(counted, live, func(string) string { return "" })

	var loop, tun *export.Device
	for i := range got {
		switch got[i].Addr {
		case "127.0.0.1":
			loop = &got[i]
		case "10.9.9.9":
			tun = &got[i]
		}
	}
	if loop == nil || len(loop.Sites) != 1 || loop.Sites[0] != "openwrt.org" {
		t.Fatalf("a row with no MAC must still receive what was learned about it: %+v", got)
	}
	if tun == nil || tun.Handshakes != 2 {
		t.Fatalf("and one the connection table never saw must appear on its own: %+v", got)
	}
	if len(got) != 3 {
		t.Fatalf("no row may be duplicated by the join: %+v", got)
	}
}

// The router's own chain copies the first packets of every connection it
// makes, not only handshakes. Counting each of them as a handshake gives the
// router a column of dozens "without a name", which is not a fact about the
// network but about which rule copied the packet.
func TestOnlyTheStartOfSomethingCountsAsAHandshake(t *testing.T) {
	c := New()
	now := time.Now()
	plain := udp4(t, "192.168.1.24", "1.1.1.1", 40000, 123, []byte("\x23\x00\x06\xec\x00\x00\x00\x00")) // NTP
	c.Observe(plain, now)
	if len(c.Devices()) != 0 {
		t.Fatalf("a packet that is not the start of a handshake is not one: %+v", c.Devices())
	}

	// A TLS record that opens a handshake but whose name cannot be read is a
	// different thing entirely, and that one does count.
	short := tlsHello(t, "192.168.1.24", "1.1.1.1", "x.example")
	short.Payload = short.Payload[:56]
	c.Observe(short, now)
	d := c.Devices()
	if len(d) != 1 || d[0].Handshakes != 1 {
		t.Fatalf("an unreadable handshake is still a handshake: %+v", d)
	}
	// Whether it can be named is decided later: it is held first, in case the
	// name is in the next segment. TestWhatCannotBeNamedIsCountedRatherThanDropped
	// covers what happens when that segment never comes.
	if d[0].Unnamed != 0 {
		t.Fatalf("it must not be given up on in the same breath: %+v", d[0])
	}
}

// The connection table can only say what is open right now, so a device that
// finished a large download vanishes from it. The kernel does say, once, what
// a finished connection moved, and that number is the only one that answers
// "who used the line today".
func TestAFinishedConnectionIsNotAForgottenOne(t *testing.T) {
	c := New()
	now := time.Now()

	// The device is known by its handshake first, so the bytes land on the
	// same row as the names rather than on a second row for the same phone.
	h := tlsHello(t, "192.168.1.115", "1.1.1.1", "api.telegram.org")
	h.HWAddr = "4e:c2:94:9f:07:79"
	c.Observe(h, now)

	c.Finished(ct.Event{
		Kind: ct.Destroy,
		Orig: ct.Tuple{SrcIP: net.ParseIP("192.168.1.115")},
		Up:   ct.Counters{Bytes: 1000, Packets: 10},
		Down: ct.Counters{Bytes: 90000, Packets: 80},
	}, now)
	c.Finished(ct.Event{
		Kind: ct.Destroy,
		Orig: ct.Tuple{SrcIP: net.ParseIP("192.168.1.115")},
		Up:   ct.Counters{Bytes: 500},
		Down: ct.Counters{Bytes: 10000},
	}, now)

	d := c.Devices()
	if len(d) != 1 {
		t.Fatalf("the bytes belong to the device that made the connection: %+v", d)
	}
	if d[0].TotalUp != 1500 || d[0].TotalDown != 100000 || d[0].Closed != 2 {
		t.Fatalf("finished connections must add up: %+v", d[0])
	}

	// Anything that is not the end of a connection carries no final counters
	// and must not be counted twice.
	c.Finished(ct.Event{Kind: ct.New, Orig: ct.Tuple{SrcIP: net.ParseIP("192.168.1.115")}, Up: ct.Counters{Bytes: 9999}}, now)
	if c.Devices()[0].TotalUp != 1500 {
		t.Fatal("only the end of a connection carries its total")
	}

	// A device that never made a readable handshake still moved bytes, and
	// that is the answer to who is using the line.
	c.Finished(ct.Event{
		Kind: ct.Destroy,
		Orig: ct.Tuple{SrcIP: net.ParseIP("192.168.1.77")},
		Up:   ct.Counters{Bytes: 42},
	}, now)
	if len(c.Devices()) != 2 {
		t.Fatalf("a silent device is still a device: %+v", c.Devices())
	}
}

// With post-quantum key shares a ClientHello runs to about 1.8 KB and does not
// fit in one segment. The second segment carries no signature of its own, so
// on its own it is unreadable; the rule already pays to copy it, and throwing
// it away means losing the name on a large share of modern browsers.
func TestAHandshakeSplitInTwoIsStillAName(t *testing.T) {
	c := New()
	now := time.Now()
	full := tlsHello(t, "192.168.1.115", "1.1.1.1", "chat.openai.com")
	payload := nflog.TransportPayload(full.Payload)
	cut := len(payload) - 12 // the name itself lands in the second half

	one := udp4(t, "192.168.1.115", "1.1.1.1", 50000, 443, nil) // shell only
	one.Payload = rebuildTCP(t, "192.168.1.115", "1.1.1.1", 50000, 443, payload[:cut])
	one.HWAddr = "4e:c2:94:9f:07:79"
	c.Observe(one, now)

	// The device is there, because a handshake was seen. The name is not,
	// because it is in the half that has not arrived.
	half := c.Devices()
	if len(half) != 1 || half[0].Handshakes != 1 {
		t.Fatalf("the handshake itself counts once: %+v", half)
	}
	if len(half[0].Sites) != 0 {
		t.Fatalf("half a handshake names nothing yet: %+v", half[0].Sites)
	}

	two := nflog.Packet{HWAddr: one.HWAddr, Payload: rebuildTCP(t, "192.168.1.115", "1.1.1.1", 50000, 443, payload[cut:])}
	c.Observe(two, now.Add(10*time.Millisecond))

	d := c.Devices()
	if len(d) != 1 {
		t.Fatalf("the joined halves must name the device once: %+v", d)
	}
	if s := d[0].TopSites(3); len(s) != 1 || s[0].Name != "chat.openai.com" {
		t.Fatalf("the name is in the second half and must be read: %+v", s)
	}
	// One handshake, whichever packet of it finally carried the name.
	// Counting the continuation as a second one would inflate every figure
	// derived from it, including the share that could not be named.
	if d[0].Handshakes != 1 {
		t.Fatalf("a handshake split in two is one handshake: %+v", d[0])
	}
	if len(c.pending) != 0 {
		t.Fatalf("a finished handshake must not be held: %d", len(c.pending))
	}
}

// A half that never gets its other half must not sit in the router's memory.
func TestHalvesNobodyFinishesAreLetGo(t *testing.T) {
	c := New()
	c.MaxPending, c.PendingFor = 2, 50*time.Millisecond
	now := time.Now()
	full := tlsHello(t, "192.168.1.115", "1.1.1.1", "x.example")
	payload := nflog.TransportPayload(full.Payload)
	for i := 0; i < 5; i++ {
		p := nflog.Packet{Payload: rebuildTCP(t, "192.168.1.115", "1.1.1.1", uint16(50000+i), 443, payload[:20])}
		c.Observe(p, now)
	}
	if len(c.pending) > 2 {
		t.Fatalf("the cap must hold: %d", len(c.pending))
	}
	// And after the window, the ones nobody finished go.
	c.Observe(nflog.Packet{Payload: rebuildTCP(t, "192.168.1.115", "1.1.1.1", 60000, 443, payload[:20])}, now.Add(time.Second))
	if len(c.pending) > 2 {
		t.Fatalf("stale halves must be let go: %d", len(c.pending))
	}
}

// rebuildTCP wraps a payload in the IPv4/TCP headers the log group delivers.
func rebuildTCP(t *testing.T, src, dst string, sport, dport uint16, payload []byte) []byte {
	t.Helper()
	const tcpHdr = 32
	pkt := make([]byte, 20+tcpHdr+len(payload))
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	pkt[9] = 6
	copy(pkt[12:16], net.ParseIP(src).To4())
	copy(pkt[16:20], net.ParseIP(dst).To4())
	binary.BigEndian.PutUint16(pkt[20:22], sport)
	binary.BigEndian.PutUint16(pkt[22:24], dport)
	pkt[32] = 0x80
	copy(pkt[20+tcpHdr:], payload)
	return pkt
}

// A flow that keeps sending and never finishes its handshake must not be able
// to spend the router's memory: what is held for one flow is bounded whatever
// it sends, and the bound is not "whatever the last packet was".
func TestOneFlowCannotSpendTheRoutersMemory(t *testing.T) {
	c := New()
	now := time.Now()
	head := nflog.TransportPayload(tlsHello(t, "192.168.1.115", "1.1.1.1", "x.example").Payload)[:20]
	chunk := make([]byte, 3000) // deliberately not a divisor of the bound
	copy(chunk, head)
	for i := 0; i < 10; i++ {
		c.Observe(nflog.Packet{Payload: rebuildTCP(t, "192.168.1.115", "1.1.1.1", 51000, 443, chunk)}, now)
	}
	for k, p := range c.pending {
		if len(p.buf) > maxPartial {
			t.Fatalf("one flow held %d bytes for %s, the bound is %d", len(p.buf), k, maxPartial)
		}
	}
}

// A flow is named by what its own handshake said, because the resolver's
// answer is filed per address and a CDN address serves a thousand names. The
// resolver is the fallback, and a hidden name says it was hidden.
func TestNameOfPrefersTheHandshakeThenTheResolver(t *testing.T) {
	c := New()
	now := time.Now()
	const phone, laptop, tv, cdn = "192.168.1.115", "192.168.1.116", "192.168.1.117", "104.18.32.47"

	if name, via := c.NameOf(phone, cdn); name != "" || via != "" {
		t.Fatalf("nothing is known yet, so nothing is answered: %q %q", name, via)
	}

	c.Observe(udp4(t, "192.168.1.1", phone, 53, 40000, dnsAnswer("chat.openai.com", cdn)), now)
	if name, via := c.NameOf(phone, cdn); name != "chat.openai.com" || via != "dns" {
		t.Fatalf("with no handshake the resolver's answer names the flow: %q %q", name, via)
	}

	h := tlsHello(t, phone, cdn, "api.openai.com")
	h.HWAddr = "4e:c2:94:9f:07:79"
	c.Observe(h, now)
	if name, via := c.NameOf(phone, cdn); name != "api.openai.com" || via != "sni" {
		t.Fatalf("the handshake's own name beats the resolver's: %q %q", name, via)
	}

	// The same address seen from another device was never named by that
	// device's handshake, so it still gets the resolver's answer.
	e := echHello(t, laptop, cdn, "cloudflare-ech.com")
	e.HWAddr = "aa:bb:cc:00:11:22"
	c.Observe(e, now)
	if name, via := c.NameOf(laptop, cdn); name != "chat.openai.com" || via != "ech" {
		t.Fatalf("a hidden name is named by the resolver and says it was hidden: %q %q", name, via)
	}
	e2 := echHello(t, laptop, "9.9.9.9", "cloudflare-ech.com")
	e2.HWAddr = "aa:bb:cc:00:11:22"
	c.Observe(e2, now)
	if name, via := c.NameOf(laptop, "9.9.9.9"); name != "" || via != "ech" {
		t.Fatalf("hidden and unanswered is still hidden, and never the decoy: %q %q", name, via)
	}

	// A handshake carried over UDP to 443 is QUIC's, and is labelled so.
	rec := nflog.TransportPayload(tlsHello(t, tv, "142.250.180.14", "www.youtube.com").Payload)
	q := udp4(t, tv, "142.250.180.14", 50000, 443, rec)
	q.HWAddr = "11:22:33:44:55:66"
	c.Observe(q, now)
	if name, via := c.NameOf(tv, "142.250.180.14"); name != "www.youtube.com" || via != "quic" {
		t.Fatalf("a name from a QUIC Initial says so: %q %q", name, via)
	}
}

func TestTheFlowNameMemoryForgetsTheOldestFirst(t *testing.T) {
	c := New()
	c.MaxNames = 2
	now := time.Now()
	for i, dst := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} {
		h := tlsHello(t, "192.168.1.115", dst, []string{"a.example", "b.example", "c.example"}[i])
		h.HWAddr = "4e:c2:94:9f:07:79"
		c.Observe(h, now)
	}
	if len(c.pairs) > 2 {
		t.Fatalf("the memory of flow names is capped like every other table: %d", len(c.pairs))
	}
	if name, _ := c.NameOf("192.168.1.115", "1.1.1.1"); name != "" {
		t.Fatalf("the oldest name is the one let go: %q", name)
	}
	if name, _ := c.NameOf("192.168.1.115", "3.3.3.3"); name != "c.example" {
		t.Fatalf("and the newest is kept: %q", name)
	}
	if c.Dropped == 0 {
		t.Fatal("and the table must say it let some go")
	}
}

// The resolver's answers name the addresses a house is using now. A memory
// that stops at its cap would keep the first answers it ever heard and never
// learn a CDN address that moved; the newest answer wins, and the oldest is
// let go to make room.
func TestTheResolverMemoryKeepsTheNewestAnswers(t *testing.T) {
	c := New()
	c.MaxNames = 2
	now := time.Now()
	answer := func(name, ip string) {
		c.Observe(udp4(t, "192.168.1.1", "192.168.1.115", 53, 40000, dnsAnswer(name, ip)), now)
	}
	answer("a.example", "1.1.1.1")
	answer("b.example", "2.2.2.2")
	answer("c.example", "3.3.3.3")
	if n, via := c.NameOf("192.168.1.115", "3.3.3.3"); n != "c.example" || via != "dns" {
		t.Fatalf("the newest answer must be kept: %q %q", n, via)
	}
	if n, _ := c.NameOf("192.168.1.115", "1.1.1.1"); n != "" {
		t.Fatalf("the oldest answer makes room: %q", n)
	}
	answer("b2.example", "2.2.2.2")
	if n, _ := c.NameOf("192.168.1.115", "2.2.2.2"); n != "b2.example" {
		t.Fatalf("a repeat answer for an address replaces the old name: %q", n)
	}
	// 2.2.2.2 was just used, so the next newcomer pushes out 3.3.3.3.
	answer("d.example", "4.4.4.4")
	if n, _ := c.NameOf("192.168.1.115", "2.2.2.2"); n != "b2.example" {
		t.Fatalf("an address answered again is the newest, not the oldest: %q", n)
	}
	if n, _ := c.NameOf("192.168.1.115", "3.3.3.3"); n != "" {
		t.Fatalf("the least recently answered address goes first: %q", n)
	}
	if len(c.names) > 2 {
		t.Fatalf("the memory is still bounded: %d", len(c.names))
	}
}

// A finished connection is counted under the device that made it, found by
// the one resolver the console shares. The router's own connections and
// connections opened from outside are not a device of the house, and an
// address that resolves to a hardware address is never a row of its own.
func TestFinishedCountsOnlyTheHousesOwnDevices(t *testing.T) {
	c := New()
	c.Resolve = func(addr string) string {
		switch addr {
		case "192.168.1.50":
			return "aa:bb:cc:00:00:50" // known only from the neighbour table
		case "192.168.1.1", "203.0.113.7":
			return "router"
		}
		return ""
	}
	c.IsLocal = func(a netip.Addr) bool {
		return netip.MustParsePrefix("192.168.1.0/24").Contains(a) || a == netip.MustParseAddr("203.0.113.7")
	}
	now := time.Now()
	end := func(src string, down uint64) {
		c.Finished(ct.Event{Kind: ct.Destroy, Orig: ct.Tuple{SrcIP: net.ParseIP(src)}, Down: ct.Counters{Bytes: down}}, now)
	}
	end("192.168.1.50", 1000)
	end("192.168.1.1", 2000) // the router's resolver going upstream
	end("203.0.113.7", 3000) // the router's own WAN address
	end("81.2.69.160", 4000) // opened from outside
	devs := c.Devices()
	if len(devs) != 1 {
		t.Fatalf("only the house's device is a row: %+v", devs)
	}
	if devs[0].MAC != "aa:bb:cc:00:00:50" || devs[0].TotalDown != 1000 {
		t.Fatalf("the row is the device's hardware address, not its address: %+v", devs[0])
	}
}

// The rules engine learns from what the capture already reads: every answer
// the resolver gives, and every handshake that names its destination, with
// the device and the connection it belongs to. Both are told outside the
// collector's lock, so the engine may ask the collector back.
func TestTheCollectorTellsTheEngineAnswersAndNames(t *testing.T) {
	c := New()
	type answer struct {
		name string
		ip   string
	}
	type named struct {
		mac           string
		local, remote netip.AddrPort
		proto         uint8
		name          string
	}
	var answers []answer
	var names []named
	c.Resolve = func(addr string) string {
		if addr == "192.168.1.1" {
			return RouterKey
		}
		return c.MACOf(addr)
	}
	c.OnAnswer = func(name string, ip net.IP) {
		c.MACOf("192.168.1.115") // takes the lock: deadlocks if called under it
		answers = append(answers, answer{name, ip.String()})
	}
	c.OnName = func(mac string, local, remote netip.AddrPort, proto uint8, name string) {
		c.MACOf("192.168.1.115")
		names = append(names, named{mac, local, remote, proto, name})
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		now := time.Now()
		a := udp4(t, "192.168.1.1", "192.168.1.115", 53, 40000, dnsAnswer("rr1.googlevideo.com", "142.250.1.1"))
		a.Prefix = nflog.AnswerPrefix
		c.Observe(a, now)
		h := tlsHello(t, "192.168.1.115", "149.154.167.220", "api.telegram.org")
		h.HWAddr = "4e:c2:94:9f:07:79"
		c.Observe(h, now)
		e := echHello(t, "192.168.1.115", "104.18.32.47", "cloudflare-ech.com")
		e.HWAddr = "4e:c2:94:9f:07:79"
		c.Observe(e, now)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a hook was called with the collector's lock held")
	}
	if len(answers) != 1 || answers[0] != (answer{"rr1.googlevideo.com", "142.250.1.1"}) {
		t.Fatalf("each answer is told with the name it answered: %+v", answers)
	}
	want := named{"4e:c2:94:9f:07:79", netip.MustParseAddrPort("192.168.1.115:51000"), netip.MustParseAddrPort("149.154.167.220:443"), 6, "api.telegram.org"}
	if len(names) != 1 || names[0] != want {
		t.Fatalf("a named handshake is told with its device and connection, and an encrypted one is not: %+v", names)
	}
}

// Only the router's own resolver is believed (R18): an answer is told only
// when it comes from port 53 of one of the router's addresses. A device on
// the LAN that sends something shaped like an answer, from port 53 or in a
// query, would otherwise choose which addresses a rule blocks for everyone.
func TestOnlyTheRoutersOwnAnswersAreTold(t *testing.T) {
	c := New()
	c.Resolve = func(addr string) string {
		if addr == "192.168.1.1" {
			return RouterKey
		}
		return "4e:c2:94:9f:07:79"
	}
	var told []string
	c.OnAnswer = func(name string, ip net.IP) { told = append(told, name+" "+ip.String()) }
	now := time.Now()
	// A LAN device sends an "answer" from port 53 to the router.
	c.Observe(udp4(t, "192.168.1.115", "192.168.1.1", 53, 40000, dnsAnswer("rr1.googlevideo.com", "8.8.8.8")), now)
	// And one inside a query, towards port 53.
	c.Observe(udp4(t, "192.168.1.115", "192.168.1.1", 40000, 53, dnsAnswer("rr2.googlevideo.com", "9.9.9.9")), now)
	// And one from port 53 of a host outside, forwarded through.
	c.Observe(udp4(t, "1.1.1.1", "192.168.1.115", 53, 40000, dnsAnswer("rr3.googlevideo.com", "1.0.0.1")), now)
	// Even marked by the answer rule, a source that is not the router is not believed.
	lan := udp4(t, "192.168.1.115", "192.168.1.1", 53, 40000, dnsAnswer("rr0.googlevideo.com", "8.8.4.4"))
	lan.Prefix = nflog.AnswerPrefix
	c.Observe(lan, now)
	if len(told) != 0 {
		t.Fatalf("an answer not sent by the router's resolver was believed: %q", told)
	}
	// A device can write the router's address as the source and 53 as the
	// port: what it cannot forge is which rule copied the packet. A copy
	// from the ingress side is never believed, whatever its header says.
	spoof := udp4(t, "192.168.1.1", "192.168.1.115", 53, 40000, dnsAnswer("rr6.googlevideo.com", "203.0.113.6"))
	c.Observe(spoof, now)
	spoof.Prefix = "netryx-other"
	c.Observe(spoof, now)
	if len(told) != 0 {
		t.Fatalf("a copy not made by the router's own answer rule was believed: %q", told)
	}
	own := udp4(t, "192.168.1.1", "192.168.1.115", 53, 40000, dnsAnswer("rr4.googlevideo.com", "142.250.1.1"))
	own.Prefix = nflog.AnswerPrefix
	c.Observe(own, now)
	if len(told) != 1 || told[0] != "rr4.googlevideo.com 142.250.1.1" {
		t.Fatalf("the router's own answer, copied by its answer rule, is told: %q", told)
	}
	// With no way to know the router's addresses, nothing is believed.
	c.Resolve = nil
	late := udp4(t, "192.168.1.1", "192.168.1.115", 53, 40000, dnsAnswer("rr5.googlevideo.com", "142.250.1.2"))
	late.Prefix = nflog.AnswerPrefix
	c.Observe(late, now)
	if len(told) != 1 {
		t.Fatalf("without Resolve no answer is told: %q", told)
	}
}

// Fix round 1, items 1 and 10: whose a named handshake is comes from the
// collector's one answer, Resolve, and a handshake the router itself made
// (copied by its output-side rule, or from one of its addresses) is the
// router's, never a device with no name, which a rule for everyone would cut.
func TestAHandshakeIsTheDeviceResolveNames(t *testing.T) {
	c := New()
	c.Resolve = func(addr string) string {
		switch addr {
		case "192.168.1.1":
			return RouterKey
		case "192.168.1.115":
			return "aa:bb:cc:00:00:01" // what the neighbour table says
		}
		return ""
	}
	var macs []string
	c.OnName = func(mac string, local, remote netip.AddrPort, proto uint8, name string) { macs = append(macs, mac) }
	now := time.Now()
	c.Observe(tlsHello(t, "192.168.1.1", "149.154.167.220", "api.telegram.org"), now)
	out := tlsHello(t, "100.67.70.175", "149.154.167.221", "api.telegram.org")
	out.Hook = 3 // NF_INET_LOCAL_OUT: the router's own output chain
	c.Observe(out, now)
	dev := tlsHello(t, "192.168.1.115", "149.154.167.222", "api.telegram.org")
	c.Observe(dev, now)
	want := []string{RouterKey, RouterKey, "aa:bb:cc:00:00:01"}
	if strings.Join(macs, ",") != strings.Join(want, ",") {
		t.Fatalf("got %q, want %q", macs, want)
	}
}

// The frame's own hardware address names the device of a handshake; the
// shared answer (Resolve) is the fallback for a copy that carries none, and
// an address of the router is the router's whatever the frame says.
func TestAHandshakeIsTheDeviceItsFrameCameFrom(t *testing.T) {
	c := New()
	c.Resolve = func(addr string) string {
		switch addr {
		case "192.168.1.1":
			return RouterKey
		case "192.168.1.115":
			return "aa:bb:cc:00:00:01" // a stale neighbour entry
		}
		return ""
	}
	var macs []string
	c.OnName = func(mac string, local, remote netip.AddrPort, proto uint8, name string) { macs = append(macs, mac) }
	now := time.Now()
	h := tlsHello(t, "192.168.1.115", "149.154.167.222", "api.telegram.org")
	h.HWAddr = "aa:bb:cc:00:00:09"
	c.Observe(h, now)
	bare := tlsHello(t, "192.168.1.115", "149.154.167.223", "api.telegram.org")
	c.Observe(bare, now)
	r := tlsHello(t, "192.168.1.1", "149.154.167.224", "api.telegram.org")
	r.HWAddr = "aa:bb:cc:00:00:0a"
	c.Observe(r, now)
	want := []string{"aa:bb:cc:00:00:09", "aa:bb:cc:00:00:01", RouterKey}
	if strings.Join(macs, ",") != strings.Join(want, ",") {
		t.Fatalf("got %q, want %q", macs, want)
	}
}

// The safe-search resolver answers filtered devices from port 553: the
// redirect is undone after the output hook, so its answers carry 553 there
// (docs/stage2b-measured.md, section 2). Copied by the answer rule, they name
// addresses like the main resolver's do; from a device, port 553 is nothing.
func TestTheSafeResolversAnswersAreTold(t *testing.T) {
	c := New()
	c.Resolve = func(addr string) string {
		if addr == "192.168.1.1" {
			return RouterKey
		}
		return "4e:c2:94:9f:07:79"
	}
	var told []string
	c.OnAnswer = func(name string, ip net.IP) { told = append(told, name+" "+ip.String()) }
	now := time.Now()
	lan := udp4(t, "192.168.1.115", "192.168.1.1", nflog.SafeAnswerPort, 40000, dnsAnswer("rr7.googlevideo.com", "203.0.113.7"))
	c.Observe(lan, now)
	if len(told) != 0 {
		t.Fatalf("a device's packet from port 553 was believed: %q", told)
	}
	safe := udp4(t, "192.168.1.1", "192.168.1.115", nflog.SafeAnswerPort, 40000, dnsAnswer("rr8.googlevideo.com", "142.250.1.8"))
	safe.Prefix = nflog.AnswerPrefix
	c.Observe(safe, now)
	if len(told) != 1 || told[0] != "rr8.googlevideo.com 142.250.1.8" {
		t.Fatalf("the safe resolver's answer, copied by the answer rule, is told: %q", told)
	}
}

// A country rule's copy of a try (nflog.TryPrefix) goes to the alerts with
// the device and the address it tried, and is nothing else: no device row, no
// handshake.
func TestATryCopyIsHandedOverAndNotCounted(t *testing.T) {
	c := New()
	var rule, mac, peer string
	c.OnTry = func(r, m string, ip net.IP) { rule, mac, peer = r, m, ip.String() }
	named := false
	c.OnName = func(string, netip.AddrPort, netip.AddrPort, uint8, string) { named = true }
	p := tlsHello(t, "192.168.50.212", "5.8.0.1", "ru.example")
	p.Prefix = nflog.TryPrefix + "blockru"
	p.HWAddr = "02:4e:58:00:00:02"
	c.Observe(p, time.Now())
	if rule != "blockru" || mac != "02:4e:58:00:00:02" || peer != "5.8.0.1" {
		t.Fatalf("OnTry got %q %q %q", rule, mac, peer)
	}
	if len(c.Devices()) != 0 || named {
		t.Fatalf("a try copy was counted: devices %v, named %v", c.Devices(), named)
	}
}
