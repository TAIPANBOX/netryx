// Package auth puts the console behind the router's own login: LuCI's users
// and passwords, checked by rpcd through uhttpd's JSON-RPC on loopback.
//
// Every page and every /api/ route needs a session, reads included; only the
// login form, its files, the brand icons, POST /api/login and GET
// /api/session answer without one. A change also needs the ACL grant netryx
// installs (netryx/control/write) and a per-session token in a header, which
// a page on another origin cannot read. The scrape endpoints answer on
// loopback without a session and from the house side only with a bearer
// token.
package auth

import (
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"netryx/internal/handoff"
)

const (
	// CookieName holds an opaque handle: 32 random bytes in hex, mapped in
	// netryx's memory to the rpcd session. The rpcd session id never leaves
	// the router, so a cookie the browser sends to another port of the same
	// host (LuCI, other services) carries nothing they could use.
	CookieName = "netryx_sid"
	// CSRFHeader carries hex(HMAC-SHA256(Secret, handle)) on every change.
	CSRFHeader = "X-Netryx-CSRF"
	// Scope is the ACL scope netryx's grants live in
	// (/usr/share/rpcd/acl.d/netryx.json).
	Scope = "netryx"

	// accessTTL is how long a positive answer of rpcd is trusted, so the
	// live view's 2 s polling does not double the load on rpcd.
	accessTTL = 5 * time.Second
	// A source may try 5 logins at once, then one a minute.
	loginBurst = 5
	loginEvery = time.Minute
	maxSources = 1024
	// maxKept bounds the kept answers; maxHandles the signed-in sessions
	// (the oldest is pushed out, and is a login from then on).
	maxKept    = 4096
	maxHandles = 256
	maxLogin   = 4096
	// destroyTimeout is Destroy's own deadline, so a request that ran out of
	// time still ends the session it opened.
	destroyTimeout = 2 * time.Second
)

// rpcTimeout bounds every call to rpcd made for a request; a variable so a
// test can make it short.
var rpcTimeout = 5 * time.Second

// Gate is the login in front of the console. Secret must be random and at
// least 32 bytes (NewSecret); it signs the CSRF tokens. With Store set, the
// sessions and the secret they were issued under are kept in that file and
// read back at the next start, so a restart signs nobody out while rpcd keeps
// their sessions; without it, both live and die with the process.
type Gate struct {
	RPC    RPC
	Now    func() time.Time
	Secret []byte
	// MetricsToken opens /metrics and the Zabbix routes from the house
	// side; empty, they answer on loopback only.
	MetricsToken string
	// Logf logs failed logins and rpcd trouble; nil logs nothing.
	Logf func(format string, a ...any)
	// IsLocal reports a house-side address (serve passes the console's own
	// notion of home, the one the flow table uses). The login rate limit
	// counts the house and link-local IPv6 per address only; nil leaves
	// only link-local counted that way.
	IsLocal func(netip.Addr) bool
	// Zone is the IANA zone the router runs in (the -zone flag); the console
	// shows every time in it. Empty means UTC.
	Zone string
	// Store keeps the sessions in a file across a restart; empty keeps them in memory only.
	Store string
	// Handoff is the directory of the codes LuCI's page mints
	// (internal/handoff); empty, POST /api/login/luci opens nothing.
	Handoff string

	noRPC atomic.Bool
}

func (g *Gate) zone() string {
	if g.Zone == "" {
		return "UTC"
	}
	return g.Zone
}

// SetNoRPC tells the gate that rpcd did not answer at start, so nobody can
// log in: GET from loopback then passes without a session (the console is
// served on loopback only in that case), and every other method answers
// 503 {"error":"no-rpc"}.
func (g *Gate) SetNoRPC(v bool) { g.noRPC.Store(v) }

// NoRPC reports what SetNoRPC set.
func (g *Gate) NoRPC() bool { return g.noRPC.Load() }

// NewSecret returns 32 random bytes for Gate.Secret.
func NewSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: no randomness: " + err.Error())
	}
	return b
}

