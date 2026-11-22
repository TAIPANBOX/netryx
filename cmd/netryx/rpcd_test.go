package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func (r *ctlRig) rpcd(stdin string, args ...string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = runRpcd(r.sock, args, strings.NewReader(stdin), &o, &e)
	return code, o.String(), e.String()
}

// rpcd asks a plugin what it offers with `list`: the six reads of `netryx
// ctl`, apps and usage taking a MAC as a string, and nothing that writes.
func TestTheUbusObjectOffersTheSixReadsOnly(t *testing.T) {
	var o, e bytes.Buffer
	code := runRpcd(filepath.Join(t.TempDir(), "none"), []string{"list"}, strings.NewReader(""), &o, &e)
	if code != 0 || e.Len() != 0 {
		t.Fatalf("list: exit %d, stderr %q", code, e.String())
	}
	var got map[string]map[string]string
	if err := json.Unmarshal(o.Bytes(), &got); err != nil {
		t.Fatalf("list is not a JSON object of methods: %v: %s", err, o.String())
	}
	want := map[string]map[string]string{
		"status": {}, "devices": {}, "rules": {}, "alerts": {},
		"apps": {"mac": "str"}, "usage": {"mac": "str"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("list = %v, want %v", got, want)
	}
}

// A call answers what `netryx ctl` reads, as one JSON object: ubus carries
// only objects, so the API's answer sits under the method's name.
func TestAUbusCallAnswersWhatCtlReads(t *testing.T) {
	r := newCtlRig(t, confWithRule)
	for _, tc := range []struct {
		method, args, path string
	}{
		{"status", "{}", "/api/overview"},
		{"devices", "", "/api/devices"},
		{"rules", "{}", "/api/rules"},
		{"alerts", "{}", "/api/alerts"},
		{"apps", `{"mac":"02:4e:58:00:00:03"}`, "/api/devices/02:4e:58:00:00:03/apps?range=24h"},
		{"usage", `{"mac":"02-4E-58-00-00-03"}`, "/api/usage?mac=02:4e:58:00:00:03"},
	} {
		code, out, errOut := r.rpcd(tc.args, "call", tc.method)
		if code != 0 || errOut != "" {
			t.Fatalf("call %s: exit %d, stderr %q", tc.method, code, errOut)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 1 {
			t.Fatalf("call %s is not one object under the method: %v: %s", tc.method, err, out)
		}
		var want any
		if err := json.Unmarshal([]byte(r.direct(tc.path)), &want); err != nil {
			t.Fatal(err)
		}
		// The two lists come keyed by each row's own id (see the next test).
		if key, ok := map[string]string{"devices": "mac", "rules": "id"}[tc.method]; ok {
			m := map[string]any{}
			for _, row := range want.([]any) {
				m[row.(map[string]any)[key].(string)] = row
			}
			want = m
		}
		if !reflect.DeepEqual(got[tc.method], want) {
			t.Errorf("call %s answered %v, the API answers %v", tc.method, got[tc.method], want)
		}
	}
	_, out, _ := r.rpcd("{}", "call", "rules")
	if !strings.Contains(out, `"id":"noyt"`) {
		t.Errorf("the rule is not in the rules call: %s", out)
	}
}

// openwrt-mcp cuts every array of an answer over 8 KiB to 16 elements and
// keeps every key of an object, so the devices and the rules come as objects
// keyed by MAC and by id: a house of 40 devices reaches the agent whole.
func TestTheDevicesAndRulesComeKeyedSoOpenwrtMcpCutsNoRow(t *testing.T) {
	r := newCtlRig(t, confWithRule)
	_, out, _ := r.rpcd("{}", "call", "devices")
	var devs map[string]map[string]any
	if err := json.Unmarshal([]byte(out), &devs); err != nil {
		t.Fatalf("devices: %v: %s", err, out)
	}
	if d := devs["devices"]["02:4e:58:00:00:03"]; d == nil || d.(map[string]any)["mac"] != "02:4e:58:00:00:03" {
		t.Errorf("the phone is not under its MAC: %s", out)
	}
	_, out, _ = r.rpcd("{}", "call", "rules")
	var rules map[string]map[string]any
	if err := json.Unmarshal([]byte(out), &rules); err != nil || rules["rules"]["noyt"] == nil {
		t.Errorf("the rule is not under its id: %v: %s", err, out)
	}
	// Forty rows keyed stay forty, where a list would be cut to sixteen.
	list := "["
	for i := 0; i < 40; i++ {
		if i > 0 {
			list += ","
		}
		list += fmt.Sprintf(`{"mac":"02:4e:58:00:01:%02x","name":"%s"}`, i, strings.Repeat("n", 300))
	}
	k, err := keyed(json.RawMessage(list+"]"), "mac")
	var m map[string]any
	if err != nil || json.Unmarshal(k, &m) != nil || len(m) != 40 {
		t.Errorf("keyed forty rows: %v, %d keys", err, len(m))
	}
	for _, bad := range []string{`[{"name":"x"}]`, `[{"mac":"a"},{"mac":"a"}]`, `{"mac":"a"}`} {
		if _, err := keyed(json.RawMessage(bad), "mac"); err == nil {
			t.Errorf("keyed %s: no error", bad)
		}
	}
}

// An answer larger than ubus carries is an error object, never a message
// ubusd would drop with rpcd's connection.
func TestAnAnswerLargerThanUbusCarriesIsAnError(t *testing.T) {
	sdir, err := os.MkdirTemp("", "nx")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sdir)
	sock := filepath.Join(sdir, "s")
	l, err := listenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	big := `{"x":"` + strings.Repeat("a", 300<<10) + `"}`
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, big) })}
	go srv.Serve(l)
	defer srv.Close()
	var o, e bytes.Buffer
	runRpcd(sock, []string{"call", "status"}, strings.NewReader("{}"), &o, &e)
	var got map[string]string
	if json.Unmarshal(o.Bytes(), &got) != nil || !strings.Contains(got["error"], "more than ubus carries") {
		t.Errorf("a 300 KiB answer: %.200s", o.String())
	}
}

