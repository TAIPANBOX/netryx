package hostorder

import (
	"encoding/binary"
	"testing"
)

// The value of this package is that it is not a constant. A test that asserts
// little-endian would pass on the machine it was written on and enshrine the
// bug it exists to prevent.
func TestNativeMatchesTheMachine(t *testing.T) {
	b := make([]byte, 2)
	Native.PutUint16(b, 0x0102)
	var probe uint16 = 0x0102
	raw := (*[2]byte)(pointerTo(&probe))
	if b[0] != raw[0] || b[1] != raw[1] {
		t.Fatalf("Native (%v) does not match how this machine stores a uint16: %x vs %x", Native, b, raw)
	}
	if Native != binary.LittleEndian && Native != binary.BigEndian {
		t.Fatal("Native must be one of the two")
	}
}
