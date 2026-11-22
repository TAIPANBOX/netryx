package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const (
	lan      = "192.168.1.5:50000"
	loopback = "127.0.0.1:50000"
	token    = "scrape-token-for-tests"
	adminPW  = "admin-password-for-tests"
	viewerPW = "viewer-password-for-tests"
)

type rig struct {
	t       *testing.T
	clock   *clock
	ubus    *fakeUbus
	gate    *Gate
	h       http.Handler
	reached []string
	logs    []string
	mu      sync.Mutex
}

// house is the rig's home prefix, as serve's IsLocal would report it.
var house = netip.MustParsePrefix("2001:db8:50::/64")

// newRig puts a gate in front of a handler that records what reached it,
// with rpcd stood in by fakeUbus over real HTTP: root may view and
// control, viewer may only view, nobody has no netryx grant at all. The
// house is 2001:db8:50::/64 and the private IPv4 ranges.
func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, clock: &clock{t: time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)}}
	r.ubus = newFakeUbus(r.clock.Now)
	r.ubus.users = map[string]string{"root": adminPW, "viewer": viewerPW, "nobody": "nobody-pw"}
	r.ubus.grants = map[string]map[string]bool{
		"root":   {"view/read": true, "control/write": true},
		"viewer": {"view/read": true},
	}
	srv := httptest.NewServer(r.ubus)
	t.Cleanup(srv.Close)
	r.gate = &Gate{
		RPC:          NewRPC(srv.URL+"/ubus", srv.Client()),
		Now:          r.clock.Now,
		Secret:       []byte("0123456789abcdef0123456789abcdef"),
		MetricsToken: token,
		IsLocal:      func(a netip.Addr) bool { return house.Contains(a) || a.Is4() && a.IsPrivate() },
		Logf: func(f string, a ...any) {
			r.mu.Lock()
			r.logs = append(r.logs, fmt.Sprintf(f, a...))
			r.mu.Unlock()
		},
	}
	r.h = r.gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.mu.Lock()
		r.reached = append(r.reached, q.Method+" "+q.URL.Path)
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("next"))
	}))
	return r
}

func (r *rig) didReach() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.reached)
	r.reached = nil
	return n > 0
}

type opt func(*http.Request)

func withCookie(sid string) opt {
	return func(q *http.Request) { q.AddCookie(&http.Cookie{Name: CookieName, Value: sid}) }
}
func withCSRF(v string) opt { return func(q *http.Request) { q.Header.Set(CSRFHeader, v) } }
func withHeader(k, v string) opt {
	return func(q *http.Request) { q.Header.Set(k, v) }
}

func (r *rig) do(method, path, remote, body string, opts ...opt) *httptest.ResponseRecorder {
	q := httptest.NewRequest(method, path, strings.NewReader(body))
	q.RemoteAddr = remote
	if body != "" {
		q.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(q)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, q)
	return rec
}

func loginBody(user, pass string) string {
	b, _ := json.Marshal(map[string]string{"user": user, "password": pass})
	return string(b)
}

// login signs in from remote and returns the session id and the CSRF token.
func (r *rig) login(user, pass, remote string) (string, string) {
	r.t.Helper()
	rec := r.do(http.MethodPost, "/api/login", remote, loginBody(user, pass))
	if rec.Code != http.StatusOK {
		r.t.Fatalf("login %s: %d %s", user, rec.Code, rec.Body)
	}
	var sid string
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			sid = c.Value
		}
	}
	var ans struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(rec.Body.Bytes(), &ans)
	if sid == "" || ans.CSRF == "" {
		r.t.Fatalf("login %s gave no cookie or no token: %q %s", user, sid, rec.Body)
	}
	return sid, ans.CSRF
}

func wantError(t *testing.T, rec *httptest.ResponseRecorder, code int, msg, what string) {
	t.Helper()
	if rec.Code != code {
		t.Errorf("%s: got %d %s, want %d", what, rec.Code, rec.Body, code)
		return
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error != msg {
		t.Errorf("%s: body %s, want {\"error\":%q}", what, rec.Body, msg)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("%s: Content-Type %q", what, ct)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("%s: an answer about the session must not be cached", what)
	}
}

// Only the login form, its files and the brand icons answer without a
// session; every other path, whatever its spelling, needs one.
func TestOnlyTheLoginFormIsOpen(t *testing.T) {
	r := newRig(t)
	for _, p := range []string{"/", "/app.js", "/style.css", "/icons/manifest.json", "/icons/youtube.svg", "/icons/news.site"} {
		rec := r.do(http.MethodGet, p, lan, "")
		if rec.Code != http.StatusOK || !r.didReach() {
			t.Errorf("%s is the login form or its files and must be open: %d", p, rec.Code)
		}
	}
	for _, p := range []string{
		"/api/overview", "/api/devices", "/api/devices/02:4e:58:00:00:01/live", "/api/flows",
		"/api/rules", "/api/nothing", "/index.html", "/app.js/", "/icons", "/APP.JS",
		"/icons/../api/overview", "/icons/./../api/devices", "//api/overview", "/api/../api/overview",
		"/style.css/../api/overview", "/anything",
	} {
		wantError(t, r.do(http.MethodGet, p, lan, ""), http.StatusUnauthorized, "login", p+" without a session")
		if r.didReach() {
			t.Errorf("%s reached the console without a session", p)
		}
	}
	// A cookie that is no session is no login: not the null session (even
	// with an ACL that wrongly gives the unauthenticated group the netryx
	// view), not something shaped otherwise, not a session rpcd does not
	// know.
	r.ubus.grants["unauthenticated"] = map[string]bool{"view/read": true, "control/write": true}
	for _, sid := range []string{"", "x", "00000000000000000000000000000000",
		"11111111111111111111111111111111", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"0123456789abcdef0123456789abcdef0", `"},{"x":"`} {
		wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)), http.StatusUnauthorized, "login", fmt.Sprintf("cookie %q", sid))
		if r.didReach() {
			t.Errorf("cookie %q reached the console", sid)
		}
	}
	sid, _ := r.login("viewer", viewerPW, lan)
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusOK || !r.didReach() {
		t.Errorf("a viewer's session reads the console: %d", rec.Code)
	}
}

