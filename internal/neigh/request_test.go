package neigh

import (
	"testing"

	"netryx/internal/hostorder"
)

// The defect this pins: a request padded past its fixed part. The kernel
// parses the remainder as attributes, cannot account for it, and writes
// "N bytes leftover after parsing attributes in process `netryx'" into the
// log of every router this is installed on. The dump still works, so nothing
// else would ever notice.
func TestTheDumpRequestCarriesNothingItDoesNotDeclare(t *testing.T) {
	req := DumpRequest(1234, 7)
	if len(req) != 28 {
		t.Fatalf("header (16) plus ndmsg (12) is 28 bytes, got %d", len(req))
	}
	le := hostorder.Native
	if n := le.Uint32(req[0:4]); int(n) != len(req) {
		t.Fatalf("the declared length must be the real one: says %d, is %d", n, len(req))
	}
	if got := le.Uint16(req[4:6]); got != rtmGetNeigh {
		t.Fatalf("wrong message type: %d", got)
	}
	if got := le.Uint16(req[6:8]); got != nlmFRequest|nlmFDump {
		t.Fatalf("a dump needs both flags, got %#x", got)
	}
	if le.Uint32(req[8:12]) != 7 || le.Uint32(req[12:16]) != 1234 {
		t.Fatal("sequence and pid must be the caller's")
	}
	if req[16] != afUnspec {
		t.Fatalf("both families come in one dump: family %d", req[16])
	}
	// Everything after the family byte is the rest of the fixed structure and
	// must be zero: a stray byte here would be read as the start of an
	// attribute.
	for i := 17; i < len(req); i++ {
		if req[i] != 0 {
			t.Fatalf("byte %d of the fixed part is not zero: %#x", i, req[i])
		}
	}
}
