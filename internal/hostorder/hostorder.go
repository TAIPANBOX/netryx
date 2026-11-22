// Package hostorder says which way round this machine puts its integers.
//
// Netlink speaks the host's byte order for its own structures: message
// headers, attribute headers, and the ndmsg fields. Only the VALUES inside
// netfilter attributes are network order. Hardcoding little-endian is right on
// every desktop and silently wrong on a big-endian router: every attribute
// list parses to nothing, every event is dropped for having no address, and
// the health check still reports success because the sockets themselves open.
// OpenWrt still ships big-endian MIPS targets, and this package builds for
// them.
package hostorder

import (
	"encoding/binary"
	"unsafe"
)

// Native is the host's byte order.
var Native = func() binary.ByteOrder {
	var x uint16 = 1
	if *(*byte)(unsafe.Pointer(&x)) == 1 {
		return binary.LittleEndian
	}
	return binary.BigEndian
}()
