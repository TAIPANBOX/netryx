package auth

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"netryx/internal/ramfile"
)

// storeRig is newRig with the sessions kept in a file, and a restart: a new
// gate over the same rpcd and file, with a fresh secret, as a new process
// would start.
func storeRig(t *testing.T) (*rig, string) {
	t.Helper()
	r := newRig(t)
	path := filepath.Join(t.TempDir(), "netryx", "sessions.json")
	r.gate.Store = path
	r.h = r.gate.Wrap(nextOf(r))
	return r, path
}

func nextOf(r *rig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.mu.Lock()
		r.reached = append(r.reached, q.Method+" "+q.URL.Path)
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
}

func (r *rig) restart() {
	old := r.gate
	r.gate = &Gate{
		RPC: old.RPC, Now: old.Now, Secret: NewSecret(), MetricsToken: old.MetricsToken,
		Logf: old.Logf, IsLocal: old.IsLocal, Zone: old.Zone, Store: old.Store,
	}
	r.h = r.gate.Wrap(nextOf(r))
}

func (r *rig) logged(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// A session survives a restart of netryx while its rpcd session lives: the
// page stays signed in and its token still writes.
func TestASessionSurvivesARestart(t *testing.T) {
	r, _ := storeRig(t)
	sid, csrf := r.login("root", adminPW, lan)
	r.restart()
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusOK {
		t.Fatalf("a read after the restart: %d %s", rec.Code, rec.Body)
	}
	if rec := r.do(http.MethodPost, "/api/rules", lan, `{}`, withCookie(sid), withCSRF(csrf)); rec.Code != http.StatusOK {
		t.Fatalf("a write with the token from before the restart: %d %s", rec.Code, rec.Body)
	}
}

// The file is private and written whole: 0600 in a 0700 directory, and no
// temporary file is left beside it.
func TestTheSessionsFileIsPrivateAndWrittenWhole(t *testing.T) {
	r, path := storeRig(t)
	r.login("root", adminPW, lan)
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("file %v %v, want a regular 0600 file", fi, err)
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("directory mode %v, want 0700", di.Mode().Perm())
	}
	ents, _ := os.ReadDir(filepath.Dir(path))
	if len(ents) != 1 {
		t.Errorf("directory holds %d entries, want the file alone", len(ents))
	}
}

// A file anyone else could read or write, or that another user owns, is not
// trusted: it is ignored and the log says why, and nobody is signed in by it.
func TestASessionsFileOthersCouldTouchIsNotLoaded(t *testing.T) {
	for _, c := range []struct {
		what  string
		spoil func(path string)
	}{
		{"readable by others", func(p string) { os.Chmod(p, 0o644) }},
		{"writable by the group", func(p string) { os.Chmod(p, 0o620) }},
		{"another owner", func(string) { ramfile.Owner = func(fs.FileInfo) (uint32, bool) { return 4242, true } }},
		{"a symbolic link", func(p string) {
			b, _ := os.ReadFile(p)
			real := p + ".real"
			os.WriteFile(real, b, 0o600)
			os.Remove(p)
			os.Symlink(real, p)
		}},
		{"damaged", func(p string) { os.WriteFile(p, []byte("{not json"), 0o600) }},
		// A pipe would hold the start for ever if it were opened and read.
		{"a named pipe", func(p string) {
			os.Remove(p)
			syscall.Mkfifo(p, 0o600)
		}},
		{"a directory others can write", func(p string) { os.Chmod(filepath.Dir(p), 0o777) }},
	} {
		t.Run(c.what, func(t *testing.T) {
			defer func(o func(fs.FileInfo) (uint32, bool)) { ramfile.Owner = o }(ramfile.Owner)
			r, path := storeRig(t)
			sid, _ := r.login("root", adminPW, lan)
			c.spoil(path)
			r.restart()
			if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s: a read after the restart: %d, want 401", c.what, rec.Code)
			}
			if !r.logged("sessions") {
				t.Errorf("%s: nothing logged: %v", c.what, r.logs)
			}
		})
	}
}

// A session that ends leaves the file: a logout, and one rpcd has let go.
func TestEndedSessionsLeaveTheFile(t *testing.T) {
	r, path := storeRig(t)
	gone, _ := r.login("root", adminPW, lan)
	out, csrf := r.login("viewer", viewerPW, "10.0.0.2:1")
	if rec := r.do(http.MethodPost, "/api/logout", lan, "", withCookie(out), withCSRF(csrf)); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), out) {
		t.Error("a logged-out handle is still in the file")
	}
	r.clock.Add(301 * time.Second) // rpcd's session times out
	r.do(http.MethodGet, "/api/overview", lan, "", withCookie(gone))
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), gone) {
		t.Error("a handle whose rpcd session is gone is still in the file")
	}
	r.restart()
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(out)); rec.Code != http.StatusUnauthorized {
		t.Errorf("a logged-out handle after a restart: %d", rec.Code)
	}
}

