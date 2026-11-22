package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// fakeUbus answers POST /ubus the way uhttpd-mod-ubus and rpcd answered in
// the lab VM (docs/stage2-measured.md, section 2): a bad password is
// result [6] with HTTP 200, a missing user [2], an unknown or expired
// session error -32002, and `session access` result [0, {"access": bool}].
type fakeUbus struct {
	mu  sync.Mutex
	now func() time.Time
	// users maps a user to its password, grants a user to the
	// "object/function" pairs its ACL gives in the scope netryx.
	users    map[string]string
	grants   map[string]map[string]bool
	sessions map[string]*fakeSession
	timeout  time.Duration
	// destroy is whether a session may end itself (the ubus grant
	// session.destroy); rpcd denies it with -32002 otherwise.
	destroy bool
	// calls counts the methods asked; logins records every login's
	// user and password as sent.
	calls  map[string]int
	logins [][2]string
	// last is the session id the last good login issued; delay slows a
	// method down; noUbusAccess names users whose sessions lack the ubus
	// grant session.access, so uhttpd answers -32002 to their access calls.
	last         string
	delay        map[string]time.Duration
	noUbusAccess map[string]bool
}

type fakeSession struct {
	user    string
	grants  map[string]bool
	expires time.Time
}

func newFakeUbus(now func() time.Time) *fakeUbus {
	return &fakeUbus{
		now:          now,
		users:        map[string]string{},
		grants:       map[string]map[string]bool{},
		sessions:     map[string]*fakeSession{},
		timeout:      300 * time.Second,
		destroy:      true,
		calls:        map[string]int{},
		delay:        map[string]time.Duration{},
		noUbusAccess: map[string]bool{},
	}
}

func (f *fakeUbus) lastSID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

func (f *fakeUbus) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

// live returns the session sid names, if it exists and has not expired.
// The null session always exists, with the grants of the group
// "unauthenticated" (measured: `session access` with it answers
// result [0, {"access": false}] for the scope netryx, not -32002).
func (f *fakeUbus) live(sid string) *fakeSession {
	if sid == nullSID {
		return &fakeSession{grants: f.grants["unauthenticated"], expires: f.now().Add(time.Hour)}
	}
	s := f.sessions[sid]
	if s == nil || !f.now().Before(s.expires) {
		return nil
	}
	return s
}

// revoke drops one grant of a live session, as an ACL change and an rpcd
// reload would.
func (f *fakeUbus) revoke(sid, grant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.sessions[sid]; s != nil {
		delete(s.grants, grant)
	}
}

func (f *fakeUbus) grant(sid, grant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.sessions[sid]; s != nil {
		s.grants[grant] = true
	}
}

func (f *fakeUbus) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     any               `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil || req.Method != "call" || len(req.Params) != 4 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var sid, obj, method string
	json.Unmarshal(req.Params[0], &sid)
	json.Unmarshal(req.Params[1], &obj)
	json.Unmarshal(req.Params[2], &method)
	var args map[string]any
	json.Unmarshal(req.Params[3], &args)

	f.mu.Lock()
	d := f.delay[method]
	f.mu.Unlock()
	time.Sleep(d)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[method]++
	answer := func(v map[string]any) {
		v["jsonrpc"] = "2.0"
		v["id"] = req.ID
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	denied := func() {
		answer(map[string]any{"error": map[string]any{"code": -32002, "message": "Access denied"}})
	}
	if obj != "session" {
		denied()
		return
	}
	switch method {
	case "login":
		u, uok := args["username"].(string)
		p, _ := args["password"].(string)
		f.logins = append(f.logins, [2]string{u, p})
		if !uok {
			answer(map[string]any{"result": []any{2}})
			return
		}
		want, known := f.users[u]
		if !known || want != p {
			answer(map[string]any{"result": []any{6}})
			return
		}
		b := make([]byte, 16)
		rand.Read(b)
		id := hex.EncodeToString(b)
		g := map[string]bool{}
		for k, v := range f.grants[u] {
			g[k] = v
		}
		f.sessions[id] = &fakeSession{user: u, grants: g, expires: f.now().Add(f.timeout)}
		f.last = id
		answer(map[string]any{"result": []any{0, map[string]any{
			"ubus_rpc_session": id, "timeout": int(f.timeout / time.Second),
			"expires": int(f.timeout/time.Second) - 1, "acls": map[string]any{},
			"data": map[string]any{"username": u},
		}}})
	case "access":
		s := f.live(sid)
		if s == nil || f.noUbusAccess[s.user] {
			denied()
			return
		}
		s.expires = f.now().Add(f.timeout) // rpcd touches a session on use
		scope, _ := args["scope"].(string)
		o, _ := args["object"].(string)
		fn, _ := args["function"].(string)
		answer(map[string]any{"result": []any{0, map[string]any{"access": scope == "netryx" && s.grants[o+"/"+fn]}}})
	case "destroy":
		if f.live(sid) == nil || !f.destroy {
			denied()
			return
		}
		delete(f.sessions, sid)
		answer(map[string]any{"result": []any{0}})
	default:
		denied()
	}
}
