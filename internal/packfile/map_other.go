//go:build !unix

package packfile

import (
	"errors"
	"os"
)

// mapFile has no mapping to offer here; Open reads the file instead.
func mapFile(*os.File, int) ([]byte, error) {
	return nil, errors.New("no mmap on this system")
}

func unmapFile([]byte) {}