// A change needs the control grant and the per-session token in a header,
// which a page on another origin cannot read.
func TestAWriteNeedsTheGrantAndTheToken(t *testing.T) {
	r := newRig(t)
	vsid, vcsrf := r.login("viewer", viewerPW, lan)
	asid, acsrf := r.login("root", adminPW, "192.168.1.6:1")
	arpc := r.ubus.lastSID()
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		// The viewer sends its own correct token: only the grant is missing.
		wantError(t, r.do(m, "/api/rules", lan, `{}`, withCookie(vsid), withCSRF(vcsrf)), http.StatusForbidden, "forbidden", m+" with view only")
		if r.didReach() {
			t.Errorf("%s with view only reached the console", m)
		}
		wantError(t, r.do(m, "/api/rules", lan, `{}`, withCookie(asid)), http.StatusForbidden, "forbidden", m+" with control, no token")
		wantError(t, r.do(m, "/api/rules", lan, `{}`, withCookie(asid), withCSRF(vcsrf)), http.StatusForbidden, "forbidden", m+" with another session's token")
		wantError(t, r.do(m, "/api/rules", lan, `{}`, withCookie(asid), withCSRF(strings.ToUpper(acsrf))), http.StatusForbidden, "forbidden", m+" with the token's spelling changed")
		wantError(t, r.do(m, "/api/rules", lan, `{}`, withCookie(asid), withCSRF(acsrf[:32])), http.StatusForbidden, "forbidden", m+" with half the token")
		if r.didReach() {
			t.Errorf("%s without the right token reached the console", m)
		}
		if rec := r.do(m, "/api/rules", lan, `{}`, withCookie(asid), withCSRF(acsrf)); rec.Code != http.StatusOK || !r.didReach() {
			t.Errorf("%s with the grant and the token: %d %s", m, rec.Code, rec.Body)
		}
	}
	// The token is the HMAC of the session id under the secret.
	m := hmac.New(sha256.New, r.gate.Secret)
	m.Write([]byte(asid))
	if want := hex.EncodeToString(m.Sum(nil)); acsrf != want {
		t.Errorf("the token is hex(HMAC-SHA256(Secret, sid)): got %s, want %s", acsrf, want)
	}
	// A write without any session is a login, not a forbidden.
	wantError(t, r.do(http.MethodPost, "/api/rules", lan, `{}`, withCSRF(acsrf)), http.StatusUnauthorized, "login", "a write without a session")
	// The control grant taken away: the next write is refused once the
	// answer kept for at most five seconds is gone.
	r.ubus.revoke(arpc, "control/write")
	r.clock.Add(5 * time.Second)
	wantError(t, r.do(http.MethodPost, "/api/rules", lan, `{}`, withCookie(asid), withCSRF(acsrf)), http.StatusForbidden, "forbidden", "a write after the grant was revoked")
	if r.didReach() {
		t.Error("a revoked grant still wrote")
	}
}

// Logins: five a minute per source address, then one a minute; a correct
// password does not get past the limit, and a success counts as well.
func TestLoginIsRateLimited(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 5; i++ {
		wantError(t, r.do(http.MethodPost, "/api/login", lan, loginBody("root", "wrong")), http.StatusUnauthorized, "login", fmt.Sprintf("bad password %d", i+1))
	}
	rec := r.do(http.MethodPost, "/api/login", lan, loginBody("root", adminPW))
	wantError(t, rec, http.StatusTooManyRequests, "rate", "the sixth attempt in a minute, with the right password")
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a refused login says when to try again")
	}
	if n := r.ubus.count("login"); n != 5 {
		t.Errorf("a limited attempt must not reach rpcd: %d logins asked", n)
	}
	// Another address has its own allowance.
	r.login("root", adminPW, "192.168.1.6:1")
	r.clock.Add(59 * time.Second)
	wantError(t, r.do(http.MethodPost, "/api/login", lan, loginBody("root", adminPW)), http.StatusTooManyRequests, "rate", "59 s later")
	r.clock.Add(time.Second)
	r.login("root", adminPW, lan) // one a minute
	wantError(t, r.do(http.MethodPost, "/api/login", lan, loginBody("root", adminPW)), http.StatusTooManyRequests, "rate", "right after the minute's one")
	r.clock.Add(time.Minute)
	wantError(t, r.do(http.MethodPost, "/api/login", lan, loginBody("root", "wrong")), http.StatusUnauthorized, "login", "the next minute's one")
	wantError(t, r.do(http.MethodPost, "/api/login", lan, loginBody("root", "wrong")), http.StatusTooManyRequests, "rate", "a second in that minute")
	// Successes count: five good logins and the sixth is refused.
	src := "10.0.0.9:1"
	for i := 0; i < 5; i++ {
		r.login("root", adminPW, src)
	}
	wantError(t, r.do(http.MethodPost, "/api/login", src, loginBody("root", adminPW)), http.StatusTooManyRequests, "rate", "a sixth good login")
	// An IPv6 source and its v4-mapped spelling are the same address.
	for i := 0; i < 5; i++ {
		r.do(http.MethodPost, "/api/login", "[::ffff:10.0.0.10]:1", loginBody("root", "wrong"))
	}
	wantError(t, r.do(http.MethodPost, "/api/login", "10.0.0.10:2", loginBody("root", adminPW)), http.StatusTooManyRequests, "rate", "the same address spelled as v4")
	// An IPv6 host outside the house may take any address of its /64: each
	// address has its own five, and the /64 together has ten.
	for i := 1; i <= 10; i++ {
		wantError(t, r.do(http.MethodPost, "/api/login", fmt.Sprintf("[2001:db8:1:2::%x]:1", i), loginBody("root", "wrong")), http.StatusUnauthorized, "login", fmt.Sprintf("address %d of the /64", i))
	}
	wantError(t, r.do(http.MethodPost, "/api/login", "[2001:db8:1:2:ffff:1:2:3]:1", loginBody("root", adminPW)), http.StatusTooManyRequests, "rate", "an 11th attempt from the same /64")
	wantError(t, r.do(http.MethodPost, "/api/login", "[2001:db8:1:3::1]:1", loginBody("root", "wrong")), http.StatusUnauthorized, "login", "the next /64")
}

