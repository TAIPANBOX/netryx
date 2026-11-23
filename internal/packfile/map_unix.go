//go:build unix

package packfile

import (
	"os"
	"syscall"
)

// mapFile maps the file read-only and shared: its pages are the page cache's,
// clean and backed by the file, so the kernel can drop them under pressure and
// read them again, which heap pages never allow.
func mapFile(f *os.File, size int) ([]byte, error) {
	return syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
}

func unmapFile(b []byte) { _ = syscall.Munmap(b) }
