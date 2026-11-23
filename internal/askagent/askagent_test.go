package askagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

const key1 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const key2 = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

// fakeAgent answers as Hermes's /v1/ask does, by the contract in
// hermes-openwrt docs/netryx-ask.md.
type fakeAgent struct {
	mu      sync.Mutex
	key     string
	status  int
	body    string
	retry   string
	got     []map[string]any
	headers []http.Header
	paths   []string
}

func (f *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	f.headers = append(f.headers, r.Header.Clone())
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	f.got = append(f.got, m)
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer "+f.key {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"code":"unauthorized","message":"The key is not the router's."}}`)
		return
	}
	if f.retry != "" {
		w.Header().Set("Retry-After", f.retry)
	}
	if f.status == 0 {
		io.WriteString(w, `{"answer":"The laptop downloaded 1.2 GB from a video site.","cost_usd":0.0031,"asks_left_this_hour":9,"budget_left_usd":0.4969}`)
		return
	}
	w.WriteHeader(f.status)
	io.WriteString(w, f.body)
}

type rig struct {
	agent *fakeAgent
	cfg   string
	key   string
	c     *Client
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{agent: &fakeAgent{key: key1}, cfg: filepath.Join(dir, "hermes"), key: filepath.Join(dir, "api.key")}
	srv := httptest.NewServer(r.agent)
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	r.writeConfig(t, "1", port, r.key)
	r.writeKey(t, key1+"\n", 0o600)
	r.c = &Client{Config: r.cfg}
	return r
}