// CSRF is the token the session behind handle must send with every change.
func (g *Gate) CSRF(handle string) string {
	m := hmac.New(sha256.New, g.Secret)
	m.Write([]byte(handle))
	return hex.EncodeToString(m.Sum(nil))
}

type keptKey struct{ sid, object, function string }

// signedIn is one session netryx holds: the rpcd session behind a handle,
// and the user it signed in as. A lent session is LuCI's own, handed to the
// console by a code: netryx never ends it, so leaving the console never signs
// anyone out of LuCI.
type signedIn struct {
	handle, sid, user string
	lent              bool
}

type gate struct {
	g    *Gate
	next http.Handler

	mu      sync.Mutex
	lim     *limiter
	kept    map[keptKey]time.Time // positive answers, when rpcd gave them
	handles map[string]*list.Element
	order   *list.List // of *signedIn, oldest first
}

// Wrap returns the console behind the gate.
func (g *Gate) Wrap(next http.Handler) http.Handler {
	if len(g.Secret) < 32 {
		panic("auth: Gate.Secret must be at least 32 random bytes")
	}
	s := &gate{
		g: g, next: next, lim: newLimiter(),
		kept: map[keptKey]time.Time{}, handles: map[string]*list.Element{}, order: list.New(),
	}
	if g.Store != "" {
		secret, kept, why := loadStore(g.Store)
		switch {
		case why != "":
			s.logf("sessions from %s are not trusted, so every viewer signs in again: %s", g.Store, why)
		case secret != nil:
			// The tokens of the kept sessions were signed with the secret
			// they were issued under.
			g.Secret = secret
			for i := range kept {
				si := kept[i]
				s.handles[si.handle] = s.order.PushBack(&si)
			}
		}
	}
	return s
}

// persistLocked writes the table of sessions to the store, when there is one,
// in the same critical section that changed it, so no process death between
// the two can leave a signed-out session in the file. The caller holds s.mu
// and logs the error, if any, once it has let go.
func (s *gate) persistLocked() error {
	if s.g.Store == "" {
		return nil
	}
	all := make([]signedIn, 0, s.order.Len())
	for e := s.order.Front(); e != nil; e = e.Next() {
		all = append(all, *e.Value.(*signedIn))
	}
	err := save(s.g.Store, s.g.Secret, all)
	if err != nil {
		// Fail closed: a table that could not be written must not stay
		// behind stale, or a restart would bring back a session that
		// signed out. Without the file a restart signs everyone out.
		if rerr := removeStore(s.g.Store); rerr != nil {
			err = fmt.Errorf("%v; and the old file could not be removed: %v", err, rerr)
		}
	}
	return err
}

func (s *gate) logPersist(err error) {
	if err != nil {
		s.logf("sessions not kept in %s, so a restart signs everyone out: %v", s.g.Store, err)
	}
}

func (s *gate) now() time.Time {
	if s.g.Now != nil {
		return s.g.Now()
	}
	return time.Now()
}

func (s *gate) logf(f string, a ...any) {
	if s.g.Logf != nil {
		s.g.Logf(f, a...)
	}
}

// open reports whether p is the login form, its files or a brand icon. The
// list is exact; a path that is not clean is never open, so no spelling of
// another route can pass for one of these.
func open(p string) bool {
	switch p {
	case "/", "/app.js", "/style.css":
		return true
	}
	if !strings.HasPrefix(p, "/icons/") || len(p) == len("/icons/") {
		return false
	}
	return path.Clean(p) == p
}

// scrape reports whether p is one of the scrape endpoints.
func scrape(p string) bool {
	return p == "/metrics" || p == "/api/snapshot" || p == "/api/zabbix/discovery"
}

func fromLoopback(r *http.Request) bool {
	a, ok := remoteAddr(r)
	return ok && a.IsLoopback()
}

