// Package nflog reads the packets the kernel decides are worth showing.
//
// The selection is a firewall rule rather than a filter in this process:
//
//	ct original packets < 8 log group 7 snaplen 600
//
// which means the kernel copies only the first packets of each connection and
// stops. A bug here cannot leak bulk traffic to userspace, the choice is
// stateful so it also catches a handshake split across two segments, and with
// the conntrack flag set every copy arrives with its connection attached, so a
// captured name already knows which device asked for it.
package nflog

import (
	"encoding/binary"

	"net"
	"netryx/internal/hostorder"

	"netryx/internal/ct"
)

// hostOrder is how netlink lays out its own headers on this machine.
var hostOrder = hostorder.Native

const (
	subsysULOG = 4
	msgPacket  = 0
	msgConfig  = 1

	nfulaPacketHdr = 1
	nfulaTimestamp = 3
	nfulaIfInDev   = 4
	nfulaIfOutDev  = 5
	nfulaHWAddr    = 8
	nfulaPayload   = 9
	nfulaPrefix    = 10
	// NFULA_SEQ is 12 in the kernel enum; 16 is NFULA_HWHEADER, and reading
	// that as a sequence number means every copy looks unnumbered.
	nfulaSeq    = 12
	nfulaCT     = 18
	nfulaCTInfo = 19

	// conntrack info, from nf_conntrack_common.h: values from IP_CT_IS_REPLY
	// up mean this packet travels in the reply direction.
	ipCTIsReply = 3

	nlaTypeMask = 0x3fff
)

// Packet is one copy the kernel sent up.
type Packet struct {
	Group    uint16
	Protocol uint16 // ethertype, host order
	Hook     uint8
	InDev    uint32
	OutDev   uint32
	HWAddr   string // the sender's hardware address, when the hook saw one
	// Seq is the kernel's own counter for this log group. It is the only way
	// to know that copies were lost: the socket reports a burst of loss once
	// and says nothing about how much, while a gap in this number is exact.
	Seq     uint32
	HasSeq  bool
	Prefix  string // the rule's log prefix
	Payload []byte // the packet itself, from the network header
	// Conn is the connection the kernel attached. Its Orig.SrcIP is the
	// device's own address even when the packet is already NATed.
	Conn    ct.Event
	HasConn bool
	// Reply says the packet travels in the connection's reply direction. It
	// decides which end of the tuple is the device: for an inbound connection
	// the original source is the machine on the internet, not the one at home.
	Reply bool
}

// ParsePacket reads one message body (after the 16-byte netlink header).
func ParsePacket(msgType uint16, b []byte) (Packet, bool) {
	if msgType>>8 != subsysULOG || msgType&0xff != msgPacket {
		return Packet{}, false
	}
	// struct nfgenmsg: family(1) version(1) res_id(2, big endian) = the group
	if len(b) < 4 {
		return Packet{}, false
	}
	p := Packet{Group: binary.BigEndian.Uint16(b[2:4])}
	for _, a := range attrs(b[4:]) {
		switch a.typ {
		case nfulaPacketHdr:
			if len(a.val) >= 4 {
				p.Protocol = binary.BigEndian.Uint16(a.val[0:2])
				p.Hook = a.val[2]
			}
		case nfulaIfInDev:
			p.InDev = be32(a.val)
		case nfulaIfOutDev:
			p.OutDev = be32(a.val)
		case nfulaHWAddr:
			// hw_addrlen(2) pad(2) then the address
			if len(a.val) >= 4 {
				l := int(binary.BigEndian.Uint16(a.val[0:2]))
				if l == 6 && len(a.val) >= 4+6 {
					p.HWAddr = net.HardwareAddr(a.val[4 : 4+6]).String()
				}
			}
		case nfulaSeq:
			if len(a.val) >= 4 {
				p.Seq, p.HasSeq = binary.BigEndian.Uint32(a.val), true
			}
		case nfulaPrefix:
			p.Prefix = trimZero(string(a.val))
		case nfulaPayload:
			p.Payload = a.val
		case nfulaCT:
			p.Conn = ct.ParseAttrs(a.val, ct.Update)
			p.HasConn = p.Conn.Orig.SrcIP != nil
		case nfulaCTInfo:
			p.Reply = be32(a.val) >= ipCTIsReply
		}
	}
	if p.Payload == nil {
		return Packet{}, false
	}
	return p, true
}