// The table of sources is bounded, and the source asked longest ago goes.
func TestTheLoginTableIsBounded(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	first := limit{"first", perAddress}
	for i := 0; i < 5; i++ {
		l.take(now, first)
	}
	if _, ok := l.take(now, first); ok {
		t.Fatal("the sixth attempt passed")
	}
	for i := 0; i < 3*maxSources; i++ {
		l.take(now, limit{fmt.Sprintf("2001:db8:%x::/64", i), perNet}, limit{fmt.Sprintf("2001:db8:%x::1", i), perAddress})
		if n := l.len(); n > maxSources {
			t.Fatalf("the table holds %d keys, over %d", n, maxSources)
		}
	}
	if l.len() != maxSources {
		t.Errorf("a full table holds exactly %d, got %d", maxSources, l.len())
	}
}

// /metrics and the Zabbix routes answer on loopback without a session;
// from the house side only with the bearer token, and an empty token
// refuses. A session is not a token.
func TestMetricsAreOpenOnLoopbackOnly(t *testing.T) {
	r := newRig(t)
	sid, _ := r.login("root", adminPW, lan)
	for _, p := range []string{"/metrics", "/api/snapshot", "/api/zabbix/discovery"} {
		wantError(t, r.do(http.MethodGet, p, lan, ""), http.StatusForbidden, "forbidden", p+" from the house without a token")
		wantError(t, r.do(http.MethodGet, p, lan, "", withHeader("Authorization", "Bearer wrong")), http.StatusForbidden, "forbidden", p+" with a wrong token")
		wantError(t, r.do(http.MethodGet, p, lan, "", withHeader("Authorization", "Bearer "+token+"x")), http.StatusForbidden, "forbidden", p+" with a longer token")
		wantError(t, r.do(http.MethodGet, p, lan, "", withHeader("Authorization", token)), http.StatusForbidden, "forbidden", p+" with the token but no scheme")
		wantError(t, r.do(http.MethodGet, p, lan, "", withCookie(sid)), http.StatusForbidden, "forbidden", p+" with a session and no token")
		wantError(t, r.do(http.MethodGet, p, "[2001:db8::5]:1", ""), http.StatusForbidden, "forbidden", p+" from a house IPv6 address")
		if r.didReach() {
			t.Errorf("%s reached from the house without the token", p)
		}
		for _, ok := range []struct {
			remote string
			opts   []opt
		}{
			{lan, []opt{withHeader("Authorization", "Bearer "+token)}},
			{lan, []opt{withHeader("Authorization", "bearer "+token)}},
			{loopback, nil},
			{"127.0.0.53:9", nil},
			{"[::1]:9", nil},
			{"[::ffff:127.0.0.1]:9", nil},
		} {
			if rec := r.do(http.MethodGet, p, ok.remote, "", ok.opts...); rec.Code != http.StatusOK || !r.didReach() {
				t.Errorf("%s from %s: %d", p, ok.remote, rec.Code)
			}
		}
	}
	r.gate.MetricsToken = ""
	r2 := r.gate.Wrap(http.NotFoundHandler())
	for _, a := range []string{"Bearer ", "Bearer", ""} {
		q := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		q.RemoteAddr = lan
		q.Header.Set("Authorization", a)
		rec := httptest.NewRecorder()
		r2.ServeHTTP(rec, q)
		wantError(t, rec, http.StatusForbidden, "forbidden", fmt.Sprintf("no token configured, Authorization %q", a))
	}
}

// rpcd forgets a session after its timeout; netryx must not remember it
// for longer than its five seconds of trust.
func TestAnExpiredSessionIsALogin(t *testing.T) {
	r := newRig(t)
	sid, csrf := r.login("root", adminPW, lan)
	rpcSID := r.ubus.lastSID()
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusOK {
		t.Fatalf("a fresh session reads: %d", rec.Code)
	}
	r.didReach()
	r.clock.Add(301 * time.Second)
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)), http.StatusUnauthorized, "login", "a read with an expired session")
	wantError(t, r.do(http.MethodPost, "/api/rules", lan, `{}`, withCookie(sid), withCSRF(csrf)), http.StatusUnauthorized, "login", "a write with an expired session")
	wantError(t, r.do(http.MethodGet, "/api/session", lan, "", withCookie(sid)), http.StatusUnauthorized, "login", "the session route with an expired session")
	if r.didReach() {
		t.Error("an expired session reached the console")
	}
	// The client says so itself: -32002 is a session gone, not a refusal.
	ok, err := r.gate.RPC.Access(context.Background(), rpcSID, Scope, "view", "read")
	if ok || !errors.Is(err, ErrSessionGone) {
		t.Errorf("Access on an expired session: %v, %v; want false, ErrSessionGone", ok, err)
	}
}

