//go:build linux

package ct

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestTheConstantsAreTheKernels(t *testing.T) {
	if nlmFRequest != unix.NLM_F_REQUEST || nlmFDump != unix.NLM_F_DUMP {
		t.Fatalf("flags drifted: %#x %#x", unix.NLM_F_REQUEST, unix.NLM_F_DUMP)
	}
	if nlmFAck != unix.NLM_F_ACK || afInet != unix.AF_INET || afInet6 != unix.AF_INET6 {
		t.Fatalf("ack or families drifted: %#x %d %d", unix.NLM_F_ACK, unix.AF_INET, unix.AF_INET6)
	}
	if afUnspec != unix.AF_UNSPEC {
		t.Fatalf("AF_UNSPEC is %d", unix.AF_UNSPEC)
	}
}