func (s *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if scrape(p) {
		if !s.scrapeAllowed(r) {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		s.next.ServeHTTP(w, r)
		return
	}
	// No rpcd at start: nobody can log in, the console listens on loopback
	// only, reads from there need no session and nothing may change.
	if s.g.NoRPC() {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusServiceUnavailable, "no-rpc")
			return
		}
		if fromLoopback(r) {
			if p == "/api/session" {
				writeJSON(w, http.StatusOK, map[string]any{"user": "", "csrf": "", "canWrite": false, "noRPC": true, "zone": s.g.zone()})
				return
			}
			s.next.ServeHTTP(w, r)
			return
		}
	}
	switch {
	case p == "/api/login":
		s.login(w, r)
		return
	case p == "/api/login/luci":
		s.luci(w, r)
		return
	case p == "/api/session" && r.Method == http.MethodGet:
		s.session(w, r)
		return
	case open(p):
		s.next.ServeHTTP(w, r)
		return
	}
	si, ok := s.viewer(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		if !s.csrfOK(r, si.handle) {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		// Logging out is the session's own business: a viewer may.
		if p == "/api/logout" {
			s.logout(w, r, si)
			return
		}
		if !s.allowed(w, r, si, "control", "write", http.StatusForbidden, "forbidden") {
			return
		}
	}
	s.next.ServeHTTP(w, r)
}

