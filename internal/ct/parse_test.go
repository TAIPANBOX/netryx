package ct

import (
	"encoding/binary"
	"net"
	"testing"
)

// The helpers build messages the way the kernel lays them out: attribute
// lengths in host order, values in network order, nested attributes carrying
// the nested flag. Tested against this rather than against the parser's own
// idea of the format.
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

func u16be(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u32be(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64be(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

func tupleBytes(src, dst net.IP, proto uint8, sport, dport uint16) []byte {
	var ipAttrs []byte
	if v4 := src.To4(); v4 != nil {
		ipAttrs = append(ipAttrs, attrBytes(ctaIPv4Src, v4, false)...)
		ipAttrs = append(ipAttrs, attrBytes(ctaIPv4Dst, dst.To4(), false)...)
	} else {
		ipAttrs = append(ipAttrs, attrBytes(ctaIPv6Src, src.To16(), false)...)
		ipAttrs = append(ipAttrs, attrBytes(ctaIPv6Dst, dst.To16(), false)...)
	}
	var protoAttrs []byte
	protoAttrs = append(protoAttrs, attrBytes(ctaProtoNum, []byte{proto}, false)...)
	protoAttrs = append(protoAttrs, attrBytes(ctaProtoSrcPort, u16be(sport), false)...)
	protoAttrs = append(protoAttrs, attrBytes(ctaProtoDstPort, u16be(dport), false)...)

	var out []byte
	out = append(out, attrBytes(ctaTupleIP, ipAttrs, true)...)
	out = append(out, attrBytes(ctaTupleProto, protoAttrs, true)...)
	return out
}

func countersBytes(pkts, bytes uint64) []byte {
	var out []byte
	out = append(out, attrBytes(ctaCountersPackets, u64be(pkts), false)...)
	out = append(out, attrBytes(ctaCountersBytes, u64be(bytes), false)...)
	return out
}

// body assembles a whole message body: nfgenmsg header plus attributes.
func body(parts ...[]byte) []byte {
	out := []byte{2 /* AF_INET */, 0 /* version */, 0, 0 /* res_id */}
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// A NEW event carries both tuples, and the difference between them IS the NAT
// mapping: the device's address on one side, the address the world sees on the
// other. This is the whole reason for using ctnetlink over the text file.
func TestNewEventCarriesTheNATMapping(t *testing.T) {
	b := body(
		attrBytes(ctaTupleOrig, tupleBytes(net.ParseIP("192.168.50.10"), net.ParseIP("140.82.121.4"), 6, 51000, 443), true),
		attrBytes(ctaTupleReply, tupleBytes(net.ParseIP("140.82.121.4"), net.ParseIP("100.67.70.175"), 6, 443, 51000), true),
		attrBytes(ctaStatus, u32be(statusSeenReply|statusSrcNAT), false),
		attrBytes(ctaID, u32be(0xdeadbeef), false),
	)
	e, ok := ParseEvent(msgType(msgCTNew), b)
	if !ok {
		t.Fatal("a well-formed NEW event must parse")
	}
	if e.Kind != New {
		t.Fatalf("kind wrong: %v", e.Kind)
	}
	if !e.Orig.SrcIP.Equal(net.ParseIP("192.168.50.10")) || e.Orig.DstPort != 443 || e.Orig.Proto != 6 {
		t.Fatalf("original tuple wrong: %+v", e.Orig)
	}
	if !e.Reply.DstIP.Equal(net.ParseIP("100.67.70.175")) {
		t.Fatalf("the address the world sees is in the reply tuple: %+v", e.Reply)
	}
	if !e.NATed() {
		t.Fatal("orig source and reply destination differ, so this is NATed")
	}
	if e.ID != 0xdeadbeef || !e.SeenReply() {
		t.Fatalf("status or id lost: %+v", e)
	}
}

// DESTROY is where the final per-direction counters arrive, and the two
// directions must not be merged.
func TestDestroyEventCarriesFinalCountersPerDirection(t *testing.T) {
	b := body(
		attrBytes(ctaTupleOrig, tupleBytes(net.ParseIP("192.168.50.10"), net.ParseIP("140.82.121.4"), 6, 51000, 443), true),
		attrBytes(ctaTupleReply, tupleBytes(net.ParseIP("140.82.121.4"), net.ParseIP("100.67.70.175"), 6, 443, 51000), true),
		attrBytes(ctaCountersOrig, countersBytes(42, 5120), true),
		attrBytes(ctaCountersReply, countersBytes(38, 40960), true),
		attrBytes(ctaStatus, u32be(statusAssured|statusSeenReply), false),
	)
	e, ok := ParseEvent(msgType(msgCTDelete), b)
	if !ok || e.Kind != Destroy {
		t.Fatalf("a DESTROY must parse as one: %+v %v", e, ok)
	}
	if e.Up.Packets != 42 || e.Up.Bytes != 5120 {
		t.Fatalf("upload counters wrong: %+v", e.Up)
	}
	if e.Down.Packets != 38 || e.Down.Bytes != 40960 {
		t.Fatalf("download counters wrong: %+v", e.Down)
	}
	if !e.Assured() {
		t.Fatal("status lost")
	}
}

// IPv6 needs no NAT, and the parser must not invent one.
func TestIPv6EventIsNotNATed(t *testing.T) {
	src := net.ParseIP("2a01:4b00:ab09:c000:24ec:a181:a05f:5412")
	dst := net.ParseIP("2606:4700:4700::1111")
	b := body(
		attrBytes(ctaTupleOrig, tupleBytes(src, dst, 6, 52000, 443), true),
		attrBytes(ctaTupleReply, tupleBytes(dst, src, 6, 443, 52000), true),
		attrBytes(ctaStatus, u32be(statusSeenReply), false),
	)
	e, ok := ParseEvent(msgType(msgCTNew), b)
	if !ok {
		t.Fatal("an IPv6 event must parse")
	}
	if !e.Orig.SrcIP.Equal(src) || !e.Reply.DstIP.Equal(src) {
		t.Fatalf("IPv6 tuples wrong: %+v / %+v", e.Orig, e.Reply)
	}
	if e.NATed() {
		t.Fatal("IPv6 has no NAT here, and the parser must not claim one")
	}
}

// An offloaded flow is counted in bursts by the flow table or the hardware, so
// everything built on its byte figures has to know they are approximate.
func TestOffloadStatusIsVisible(t *testing.T) {
	mk := func(status uint32) Event {
		b := body(
			attrBytes(ctaTupleOrig, tupleBytes(net.ParseIP("192.168.50.12"), net.ParseIP("142.250.180.14"), 6, 51004, 443), true),
			attrBytes(ctaStatus, u32be(status), false),
		)
		e, ok := ParseEvent(msgType(msgCTNew), b)
		if !ok {
			t.Fatal("must parse")
		}
		return e
	}
	if !mk(statusOffload).Offloaded() {
		t.Fatal("software offload must be visible")
	}
	if !mk(statusHWOffload).Offloaded() {
		t.Fatal("hardware offload must be visible")
	}
	if mk(statusAssured).Offloaded() {
		t.Fatal("an ordinary flow must not be called offloaded")
	}
}

// Anything that is not a conntrack message, or is too short to hold one, is
// refused rather than half-read.
func TestGarbageIsRefused(t *testing.T) {
	good := body(attrBytes(ctaTupleOrig, tupleBytes(net.ParseIP("10.0.0.1"), net.ParseIP("10.0.0.2"), 6, 1, 2), true))
	if _, ok := ParseEvent(uint16(2)<<8|msgCTNew, good); ok {
		t.Fatal("a message from another netfilter subsystem is not ours")
	}
	if _, ok := ParseEvent(msgType(7), good); ok {
		t.Fatal("an unknown conntrack message type must be refused")
	}
	if _, ok := ParseEvent(msgType(msgCTNew), []byte{1, 2}); ok {
		t.Fatal("a body too short for the header must be refused")
	}
	if _, ok := ParseEvent(msgType(msgCTNew), body()); ok {
		t.Fatal("a message with no tuple names no flow")
	}
	// an attribute claiming more than it has must end the walk, not run off
	truncated := append(attrBytes(ctaTupleOrig, tupleBytes(net.ParseIP("10.0.0.1"), net.ParseIP("10.0.0.2"), 6, 1, 2), true),
		0x40, 0x00, 0x01, 0x00) // an attribute that claims 64 bytes and has none
	bad := body(truncated)
	if _, ok := ParseEvent(msgType(msgCTNew), bad); !ok {
		t.Fatal("what was read before the bad attribute is still usable")
	}
}