// Device returns the address of the machine this packet belongs to, preferring
// the connection's pre-NAT source over anything visible in the packet itself.
func (p Packet) Device() net.IP {
	if p.HasConn {
		// In the reply direction the connection's original source is the far
		// end: a port forward or an inbound IPv6 connection would otherwise
		// be filed under the address of the machine on the internet.
		if p.Reply && p.Conn.Reply.SrcIP != nil {
			return p.Conn.Reply.SrcIP
		}
		if !p.Reply && p.Conn.Orig.SrcIP != nil {
			return p.Conn.Orig.SrcIP
		}
	}
	return sourceOf(p.Payload)
}

// Peer is the address at the other end: where the packet is going, or where
// it came from in the reply direction. It is what a destination is looked up
// by, so it copies rather than pointing into the read buffer.
func (p Packet) Peer() net.IP { return addrAt(p.Payload, false) }

// Ports returns the transport ports, or zeros when the packet carries none.
func (p Packet) Ports() (src, dst uint16) {
	pkt := p.Payload
	var off int
	switch {
	case len(pkt) >= 20 && pkt[0]>>4 == 4:
		off = int(pkt[0]&0x0f) * 4
		if pkt[9] != 6 && pkt[9] != 17 {
			return 0, 0
		}
	case len(pkt) >= 40 && pkt[0]>>4 == 6:
		off = 40
		if pkt[6] != 6 && pkt[6] != 17 {
			return 0, 0
		}
	default:
		return 0, 0
	}
	if len(pkt) < off+4 {
		return 0, 0
	}
	return binary.BigEndian.Uint16(pkt[off : off+2]), binary.BigEndian.Uint16(pkt[off+2 : off+4])
}

// Source is the source address in the packet's own header, whatever the
// connection says: for a packet the router sends, one of its own addresses.
func (p Packet) Source() net.IP { return sourceOf(p.Payload) }

// sourceOf reads the source address straight from the network header, which is
// the fallback when no connection is attached.
func sourceOf(pkt []byte) net.IP { return addrAt(pkt, true) }

// addrAt copies an address out of the header. The copy is the point: the
// payload is a window onto a read buffer that the next packet overwrites, and
// an address kept by reference changes into somebody else's later.
func addrAt(pkt []byte, source bool) net.IP {
	if len(pkt) < 20 {
		return nil
	}
	switch pkt[0] >> 4 {
	case 4:
		off := 12
		if !source {
			off = 16
		}
		return net.IP(append([]byte{}, pkt[off:off+4]...))
	case 6:
		if len(pkt) < 40 {
			return nil
		}
		off := 8
		if !source {
			off = 24
		}
		return net.IP(append([]byte{}, pkt[off:off+16]...))
	}
	return nil
}

// TransportPayload returns the bytes after the transport header, which is
// where a TLS record or a QUIC packet begins.
func TransportPayload(pkt []byte) []byte {
	if len(pkt) < 20 {
		return nil
	}
	switch pkt[0] >> 4 {
	case 4:
		ihl := int(pkt[0]&0x0f) * 4
		if len(pkt) < ihl+20 {
			return nil
		}
		switch pkt[9] {
		case 6: // TCP
			off := ihl + int(pkt[ihl+12]>>4)*4
			if off > len(pkt) {
				return nil
			}
			return pkt[off:]
		case 17: // UDP
			if len(pkt) < ihl+8 {
				return nil
			}
			return pkt[ihl+8:]
		}
	case 6:
		if len(pkt) < 40 {
			return nil
		}
		switch pkt[6] { // next header, extension headers not walked
		case 6:
			if len(pkt) < 40+20 {
				return nil
			}
			off := 40 + int(pkt[40+12]>>4)*4
			if off > len(pkt) {
				return nil
			}
			return pkt[off:]
		case 17:
			if len(pkt) < 48 {
				return nil
			}
			return pkt[48:]
		}
	}
	return nil
}

type attr struct {
	typ uint16
	val []byte
}

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

func be32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

func trimZero(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return s[:i]
		}
	}
	return s
}