// What is refused is refused before anything is sent, and the agent reads why
// as an error object, in words rather than a bare ubus status.
func TestAUbusCallRefusesAnythingElseBeforeAsking(t *testing.T) {
	dead := filepath.Join(t.TempDir(), "none")
	for _, tc := range []struct {
		args  []string
		stdin string
	}{
		{nil, ""},
		{[]string{"call"}, "{}"},
		{[]string{"call", "serve"}, "{}"},
		{[]string{"call", "status", "extra"}, "{}"},
		{[]string{"list", "extra"}, ""},
		{[]string{"call", "apps"}, "{}"},
		{[]string{"call", "apps"}, `{"mac":"../rules"}`},
		{[]string{"call", "apps"}, `{"mac":"02:4e:58:00:00:03/live"}`},
		{[]string{"call", "apps"}, `{"mac":7}`},
		{[]string{"call", "usage"}, `{"mac":"02:4e:58:00:00:03","range":"7d"}`},
		{[]string{"call", "status"}, `{"x":1}`},
		{[]string{"call", "status"}, `not json`},
		{[]string{"call", "status"}, `[]`},
		// Valid in every way but its size.
		{[]string{"call", "apps"}, `{"mac":"02:4e:58:00:00:03"` + strings.Repeat(" ", 8192) + `}`},
	} {
		var o, e bytes.Buffer
		code := runRpcd(dead, tc.args, strings.NewReader(tc.stdin), &o, &e)
		var got map[string]string
		if err := json.Unmarshal(o.Bytes(), &got); err != nil || got["error"] == "" || len(got) != 1 {
			t.Errorf("%q %q: exit %d, stdout %q: not one error object", tc.args, tc.stdin, code, o.String())
			continue
		}
		if strings.HasPrefix(tc.stdin, `{"mac":"02:4e:58:00:00:03"  `) && !strings.Contains(got["error"], "larger than") {
			t.Errorf("an oversized call: %q", got["error"])
		}
		if code != 0 || strings.Contains(got["error"], "does not answer") {
			t.Errorf("%q %q: exit %d, error %q: it reached for the socket", tc.args, tc.stdin, code, got["error"])
		}
	}
}

// A service that is not running, or an API error, is said as an error object,
// never an empty answer the agent would read as an empty house.
func TestAUbusCallSaysWhenTheServiceCannotAnswer(t *testing.T) {
	var o, e bytes.Buffer
	runRpcd(filepath.Join(t.TempDir(), "none"), []string{"call", "rules"}, strings.NewReader("{}"), &o, &e)
	var got map[string]string
	if json.Unmarshal(o.Bytes(), &got) != nil || !strings.Contains(got["error"], "does not answer") {
		t.Errorf("no service: %q", o.String())
	}
	r := newCtlRig(t, "")
	_, out, _ := r.rpcd(`{"mac":"02:4e:58:00:00:99"}`, "call", "apps")
	got = nil
	if json.Unmarshal([]byte(out), &got) != nil || got["error"] == "" {
		t.Errorf("API error: %q", out)
	}
}

// The ubus object is root's: the console's rpcd ACL grants no call of it, so a
// LuCI login reaches it no more than the socket.
func TestTheConsolesACLGrantsNoCallOfTheUbusObject(t *testing.T) {
	b, err := os.ReadFile("../../package/netryx/files/netryx.acl.json")
	if err != nil {
		t.Fatal(err)
	}
	var acl map[string]struct {
		Read, Write struct {
			Ubus map[string]any `json:"ubus"`
		}
	}
	if err := json.Unmarshal(b, &acl); err != nil {
		t.Fatal(err)
	}
	for name, g := range acl {
		for _, u := range []map[string]any{g.Read.Ubus, g.Write.Ubus} {
			for obj := range u {
				// rpcd matches ACL object names as patterns.
				if m, _ := path.Match(obj, "netryx"); m {
					t.Errorf("ACL group %s grants ubus %q", name, obj)
				}
			}
		}
	}
}