// validHandle reports whether h has the shape of a handle: 64 lower-case
// hex digits.
func validHandle(h string) bool {
	if len(h) != 64 {
		return false
	}
	for i := 0; i < len(h); i++ {
		if c := h[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// lookup returns the session behind the request's cookie.
func (s *gate) lookup(r *http.Request) (signedIn, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil || !validHandle(c.Value) {
		return signedIn{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.handles[c.Value]
	if !ok {
		return signedIn{}, false
	}
	return *e.Value.(*signedIn), true
}

// viewer returns the request's session if it may view, and otherwise
// answers 401 (no session, unknown, expired, no view grant) or 503 (rpcd
// did not answer).
func (s *gate) viewer(w http.ResponseWriter, r *http.Request) (signedIn, bool) {
	si, ok := s.lookup(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "login")
		return signedIn{}, false
	}
	if !s.allowed(w, r, si, "view", "read", http.StatusUnauthorized, "login") {
		return signedIn{}, false
	}
	return si, true
}

// allowed asks rpcd (or the answer kept from the last 5 s) whether the
// session has the grant, and answers the request itself when it does not:
// a session rpcd no longer knows is a login (and its handle is dropped), a
// missing grant is denyCode, and rpcd not answering is 503.
func (s *gate) allowed(w http.ResponseWriter, r *http.Request, si signedIn, object, function string, denyCode int, deny string) bool {
	ok, err := s.access(r.Context(), si.sid, object, function)
	switch {
	case errors.Is(err, ErrSessionGone):
		s.drop(si.handle)
		writeErr(w, http.StatusUnauthorized, "login")
		return false
	case err != nil:
		s.logf("rpcd did not answer whether a session may %s %s: %v", function, object, err)
		writeErr(w, http.StatusServiceUnavailable, "rpc")
		return false
	case !ok:
		writeErr(w, denyCode, deny)
		return false
	}
	return true
}

// access is RPC.Access with a positive answer kept for less than accessTTL.
// A refusal or an error is never kept.
func (s *gate) access(ctx context.Context, sid, object, function string) (bool, error) {
	k := keptKey{sid, object, function}
	now := s.now()
	s.mu.Lock()
	if at, ok := s.kept[k]; ok {
		if d := now.Sub(at); d >= 0 && d < accessTTL {
			s.mu.Unlock()
			return true, nil
		}
		delete(s.kept, k)
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	ok, err := s.g.RPC.Access(ctx, sid, Scope, object, function)
	if err != nil || !ok {
		if errors.Is(err, ErrSessionGone) {
			s.forget(sid)
		}
		return false, err
	}
	s.mu.Lock()
	if len(s.kept) >= maxKept {
		for kk, at := range s.kept {
			if d := now.Sub(at); d < 0 || d >= accessTTL {
				delete(s.kept, kk)
			}
		}
		if len(s.kept) >= maxKept {
			clear(s.kept)
		}
	}
	s.kept[k] = now
	s.mu.Unlock()
	return true, nil
}

// forget drops every kept answer of sid.
func (s *gate) forget(sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.kept {
		if k.sid == sid {
			delete(s.kept, k)
		}
	}
}

// drop removes a handle; the session behind it is a login from then on.
func (s *gate) drop(handle string) (signedIn, bool) {
	s.mu.Lock()
	e, ok := s.handles[handle]
	if !ok {
		s.mu.Unlock()
		return signedIn{}, false
	}
	delete(s.handles, handle)
	s.order.Remove(e)
	err := s.persistLocked()
	s.mu.Unlock()
	s.logPersist(err)
	return *e.Value.(*signedIn), true
}

// add maps a new handle to the session; a full table pushes out the
// oldest, whose session is returned to be ended.
func (s *gate) add(si *signedIn) (evicted *signedIn) {
	s.mu.Lock()
	if s.order.Len() >= maxHandles {
		old := s.order.Front()
		evicted = old.Value.(*signedIn)
		s.order.Remove(old)
		delete(s.handles, evicted.handle)
	}
	s.handles[si.handle] = s.order.PushBack(si)
	err := s.persistLocked()
	s.mu.Unlock()
	s.logPersist(err)
	return evicted
}

// destroy ends an rpcd session with a deadline of its own, so it runs even
// when the request's time is up.
func (s *gate) destroy(sid string) error {
	ctx, cancel := context.WithTimeout(context.Background(), destroyTimeout)
	defer cancel()
	s.forget(sid)
	return s.g.RPC.Destroy(ctx, sid)
}

func newHandle() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func (s *gate) csrfOK(r *http.Request, handle string) bool {
	got := r.Header.Get(CSRFHeader)
	return got != "" && hmac.Equal([]byte(got), []byte(s.g.CSRF(handle)))
}

// scrapeAllowed: loopback, or the configured bearer token, compared in
// constant time. An empty token opens nothing.
func (s *gate) scrapeAllowed(r *http.Request) bool {
	if fromLoopback(r) {
		return true
	}
	if s.g.MetricsToken == "" {
		return false
	}
	scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	want := sha256.Sum256([]byte(s.g.MetricsToken))
	got := sha256.Sum256([]byte(tok))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

func remoteAddr(r *http.Request) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}

// source names the login's sender for the log.
func source(r *http.Request) string {
	if a, ok := remoteAddr(r); ok {
		return a.WithZone("").String()
	}
	return r.RemoteAddr
}

// limits are the buckets a login spends from. An IPv4 address, a
// link-local IPv6 one and a house-side IPv6 one (IsLocal) spend only from
// their own: five at once, then one a minute. Any other IPv6 address also
// spends from its /64's, asked first so a refused /64 adds no entry for a
// made-up address: a host out there may take any address of its /64 and
// would get a fresh five with each.
//
// The house gets no shared bucket on purpose. A device in the house can
// already reach LuCI's own login on /ubus, which has no limit at all, so
// netryx cannot raise that bar; a bucket the house shared would only let
// one device lock its neighbours out of the console. Outside IPv6 does not
// reach the console in the default firewall, but it is limited all the
// same.
func (s *gate) limits(r *http.Request) []limit {
	a, ok := remoteAddr(r)
	if !ok {
		return []limit{{r.RemoteAddr, perAddress}}
	}
	own := limit{a.String(), perAddress}
	if !a.Is6() || a.IsLinkLocalUnicast() {
		return []limit{own}
	}
	a = a.WithZone("")
	if s.g.IsLocal != nil && s.g.IsLocal(a) {
		return []limit{own}
	}
	return []limit{{netip.PrefixFrom(a, 64).Masked().String(), perNet}, own}
}

func (s *gate) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	src := source(r)
	s.mu.Lock()
	wait, ok := s.lim.take(s.now(), s.limits(r)...)
	s.mu.Unlock()
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		writeErr(w, http.StatusTooManyRequests, "rate")
		return
	}
	// JSON only: a page elsewhere can post a plain form without asking,
	// but a JSON body makes the browser ask first, and nobody answers yes.
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "json")
		return
	}
	var in struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxLogin)).Decode(&in); err != nil ||
		in.User == "" || len(in.User) > 64 || !utf8.ValidString(in.User) || len(in.Password) > 1024 {
		writeErr(w, http.StatusBadRequest, "bad login")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), rpcTimeout)
	defer cancel()
	sid, err := s.g.RPC.Login(ctx, in.User, in.Password)
	switch {
	case errors.Is(err, ErrBadLogin):
		s.logf("login failed for %q from %s", in.User, src)
		writeErr(w, http.StatusUnauthorized, "login")
		return
	case err != nil:
		s.logf("login for %q from %s impossible: %v", in.User, src, err)
		writeErr(w, http.StatusServiceUnavailable, "rpc")
		return
	}
	// A LuCI user without the netryx view gets no session here at all.
	// -32002 here is uhttpd refusing the session's own access call (an ACL
	// without ubus session.access): a refusal, not rpcd down.
	ok, err = s.access(ctx, sid, "view", "read")
	if err != nil || !ok {
		s.destroy(sid)
		if err != nil && !errors.Is(err, ErrSessionGone) {
			s.logf("login for %q from %s: rpcd did not answer the view grant: %v", in.User, src, err)
			writeErr(w, http.StatusServiceUnavailable, "rpc")
			return
		}
		s.logf("login for %q from %s refused: no netryx grant in its ACL", in.User, src)
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	canWrite, _ := s.access(ctx, sid, "control", "write")
	si := &signedIn{handle: newHandle(), sid: sid, user: in.User}
	s.open(w, r, si)
	s.logf("login for %q from %s", in.User, src)
	writeJSON(w, http.StatusOK, map[string]any{"csrf": s.g.CSRF(si.handle), "user": in.User, "canWrite": canWrite, "zone": s.g.zone()})
}

