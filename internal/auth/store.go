package auth

// Sessions that survive a restart of netryx: the table of handles and the
// CSRF secret are kept in a file in RAM (/tmp, gone with a reboot), private
// to root, written whole. A session read back still has to pass rpcd on
// every request, so a file can only keep alive what rpcd keeps alive.

import (
	"encoding/hex"
	"encoding/json"
	"time"

	"netryx/internal/ramfile"
)

// maxStore bounds what is read back: 256 sessions are a few tens of KB.
const maxStore = 1 << 20

type storedSession struct {
	Handle string `json:"handle"`
	SID    string `json:"sid"`
	User   string `json:"user"`
	// Lent marks LuCI's own session, handed over by a code: never ended by netryx.
	Lent bool `json:"lent,omitempty"`
	// Active is when a person last used the page, unix seconds.
	Active int64 `json:"active,omitempty"`
}

type storedFile struct {
	// Secret is hex: the CSRF tokens of the sessions kept are signed with it.
	Secret   string          `json:"secret"`
	Sessions []storedSession `json:"sessions"`
}

// save is saveStore; a test makes it fail.
var save = saveStore

// loadStore reads the kept sessions and secret. Anything not exactly as
// netryx writes it (missing, a link, another owner, open to others, damaged)
// is no session at all; why is returned for the log, "" when there was no file.
func loadStore(path string) ([]byte, []signedIn, string) {
	b, why := ramfile.Read(path, maxStore)
	if b == nil {
		return nil, nil, why
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
		si := signedIn{handle: s.Handle, sid: s.SID, user: s.User, lent: s.Lent}
		if s.Active > 0 {
			si.active = time.Unix(s.Active, 0)
		}
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
	sf := storedFile{Secret: hex.EncodeToString(secret), Sessions: []storedSession{}}
	for _, s := range sessions {
		sf.Sessions = append(sf.Sessions, storedSession{Handle: s.handle, SID: s.sid, User: s.user, Lent: s.lent, Active: unixOrZero(s.active)})
	}
	b, err := json.Marshal(sf)
	if err != nil {
		return err
	}
	return ramfile.Write(path, b)
}

// removeStore deletes the kept table, but only from netryx's own directory:
// a directory that is a link, or not this process's alone, is left as it
// is, so the fail-closed removal never deletes a file elsewhere as root.
func removeStore(path string) error { return ramfile.Remove(path) }

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
