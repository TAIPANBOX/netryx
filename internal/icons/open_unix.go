//go:build unix

package icons

import (
	"os"
	"syscall"
)

// openIcon opens a kept icon without following a symlink, and without
// waiting: a FIFO swapped in under an icon's name after the Lstat would
// otherwise block the HTTP handler until some writer came along. On a
// regular file O_NONBLOCK changes nothing.
func openIcon(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
