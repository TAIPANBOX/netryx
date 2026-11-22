package nflog

import (
	"encoding/binary"
	"net"
	"testing"

	"netryx/internal/ct"
)

func attrBytes(typ uint16, val []byte, nested bool) []byte {
	l := 4 + len(val)
	b := make([]byte, (l+3)&^3)
	binary.LittleEndian.PutUint16(b[0:2], uint16(l))
	t := typ
	if nested {
		t |= 0x8000
	}
	binary.LittleEndian.PutUint16(b[2:4], t)
	copy(b[4:], val)
	return b
}

// tcp4 builds a real IPv4/TCP packet with a payload, the way it appears in a
// log message: from the network header on, with no Ethernet header.
//
// The TCP header carries OPTIONS on purpose. Every real handshake does
// (timestamps, SACK, window scale), so a parser that assumes a twenty-byte
// header reads the payload from the wrong offset on live traffic while passing
// any test built without them.
func tcp4(t *testing.T, src, dst string, sport, dport uint16, payload []byte) []byte {
	t.Helper()
	const tcpHdr = 32 // eight words: twenty bytes plus twelve of options
	pkt := make([]byte, 20+tcpHdr+len(payload))
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	pkt[9] = 6
	copy(pkt[12:16], net.ParseIP(src).To4())
	copy(pkt[16:20], net.ParseIP(dst).To4())
	binary.BigEndian.PutUint16(pkt[20:22], sport)
	binary.BigEndian.PutUint16(pkt[22:24], dport)
	pkt[32] = 0x80 // data offset 8 words
	// options: timestamps, then end of list
	pkt[40], pkt[41] = 8, 10
	copy(pkt[20+tcpHdr:], payload)
	return pkt
}

