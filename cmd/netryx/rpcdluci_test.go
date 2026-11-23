package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netryx/internal/handoff"
	"netryx/internal/ramfile"
)

const luciSID = "0123456789abcdef0123456789abcdef"

// luciNonce is what LuCI's page put in the browser's cookie and sends along.
const luciNonce = "fedcba9876543210fedcba9876543210"

type luciRig struct {
	dir, state string
	users      map[string]string
	asked      []string
	now        time.Time
}

func newLuciRig(t *testing.T) *luciRig {
	base := t.TempDir()
	r := &luciRig{dir: filepath.Join(base, "luci"), state: filepath.Join(base, "run", "console.json"), users: map[string]string{luciSID: "root"}, now: time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)}
	return r
}

func (r *luciRig) deps() luciDeps {
	return luciDeps{
		Dir: r.dir, State: r.state, Now: func() time.Time { return r.now },
		User: func(sid string) (string, error) {
			r.asked = append(r.asked, sid)
			u, ok := r.users[sid]
			if !ok {
				return "", errors.New("no such session")
			}
			return u, nil
		},
	}
}

func (r *luciRig) call(t *testing.T, args []string, stdin string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if code := runRpcdLuci(r.deps(), args, strings.NewReader(stdin), &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("answer is not one JSON object: %v %q", err, out.String())
	}
	return v
}

func (r *luciRig) listening(t *testing.T, listen string, https bool) {
	b, _ := json.Marshal(consoleState{Listen: listen, HTTPS: https})
	if err := ramfile.Write(r.state, b); err != nil {
		t.Fatal(err)
	}
}

func TestTheLuCIObjectOffersTheHandoffOnly(t *testing.T) {
	r := newLuciRig(t)
	got := r.call(t, []string{"list"}, "")
	sig, _ := got["handoff"].(map[string]any)
	if len(got) != 1 || len(sig) != 1 || sig["nonce"] != "str" {
		t.Fatalf("list %v, want the method handoff alone, taking the browser's nonce", got)
	}
	for _, m := range []string{"status", "devices", "login", "../x"} {
		if v := r.call(t, []string{"call", m}, "{}"); v["error"] == nil || v["code"] != nil {
			t.Errorf("call %s: %v, want an error and no code", m, v)
		}
	}
}

func TestAHandoffIsMintedForTheCallersOwnSession(t *testing.T) {
	r := newLuciRig(t)
	r.listening(t, "192.168.1.1:8788", true)
	v := r.call(t, []string{"call", "handoff"}, `{"ubus_rpc_session":"`+luciSID+`","nonce":"`+luciNonce+`"}`)
	code, _ := v["code"].(string)
	if len(code) != 32 {
		t.Fatalf("answer %v, want a 32-hex code", v)
	}
	if v["scheme"] != "https" || v["port"] != float64(8788) || v["loopback"] != false {
		t.Fatalf("answer %v, want https on 8788, reachable from the house", v)
	}
	if len(r.asked) != 1 || r.asked[0] != luciSID {
		t.Fatalf("asked rpcd about %v, want the caller's session", r.asked)
	}
	if _, err := handoff.Redeem(r.dir, code, "0000000000000000000000000000000f", r.now); err == nil {
		t.Fatal("the code opened for another browser's nonce")
	}
	v = r.call(t, []string{"call", "handoff"}, `{"ubus_rpc_session":"`+luciSID+`","nonce":"`+luciNonce+`"}`)
	code, _ = v["code"].(string)
	c, err := handoff.Redeem(r.dir, code, luciNonce, r.now)
	if err != nil || c.SID != luciSID || c.User != "root" {
		t.Fatalf("the code redeems as %+v (%v), want the caller's session and user", c, err)
	}
}

func TestAHandoffSaysWhenTheConsoleIsNotReachable(t *testing.T) {
	r := newLuciRig(t)
	in := `{"ubus_rpc_session":"` + luciSID + `","nonce":"` + luciNonce + `"}`
	// Not running: no code at all.
	if v := r.call(t, []string{"call", "handoff"}, in); v["code"] != nil || !strings.Contains(v["error"].(string), "not running") {
		t.Fatalf("no console: %v", v)
	}
	// On loopback only: the page is told so, with no code minted for nothing.
	r.listening(t, "127.0.0.1:8788", false)
	v := r.call(t, []string{"call", "handoff"}, in)
	if v["code"] != nil || v["loopback"] != true || v["port"] != float64(8788) {
		t.Fatalf("loopback console: %v, want loopback true and no code", v)
	}
	r.listening(t, "[::]:9443", false)
	if v := r.call(t, []string{"call", "handoff"}, in); v["scheme"] != "http" || v["port"] != float64(9443) || v["code"] == nil {
		t.Fatalf("http on all addresses: %v", v)
	}
}

func TestAHandoffWithoutAGoodSessionMintsNothing(t *testing.T) {
	r := newLuciRig(t)
	r.listening(t, "192.168.1.1:8788", true)
	for _, in := range []string{
		``,
		`{}`,
		`{"ubus_rpc_session":"00000000000000000000000000000000","nonce":"` + luciNonce + `"}`, // rpcd knows no such session
		`{"ubus_rpc_session":"../../etc"}`,
		`{"ubus_rpc_session":"` + luciSID + `","nonce":"` + luciNonce + `","user":"root"}`, // nothing else is taken
		`{"ubus_rpc_session":"` + luciSID + `"}`,                                           // no nonce: the browser cannot be bound
		`{"ubus_rpc_session":7}`,
		`not json`,
	} {
		if v := r.call(t, []string{"call", "handoff"}, in); v["code"] != nil || v["error"] == nil {
			t.Errorf("%s: %v, want an error and no code", in, v)
		}
	}
	// A malformed session is refused before rpcd is asked.
	for _, s := range r.asked {
		if s != "00000000000000000000000000000000" {
			t.Errorf("rpcd was asked about %q", s)
		}
	}
}
