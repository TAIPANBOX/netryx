package auth

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func withActivity(sec string) opt { return func(q *http.Request) { q.Header.Set(ActivityHeader, sec) } }

func idleRig(t *testing.T) *rig {
	r := luciRig(t)
	r.gate.IdleLimit = time.Hour
	r.ubus.timeout = 24 * time.Hour // rpcd itself would keep the sessions alive
	return r
}

// A page left open polls every few seconds; that alone keeps nothing alive.
// An hour with nobody at the page, and the session is over.
func TestPollingAloneDoesNotKeepASessionAlive(t *testing.T) {
	r := idleRig(t)
	sid, _ := r.login("viewer", viewerPW, lan)
	rpcSID := r.ubus.lastSID()
	for i := 0; i < 59; i++ {
		r.clock.Add(time.Minute)
		if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid), withActivity("3600")); rec.Code != http.StatusOK {
			t.Fatalf("minute %d: %d", i+1, rec.Code)
		}
	}
	r.clock.Add(time.Minute + time.Second)
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)), http.StatusUnauthorized, "idle", "an hour of polling with nobody at the page")
	if r.ubus.live(rpcSID) != nil {
		t.Error("netryx's own rpcd session was left open after it went idle")
	}
}

// Somebody at the page (a mouse, a key, the tab coming into focus within the
// last minute) keeps the session going as long as they are there.
func TestActivityKeepsTheSessionAlive(t *testing.T) {
	r := idleRig(t)
	sid, _ := r.login("viewer", viewerPW, lan)
	for i := 0; i < 180; i++ {
		r.clock.Add(time.Minute)
		if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid), withActivity("20")); rec.Code != http.StatusOK {
			t.Fatalf("minute %d with somebody at the page: %d", i+1, rec.Code)
		}
	}
}

// Activity reported after the hour does not bring an idle session back, and a
// report of activity long ago refreshes nothing.
func TestActivityAfterTheLimitDoesNotReviveTheSession(t *testing.T) {
	r := idleRig(t)
	sid, _ := r.login("viewer", viewerPW, lan)
	r.clock.Add(30 * time.Minute)
	r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid), withActivity("1800")) // half an hour ago: refreshes nothing
	r.clock.Add(31 * time.Minute)
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid), withActivity("0")), http.StatusUnauthorized, "idle", "activity after an idle hour")
	for _, bad := range []string{"-5", "x", "99999999999999999999"} {
		r2 := idleRig(t)
		s2, _ := r2.login("viewer", viewerPW, lan)
		r2.clock.Add(59 * time.Minute)
		r2.do(http.MethodGet, "/api/overview", lan, "", withCookie(s2), withActivity(bad))
		r2.clock.Add(2 * time.Minute)
		wantError(t, r2.do(http.MethodGet, "/api/overview", lan, "", withCookie(s2)), http.StatusUnauthorized, "idle", "after a malformed activity "+bad)
	}
}

// A change is somebody at the page.
func TestAWriteIsActivity(t *testing.T) {
	r := idleRig(t)
	sid, csrf := r.login("root", adminPW, lan)
	r.clock.Add(50 * time.Minute)
	if rec := r.do(http.MethodPut, "/api/rules/x", lan, `{"a":1}`, withCookie(sid), withCSRF(csrf)); rec.Code != http.StatusOK {
		t.Fatalf("a write: %d %s", rec.Code, rec.Body)
	}
	r.clock.Add(50 * time.Minute)
	if rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)); rec.Code != http.StatusOK {
		t.Fatalf("50 minutes after a write: %d", rec.Code)
	}
}

// A session from LuCI that goes idle is let go, never ended: LuCI's own
// timeout then ends it, since the console no longer touches it in rpcd.
func TestAnIdleLentSessionIsLetGoNotEnded(t *testing.T) {
	r := idleRig(t)
	luci := r.luciSession("root", adminPW)
	rec := r.do(http.MethodPost, "/api/login/luci", lan, codeBody(r.mint(luci, "root")), withNonce(testNonce))
	h := cookieOf(rec)
	r.clock.Add(time.Hour + time.Second)
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(h)), http.StatusUnauthorized, "idle", "an idle lent session")
	if r.ubus.live(luci) == nil {
		t.Fatal("the console ended LuCI's session")
	}
}

// The time of the last activity is kept across a restart: a restart does not
// hand an idle session a fresh hour.
func TestIdleTimeSurvivesARestart(t *testing.T) {
	r := idleRig(t)
	r.gate.Store = t.TempDir() + "/s/sessions.json"
	r.h = r.gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { w.WriteHeader(http.StatusOK) }))
	sid, _ := r.login("viewer", viewerPW, lan)
	r.clock.Add(50 * time.Minute)
	r.h = r.gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { w.WriteHeader(http.StatusOK) }))
	r.clock.Add(11 * time.Minute)
	rec := r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid))
	var e struct{ Error string }
	json.Unmarshal(rec.Body.Bytes(), &e)
	if rec.Code != http.StatusUnauthorized || e.Error != "idle" {
		t.Fatalf("61 idle minutes across a restart: %d %s", rec.Code, rec.Body)
	}
}

// Only a change that carries the session's token is use: a request without
// it is refused, and refreshes nothing.
func TestAChangeWithoutTheTokenIsNotActivity(t *testing.T) {
	r := idleRig(t)
	sid, _ := r.login("root", adminPW, lan)
	r.clock.Add(50 * time.Minute)
	wantError(t, r.do(http.MethodPost, "/api/rules", lan, `{"a":1}`, withCookie(sid)), http.StatusForbidden, "forbidden", "a change without the token")
	r.clock.Add(11 * time.Minute)
	wantError(t, r.do(http.MethodGet, "/api/overview", lan, "", withCookie(sid)), http.StatusUnauthorized, "idle", "61 minutes after a change without the token")
}
