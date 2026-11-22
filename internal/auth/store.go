package auth

// Sessions that survive a restart of netryx: the table of handles and the
// CSRF secret are kept in a file in RAM (/tmp, gone with a reboot), private
// to root, written whole. A session read back still has to pass rpcd on
// every request, so a file can only keep alive what rpcd keeps alive.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// maxStore bounds what is read back: 256 sessions are a few tens of KB.
const maxStore = 1 << 20

type storedSession struct {
	Handle string `json:"handle"`
	SID    string `json:"sid"`
	User   string `json:"user"`
}

type storedFile struct {
	// Secret is hex: the CSRF tokens of the sessions kept are signed with it.
	Secret   string          `json:"secret"`
	Sessions []storedSession `json:"sessions"`
}

// fileOwner is the uid that owns a file, where the system says.
func fileOwner(fi fs.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

// ownerOf is fileOwner; a test stands in another owner.
var ownerOf = fileOwner

// save is saveStore; a test makes it fail.
var save = saveStore

// private says a file or directory is this process's own and no one else's:
// owned by this uid, and neither group nor others may write it (nor, for a
// file, read it).
func private(fi fs.FileInfo, file bool) error {
	uid, ok := ownerOf(fi)
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

// loadStore reads the kept sessions and secret. Anything not exactly as
// netryx writes it (missing, a link, another owner, open to others, damaged)
// is no session at all; why is returned for the log, "" when there was no file.
func loadStore(path string) ([]byte, []signedIn, string) {
	di, err := os.Lstat(filepath.Dir(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, ""
	}
	if err != nil || !di.IsDir() {
		return nil, nil, "its directory is not a directory"
	}
	if err := private(di, false); err != nil {
		return nil, nil, "its directory: " + err.Error()
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, ""
	}
	if err != nil || !fi.Mode().IsRegular() {
		return nil, nil, "it is not a plain file"
	}
	if err := private(fi, true); err != nil {
		return nil, nil, err.Error()
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err.Error()
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxStore+1))
	if err != nil || len(b) > maxStore {
		return nil, nil, "it is too large or unreadable"
	}
	var sf storedFile
	if err := json.Unmarshal(b, &sf); err != nil {
		return nil, nil, "it is damaged"
	}
	secret, err := hex.DecodeString(sf.Secret)
	if err != nil || len(secret) < 32 {
		return nil, nil, "it holds no secret"
	}
	return secret, sanitize(sf), ""
}

// sanitize keeps the sessions that look like the ones netryx opens, at most
// maxHandles of them, the newest last.
func sanitize(sf storedFile) []signedIn {
	var out []signedIn
	at := map[string]int{}
	for _, s := range sf.Sessions {
		if !validHandle(s.Handle) || s.SID == "" || len(s.SID) > 128 || s.User == "" || len(s.User) > 64 {
			continue
		}
		si := signedIn{handle: s.Handle, sid: s.SID, user: s.User}
		// A handle twice is one session, the later entry's: two would leave
		// one in the order that the map no longer knows.
		if i, seen := at[s.Handle]; seen {
			out = append(out[:i], out[i+1:]...)
			for h, j := range at {
				if j > i {
					at[h] = j - 1
				}
			}
		}
		at[s.Handle] = len(out)
		out = append(out, si)
	}
	if len(out) > maxHandles {
		out = out[len(out)-maxHandles:]
	}
	return out
}

// saveStore writes the table whole: a private temporary file beside the
// target, renamed over it, so a reader never sees half a file.
func saveStore(path string, secret []byte, sessions []signedIn) error {
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
	sf := storedFile{Secret: hex.EncodeToString(secret), Sessions: []storedSession{}}
	for _, s := range sessions {
		sf.Sessions = append(sf.Sessions, storedSession{Handle: s.handle, SID: s.sid, User: s.user})
	}
	b, err := json.Marshal(sf)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".sessions-*")
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

// removeStore deletes the kept table, but only from netryx's own directory:
// a directory that is a link, or not this process's alone, is left as it
// is, so the fail-closed removal never deletes a file elsewhere as root.
func removeStore(path string) error {
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
