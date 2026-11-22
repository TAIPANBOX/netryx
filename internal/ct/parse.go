// Package ct reads the kernel's connection tracking over netlink.
//
// The text file /proc/net/nf_conntrack cannot say when a flow started, cannot
// give a duration, is deprecated, and prints zero counters when accounting is
// off without saying so. ctnetlink gives one exact record per flow: the NEW
// event carries both tuples, which is the NAT mapping, and DESTROY carries the
// final counters for each direction.
package ct

import (
	"encoding/binary"

	"net"
	"netryx/internal/hostorder"
)

// hostOrder is how netlink lays out its own headers on this machine.
var hostOrder = hostorder.Native

// netfilter subsystem and message types
const (
	subsysCTNetlink = 1
	msgCTNew        = 0
	msgCTGet        = 1
	msgCTDelete     = 2
)

// attributes, from nfnetlink_conntrack.h
const (
	ctaTupleOrig     = 1
	ctaTupleReply    = 2
	ctaStatus        = 3
	ctaTimeout       = 7
	ctaMark          = 8
	ctaCountersOrig  = 9
	ctaCountersReply = 10
	ctaID            = 12

	ctaTupleIP    = 1
	ctaTupleProto = 2

	ctaIPv4Src = 1
	ctaIPv4Dst = 2
	ctaIPv6Src = 3
	ctaIPv6Dst = 4

	ctaProtoNum     = 1
	ctaProtoSrcPort = 2
	ctaProtoDstPort = 3

	ctaCountersPackets = 1
	ctaCountersBytes   = 2

	nlaTypeMask = 0x3fff // the nested and byteorder flags are not part of the type
)

// connection status bits worth naming
const (
	statusSeenReply = 1 << 1
	statusAssured   = 1 << 2
	statusSrcNAT    = 1 << 4
	statusDstNAT    = 1 << 5
	statusDying     = 1 << 9
	statusOffload   = 1 << 14
	statusHWOffload = 1 << 15
)

// Kind says what happened to the flow.
type Kind int

const (
	New Kind = iota
	Destroy
	Update
)

// Tuple is one direction's addressing.
type Tuple struct {
	SrcIP   net.IP
	DstIP   net.IP
	Proto   uint8
	SrcPort uint16
	DstPort uint16
}

// Counters are packets and bytes for one direction.
type Counters struct {
	Packets uint64
	Bytes   uint64
}

// Event is one connection as the kernel reports it.
type Event struct {
	Kind   Kind
	Orig   Tuple // as the device sent it, before NAT
	Reply  Tuple // as the world answers, so Reply.DstIP is the NATed address
	Up     Counters
	Down   Counters
	Status uint32
	Mark   uint32
	ID     uint32
}

func (e Event) Assured() bool   { return e.Status&statusAssured != 0 }
func (e Event) SeenReply() bool { return e.Status&statusSeenReply != 0 }

// Offloaded reports whether the kernel handed this flow to the flow table or
// to hardware. Its counters then refresh in bursts rather than per packet, so
// the totals are approximate and anything built on them must say so.
func (e Event) Offloaded() bool { return e.Status&(statusOffload|statusHWOffload) != 0 }

// NATed reports whether the address the world sees differs from the device's.
func (e Event) NATed() bool {
	return e.Status&(statusSrcNAT|statusDstNAT) != 0 ||
		(e.Orig.SrcIP != nil && e.Reply.DstIP != nil && !e.Orig.SrcIP.Equal(e.Reply.DstIP))
}

// ParseEvent reads one netlink message body (after the 16-byte header).
func ParseEvent(msgType uint16, b []byte) (Event, bool) {
	if msgType>>8 != subsysCTNetlink {
		return Event{}, false
	}
	var e Event
	switch msgType & 0xff {
	case msgCTNew:
		e.Kind = New
	case msgCTDelete:
		e.Kind = Destroy
	case msgCTGet:
		e.Kind = Update
	default:
		return Event{}, false
	}
	// struct nfgenmsg: family(1) version(1) res_id(2)
	if len(b) < 4 {
		return Event{}, false
	}
	e = ParseAttrs(b[4:], e.Kind)
	if e.Orig.SrcIP == nil {
		return Event{}, false
	}
	return e, true
}

// ParseAttrs reads a bare conntrack attribute list. The packet log carries one
// of these inside every message when it is configured to attach the
// connection, which is how a captured handshake knows whose it is without any
// join against a separate table.
func ParseAttrs(b []byte, kind Kind) Event {
	e := Event{Kind: kind}
	for _, a := range attrs(b) {
		switch a.typ {
		case ctaTupleOrig:
			e.Orig = parseTuple(a.val)
		case ctaTupleReply:
			e.Reply = parseTuple(a.val)
		case ctaCountersOrig:
			e.Up = parseCounters(a.val)
		case ctaCountersReply:
			e.Down = parseCounters(a.val)
		case ctaStatus:
			e.Status = be32(a.val)
		case ctaMark:
			e.Mark = be32(a.val)
		case ctaID:
			e.ID = be32(a.val)
		}
	}
	return e
}

type attr struct {
	typ uint16
	val []byte
}

// attrs walks a netlink attribute list. Lengths are host order even here;
// only the VALUES in this subsystem are big-endian.
func attrs(b []byte) []attr {
	var out []attr
	for len(b) >= 4 {
		l := int(hostOrder.Uint16(b[0:2]))
		t := hostOrder.Uint16(b[2:4]) & nlaTypeMask
		if l < 4 || l > len(b) {
			return out
		}
		out = append(out, attr{typ: t, val: b[4:l]})
		b = b[(l+3)&^3:]
	}
	return out
}

func parseTuple(b []byte) Tuple {
	var t Tuple
	for _, a := range attrs(b) {
		switch a.typ {
		case ctaTupleIP:
			for _, ip := range attrs(a.val) {
				switch ip.typ {
				case ctaIPv4Src, ctaIPv6Src:
					t.SrcIP = append(net.IP(nil), ip.val...)
				case ctaIPv4Dst, ctaIPv6Dst:
					t.DstIP = append(net.IP(nil), ip.val...)
				}
			}
		case ctaTupleProto:
			for _, p := range attrs(a.val) {
				switch p.typ {
				case ctaProtoNum:
					if len(p.val) >= 1 {
						t.Proto = p.val[0]
					}
				case ctaProtoSrcPort:
					t.SrcPort = be16(p.val)
				case ctaProtoDstPort:
					t.DstPort = be16(p.val)
				}
			}
		}
	}
	return t
}

func parseCounters(b []byte) Counters {
	var c Counters
	for _, a := range attrs(b) {
		switch a.typ {
		case ctaCountersPackets:
			c.Packets = be64(a.val)
		case ctaCountersBytes:
			c.Bytes = be64(a.val)
		}
	}
	return c
}

func be16(b []byte) uint16 {
	if len(b) < 2 {
		return 0
	}
	return binary.BigEndian.Uint16(b)
}

func be32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

func be64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

// msgType is the netlink message type: the subsystem in the high byte, the
// message kind in the low one. It lives here rather than beside the socket
// because the request builder needs it and that must compile everywhere.
func msgType(kind int) uint16 { return uint16(subsysCTNetlink)<<8 | uint16(kind) }
