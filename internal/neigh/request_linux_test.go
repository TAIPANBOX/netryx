//go:build linux

package neigh

import (
	"testing"

	"golang.org/x/sys/unix"
)

// The constants above are written out so the builder compiles on any machine.
// That is only safe while they are the kernel's actual numbers, and this is
// what says so.
func TestTheConstantsAreTheKernels(t *testing.T) {
	if rtmGetNeigh != unix.RTM_GETNEIGH {
		t.Fatalf("RTM_GETNEIGH is %d, not %d", unix.RTM_GETNEIGH, rtmGetNeigh)
	}
	if nlmFRequest != unix.NLM_F_REQUEST || nlmFDump != unix.NLM_F_DUMP {
		t.Fatalf("flags drifted: %#x %#x", unix.NLM_F_REQUEST, unix.NLM_F_DUMP)
	}
	if afUnspec != unix.AF_UNSPEC {
		t.Fatalf("AF_UNSPEC is %d", unix.AF_UNSPEC)
	}
}
