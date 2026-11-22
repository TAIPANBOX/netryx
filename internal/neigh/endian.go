package neigh

import (
	"encoding/binary"
	"unsafe"
)

// hostEndian returns the byte order netlink speaks, which is the host's.
func hostEndian() binary.ByteOrder {
	var x uint16 = 1
	if *(*byte)(unsafe.Pointer(&x)) == 1 {
		return binary.LittleEndian
	}
	return binary.BigEndian
}