// A positive answer of rpcd is trusted for less than five seconds, so the
// live view's polling does not double the load; a negative or failed one is
// never kept.
func TestAPositiveAnswerIsKeptForFiveSecondsAtMost(t *testing.T) {
	r := newRig(t)
	sid, _ := r.login("root", adminPW, lan)
	rpcSID := r.ubus.lastSID()
	before := r.ubus.count("access")
	for i := 0; i < 3; i++ {
		if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusOK {
			t.Fatalf("read %d: %d", i, rec.Code)
		}
	}
	if n := r.ubus.count("access") - before; n > 1 {
		t.Errorf("three reads within a second asked rpcd %d times", n)
	}
	r.ubus.revoke(rpcSID, "view/read")
	r.clock.Add(4900 * time.Millisecond)
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusOK {
		t.Errorf("within five seconds the kept answer holds: %d", rec.Code)
	}
	r.clock.Add(100 * time.Millisecond)
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)), http.StatusUnauthorized, "login", "five seconds after the grant was taken")
	// The refusal is not kept: the grant back, the next read passes.
	r.ubus.grant(rpcSID, "view/read")
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusOK {
		t.Errorf("a refusal must not be kept: %d", rec.Code)
	}
	// A clock that jumps back does not stretch the trust.
	r.ubus.revoke(rpcSID, "view/read")
	r.clock.Add(-time.Hour)
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)), http.StatusUnauthorized, "login", "after the clock went back")
}

// rpcd unreachable is no login and no answer from memory either.
func TestAnRPCFailureIsNeverALogin(t *testing.T) {
	r := newRig(t)
	sid, _ := r.login("root", adminPW, lan)
	r.gate.RPC = NewRPC("http://127.0.0.1:1/ubus", nil)
	r.clock.Add(5 * time.Second)
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)), http.StatusServiceUnavailable, "rpc", "a read while rpcd is down")
	wantError(t, r.do(http.MethodPost, "/api/login", "10.1.1.1:1", loginBody("root", adminPW)), http.StatusServiceUnavailable, "rpc", "a login while rpcd is down")
	if r.didReach() {
		t.Error("a request passed while rpcd did not answer")
	}
}

// The cookie is HttpOnly, SameSite=Strict, Path=/, Secure over TLS; the
// password reaches neither the log nor any answer.
func TestTheLoginCookieAndTheLog(t *testing.T) {
	r := newRig(t)
	rec := r.do(http.MethodPost, "/api/login", lan, loginBody("root", adminPW))
	var c *http.Cookie
	for _, k := range rec.Result().Cookies() {
		if k.Name == CookieName {
			c = k
		}
	}
	if c == nil {
		t.Fatalf("login set no cookie: %d %s", rec.Code, rec.Body)
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure {
		t.Errorf("cookie over http: %+v", c)
	}
	var ans struct {
		CSRF     string `json:"csrf"`
		User     string `json:"user"`
		CanWrite bool   `json:"canWrite"`
	}
	json.Unmarshal(rec.Body.Bytes(), &ans)
	if ans.CSRF != r.gate.CSRF(c.Value) || ans.User != "root" || !ans.CanWrite {
		t.Errorf("login answer %s", rec.Body)
	}
	q := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(loginBody("root", adminPW)))
	q.RemoteAddr = "10.2.2.2:1"
	q.Header.Set("Content-Type", "application/json")
	q.TLS = &tls.ConnectionState{}
	rec = httptest.NewRecorder()
	r.h.ServeHTTP(rec, q)
	if cs := rec.Result().Cookies(); len(cs) != 1 || !cs[0].Secure {
		t.Errorf("over TLS the cookie is Secure: %+v", cs)
	}
	wantError(t, r.do(http.MethodPost, "/api/login", lan, loginBody("root", "wrong-"+adminPW)), http.StatusUnauthorized, "login", "a bad password")
	// A login that is not JSON is refused before rpcd: a page elsewhere
	// cannot post one as a plain form.
	q = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(loginBody("root", adminPW)))
	q.RemoteAddr = "10.3.3.3:1"
	q.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	r.h.ServeHTTP(rec, q)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a login sent as text/plain: %d", rec.Code)
	}
	if rec := r.do(http.MethodGet, "/api/login", lan, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/login: %d", rec.Code)
	}
	logs := strings.Join(r.logs, "\n")
	if strings.Contains(logs, adminPW) {
		t.Errorf("the password reached the log:\n%s", logs)
	}
	if !strings.Contains(logs, `login failed for "root" from 192.168.1.5`) {
		t.Errorf("a failed login is logged with its user and address:\n%s", logs)
	}
	if strings.Contains(rec.Body.String(), adminPW) {
		t.Error("the password came back in an answer")
	}
}

// A LuCI user whose ACL does not give the netryx view gets no session.
func TestALoginWithoutTheViewGrantIsRefused(t *testing.T) {
	r := newRig(t)
	rec := r.do(http.MethodPost, "/api/login", lan, loginBody("nobody", "nobody-pw"))
	wantError(t, rec, http.StatusForbidden, "forbidden", "a login without the netryx grant")
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a refused login set a cookie")
	}
	r.ubus.mu.Lock()
	n := len(r.ubus.sessions)
	r.ubus.mu.Unlock()
	if n != 0 {
		t.Errorf("the refused login's rpcd session was left open (%d)", n)
	}
	// A session whose ACL lacks ubus session.access gets -32002 from
	// uhttpd on the view check: that is a refusal (403), not rpcd down.
	r.ubus.users["noubus"] = "noubus-pw"
	r.ubus.grants["noubus"] = map[string]bool{"view/read": true}
	r.ubus.noUbusAccess["noubus"] = true
	wantError(t, r.do(http.MethodPost, "/api/login", "10.4.4.4:1", loginBody("noubus", "noubus-pw")), http.StatusForbidden, "forbidden", "a login whose view check is -32002")
}

