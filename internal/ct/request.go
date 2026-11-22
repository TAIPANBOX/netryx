package ct

import (
	"fmt"

	"netryx/internal/hostorder"
)

// The kernel's own numbers, spelled here so this file builds on any machine
// and can therefore be tested on one. A linux-only test asserts each of them
// against the value in golang.org/x/sys/unix, which is what keeps them from
// drifting into fiction.
const (
	nlMsgHdrLen = 16    // struct nlmsghdr
	nfGenMsgLen = 4     // struct nfgenmsg: family, version, res_id
	nlmFRequest = 0x001 // NLM_F_REQUEST
	nlmFDump    = 0x300 // NLM_F_ROOT|NLM_F_MATCH
	nlmFAck     = 0x004 // NLM_F_ACK: answer with an error message, 0 on success
	afUnspec    = 0     // AF_UNSPEC: both families in one dump
	afInet      = 2     // AF_INET
	afInet6     = 10    // AF_INET6
)

// DumpRequest builds the ctnetlink dump message: every tracked connection,
// both families.
//
// As with the neighbour dump, the length must be the header plus the fixed
// part and nothing else. Anything after it is read as attributes, and a
// remainder the kernel cannot parse is a warning in the log of every router
// this is installed on.
func DumpRequest(pid, seq uint32) []byte {
	req := make([]byte, nlMsgHdrLen+nfGenMsgLen)
	le := hostorder.Native
	le.PutUint32(req[0:4], uint32(len(req)))
	le.PutUint16(req[4:6], msgType(msgCTGet))
	le.PutUint16(req[6:8], nlmFRequest|nlmFDump)
	le.PutUint32(req[8:12], seq)
	le.PutUint32(req[12:16], pid)
	req[16] = afUnspec // nfgenmsg.nfgen_family
	req[17] = 0        // nfgenmsg.version
	return req
}

// DeleteRequest builds the ctnetlink message that ends one connection. The
// kernel finds the connection by either of its tuples: reply false names it
// by the original tuple (as the device opened it), reply true by the reply
// tuple, which is how a connection opened from outside behind a port forward
// is known by its device's side. The family is the tuple's, because the
// kernel reads the addresses by it, and ACK is set so a refusal comes back.
//
// Only TCP and UDP are named: other protocols carry other attributes in their
// tuple, and nothing else is offloaded or worth cutting.
func DeleteRequest(pid, seq uint32, t Tuple, reply bool) ([]byte, error) {
	src4, dst4 := t.SrcIP.To4(), t.DstIP.To4()
	family := byte(afInet)
	src, dst := src4, dst4
	ipSrc, ipDst := uint16(ctaIPv4Src), uint16(ctaIPv4Dst)
	switch {
	case src4 != nil && dst4 != nil:
	case src4 == nil && dst4 == nil && len(t.SrcIP) == 16 && len(t.DstIP) == 16:
		family, src, dst = afInet6, t.SrcIP, t.DstIP
		ipSrc, ipDst = ctaIPv6Src, ctaIPv6Dst
	default:
		return nil, fmt.Errorf("conntrack delete: %v -> %v is not one family", t.SrcIP, t.DstIP)
	}
	if t.Proto != 6 && t.Proto != 17 {
		return nil, fmt.Errorf("conntrack delete: protocol %d is not TCP or UDP", t.Proto)
	}
	be := func(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
	ip := append(nlAttr(ipSrc, src, false), nlAttr(ipDst, dst, false)...)
	proto := nlAttr(ctaProtoNum, []byte{t.Proto}, false)
	proto = append(proto, nlAttr(ctaProtoSrcPort, be(t.SrcPort), false)...)
	proto = append(proto, nlAttr(ctaProtoDstPort, be(t.DstPort), false)...)
	tuple := append(nlAttr(ctaTupleIP, ip, true), nlAttr(ctaTupleProto, proto, true)...)
	which := uint16(ctaTupleOrig)
	if reply {
		which = ctaTupleReply
	}
	body := nlAttr(which, tuple, true)

	req := make([]byte, nlMsgHdrLen+nfGenMsgLen, nlMsgHdrLen+nfGenMsgLen+len(body))
	req = append(req, body...)
	le := hostorder.Native
	le.PutUint32(req[0:4], uint32(len(req)))
	le.PutUint16(req[4:6], msgType(msgCTDelete))
	le.PutUint16(req[6:8], nlmFRequest|nlmFAck)
	le.PutUint32(req[8:12], seq)
	le.PutUint32(req[12:16], pid)
	req[16] = family // nfgenmsg.nfgen_family
	req[17] = 0      // nfgenmsg.version
	return req, nil
}

// nlAttrNested is NLA_F_NESTED, the flag on an attribute that holds others.
const nlAttrNested = 0x8000

// nlAttr lays out one netlink attribute: length and type in host order, the
// value as given, padded to four bytes.
func nlAttr(typ uint16, val []byte, nested bool) []byte {
	l := 4 + len(val)
	b := make([]byte, (l+3)&^3)
	if nested {
		typ |= nlAttrNested
	}
	hostorder.Native.PutUint16(b[0:2], uint16(l))
	hostorder.Native.PutUint16(b[2:4], typ)
	copy(b[4:], val)
	return b
}