// open maps the session to a new handle and sets the cookie; a full table
// pushes out the oldest, whose rpcd session is ended unless it was lent.
func (s *gate) open(w http.ResponseWriter, r *http.Request, si *signedIn) {
	if old := s.add(si); old != nil && !old.lent {
		if err := s.destroy(old.sid); err != nil {
			s.logf("the oldest console session was pushed out; rpcd did not end it: %v", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: si.handle, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
	})
}

// luci redeems a code LuCI's page minted for a signed-in user: once, within
// 30 s, and only for an rpcd session that is alive and may view. The code
// comes in a JSON body, is limited like a password and is never logged.
func (s *gate) luci(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	src := source(r)
	s.mu.Lock()
	wait, ok := s.lim.take(s.now(), s.limits(r)...)
	s.mu.Unlock()
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		writeErr(w, http.StatusTooManyRequests, "rate")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "json")
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad login")
		return
	}
	if s.g.Handoff == "" {
		writeErr(w, http.StatusUnauthorized, "login")
		return
	}
	c, err := handoff.Redeem(s.g.Handoff, in.Code, s.now())
	if err != nil {
		s.logf("a LuCI handoff from %s was refused: the code is unknown, used or expired", src)
		writeErr(w, http.StatusUnauthorized, "login")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), rpcTimeout)
	defer cancel()
	// The session is LuCI's: whatever the answer, it is never ended here.
	ok, err = s.access(ctx, c.SID, "view", "read")
	switch {
	case errors.Is(err, ErrSessionGone):
		s.logf("a LuCI handoff for %q from %s was refused: rpcd no longer knows its session", c.User, src)
		writeErr(w, http.StatusUnauthorized, "login")
		return
	case err != nil:
		s.logf("a LuCI handoff for %q from %s: rpcd did not answer the view grant: %v", c.User, src, err)
		writeErr(w, http.StatusServiceUnavailable, "rpc")
		return
	case !ok:
		s.logf("a LuCI handoff for %q from %s was refused: no netryx grant in its ACL", c.User, src)
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	canWrite, _ := s.access(ctx, c.SID, "control", "write")
	si := &signedIn{handle: newHandle(), sid: c.SID, user: c.User, lent: true}
	s.open(w, r, si)
	s.logf("login for %q from %s through LuCI", c.User, src)
	writeJSON(w, http.StatusOK, map[string]any{"csrf": s.g.CSRF(si.handle), "user": c.User, "canWrite": canWrite, "zone": s.g.zone()})
}

