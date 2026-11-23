// Package packfile is the frame around netryx's binary data files: the
// address table and the vendor list, prepared when the package is built and
// read through a read-only mapping on the router, so the tables cost the
// service no heap.
//
// A pack is
//
//	magic   8 bytes, which file this is
//	version 4 bytes, little-endian
//	body    the table, whose layout belongs to the package that reads it
//	sum     32 bytes, the SHA-256 of everything before it
//
// The package manager's signature covers the file as package contents; the sum
// additionally catches a file damaged or cut short on the router's flash. A
// reader checks the frame here and every count and offset of its body against
// the body's length before it answers anything.
package packfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MagicLen is the length of a pack's magic.
const MagicLen = 8

const (
	headerLen = MagicLen + 4
	sumLen    = sha256.Size
)

// MaxSize bounds a pack. The real address table is about 15 MB; a file far
// larger is not one of ours, and is refused before it is mapped.
const MaxSize = 256 << 20

// Open reads the file at path: mapped read-only where the system can map it,
// otherwise read whole into memory. The mapping is never removed: the tables
// read from it are held for the life of the process, and strings handed out
// point into it.
func Open(path string) (b []byte, mapped bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !st.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is not a regular file", path)
	}
	size := st.Size()
	if size < headerLen+sumLen {
		return nil, false, fmt.Errorf("%s is %d bytes, too short to be a data pack", path, size)
	}
	if size > MaxSize {
		return nil, false, fmt.Errorf("%s is %d bytes, over the %d-byte limit of a data pack", path, size, MaxSize)
	}
	if b, err := mapFile(f, int(size)); err == nil {
		return b, true, nil
	}
	b = make([]byte, size)
	if _, err := io.ReadFull(f, b); err != nil {
		return nil, false, err
	}
	return b, false, nil
}

// Check verifies the frame of a pack: its magic, its version and the sum at its
// end. It returns the body between the header and the sum.
func Check(b []byte, magic string, version uint32) ([]byte, error) {
	if len(magic) != MagicLen {
		panic("packfile: a magic is 8 bytes")
	}
	if len(b) < headerLen+sumLen {
		return nil, fmt.Errorf("%d bytes is too short to be a data pack", len(b))
	}
	if string(b[:MagicLen]) != magic {
		return nil, errors.New("not this kind of data pack (wrong magic)")
	}
	if v := binary.LittleEndian.Uint32(b[MagicLen:headerLen]); v != version {
		return nil, fmt.Errorf("data pack version %d, this netryx reads version %d", v, version)
	}
	end := len(b) - sumLen
	sum := sha256.Sum256(b[:end])
	if !bytes.Equal(sum[:], b[end:]) {
		return nil, errors.New("the checksum does not match: the file is damaged or cut short")
	}
	return b[headerLen:end], nil
}

// HasMagic reports whether the file at path begins with magic. A file that
// cannot be read does not.
func HasMagic(path, magic string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var got [MagicLen]byte
	if _, err := io.ReadFull(f, got[:]); err != nil {
		return false
	}
	return string(got[:]) == magic
}

// Seal frames body as a pack.
func Seal(magic string, version uint32, body []byte) []byte {
	if len(magic) != MagicLen {
		panic("packfile: a magic is 8 bytes")
	}
	out := make([]byte, 0, headerLen+len(body)+sumLen)
	out = append(out, magic...)
	out = binary.LittleEndian.AppendUint32(out, version)
	out = append(out, body...)
	sum := sha256.Sum256(out)
	return append(out, sum[:]...)
}

// WriteFile writes data to path through a temporary file beside it and one
// rename, so a failed write never leaves half a pack where a reader looks.
func WriteFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Chmod(0o644)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(name, path)
	}
	if werr != nil {
		os.Remove(name)
	}
	return werr
}

// Release gives back what Open returned, for a file that was refused: a
// mapping is removed, memory is left to the collector.
func Release(b []byte, mapped bool) {
	if mapped {
		unmapFile(b)
	}
}