// A slow login must not leave an orphan session: the session is ended with
// a context of its own when the view check runs out of time.
func TestASlowLoginLeavesNoSession(t *testing.T) {
	r := newRig(t)
	defer func(d time.Duration) { rpcTimeout = d }(rpcTimeout)
	rpcTimeout = 300 * time.Millisecond
	r.ubus.delay["login"] = 200 * time.Millisecond
	r.ubus.delay["access"] = 200 * time.Millisecond
	wantError(t, r.do(http.MethodPost, "/api/login", lan, loginBody("root", adminPW)), http.StatusServiceUnavailable, "rpc", "a view check that ran out of time")
	r.ubus.mu.Lock()
	n := len(r.ubus.sessions)
	r.ubus.mu.Unlock()
	if n != 0 {
		t.Errorf("a slow login left %d rpcd sessions open", n)
	}
}

// The cookie is an opaque handle, never the rpcd session: a root rpcd
// session in a cookie with Path=/ would be sent to every port of the
// router's host (LuCI, other services). The rpcd id never leaves the router.
func TestTheCookieIsNotTheRPCSession(t *testing.T) {
	r := newRig(t)
	h, csrf := r.login("root", adminPW, lan)
	rpcSID := r.ubus.lastSID()
	if h == rpcSID || strings.Contains(h, rpcSID) || len(h) != 64 {
		t.Fatalf("the cookie %q carries or is shaped as the rpcd session %q", h, rpcSID)
	}
	rec := r.do(http.MethodGet, "/api/session", lan, "", withCookie(h))
	if strings.Contains(rec.Body.String(), rpcSID) {
		t.Errorf("the rpcd session id came back in an answer: %s", rec.Body)
	}
	// The rpcd id itself, sent as the cookie, is no login.
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(rpcSID)), http.StatusUnauthorized, "login", "the rpcd session id as the cookie")
	m := hmac.New(sha256.New, r.gate.Secret)
	m.Write([]byte(h))
	if csrf != hex.EncodeToString(m.Sum(nil)) {
		t.Error("the token is the HMAC of the handle")
	}
	if r.didReach() {
		t.Error("the rpcd session id reached the console")
	}
}

// The table of handles is bounded; the oldest goes first, is a login from
// then on, and its rpcd session is ended.
func TestAnEvictedHandleIsALogin(t *testing.T) {
	r := newRig(t)
	first, _ := r.login("root", adminPW, "10.8.0.1:1")
	firstRPC := r.ubus.lastSID()
	second, _ := r.login("root", adminPW, "10.8.0.2:1")
	for i := 0; i < 255; i++ {
		r.login("root", adminPW, fmt.Sprintf("10.9.%d.%d:1", i/200, i%200))
	}
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(first)), http.StatusUnauthorized, "login", "the 257th login pushed out the first")
	if r.ubus.live(firstRPC) != nil {
		t.Error("the evicted handle's rpcd session was left open")
	}
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(second)); rec.Code != http.StatusOK {
		t.Errorf("the second login still reads: %d", rec.Code)
	}
}

// Without rpcd nobody can log in: the console is on loopback only, reads
// from loopback need no session, and nothing may change.
func TestWithoutRPCLoopbackReadsAndNothingChanges(t *testing.T) {
	r := newRig(t)
	wantError(t, r.do(http.MethodGet, "/api/overview", loopback, ""), http.StatusUnauthorized, "login", "loopback while rpcd answers")
	r.gate.SetNoRPC(true)
	for _, remote := range []string{loopback, "[::1]:9"} {
		if rec := r.do(http.MethodGet, "/api/overview", remote, ""); rec.Code != http.StatusOK || !r.didReach() {
			t.Errorf("a loopback read without rpcd from %s: %d", remote, rec.Code)
		}
	}
	rec := r.do(http.MethodGet, "/api/session", loopback, "")
	var ans struct {
		NoRPC    bool   `json:"noRPC"`
		CanWrite bool   `json:"canWrite"`
		Zone     string `json:"zone"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &ans) != nil || !ans.NoRPC || ans.CanWrite || ans.Zone != "UTC" {
		t.Errorf("the session route says there is no rpcd: %d %s", rec.Code, rec.Body)
	}
	for _, p := range []string{"/api/rules", "/api/login", "/api/logout"} {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			wantError(t, r.do(m, p, loopback, `{}`), http.StatusServiceUnavailable, "no-rpc", m+" "+p+" without rpcd")
		}
	}
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, ""), http.StatusUnauthorized, "login", "a house read without rpcd")
	if r.didReach() {
		t.Error("a change or a house read passed without rpcd")
	}
	if rec := r.do(http.MethodGet, "/metrics", loopback, ""); rec.Code != http.StatusOK {
		t.Errorf("metrics on loopback without rpcd: %d", rec.Code)
	}
	wantError(t, r.do(http.MethodGet, "/metrics", lan, ""), http.StatusForbidden, "forbidden", "metrics from the house without rpcd")
}

// GET /api/session says who is signed in, the token and whether they may
// change things; without a session it is a login.
func TestTheSessionRouteSaysWhoAndWhat(t *testing.T) {
	r := newRig(t)
	wantError(t, r.do(http.MethodGet, "/api/session", lan, ""), http.StatusUnauthorized, "login", "the session route without a cookie")
	for _, c := range []struct {
		user, pw string
		write    bool
	}{{"viewer", viewerPW, false}, {"root", adminPW, true}} {
		sid, csrf := r.login(c.user, c.pw, lan)
		rec := r.do(http.MethodGet, "/api/session", lan, "", withCookie(sid))
		var ans struct {
			User     string `json:"user"`
			CSRF     string `json:"csrf"`
			CanWrite bool   `json:"canWrite"`
			Zone     string `json:"zone"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &ans) != nil ||
			ans.User != c.user || ans.CSRF != csrf || ans.CanWrite != c.write || ans.Zone != "UTC" {
			t.Errorf("%s: %d %s", c.user, rec.Code, rec.Body)
		}
	}
	if r.didReach() {
		t.Error("the session route is the gate's own")
	}
}