func msgBody(group uint16, parts ...[]byte) []byte {
	b := []byte{2, 0, 0, 0}
	binary.BigEndian.PutUint16(b[2:4], group)
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func msgType(kind int) uint16 { return uint16(subsysULOG)<<8 | uint16(kind) }

// The attached connection is the whole reason for using the log rather than a
// raw socket: a packet that is already NATed still names the device.
func TestPacketCarriesTheDeviceThroughNAT(t *testing.T) {
	// what the wire shows: the router's WAN address as the source
	pkt := tcp4(t, "100.67.70.175", "140.82.121.4", 51000, 443, []byte{0x16, 0x03, 0x01})
	// what conntrack knows: the device behind it
	ctAttrs := ctTuple(t, "192.168.50.10", "140.82.121.4", 6, 51000, 443)
	b := msgBody(7,
		attrBytes(nfulaPacketHdr, []byte{0x08, 0x00, 0x02, 0x00}, false), // ETH_P_IP, hook forward
		attrBytes(nfulaIfInDev, u32be(9), false),
		attrBytes(nfulaPrefix, append([]byte("netryx"), 0), false),
		attrBytes(nfulaCT, ctAttrs, true),
		attrBytes(nfulaPayload, pkt, false),
	)
	p, ok := ParsePacket(msgType(msgPacket), b)
	if !ok {
		t.Fatal("a well-formed packet message must parse")
	}
	if p.Group != 7 || p.Protocol != 0x0800 || p.Hook != 2 || p.InDev != 9 || p.Prefix != "netryx" {
		t.Fatalf("metadata wrong: %+v", p)
	}
	if !p.HasConn {
		t.Fatal("the connection was attached and must be read")
	}
	if got := p.Device(); !got.Equal(net.ParseIP("192.168.50.10")) {
		t.Fatalf("the device must come from the connection, not the packet: %v", got)
	}
	// and the packet's own source is the NATed one, which is what makes the
	// attached connection necessary rather than convenient
	if src := sourceOf(p.Payload); !src.Equal(net.ParseIP("100.67.70.175")) {
		t.Fatalf("the packet itself still shows the NAT address: %v", src)
	}
	if tp := TransportPayload(p.Payload); len(tp) != 3 || tp[0] != 0x16 {
		t.Fatalf("transport payload wrong: %x", tp)
	}
}

// Without the conntrack flag the message still arrives, and the device then
// falls back to whatever the packet itself says.
func TestWithoutAConnectionTheDeviceFallsBackToThePacket(t *testing.T) {
	pkt := tcp4(t, "192.168.1.92", "1.1.1.1", 40000, 443, nil)
	b := msgBody(7, attrBytes(nfulaPayload, pkt, false))
	p, ok := ParsePacket(msgType(msgPacket), b)
	if !ok {
		t.Fatal("must parse")
	}
	if p.HasConn {
		t.Fatal("no connection was attached")
	}
	if got := p.Device(); !got.Equal(net.ParseIP("192.168.1.92")) {
		t.Fatalf("fallback wrong: %v", got)
	}
}

func TestIPv6TransportPayload(t *testing.T) {
	pkt := make([]byte, 40+32+3)
	pkt[0] = 0x60
	pkt[6] = 6 // TCP
	copy(pkt[8:24], net.ParseIP("2a01:4b00:ab09:c000::10").To16())
	copy(pkt[24:40], net.ParseIP("2606:4700::1111").To16())
	pkt[40+12] = 0x80 // eight words, with options, as real traffic has
	copy(pkt[40+32:], []byte{0x16, 0x03, 0x01})
	b := msgBody(7, attrBytes(nfulaPayload, pkt, false))
	p, _ := ParsePacket(msgType(msgPacket), b)
	if got := p.Device(); !got.Equal(net.ParseIP("2a01:4b00:ab09:c000::10")) {
		t.Fatalf("IPv6 source wrong: %v", got)
	}
	if tp := TransportPayload(pkt); len(tp) != 3 || tp[0] != 0x16 {
		t.Fatalf("IPv6 transport payload wrong: %x", tp)
	}
}

func TestOnlyPacketMessagesAreRead(t *testing.T) {
	pkt := tcp4(t, "10.0.0.1", "10.0.0.2", 1, 2, nil)
	good := msgBody(7, attrBytes(nfulaPayload, pkt, false))
	if _, ok := ParsePacket(msgType(msgConfig), good); ok {
		t.Fatal("a config message is not a packet")
	}
	if _, ok := ParsePacket(uint16(1)<<8|msgPacket, good); ok {
		t.Fatal("a message from another subsystem is not ours")
	}
	if _, ok := ParsePacket(msgType(msgPacket), msgBody(7)); ok {
		t.Fatal("a message with no payload carries no packet")
	}
	if _, ok := ParsePacket(msgType(msgPacket), []byte{1}); ok {
		t.Fatal("a body too short for its header must be refused")
	}
}

// ctTuple builds the nested conntrack attributes the kernel attaches.
func ctTuple(t *testing.T, src, dst string, proto uint8, sport, dport uint16) []byte {
	t.Helper()
	ip := append(attrBytes(1 /*CTA_IP_V4_SRC*/, net.ParseIP(src).To4(), false),
		attrBytes(2 /*CTA_IP_V4_DST*/, net.ParseIP(dst).To4(), false)...)
	pr := attrBytes(1 /*CTA_PROTO_NUM*/, []byte{proto}, false)
	pr = append(pr, attrBytes(2 /*CTA_PROTO_SRC_PORT*/, u16be(sport), false)...)
	pr = append(pr, attrBytes(3 /*CTA_PROTO_DST_PORT*/, u16be(dport), false)...)
	tuple := append(attrBytes(1 /*CTA_TUPLE_IP*/, ip, true), attrBytes(2 /*CTA_TUPLE_PROTO*/, pr, true)...)
	return attrBytes(1 /*CTA_TUPLE_ORIG*/, tuple, true)
}

func u16be(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u32be(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

var _ = ct.Update

// An inbound connection travels the other way: the connection's ORIGINAL
// source is then the machine on the internet, and filing the flow under it
// would name the attacker as the device.
func TestReplyDirectionNamesTheHouseEnd(t *testing.T) {
	// somebody on the internet connected in; this copy is the router's answer
	pkt := tcp4(t, "192.168.50.20", "203.0.113.9", 443, 51000, nil)
	conn := ctBothTuples(t,
		"203.0.113.9", "100.67.70.175", 51000, 443, // original: from the internet
		"192.168.50.20", "203.0.113.9", 443, 51000) // reply: from the house
	b := msgBody(7,
		attrBytes(nfulaCT, conn, true),
		attrBytes(nfulaCTInfo, u32be(ipCTIsReply), false),
		attrBytes(nfulaPayload, pkt, false),
	)
	p, ok := ParsePacket(msgType(msgPacket), b)
	if !ok {
		t.Fatal("must parse")
	}
	if !p.Reply {
		t.Fatal("the direction must be read")
	}
	if got := p.Device(); !got.Equal(net.ParseIP("192.168.50.20")) {
		t.Fatalf("the device is the house end, got %v", got)
	}

	// the same connection, original direction: the device is the original source
	b2 := msgBody(7,
		attrBytes(nfulaCT, conn, true),
		attrBytes(nfulaCTInfo, u32be(0), false),
		attrBytes(nfulaPayload, tcp4(t, "203.0.113.9", "100.67.70.175", 51000, 443, nil), false),
	)
	p2, _ := ParsePacket(msgType(msgPacket), b2)
	if p2.Reply {
		t.Fatal("this copy is in the original direction")
	}
	if got := p2.Device(); !got.Equal(net.ParseIP("203.0.113.9")) {
		t.Fatalf("in the original direction the device is the original source, got %v", got)
	}
}

// ctBothTuples builds a connection with both tuples, which is what the kernel
// attaches to a logged packet.
func ctBothTuples(t *testing.T, oSrc, oDst string, oSport, oDport uint16, rSrc, rDst string, rSport, rDport uint16) []byte {
	t.Helper()
	tup := func(src, dst string, sport, dport uint16) []byte {
		ip := append(attrBytes(1, net.ParseIP(src).To4(), false), attrBytes(2, net.ParseIP(dst).To4(), false)...)
		pr := attrBytes(1, []byte{6}, false)
		pr = append(pr, attrBytes(2, u16be(sport), false)...)
		pr = append(pr, attrBytes(3, u16be(dport), false)...)
		return append(attrBytes(1, ip, true), attrBytes(2, pr, true)...)
	}
	out := attrBytes(1 /*CTA_TUPLE_ORIG*/, tup(oSrc, oDst, oSport, oDport), true)
	return append(out, attrBytes(2 /*CTA_TUPLE_REPLY*/, tup(rSrc, rDst, rSport, rDport), true)...)
}

// At the ingress hook there is no connection to attach: conntrack has not run
// yet. What the frame does carry is its sender's hardware address, and that is
// the better identity anyway - an IPv6 address rotates twice a day on a phone,
// a MAC does not.
func TestAtIngressTheFrameNamesItsOwnDevice(t *testing.T) {
	hw := func(l uint16, addr ...byte) []byte {
		v := make([]byte, 4+len(addr))
		binary.BigEndian.PutUint16(v[0:2], l)
		copy(v[4:], addr)
		return v
	}
	mac := []byte{0x4e, 0xc2, 0x94, 0x9f, 0x07, 0x79}
	body := msgBody(7,
		attrBytes(nfulaHWAddr, hw(6, mac...), false),
		attrBytes(nfulaPayload, tcp4(t, "192.168.1.23", "149.154.167.99", 51000, 443, []byte("x")), false),
	)
	p, ok := ParsePacket(msgType(msgPacket), body)
	if !ok {
		t.Fatal("a packet message with a hardware address must parse")
	}
	if p.HWAddr != "4e:c2:94:9f:07:79" {
		t.Fatalf("the sender's MAC is the identity at ingress, got %q", p.HWAddr)
	}
	// and with no connection attached the address still has to come from the
	// packet, because at ingress it has not been through NAT yet
	if got := p.Device().String(); got != "192.168.1.23" {
		t.Fatalf("with no conntrack the packet names the device, got %q", got)
	}

	// A hardware address this code cannot read must leave the field empty, not
	// half an address: a wrong MAC is a device that does not exist, and it
	// would sit in the table looking exactly like a real one.
	for _, bad := range [][]byte{
		hw(6, mac[:3]...),           // says six bytes, carries three
		hw(20, make([]byte, 20)...), // not Ethernet
		{0, 6},                      // truncated before the address
	} {
		body := msgBody(7, attrBytes(nfulaHWAddr, bad, false),
			attrBytes(nfulaPayload, tcp4(t, "192.168.1.23", "1.1.1.1", 51000, 443, nil), false))
		p, ok := ParsePacket(msgType(msgPacket), body)
		if !ok {
			t.Fatal("a hardware address it cannot read is not a broken message")
		}
		if p.HWAddr != "" {
			t.Fatalf("unreadable hardware address became %q", p.HWAddr)
		}
	}
}

// The sequence number is how loss is seen at all, and its attribute number is
// part of the kernel's ABI: NFULA_SEQ is 12. This test writes the 12 out
// rather than using the package's own constant, because a wrong constant is
// self-consistent and invisible to every test that shares it. The first
// version of this code read 16, which is NFULA_HWHEADER, and every copy
// therefore looked unnumbered while the counter sat honestly at zero.
func TestTheSequenceNumberIsReadFromTheAttributeTheKernelUses(t *testing.T) {
	seq := []byte{0, 0, 0x30, 0x39} // 12345, big endian, as the kernel sends it
	body := msgBody(7,
		attrBytes(12, seq, false),
		attrBytes(nfulaPayload, tcp4(t, "192.168.1.5", "1.1.1.1", 4000, 443, nil), false),
	)
	p, ok := ParsePacket(msgType(msgPacket), body)
	if !ok {
		t.Fatal("a packet message with a sequence number must parse")
	}
	if !p.HasSeq || p.Seq != 12345 {
		t.Fatalf("attribute 12 is the sequence number: %v %d", p.HasSeq, p.Seq)
	}

	// And a message without one must say so rather than reporting zero, which
	// a gap counter would read as a jump back to the start.
	body = msgBody(7, attrBytes(nfulaPayload, tcp4(t, "192.168.1.5", "1.1.1.1", 4000, 443, nil), false))
	p, _ = ParsePacket(msgType(msgPacket), body)
	if p.HasSeq {
		t.Fatal("no attribute means no number, not number zero")
	}
}
