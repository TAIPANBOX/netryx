package rules

import (
	"syscall"
	"unsafe"
)

// KernelEast returns the offset in minutes EAST of UTC that the kernel uses
// for nft's `meta day` (the negated tz_minuteswest of sys_tz), read with the
// raw gettimeofday syscall, whose struct timezone is the only place it shows.
func KernelEast() (int, error) {
	var tz struct{ minuteswest, dsttime int32 }
	_, _, e := syscall.Syscall(syscall.SYS_GETTIMEOFDAY,
		0, uintptr(unsafe.Pointer(&tz)), 0)
	if e != 0 {
		return 0, e
	}
	return -int(tz.minuteswest), nil
}