func (s *gate) session(w http.ResponseWriter, r *http.Request) {
	si, ok := s.viewer(w, r)
	if !ok {
		return
	}
	canWrite, _ := s.access(r.Context(), si.sid, "control", "write")
	writeJSON(w, http.StatusOK, map[string]any{"user": si.user, "csrf": s.g.CSRF(si.handle), "canWrite": canWrite, "zone": s.g.zone()})
}

// logout drops the handle, so the session is refused at once whatever rpcd
// says, and ends the rpcd session.
func (s *gate) logout(w http.ResponseWriter, r *http.Request, si signedIn) {
	s.drop(si.handle)
	if si.lent {
		// LuCI's own session: the console lets go of it, LuCI keeps it.
		s.forget(si.sid)
	} else if err := s.destroy(si.sid); err != nil {
		s.logf("logout: rpcd did not end the session (%v); its handle is gone, so netryx refuses it", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// rate is a token bucket's shape: burst tokens at once, one more every
// `every`.
type rate struct {
	burst float64
	every time.Duration
}

var (
	// perAddress: 5 logins at once, then one a minute.
	perAddress = rate{loginBurst, loginEvery}
	// perNet: an IPv6 /64 outside the house, 10 logins at once, then one
	// every ten seconds (see limits for why the house has none).
	perNet = rate{10, 10 * time.Second}
)

type limit struct {
	key string
	r   rate
}

// limiter keeps a token bucket per key. Its table holds at most maxSources
// keys of both kinds; a full table first lets go of a key whose bucket has
// refilled (it holds nothing a fresh one would not), and only when none has
// of the one asked longest ago.
type limiter struct {
	m   map[string]*list.Element
	lru *list.List // of *bucket, most recent first
}

type bucket struct {
	key    string
	r      rate
	tokens float64
	at     time.Time
}

func newLimiter() *limiter { return &limiter{m: map[string]*list.Element{}, lru: list.New()} }

func (l *limiter) len() int { return len(l.m) }

func (b *bucket) refill(now time.Time) {
	if now.After(b.at) {
		b.tokens += float64(now.Sub(b.at)) / float64(b.r.every)
		if b.tokens > b.r.burst {
			b.tokens = b.r.burst
		}
		b.at = now
	}
}

// idle reports whether the bucket has refilled, so dropping it loses no
// count.
func (b *bucket) idle(now time.Time) bool {
	b.refill(now)
	return b.tokens >= b.r.burst
}

// get returns key's bucket, making room for a new one; keep is never let go.
func (l *limiter) get(lim limit, now time.Time, keep *bucket) *bucket {
	if e, ok := l.m[lim.key]; ok {
		l.lru.MoveToFront(e)
		return e.Value.(*bucket)
	}
	if len(l.m) >= maxSources {
		var out *list.Element
		for e := l.lru.Back(); e != nil; e = e.Prev() {
			if b := e.Value.(*bucket); b != keep && b.idle(now) {
				out = e
				break
			}
		}
		if out == nil {
			for out = l.lru.Back(); out != nil && out.Value.(*bucket) == keep; out = out.Prev() {
			}
		}
		delete(l.m, out.Value.(*bucket).key)
		l.lru.Remove(out)
	}
	b := &bucket{key: lim.key, r: lim.r, tokens: lim.r.burst, at: now}
	l.m[lim.key] = l.lru.PushFront(b)
	return b
}

// take spends one token from every bucket of lims, or from none: a bucket
// that is empty refuses, and says how long until its next token.
func (l *limiter) take(now time.Time, lims ...limit) (time.Duration, bool) {
	bs := make([]*bucket, 0, len(lims))
	var keep *bucket
	for _, lim := range lims {
		b := l.get(lim, now, keep)
		b.refill(now)
		if b.tokens < 1 {
			return time.Duration((1 - b.tokens) * float64(b.r.every)), false
		}
		bs = append(bs, b)
		keep = b
	}
	for _, b := range bs {
		b.tokens--
	}
	return 0, true
}
