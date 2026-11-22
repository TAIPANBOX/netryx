package hostorder

import "unsafe"

func pointerTo(v *uint16) unsafe.Pointer { return unsafe.Pointer(v) }
