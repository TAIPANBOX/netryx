package neigh

import "netryx/internal/hostorder"

// Sizes of the two structures this message is made of. Named rather than
// spelled as numbers at the call site, because the whole class of defect here
// is a message whose declared length does not match what it carries.
// The kernel's own numbers, spelled here so this file builds on any machine
// and can therefore be tested on one. A linux-only test checks each against
// golang.org/x/sys/unix.
const (
	nlMsgHdrLen = 16    // struct nlmsghdr
	ndMsgLen    = 12    // struct ndmsg: family, pad1, pad2, ifindex, state, flags, type
	rtmGetNeigh = 30    // RTM_GETNEIGH
	nlmFRequest = 0x001 // NLM_F_REQUEST
	nlmFDump    = 0x300 // NLM_F_ROOT|NLM_F_MATCH
	afUnspec    = 0     // both families in one dump
)

// DumpRequest builds the RTM_GETNEIGH dump message.
//
// The length has to be exactly the header plus the ndmsg. Four bytes of
// padding beyond it are not harmless: the kernel parses whatever follows the
// fixed part as attributes, finds a remainder it cannot account for, and logs
//
//	netlink: 4 bytes leftover after parsing attributes in process `netryx'
//
// once per request. The dump still works, which is why this survived: the
// only symptom was a line in somebody's kernel log, blaming a process they
// installed.
func DumpRequest(pid, seq uint32) []byte {
	req := make([]byte, nlMsgHdrLen+ndMsgLen)
	le := hostorder.Native
	le.PutUint32(req[0:4], uint32(len(req)))
	le.PutUint16(req[4:6], rtmGetNeigh)
	le.PutUint16(req[6:8], nlmFRequest|nlmFDump)
	le.PutUint32(req[8:12], seq)
	le.PutUint32(req[12:16], pid)
	req[16] = afUnspec // ndmsg.ndm_family
	return req
}
