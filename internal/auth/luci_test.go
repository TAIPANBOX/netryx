package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"netryx/internal/handoff"
)

// luciRig is a rig whose gate redeems LuCI handoff codes from its own
// directory; luciSession signs in as LuCI does and returns rpcd's session.
func luciRig(t *testing.T) *rig {
	r := newRig(t)
	r.gate.Handoff = t.TempDir() + "/luci"
	return r
}

func (r *rig) luciSession(user, pass string) string {
	r.t.Helper()
	sid, err := r.gate.RPC.Login(context.Background(), user, pass)
	if err != nil {
		r.t.Fatalf("LuCI login %s: %v", user, err)
	}
	return sid
}

func (r *rig) mint(sid, user string) string {
	r.t.Helper()
	code, err := handoff.Mint(r.gate.Handoff, sid, user, r.clock.Now())
	if err != nil {
		r.t.Fatal(err)
	}
	return code
}

func codeBody(code string) string {
	b, _ := json.Marshal(map[string]string{"code": code})
	return string(b)
}

func cookieOf(rec interface{ Result() *http.Response }) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c.Value
		}
	}
	return ""
}

func TestASignedInLuCIUserOpensTheConsoleWithACode(t *testing.T) {
	r := luciRig(t)
	luci := r.luciSession("root", adminPW)
	code := r.mint(luci, "root")
	rec := r.do(http.MethodPost, "/api/login/luci", lan, codeBody(code))
	if rec.Code != http.StatusOK {
		t.Fatalf("redeem: %d %s", rec.Code, rec.Body)
	}
	var ans struct {
		User     string `json:"user"`
		CSRF     string `json:"csrf"`
		CanWrite bool   `json:"canWrite"`
	}
	json.Unmarshal(rec.Body.Bytes(), &ans)
	if ans.User != "root" || ans.CSRF == "" || !ans.CanWrite {
		t.Fatalf("answer %s, want root with a token and the write grant", rec.Body)
	}
	h := cookieOf(rec)
	if !validHandle(h) || h == luci {
		t.Fatalf("cookie %q: want an opaque handle, never LuCI's session", h)
	}
	if got := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(h)); got.Code != http.StatusOK {
		t.Fatalf("a read with the handed-off session: %d", got.Code)
	}
	if r.ubus.count("login") != 1 {
		t.Errorf("rpcd saw %d logins, want only LuCI's own", r.ubus.count("login"))
	}
}

func TestACodeOpensOneSessionOnly(t *testing.T) {
	r := luciRig(t)
	code := r.mint(r.luciSession("root", adminPW), "root")
	if rec := r.do(http.MethodPost, "/api/login/luci", lan, codeBody(code)); rec.Code != http.StatusOK {
		t.Fatalf("first: %d", rec.Code)
	}
	rec := r.do(http.MethodPost, "/api/login/luci", lan, codeBody(code))
	wantError(t, rec, http.StatusUnauthorized, "login", "the same code again")
	if cookieOf(rec) != "" {
		t.Error("a used code set a cookie")
	}
}

func TestAnExpiredCodeOpensNothing(t *testing.T) {
	r := luciRig(t)
	code := r.mint(r.luciSession("root", adminPW), "root")
	r.clock.Add(handoff.TTL)
	rec := r.do(http.MethodPost, "/api/login/luci", lan, codeBody(code))
	wantError(t, rec, http.StatusUnauthorized, "login", "a code 30 s old")
	if cookieOf(rec) != "" {
		t.Error("an expired code set a cookie")
	}
}

// A code minted for a session rpcd no longer knows (logged out of LuCI, or a
// session that never was) opens nothing.
func TestACodeForADeadOrForeignSessionOpensNothing(t *testing.T) {
	r := luciRig(t)
	luci := r.luciSession("root", adminPW)
	code := r.mint(luci, "root")
	if err := r.gate.RPC.Destroy(context.Background(), luci); err != nil {
		t.Fatal(err)
	}
	wantError(t, r.do(http.MethodPost, "/api/login/luci", lan, codeBody(code)), http.StatusUnauthorized, "login", "LuCI logged out")
	forged := r.mint("ffffffffffffffffffffffffffffffff", "root")
	wantError(t, r.do(http.MethodPost, "/api/login/luci", lan, codeBody(forged)), http.StatusUnauthorized, "login", "a session rpcd never issued")
}

func TestACodeForAUserWithoutTheViewGrantOpensNothing(t *testing.T) {
	r := luciRig(t)
	code := r.mint(r.luciSession("nobody", "nobody-pw"), "nobody")
	rec := r.do(http.MethodPost, "/api/login/luci", lan, codeBody(code))
	wantError(t, rec, http.StatusForbidden, "forbidden", "a LuCI user without the netryx grant")
	if cookieOf(rec) != "" {
		t.Error("a user without the grant got a cookie")
	}
}