// Logout is a write: it needs the token, ends the rpcd session and clears
// the cookie; the session is refused afterwards even when rpcd would not
// end it.
func TestLogoutEndsTheSession(t *testing.T) {
	r := newRig(t)
	sid, csrf := r.login("viewer", viewerPW, lan)
	rpcSID := r.ubus.lastSID()
	wantError(t, r.do(http.MethodPost, "/api/logout", lan, "", withCookie(sid)), http.StatusForbidden, "forbidden", "a logout without the token")
	rec := r.do(http.MethodPost, "/api/logout", lan, "", withCookie(sid), withCSRF(csrf))
	if rec.Code != http.StatusOK {
		t.Fatalf("a viewer logs out: %d %s", rec.Code, rec.Body)
	}
	if cs := rec.Result().Cookies(); len(cs) != 1 || cs[0].Name != CookieName || cs[0].MaxAge >= 0 {
		t.Errorf("logout clears the cookie: %+v", cs)
	}
	if r.ubus.live(rpcSID) != nil {
		t.Error("logout left the rpcd session open")
	}
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)), http.StatusUnauthorized, "login", "a read after logout")

	// rpcd refusing to end a session (an ACL from before the upgrade)
	// still ends it for netryx, at once, kept answer or not: the handle is
	// gone, and the rpcd session id never left the router.
	r.ubus.destroy = false
	sid, csrf = r.login("root", adminPW, "10.0.0.1:1")
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	r.didReach()
	if rec := r.do(http.MethodPost, "/api/logout", lan, "", withCookie(sid), withCSRF(csrf)); rec.Code != http.StatusOK {
		t.Fatalf("logout when rpcd refuses: %d", rec.Code)
	}
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)), http.StatusUnauthorized, "login", "a read after logout rpcd refused")
	if r.didReach() {
		t.Error("a logged-out session reached the console")
	}
}

