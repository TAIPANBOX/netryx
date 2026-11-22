package ct

import (
	"net"
	"testing"

	"netryx/internal/hostorder"
)

func TestTheDumpRequestCarriesNothingItDoesNotDeclare(t *testing.T) {
	req := DumpRequest(4321, 9)
	if len(req) != 20 {
		t.Fatalf("header (16) plus nfgenmsg (4) is 20 bytes, got %d", len(req))
	}
	le := hostorder.Native
	if n := le.Uint32(req[0:4]); int(n) != len(req) {
		t.Fatalf("the declared length must be the real one: says %d, is %d", n, len(req))
	}
	if got := le.Uint16(req[4:6]); got != msgType(msgCTGet) {
		t.Fatalf("wrong message type: %#x", got)
	}
	if got := le.Uint16(req[6:8]); got != nlmFRequest|nlmFDump {
		t.Fatalf("a dump needs both flags, got %#x", got)
	}
	if le.Uint32(req[8:12]) != 9 || le.Uint32(req[12:16]) != 4321 {
		t.Fatal("sequence and pid must be the caller's")
	}
	if req[16] != afUnspec || req[17] != 0 {
		t.Fatalf("family and version: %d %d", req[16], req[17])
	}
	for i := 18; i < len(req); i++ {
		if req[i] != 0 {
			t.Fatalf("byte %d of the fixed part is not zero: %#x", i, req[i])
		}
	}
}

// A cut is the kernel's own delete message: the tuple of the connection as
// the device opened it, in the same nested layout the kernel sends us in an
// event, so the parser that reads events reads the request back to the same
// connection. The family is the tuple's, because the kernel parses the
// addresses by it, and the ACK flag is set so a refusal comes back as an error.
func TestCtDeleteRequestCarriesTheOriginalTuple(t *testing.T) {
	le := hostorder.Native
	for _, tc := range []struct {
		name   string
		tuple  Tuple
		family byte
	}{
		{"v4", Tuple{SrcIP: net.ParseIP("192.168.50.13").To4(), DstIP: net.ParseIP("149.154.167.220").To4(), Proto: 6, SrcPort: 51000, DstPort: 443}, 2},
		{"v6", Tuple{SrcIP: net.ParseIP("fd00::13"), DstIP: net.ParseIP("2001:67c:4e8:f004::9"), Proto: 17, SrcPort: 40001, DstPort: 443}, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, which := range []struct {
				attr  uint16
				reply bool
			}{{ctaTupleOrig, false}, {ctaTupleReply, true}} {
				req, err := DeleteRequest(77, 5, tc.tuple, which.reply)
				if err != nil {
					t.Fatal(err)
				}
				if n := le.Uint32(req[0:4]); int(n) != len(req) {
					t.Fatalf("declared length %d, real %d", n, len(req))
				}
				if got := le.Uint16(req[4:6]); got != msgType(msgCTDelete) {
					t.Fatalf("message type %#x, want the conntrack delete %#x", got, msgType(msgCTDelete))
				}
				if got := le.Uint16(req[6:8]); got != nlmFRequest|nlmFAck {
					t.Fatalf("flags %#x, want request and ack", got)
				}
				if le.Uint32(req[8:12]) != 5 || le.Uint32(req[12:16]) != 77 {
					t.Fatal("sequence and pid must be the caller's")
				}
				if req[16] != tc.family || req[17] != 0 {
					t.Fatalf("family %d version %d, want family %d", req[16], req[17], tc.family)
				}
				as := attrs(req[20:])
				if len(as) != 1 || as[0].typ != which.attr {
					t.Fatalf("one tuple attribute of type %d, got %+v", which.attr, as)
				}
				if f := le.Uint16(req[22:24]); f&0x8000 == 0 {
					t.Fatalf("the tuple must be marked nested: %#x", f)
				}
				e := ParseAttrs(req[20:], Destroy)
				got := e.Orig
				if which.reply {
					got = e.Reply
				}
				if !got.SrcIP.Equal(tc.tuple.SrcIP) || !got.DstIP.Equal(tc.tuple.DstIP) ||
					got.Proto != tc.tuple.Proto || got.SrcPort != tc.tuple.SrcPort || got.DstPort != tc.tuple.DstPort {
					t.Fatalf("the request names %+v, not %+v", got, tc.tuple)
				}
				if tc.name == "v4" && len(got.SrcIP) != 4 {
					t.Fatalf("an IPv4 address is 4 bytes in the request, got %d", len(got.SrcIP))
				}
			}
		})
	}

	for name, bad := range map[string]Tuple{
		"mixed families": {SrcIP: net.ParseIP("10.0.0.1"), DstIP: net.ParseIP("fd00::1"), Proto: 6, SrcPort: 1, DstPort: 2},
		"no address":     {DstIP: net.ParseIP("10.0.0.1"), Proto: 6, SrcPort: 1, DstPort: 2},
		"icmp":           {SrcIP: net.ParseIP("10.0.0.1"), DstIP: net.ParseIP("10.0.0.2"), Proto: 1},
	} {
		if _, err := DeleteRequest(1, 1, bad, false); err == nil {
			t.Errorf("%s: a tuple the kernel cannot look up must be refused, not sent", name)
		}
	}
}