// The user is the one the code was minted for, and a viewer stays a viewer.
func TestAHandedOffViewerCannotWrite(t *testing.T) {
	r := luciRig(t)
	rec := r.do(http.MethodPost, "/api/login/luci", lan, codeBody(r.mint(r.luciSession("viewer", viewerPW), "viewer")))
	var ans struct {
		User     string `json:"user"`
		CSRF     string `json:"csrf"`
		CanWrite bool   `json:"canWrite"`
	}
	json.Unmarshal(rec.Body.Bytes(), &ans)
	if rec.Code != http.StatusOK || ans.User != "viewer" || ans.CanWrite {
		t.Fatalf("viewer: %d %s", rec.Code, rec.Body)
	}
	wantError(t, r.do(http.MethodPut, "/api/rules/x", lan, "{}", withCookie(cookieOf(rec)), withCSRF(ans.CSRF)), http.StatusForbidden, "forbidden", "a write by a handed-off viewer")
}

// Leaving the console never signs the user out of LuCI: the session was
// LuCI's, lent for the console's use.
func TestLeavingTheConsoleKeepsTheLuCISession(t *testing.T) {
	r := luciRig(t)
	luci := r.luciSession("root", adminPW)
	rec := r.do(http.MethodPost, "/api/login/luci", lan, codeBody(r.mint(luci, "root")))
	var ans struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(rec.Body.Bytes(), &ans)
	h := cookieOf(rec)
	if out := r.do(http.MethodPost, "/api/logout", lan, "", withCookie(h), withCSRF(ans.CSRF)); out.Code != http.StatusOK {
		t.Fatalf("logout: %d", out.Code)
	}
	if r.ubus.live(luci) == nil {
		t.Fatal("leaving the console ended the LuCI session")
	}
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(h)), http.StatusUnauthorized, "login", "the handle after logout")
}

// The code travels in a JSON body only and never reaches a log; a malformed
// one is the same refusal as an unknown one.
func TestTheCodeIsNeverLoggedAndOnlyTakenAsJSON(t *testing.T) {
	r := luciRig(t)
	code := r.mint(r.luciSession("root", adminPW), "root")
	q, _ := http.NewRequest(http.MethodPost, "/api/login/luci", strings.NewReader("code="+code))
	q.RemoteAddr = lan
	q.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, q)
	wantError(t, rec, http.StatusUnsupportedMediaType, "json", "a plain form")
	wantError(t, r.do(http.MethodGet, "/api/login/luci?code="+code, lan, ""), http.StatusMethodNotAllowed, "method not allowed", "a GET with the code in the query")
	wantError(t, r.do(http.MethodPost, "/api/login/luci", lan, codeBody("../../etc/passwd")), http.StatusUnauthorized, "login", "a malformed code")
	if rec := r.do(http.MethodPost, "/api/login/luci", lan, codeBody(code)); rec.Code != http.StatusOK {
		t.Fatalf("the code after the refusals: %d", rec.Code)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.logs {
		if strings.Contains(l, code) {
			t.Fatalf("a log line carries the code: %q", l)
		}
	}
}

// A guesser is limited like a password guesser.
func TestHandoffRedeemsAreRateLimited(t *testing.T) {
	r := luciRig(t)
	for i := 0; i < loginBurst; i++ {
		r.do(http.MethodPost, "/api/login/luci", lan, codeBody(strings.Repeat("0", 32)))
	}
	wantError(t, r.do(http.MethodPost, "/api/login/luci", lan, codeBody(strings.Repeat("0", 32))), http.StatusTooManyRequests, "rate", "past the burst")
}

// A handed-off session is kept across a restart as a lent one, so a later
// logout still leaves LuCI's session alone.
func TestALentSessionStaysLentAcrossARestart(t *testing.T) {
	r := luciRig(t)
	r.gate.Store = t.TempDir() + "/s/sessions.json"
	r.h = r.gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { w.WriteHeader(http.StatusOK) }))
	luci := r.luciSession("root", adminPW)
	rec := r.do(http.MethodPost, "/api/login/luci", lan, codeBody(r.mint(luci, "root")))
	var ans struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(rec.Body.Bytes(), &ans)
	h := cookieOf(rec)
	// The restart: a new gate over the same file.
	r.h = r.gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { w.WriteHeader(http.StatusOK) }))
	if out := r.do(http.MethodPost, "/api/logout", lan, "", withCookie(h), withCSRF(ans.CSRF)); out.Code != http.StatusOK {
		t.Fatalf("logout after the restart: %d %s", out.Code, out.Body)
	}
	if r.ubus.live(luci) == nil {
		t.Fatal("a logout after a restart ended the LuCI session")
	}
	_ = time.Second
}