// The client speaks rpcd's shapes as measured, and keeps the credentials to
// the request body.
func TestTheRPCSpeaksRpcdsShapes(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)}
	u := newFakeUbus(c.Now)
	u.users["root"] = adminPW
	u.grants["root"] = map[string]bool{"view/read": true}
	srv := httptest.NewServer(u)
	defer srv.Close()
	rpc := NewRPC(srv.URL+"/ubus", nil)
	ctx := context.Background()

	sid, err := rpc.Login(ctx, "root", adminPW)
	if err != nil || len(sid) != 32 {
		t.Fatalf("login: %q %v", sid, err)
	}
	if _, err := rpc.Login(ctx, "root", "wrong"); !errors.Is(err, ErrBadLogin) {
		t.Errorf("a bad password (result [6]) is ErrBadLogin: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "wrong") {
		t.Errorf("the error carries the password: %v", err)
	}
	if ok, err := rpc.Access(ctx, sid, Scope, "view", "read"); !ok || err != nil {
		t.Errorf("view: %v %v", ok, err)
	}
	if ok, err := rpc.Access(ctx, sid, Scope, "control", "write"); ok || err != nil {
		t.Errorf("control: %v %v; want false, nil", ok, err)
	}
	if ok, err := rpc.Access(ctx, "11111111111111111111111111111111", Scope, "view", "read"); ok || !errors.Is(err, ErrSessionGone) {
		t.Errorf("an unknown session (-32002): %v %v", ok, err)
	}
	if err := rpc.Destroy(ctx, sid); err != nil {
		t.Errorf("destroy: %v", err)
	}
	if ok, err := rpc.Access(ctx, sid, Scope, "view", "read"); ok || !errors.Is(err, ErrSessionGone) {
		t.Errorf("a destroyed session: %v %v", ok, err)
	}

	// A login answer carrying the null session is no session.
	null := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"00000000000000000000000000000000"}]}`))
	}))
	defer null.Close()
	if sid, err := NewRPC(null.URL, nil).Login(ctx, "root", adminPW); err == nil {
		t.Errorf("a login answered with the null session was taken: %q", sid)
	}

	// The probe sends empty credentials, never a password, and any
	// JSON-RPC answer proves rpcd is there.
	u.logins = nil
	if err := Probe(ctx, rpc); err != nil {
		t.Errorf("probe with rpcd present: %v", err)
	}
	if len(u.logins) != 1 || u.logins[0] != [2]string{"", ""} {
		t.Errorf("the probe sent %q", u.logins)
	}
	// Nothing listening, and a server that is not rpcd, are no rpcd.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + l.Addr().String() + "/ubus"
	l.Close()
	if err := Probe(ctx, NewRPC(dead, nil)); !errors.Is(err, ErrNoRPC) {
		t.Errorf("probe with nothing listening: %v", err)
	}
	html := httptest.NewServer(http.NotFoundHandler())
	defer html.Close()
	if err := Probe(ctx, NewRPC(html.URL+"/ubus", nil)); !errors.Is(err, ErrNoRPC) {
		t.Errorf("probe of a server that is not rpcd: %v", err)
	}
	if _, err := NewRPC(html.URL+"/ubus", nil).Access(ctx, sid, Scope, "view", "read"); !errors.Is(err, ErrNoRPC) {
		t.Errorf("access to a server that is not rpcd: %v", err)
	}
}

// The default client talks to loopback only: a redirect to https on the
// router itself is followed (uhttpd with redirect_https), anything leaving
// the router is not dialled.
func TestTheRPCReachesOnlyLoopback(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)}
	u := newFakeUbus(c.Now)
	u.users["root"] = adminPW
	tlsSrv := httptest.NewTLSServer(u)
	defer tlsSrv.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tlsSrv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	if _, err := NewRPC(redir.URL+"/ubus", nil).Login(context.Background(), "root", adminPW); err != nil {
		t.Errorf("uhttpd's redirect to https on loopback: %v", err)
	}
	away := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://192.0.2.1/ubus", http.StatusTemporaryRedirect)
	}))
	defer away.Close()
	for _, url := range []string{"http://192.0.2.1/ubus", away.URL + "/ubus"} {
		if _, err := NewRPC(url, nil).Login(context.Background(), "root", adminPW); !errors.Is(err, ErrNoRPC) {
			t.Errorf("%s: a password must never leave the router: %v", url, err)
		}
	}
	// A real rpcd on one of this machine's own non-loopback addresses is
	// still not dialled: the client refuses the address, not the answer.
	ip := ""
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
				ip = n.IP.String()
				break
			}
		}
	}
	if ip == "" {
		t.Log("no non-loopback IPv4 address here: the dial refusal is checked by TEST-NET only")
		return
	}
	l, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatal(err)
	}
	wide := httptest.NewUnstartedServer(u)
	wide.Listener.Close()
	wide.Listener = l
	wide.Start()
	defer wide.Close()
	before := u.count("login")
	if _, err := NewRPC(wide.URL+"/ubus", nil).Login(context.Background(), "root", adminPW); !errors.Is(err, ErrNoRPC) {
		t.Errorf("%s answered: a password must never leave loopback: %v", wide.URL, err)
	}
	if u.count("login") != before {
		t.Errorf("the password reached a listener on %s", ip)
	}
}

// The ACL netryx ships gives its view to read, its control to write, and
// lets a session ask its own grants and end itself: measured in the lab VM,
// a login whose only group is netryx gets -32002 on session.access without
// ubus session.access, and every session gets -32002 on session.destroy
// without ubus session.destroy.
func TestTheACLFileGrantsViewAndControl(t *testing.T) {
	b, err := os.ReadFile("../../package/netryx/files/netryx.acl.json")
	if err != nil {
		t.Fatal(err)
	}
	var acl map[string]struct {
		Description string                     `json:"description"`
		Read        map[string]json.RawMessage `json:"read"`
		Write       map[string][]string        `json:"write"`
	}
	if err := json.Unmarshal(b, &acl); err != nil {
		t.Fatalf("the ACL is not JSON: %v", err)
	}
	g, ok := acl["netryx"]
	if !ok || len(acl) != 1 || g.Description == "" {
		t.Fatalf("one group named netryx: %s", b)
	}
	var view []string
	json.Unmarshal(g.Read["netryx"], &view)
	if len(view) != 1 || view[0] != "view" {
		t.Errorf("read grants netryx view: %s", g.Read["netryx"])
	}
	var ubus map[string][]string
	json.Unmarshal(g.Read["ubus"], &ubus)
	if s := strings.Join(ubus["session"], ","); s != "access,destroy" || len(ubus) != 1 {
		t.Errorf("read grants ubus session access and destroy and nothing else of ubus: %s", g.Read["ubus"])
	}
	if len(g.Read) != 2 {
		t.Errorf("read grants exactly netryx and ubus: %s", b)
	}
	if c := g.Write["netryx"]; len(g.Write) != 1 || len(c) != 1 || c[0] != "control" {
		t.Errorf("write grants netryx control and nothing else: %s", b)
	}
}

// House-side IPv6 (the house's own prefix) and link-local sources are
// limited per address only, with no bucket a neighbour can drain: a device
// in the house can already reach LuCI's own login, which has no limit, so
// a shared bucket would add a lockout and keep nobody out. Each address
// keeps its own five, then one a minute.
func TestHouseIPv6IsCountedPerAddress(t *testing.T) {
	for _, c := range []struct{ pfx, zone, neighbour string }{
		{"2001:db8:50::", "", "[2001:db8:50::11]:1"},
		{"fe80::", "%eth0", "[fe80::2%eth0]:1"},
		{"fe80::", "", "[fe80::1:2]:1"},
	} {
		r := newRig(t)
		// One device makes a hundred attempts across twenty of its addresses.
		for i := 0; i < 100; i++ {
			src := fmt.Sprintf("[%sa:%x%s]:1", c.pfx, i/5, c.zone)
			wantError(t, r.do(http.MethodPost, "/api/login", src, loginBody("root", "wrong")), http.StatusUnauthorized, "login", fmt.Sprintf("%s attempt %d", src, i%5+1))
		}
		if rec := r.do(http.MethodPost, "/api/login", c.neighbour, loginBody("root", adminPW)); rec.Code != http.StatusOK {
			t.Errorf("%s after a device of its /64 made 100 attempts across addresses: %d %s", c.neighbour, rec.Code, rec.Body)
		}
		// One address: five at once, then one a minute.
		own := fmt.Sprintf("[%sb:1%s]:1", c.pfx, c.zone)
		for i := 0; i < 5; i++ {
			wantError(t, r.do(http.MethodPost, "/api/login", own, loginBody("root", "wrong")), http.StatusUnauthorized, "login", fmt.Sprintf("%s attempt %d", own, i+1))
		}
		wantError(t, r.do(http.MethodPost, "/api/login", own, loginBody("root", adminPW)), http.StatusTooManyRequests, "rate", own+" a sixth time")
		r.clock.Add(59 * time.Second)
		wantError(t, r.do(http.MethodPost, "/api/login", own, loginBody("root", adminPW)), http.StatusTooManyRequests, "rate", own+" 59 s later")
		r.clock.Add(time.Second)
		r.login("root", adminPW, own)
		wantError(t, r.do(http.MethodPost, "/api/login", own, loginBody("root", adminPW)), http.StatusTooManyRequests, "rate", own+" right after the minute's one")
	}
}

// IPv6 from outside the house may take any address of its /64, so the /64
// shares one bucket besides each address's own: ten at once, then one every
// ten seconds. Outside IPv6 does not reach the console in the default
// firewall, but it is limited all the same.
func TestAnOutsideIPv6NetworkSharesItsAttempts(t *testing.T) {
	r := newRig(t)
	pfx := "2001:db8:9:9::"
	for i := 1; i <= 10; i++ {
		wantError(t, r.do(http.MethodPost, "/api/login", fmt.Sprintf("[%s%x]:1", pfx, 0x100+i), loginBody("root", "wrong")), http.StatusUnauthorized, "login", fmt.Sprintf("%s address %d", pfx, i))
	}
	rec := r.do(http.MethodPost, "/api/login", fmt.Sprintf("[%sffff]:1", pfx), loginBody("root", adminPW))
	wantError(t, rec, http.StatusTooManyRequests, "rate", pfx+" an 11th address")
	if rec.Header().Get("Retry-After") != "10" {
		t.Errorf("the /64's next token is ten seconds off: Retry-After %q", rec.Header().Get("Retry-After"))
	}
	r.clock.Add(9 * time.Second)
	wantError(t, r.do(http.MethodPost, "/api/login", fmt.Sprintf("[%sfffe]:1", pfx), loginBody("root", adminPW)), http.StatusTooManyRequests, "rate", pfx+" nine seconds later")
	r.clock.Add(time.Second)
	wantError(t, r.do(http.MethodPost, "/api/login", fmt.Sprintf("[%sfffd]:1", pfx), loginBody("root", "wrong")), http.StatusUnauthorized, "login", pfx+" ten seconds later")
	wantError(t, r.do(http.MethodPost, "/api/login", fmt.Sprintf("[%sfffc]:1", pfx), loginBody("root", "wrong")), http.StatusTooManyRequests, "rate", pfx+" the next at once")
	// Another /64 has its own ten; one address of it still only five.
	for i := 0; i < 5; i++ {
		wantError(t, r.do(http.MethodPost, "/api/login", "[2001:db8:9:a::1]:1", loginBody("root", "wrong")), http.StatusUnauthorized, "login", fmt.Sprintf("the next /64, attempt %d", i+1))
	}
	wantError(t, r.do(http.MethodPost, "/api/login", "[2001:db8:9:a::1]:1", loginBody("root", adminPW)), http.StatusTooManyRequests, "rate", "one outside address a sixth time")
	wantError(t, r.do(http.MethodPost, "/api/login", "[2001:db8:9:a::2]:1", loginBody("root", "wrong")), http.StatusUnauthorized, "login", "its neighbour, the /64's sixth")
}

// Made-up sources cannot push a live source out of the table and so reset
// its count: entries whose allowance has refilled go first.
func TestAFloodDoesNotResetALiveCount(t *testing.T) {
	r := newRig(t)
	live := "10.0.0.1:1"
	for i := 0; i < 5; i++ {
		r.do(http.MethodPost, "/api/login", live, loginBody("root", "wrong"))
	}
	// The table fills behind it, each source asking once.
	for i := 0; i < 1023; i++ {
		r.do(http.MethodPost, "/api/login", fmt.Sprintf("10.1.%d.%d:1", i/250, i%250), loginBody("root", "wrong"))
	}
	// Two minutes on, those have their five again; the live one has two.
	r.clock.Add(2 * time.Minute)
	for i := 0; i < 600; i++ {
		r.do(http.MethodPost, "/api/login", fmt.Sprintf("10.2.%d.%d:1", i/250, i%250), loginBody("root", "wrong"))
	}
	wantError(t, r.do(http.MethodPost, "/api/login", live, loginBody("root", "wrong")), http.StatusUnauthorized, "login", "the live source's first of two")
	wantError(t, r.do(http.MethodPost, "/api/login", live, loginBody("root", "wrong")), http.StatusUnauthorized, "login", "the live source's second of two")
	wantError(t, r.do(http.MethodPost, "/api/login", live, loginBody("root", adminPW)), http.StatusTooManyRequests, "rate", "the live source after the flood")
	// A flood of made-up IPv6 sources from one /64 outside the house
	// reaches rpcd at most ten times.
	for i := 0; i < 2000; i++ {
		r.do(http.MethodPost, "/api/login", fmt.Sprintf("[2001:db8:7:7::%x]:1", i+1), loginBody("root", "wrong"))
	}
	if n := r.ubus.count("login"); n > 5+1023+600+2+10 {
		t.Errorf("a flood from one /64 reached rpcd %d times, over its ten", n-(5+1023+600+2))
	}
}

// The console shows every time in the router's own zone, so the session says
// which one it is (UTC when the router names none).
func TestTheSessionCarriesTheRoutersZone(t *testing.T) {
	r := newRig(t)
	r.gate.Zone = "Africa/Nairobi"
	sid, _ := r.login("root", adminPW, lan)
	rec := r.do(http.MethodGet, "/api/session", lan, "", withCookie(sid))
	var ans struct {
		Zone string `json:"zone"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &ans) != nil || ans.Zone != "Africa/Nairobi" {
		t.Errorf("the session route did not say the router's zone: %s", rec.Body)
	}
	r.gate.SetNoRPC(true)
	rec = r.do(http.MethodGet, "/api/session", loopback, "")
	if json.Unmarshal(rec.Body.Bytes(), &ans) != nil || ans.Zone != "Africa/Nairobi" {
		t.Errorf("without rpcd the session route lost the zone: %s", rec.Body)
	}
}