func (r *rig) writeConfig(t *testing.T, enabled, port, keyFile string) {
	t.Helper()
	conf := "config hermes 'main'\n\toption profile 'owner'\n\nconfig api 'api'\n\toption enabled '" + enabled + "'\n\toption port '" + port + "'\n\toption key_file '" + keyFile + "'\n"
	if err := os.WriteFile(r.cfg, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) writeKey(t *testing.T, k string, mode os.FileMode) {
	t.Helper()
	os.Remove(r.key)
	if err := os.WriteFile(r.key, []byte(k), mode); err != nil {
		t.Fatal(err)
	}
	os.Chmod(r.key, mode)
}

func TestTheAgentIsOffUnlessHermesTurnsItOn(t *testing.T) {
	r := newRig(t)
	if ok, why := r.c.Available(); !ok {
		t.Fatalf("enabled with a good key: not available (%s)", why)
	}
	for _, name := range []string{"no Hermes", "questions off", "no key file", "a short key", "a key others read", "a key that is a link"} {
		rr := newRig(t)
		switch name {
		case "no Hermes":
			os.Remove(rr.cfg)
		case "questions off":
			rr.writeConfig(t, "0", "8642", rr.key)
		case "no key file":
			os.Remove(rr.key)
		case "a short key":
			rr.writeKey(t, "abc123\n", 0o600)
		case "a key others read":
			rr.writeKey(t, key1, 0o644)
		case "a key that is a link":
			real := rr.key + ".real"
			os.WriteFile(real, []byte(key1), 0o600)
			os.Remove(rr.key)
			os.Symlink(real, rr.key)
		}
		ok, why := rr.c.Available()
		if ok || !strings.HasPrefix(why, "agent not enabled on this router") {
			t.Errorf("%s: available %v (%q), want not enabled", name, ok, why)
		}
		if _, err := rr.c.Ask(context.Background(), "What made this spike?", ""); !isCode(err, "not_enabled") {
			t.Errorf("%s: ask gave %v, want not_enabled", name, err)
		}
		if len(rr.agent.paths) != 0 {
			t.Errorf("%s: the agent was asked anyway", name)
		}
	}
}

func isCode(err error, code string) bool {
	var ref *Refusal
	return errors.As(err, &ref) && ref.Code == code
}

func TestAQuestionCarriesTheKeyAndNothingButTheQuestionAndContext(t *testing.T) {
	r := newRig(t)
	a, err := r.c.Ask(context.Background(), "What made this spike?", "14:02-14:05 UTC, laptop, 1.2 GB")
	if err != nil {
		t.Fatal(err)
	}
	if a.Answer != "The laptop downloaded 1.2 GB from a video site." || a.CostUSD == nil || *a.CostUSD != 0.0031 || a.AsksLeft == nil || *a.AsksLeft != 9 || a.BudgetLeft == nil || *a.BudgetLeft != 0.4969 {
		t.Fatalf("answer %+v", a)
	}
	if r.agent.paths[0] != "POST /v1/ask" {
		t.Fatalf("asked %s", r.agent.paths[0])
	}
	h := r.agent.headers[0]
	if h.Get("Content-Type") != "application/json" || h.Get("Content-Length") == "" {
		t.Errorf("headers %v", h)
	}
	got := r.agent.got[0]
	if len(got) != 2 || got["question"] != "What made this spike?" || got["context"] != "14:02-14:05 UTC, laptop, 1.2 GB" {
		t.Errorf("body %v, want question and context only", got)
	}
	// No context: the key is left out, not sent empty.
	r.c.Ask(context.Background(), "Who is on my network?", "")
	if _, has := r.agent.got[1]["context"]; has || len(r.agent.got[1]) != 1 {
		t.Errorf("body %v, want the question alone", r.agent.got[1])
	}
}

// The key is read from its file for every question, so a key Hermes replaced
// needs no restart of netryx.
func TestTheKeyIsReadForEveryQuestion(t *testing.T) {
	r := newRig(t)
	if _, err := r.c.Ask(context.Background(), "one", ""); err != nil {
		t.Fatal(err)
	}
	r.agent.key = key2
	r.writeKey(t, key2, 0o600)
	if _, err := r.c.Ask(context.Background(), "two", ""); err != nil {
		t.Fatalf("after the key was replaced: %v", err)
	}
}

// Every refusal of the contract reaches the person in the agent's own words,
// with its code and, where it has one, when to try again.
func TestEveryRefusalIsPassedOnInTheAgentsWords(t *testing.T) {
	cases := []struct {
		status int
		code   string
		retry  int
	}{
		{400, "bad_request", 0}, {404, "not_found", 0}, {405, "method_not_allowed", 0},
		{409, "busy", 60}, {413, "too_large", 0}, {429, "hourly_limit", 701}, {429, "daily_budget", 3600},
		{502, "agent_failed", 0}, {503, "usage_unreadable", 0}, {504, "timeout", 0},
	}
	for _, c := range cases {
		r := newRig(t)
		msg := fmt.Sprintf("Words for %s.", c.code)
		r.agent.status = c.status
		body := fmt.Sprintf(`{"error":{"code":%q,"message":%q}}`, c.code, msg)
		if c.retry > 0 {
			body = fmt.Sprintf(`{"error":{"code":%q,"message":%q,"retry_after":%d}}`, c.code, msg, c.retry)
			// busy says when in the header alone; the limits in the body alone.
			if c.code == "busy" {
				body = fmt.Sprintf(`{"error":{"code":%q,"message":%q}}`, c.code, msg)
				r.agent.retry = strconv.Itoa(c.retry)
			}
		}
		r.agent.body = body
		_, err := r.c.Ask(context.Background(), "q", "")
		var ref *Refusal
		if !errors.As(err, &ref) || ref.Status != c.status || ref.Code != c.code || ref.Message != msg || ref.RetryAfter != c.retry {
			t.Errorf("%d %s: %+v (%v)", c.status, c.code, ref, err)
		}
	}
	// A key the agent does not take is its 401, said in its words: never the
	// console's own sign-in.
	r := newRig(t)
	r.agent.key = key2
	_, err := r.c.Ask(context.Background(), "q", "")
	if !isCode(err, "unauthorized") || strings.Contains(err.Error(), key1) {
		t.Errorf("another key: %v", err)
	}
	// An answer that is not the contract's shape is said plainly.
	r = newRig(t)
	r.agent.status, r.agent.body = 500, "<html>oops</html>"
	if _, err := r.c.Ask(context.Background(), "q", ""); !isCode(err, "agent_failed") {
		t.Errorf("a body not of the contract: %v", err)
	}
}

// A refused connection: the agent is not running, or its questions are off.
func TestARefusedConnectionIsAnAgentNotEnabled(t *testing.T) {
	r := newRig(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	r.writeConfig(t, "1", port, r.key)
	_, err := r.c.Ask(context.Background(), "q", "")
	var ref *Refusal
	if !errors.As(err, &ref) || ref.Code != "not_enabled" || !strings.HasPrefix(ref.Message, "agent not enabled on this router") {
		t.Fatalf("nothing listening: %+v (%v)", ref, err)
	}
}

// A question is held to the contract before anything is sent: 1 to 2000
// characters; a context past 8000 characters is cut to 8000, whole characters.
func TestAQuestionIsHeldToTheContractBeforeItIsSent(t *testing.T) {
	r := newRig(t)
	for _, q := range []string{"", "   ", strings.Repeat("ї", 2001)} {
		if _, err := r.c.Ask(context.Background(), q, ""); !isCode(err, "bad_request") {
			t.Errorf("question of %d characters: %v", utf8.RuneCountInString(q), err)
		}
	}
	if len(r.agent.paths) != 0 {
		t.Fatal("a refused question reached the agent")
	}
	if _, err := r.c.Ask(context.Background(), strings.Repeat("ї", 2000), strings.Repeat("a", 9000)); err != nil {
		t.Fatalf("2000 characters and a long context: %v", err)
	}
	ctx, _ := r.agent.got[0]["context"].(string)
	if n := utf8.RuneCountInString(ctx); n != MaxContext {
		t.Fatalf("context of %d characters, want %d", n, MaxContext)
	}
	// Characters of two bytes: the context is cut further, so the whole body
	// stays within 16384 bytes, and never inside a character.
	if _, err := r.c.Ask(context.Background(), strings.Repeat("ї", 2000), strings.Repeat("ї", 9000)); err != nil {
		t.Fatalf("a long context of two-byte characters: %v", err)
	}
	if n, _ := strconv.Atoi(r.agent.headers[1].Get("Content-Length")); n > MaxBody || n == 0 {
		t.Fatalf("body of %d bytes, want at most %d", n, MaxBody)
	}
	if ctx, _ := r.agent.got[1]["context"].(string); !utf8.ValidString(ctx) || ctx == "" {
		t.Fatal("the context was cut inside a character, or to nothing")
	}
}
