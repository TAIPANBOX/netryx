// Package ramfile keeps small files in RAM (/tmp, gone with a reboot) that
// only this process may read or write: the console's sessions and the last
// hour of minutes. A file is written whole (a private temporary file renamed
// over it) and read back only when it and its directory are this process's
// own and closed to others; anything else is no file, with the reason.
package ramfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// fileOwner is the uid that owns a file, where the system says.
func fileOwner(fi fs.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

// Owner is fileOwner; a test stands in another owner.
var Owner = fileOwner

// private says a file or directory is this process's own and no one else's:
// owned by this uid, and neither group nor others may write it (nor, for a
// file, read it).
func private(fi fs.FileInfo, file bool) error {
	uid, ok := Owner(fi)
	if !ok {
		return errors.New("its owner cannot be told")
	}
	if uid != uint32(os.Geteuid()) {
		return fmt.Errorf("it belongs to uid %d, not to this process", uid)
	}
	mask := fs.FileMode(0o022)
	if file {
		mask = 0o077
	}
	if fi.Mode().Perm()&mask != 0 {
		return fmt.Errorf("its mode %v lets others in", fi.Mode().Perm())
	}
	return nil
}

// Read returns the file's bytes, at most max of them. Anything not exactly as
// Write leaves it (missing, a link, another owner, open to others, too large)
// is no file; why is returned for the log, "" when there was no file at all.
func Read(path string, max int) ([]byte, string) {
	di, err := os.Lstat(filepath.Dir(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ""
	}
	if err != nil || !di.IsDir() {
		return nil, "its directory is not a directory"
	}
	if err := private(di, false); err != nil {
		return nil, "its directory: " + err.Error()
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ""
	}
	if err != nil || !fi.Mode().IsRegular() {
		return nil, "it is not a plain file"
	}
	if err := private(fi, true); err != nil {
		return nil, err.Error()
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err.Error()
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil || len(b) > max {
		return nil, "it is too large or unreadable"
	}
	return b, ""
}

// Write writes b whole: a private temporary file beside the target, renamed
// over it, so a reader never sees half a file. The directory is made 0700 if
// missing and refused if it is not this process's alone.
func Write(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	di, err := os.Lstat(dir)
	if err != nil || !di.IsDir() {
		return errors.New("its directory is not a directory")
	}
	if err := private(di, false); err != nil {
		return fmt.Errorf("its directory: %v", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // gone after the rename; left only by a failure
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Remove deletes the file, but only from this process's own directory: a
// directory that is a link, or not this process's alone, is left as it is,
// so a fail-closed removal never deletes a file elsewhere as root.
func Remove(path string) error {
	di, err := os.Lstat(filepath.Dir(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || !di.IsDir() {
		return errors.New("its directory is not a directory, so nothing was removed")
	}
	if err := private(di, false); err != nil {
		return fmt.Errorf("its directory: %v, so nothing was removed", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