// What is loaded is held to what a session looks like: a malformed handle or
// an empty session is skipped, and the table stays bounded.
func TestOnlyWellFormedSessionsAreLoaded(t *testing.T) {
	ok := strings.Repeat("ab", 32)
	got := sanitize(storedFile{Secret: strings.Repeat("00", 32), Sessions: []storedSession{
		{Handle: ok, SID: "0123456789abcdef0123456789abcdef", User: "root"},
		{Handle: "../../x", SID: "s", User: "root"},
		{Handle: strings.Repeat("cd", 32), SID: "", User: "root"},
		{Handle: strings.Repeat("ef", 32), SID: "sid", User: strings.Repeat("u", 65)},
	}})
	if len(got) != 1 || got[0].handle != ok {
		t.Errorf("loaded %+v, want the one well-formed session", got)
	}
	// A handle twice is one session, the later entry's.
	got = sanitize(storedFile{Sessions: []storedSession{
		{Handle: ok, SID: "first", User: "root"},
		{Handle: ok, SID: "second", User: "root"},
	}})
	if len(got) != 1 || got[0].sid != "second" {
		t.Errorf("a handle twice loaded %+v, want the one later entry", got)
	}
}

// A table that cannot be written is never left behind stale: a logout that
// could not be kept removes the file, so a restart brings nobody back,
// least of all the session that signed out.
func TestAFailedWriteLeavesNoStaleSessions(t *testing.T) {
	r, path := storeRig(t)
	sid, csrf := r.login("viewer", viewerPW, lan)
	r.ubus.destroy = false // rpcd refuses to end it: only netryx's table stands between
	save = func(string, []byte, []signedIn) error { return os.ErrPermission }
	defer func() { save = saveStore }()
	if rec := r.do(http.MethodPost, "/api/logout", lan, "", withCookie(sid), withCSRF(csrf)); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Fatal("the file with the signed-out session was left behind")
	}
	save = saveStore
	r.restart()
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusUnauthorized {
		t.Errorf("the signed-out session came back after a restart: %d", rec.Code)
	}
}

// The fail-closed removal touches only netryx's own directory: when the
// directory is a link elsewhere, nothing there is deleted.
func TestAFailedWriteRemovesNothingOutsideItsOwnDirectory(t *testing.T) {
	r, path := storeRig(t)
	r.login("root", adminPW, lan)
	elsewhere := filepath.Join(t.TempDir(), "other")
	os.MkdirAll(elsewhere, 0o700)
	victim := filepath.Join(elsewhere, "sessions.json")
	os.WriteFile(victim, []byte("someone else's"), 0o600)
	dir := filepath.Dir(path)
	os.RemoveAll(dir)
	os.Symlink(elsewhere, dir)
	save = func(string, []byte, []signedIn) error { return os.ErrPermission }
	defer func() { save = saveStore }()
	sid, csrf := r.login("viewer", viewerPW, "10.0.0.3:1")
	r.do(http.MethodPost, "/api/logout", lan, "", withCookie(sid), withCSRF(csrf))
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("a file behind a linked directory was removed: %v", err)
	}
}

// Nor in a directory others can write: there nothing is removed, and what is
// left is not loaded either, because the directory is not netryx's alone.
func TestAFailedWriteRemovesNothingInADirectoryOthersCanWrite(t *testing.T) {
	r, path := storeRig(t)
	sid, csrf := r.login("viewer", viewerPW, lan)
	os.Chmod(filepath.Dir(path), 0o777)
	save = func(string, []byte, []signedIn) error { return os.ErrPermission }
	defer func() { save = saveStore }()
	r.do(http.MethodPost, "/api/logout", lan, "", withCookie(sid), withCSRF(csrf))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a file in a directory others can write was removed: %v", err)
	}
	save = saveStore
	r.restart()
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusUnauthorized {
		t.Errorf("the file left there was loaded: %d", rec.Code)
	}
}

// The console asks rpcd for LuCI's session time, so a viewer who steps away
// for longer than rpcd's default five minutes is still signed in.
func TestASessionLastsTheSessionTimeAsked(t *testing.T) {
	r := newRig(t)
	r.gate.RPC = NewRPC(r.gate.RPC.(*rpcClient).url, r.gate.RPC.(*rpcClient).c, SessionTime(time.Hour))
	r.h = r.gate.Wrap(nextOf(r))
	sid, _ := r.login("root", adminPW, lan)
	r.clock.Add(50 * time.Minute)
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusOK {
		t.Fatalf("after 50 quiet minutes of an hour's session: %d", rec.Code)
	}
	r.clock.Add(61 * time.Minute)
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusUnauthorized {
		t.Errorf("after more than an hour without a request: %d, want 401", rec.Code)
	}
}
