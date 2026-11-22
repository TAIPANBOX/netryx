package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// ctlRig is a console answering on a socket in a temporary directory, with
// the rule config the test gives it, and the stand-in for uci and nft.
type ctlRig struct {
	c    *console
	rt   *router
	sock string
}

func newCtlRig(t *testing.T, conf string) *ctlRig {
	t.Helper()
	rt := &router{}
	o := rulesOptions(t, conf, rt)
	// One known country, so XX is a country that does not exist; one lease,
	// so the phone is a device the house knows.
	dir := t.TempDir()
	o.Geo = filepath.Join(dir, "ip2asn.tsv")
	write(t, o.Geo, "1.1.1.0\t1.1.1.255\t13335\tUS\tCLOUDFLARENET\n")
	o.Leases = filepath.Join(dir, "dhcp.leases")
	write(t, o.Leases, "1893456000 02:4e:58:00:00:03 192.168.50.30 phone *\n")
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	// What serve's start does once: read the sections.
	if err := c.rules.Reload(); err != nil {
		t.Fatal(err)
	}
	// A unix socket path is limited to about a hundred bytes, which a test's
	// temporary directory name can exceed.
	sdir, err := os.MkdirTemp("", "nx")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(sdir, "s")
	closeSock, err := c.serveSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSock(); os.RemoveAll(sdir) })
	return &ctlRig{c: c, rt: rt, sock: sock}
}

func (r *ctlRig) ctl(args ...string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = runCtl(r.sock, args, &o, &e)
	return code, o.String(), e.String()
}

func (r *ctlRig) direct(path string) string {
	rec := httptest.NewRecorder()
	r.c.api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String()
}

const confWithRule = `
config device 'd_024e58000003'
	option mac '02:4e:58:00:00:03'
	option name 'Кухонний планшет'

config rule 'noyt'
	option action 'block'
	option what 'all'
	option who 'device'
	list member '02:4e:58:00:00:03'
`

// `netryx ctl` prints what the API answers: the agent reads the same JSON the
// page does, from the socket.
func TestCtlPrintsTheAPIsAnswer(t *testing.T) {
	r := newCtlRig(t, confWithRule)
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"devices"}, "/api/devices"},
		{[]string{"rules"}, "/api/rules"},
		{[]string{"status"}, "/api/overview"},
		{[]string{"apps", "02:4e:58:00:00:03"}, "/api/devices/02:4e:58:00:00:03/apps?range=24h"},
		// Any spelling of a MAC is put in the one form the API keeps.
		{[]string{"apps", "02-4E-58-00-00-03"}, "/api/devices/02:4e:58:00:00:03/apps?range=24h"},
		{[]string{"usage", "02:4E:58:00:00:03"}, "/api/usage?mac=02:4e:58:00:00:03"},
	} {
		code, out, errOut := r.ctl(tc.args...)
		if code != 0 || errOut != "" {
			t.Fatalf("ctl %v: exit %d, stderr %q", tc.args, code, errOut)
		}
		want := r.direct(tc.path)
		if strings.TrimSpace(out) != strings.TrimSpace(want) || want == "" {
			t.Errorf("ctl %v printed %q, the API answers %q", tc.args, out, want)
		}
	}
	_, out, _ := r.ctl("rules")
	if !strings.Contains(out, `"id":"noyt"`) {
		t.Errorf("the rule is not in `ctl rules`: %s", out)
	}
}

// An API error is the exit status and stderr, nothing on stdout.
func TestCtlExitsOneWithTheAPIsError(t *testing.T) {
	r := newCtlRig(t, "")
	code, out, errOut := r.ctl("apps", "02:4e:58:00:00:99")
	if code != 1 || out != "" || errOut == "" {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	// A dead socket is an error too, not a silent empty answer.
	var o, e bytes.Buffer
	if c := runCtl(filepath.Join(t.TempDir(), "none"), []string{"rules"}, &o, &e); c != 1 || e.Len() == 0 || o.Len() != 0 {
		t.Errorf("no socket: exit %d, stdout %q, stderr %q", c, o.String(), e.String())
	}
}

// The forms are exec argv forms for openwrt-mcp: nothing else is accepted,
// and what is refused is refused before anything is sent.
func TestCtlRefusesOtherArguments(t *testing.T) {
	dead := filepath.Join(t.TempDir(), "none")
	for _, args := range [][]string{
		nil,
		{"help"},
		{"devices", "extra"},
		{"rules", "-v"},
		{"status", "--json"},
		{"apps"},
		{"apps", "02:4e:58:00:00:03", "7d"},
		{"apps", "../rules"},
		{"apps", "02:4e:58:00:00:03/live"},
		{"apps", "02:4e:58:00:00:03?x=1"},
		{"apps", "02:4e:58:00:00:0"},
		{"apps", "zz:4e:58:00:00:03"},
		{"/api/rules"},
		{"rules;id"},
	} {
		var o, e bytes.Buffer
		code := runCtl(dead, args, &o, &e)
		// With a dead socket, an accepted form would say it cannot connect;
		// a refused one says how it is used and never tries.
		if code != 1 || o.Len() != 0 || !strings.Contains(e.String(), "usage") || strings.Contains(e.String(), "does not answer") {
			t.Errorf("ctl %q: exit %d, stdout %q, stderr %q", args, code, o.String(), e.String())
		}
	}
}

// The section the agent wrote is wrong (a country that does not exist): the
// rule is kept, inactive, and `ctl rules` says why, so the agent can fix it.
func TestAMalformedAgentRuleShowsItsReasonInCtl(t *testing.T) {
	r := newCtlRig(t, `
config rule 'badcountry'
	option action 'block'
	option what 'country'
	list target 'XX'
	option who 'all'

config rule 'fine'
	option action 'block'
	option what 'all'
	option who 'all'
`)
	code, out, _ := r.ctl("rules")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, `"id":"badcountry"`) || !strings.Contains(out, `"error":"unknown country XX"`) {
		t.Errorf("the reason is not shown: %s", out)
	}
	if !strings.Contains(out, `"id":"badcountry","enabled":true,"what":"country","targets":["XX"],"who":"all","members":[],"group":"","days":[],"from":"","to":"","until":null,"activeNow":false,"hits":0,"applied":false`) {
		t.Errorf("a rejected rule must not read as applied: %s", out)
	}
}

// The socket is root's alone from the first moment: made under a umask that
// would make it wide open, it is still 0600, and owned by whoever started
// the service.
func TestTheSocketIsRootOnly(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	r := newCtlRig(t, "")
	fi, err := os.Lstat(r.sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is %v, not a socket", r.sock, fi.Mode())
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("the socket is %o, want 600", perm)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		t.Errorf("the socket belongs to uid %v, not to the service's %d", fi.Sys(), os.Getuid())
	}
}

// A socket a crash left behind is replaced; a file that is not a socket is
// not touched; closing removes the socket.
func TestAStaleSocketIsReplacedAndAFileIsNotTouched(t *testing.T) {
	c, err := buildConsole(rulesOptions(t, "", &router{}))
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := os.MkdirTemp("", "nx")
	defer os.RemoveAll(dir)

	stale := filepath.Join(dir, "stale")
	l, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close() // a killed service leaves the file
	closeStale, err := c.serveSocket(stale)
	if err != nil {
		t.Fatalf("a stale socket was not replaced: %v", err)
	}
	var o, e bytes.Buffer
	if code := runCtl(stale, []string{"rules"}, &o, &e); code != 0 {
		t.Errorf("the replacement does not answer: %s", e.String())
	}
	closeStale()
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Errorf("the socket outlived the service: %v", err)
	}

	file := filepath.Join(dir, "file")
	os.WriteFile(file, []byte("mine"), 0o600)
	if _, err := c.serveSocket(file); err == nil {
		t.Error("a regular file in the socket's place was replaced")
	}
	if b, _ := os.ReadFile(file); string(b) != "mine" {
		t.Errorf("the file was touched: %q", b)
	}
}

// The socket passes the login and the Host check, so what it answers is the
// whole of its protection: four reads and nothing else. A write from the
// agent is a UCI change, never a call here.
func TestTheSocketServesNoWriteRoute(t *testing.T) {
	r := newCtlRig(t, confWithRule)
	cl := socketClient(r.sock)
	do := func(method, path, body string) int {
		t.Helper()
		req, _ := http.NewRequest(method, "http://netryx"+path, strings.NewReader(body))
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/rules", `{"what":"all","who":"all"}`, 405},
		{"PUT", "/api/rules/noyt", `{"what":"all","who":"all"}`, 404},
		{"DELETE", "/api/rules/noyt", "", 404},
		{"POST", "/api/allow", `{}`, 404},
		{"POST", "/api/devices", `{}`, 405},
		{"POST", "/api/devices/02:4e:58:00:00:03/apps?range=24h", `{}`, 405},
		{"PUT", "/api/devices/02:4e:58:00:00:03", `{"name":"x"}`, 404},
		{"POST", "/api/devices/02:4e:58:00:00:03/conns/cut", `{}`, 404},
		{"GET", "/api/session", "", 404},
		{"POST", "/api/session", `{}`, 404},
		{"GET", "/api/groups", "", 404},
		{"GET", "/api/flows", "", 404},
		{"GET", "/api/devices/02:4e:58:00:00:03", "", 404},
		{"GET", "/api/devices/02:4e:58:00:00:03/live", "", 404},
		{"GET", "/metrics", "", 404},
		{"GET", "/", "", 404},
		{"GET", "/api/devices", "", 200},
		{"GET", "/api/rules", "", 200},
		{"GET", "/api/overview", "", 200},
		{"GET", "/api/devices/02:4e:58:00:00:03/apps?range=24h", "", 200},
		{"GET", "/api/devices/02:4e:58:00:00:03/apps", "", 404},
		{"GET", "/api/devices/02:4e:58:00:00:03/apps?range=30d", "", 404},
		{"GET", "/api/devices/02:4e:58:00:00:03/apps?range=24h&split=app", "", 404},
		{"GET", "/api/devices/::::::::::::::::/apps?range=24h", "", 404},
		{"GET", "/api/devices/02:4e:58:00:00:0:3/apps?range=24h", "", 404},
		{"GET", "/api/usage?mac=02:4e:58:00:00:03", "", 200},
		{"POST", "/api/usage?mac=02:4e:58:00:00:03", `{}`, 405},
		{"GET", "/api/usage", "", 404},
		{"GET", "/api/usage?mac=02:4e:58:00:00:03&x=1", "", 404},
		{"GET", "/api/filters", "", 404},
		{"PUT", "/api/filters", `{"forceDns":false}`, 404},
		{"GET", "/api/adblock", "", 404},
		{"PUT", "/api/adblock/file_url_0", `{}`, 404},
	} {
		if got := do(tc.method, tc.path, tc.body); got != tc.want {
			t.Errorf("%s %s over the socket: %d, want %d", tc.method, tc.path, got, tc.want)
		}
	}
	for _, c := range r.rt.all() {
		if c.name == "uci" {
			t.Errorf("a refused write reached uci: %+v", c)
		}
	}
}

// A bigger answer than ctl will read is an error, not a cut JSON.
func TestCtlRefusesAnAnswerLargerThanItReads(t *testing.T) {
	dir, _ := os.MkdirTemp("", "nx")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	l, err := listenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), ctlMaxAnswer+1))
	})}
	go srv.Serve(l)
	defer srv.Close()
	var o, e bytes.Buffer
	if code := runCtl(sock, []string{"rules"}, &o, &e); code != 1 || o.Len() != 0 || !strings.Contains(e.String(), "larger") {
		t.Errorf("exit %d, stdout %d bytes, stderr %q", code, o.Len(), e.String())
	}
}

// A device section the agent wrote (its name starts with agent_) names no
// device: the console's own name and group for a MAC come from the owner's
// sections only.
func TestAnAgentDeviceSectionDoesNotNameADevice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netryx")
	write(t, path, `
config device 'd_024e58000003'
	option mac '02:4e:58:00:00:03'
	option name 'iPhone'
	option group 'kids'

config device 'agent_x'
	option mac '02:4e:58:00:00:03'
	option name 'Renamed'
	option group 'agent_g'

config device 'agent_y'
	option mac '02:4e:58:00:00:07'
	option name 'Invented'
`)
	got, err := deviceSections(path)
	if err != nil {
		t.Fatal(err)
	}
	if l := got["02:4e:58:00:00:03"]; l.Name != "iPhone" || l.Group != "kids" {
		t.Errorf("the owner's device was renamed: %+v", l)
	}
	if _, ok := got["02:4e:58:00:00:07"]; ok {
		t.Errorf("an agent section named a device: %+v", got)
	}
}

// The agent reads the alerts; it cannot change their settings or the
// quarantine over the socket.
func TestCtlAlertsIsReadOnly(t *testing.T) {
	if p, ok := ctlPath([]string{"alerts"}); !ok || p != "/api/alerts" {
		t.Fatalf("ctl alerts reads %q, %v; want /api/alerts", p, ok)
	}
	if _, ok := ctlPath([]string{"alerts", "x"}); ok {
		t.Fatal("ctl alerts takes no argument")
	}
	r := newCtlRig(t, confWithRule)
	cl := socketClient(r.sock)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/alerts", 200},
		{"PUT", "/api/alerts", 405},
		{"PUT", "/api/quarantine", 404},
		{"PUT", "/api/devices/02:4e:58:00:00:03", 404},
	} {
		req, _ := http.NewRequest(tc.method, "http://netryx"+tc.path, strings.NewReader(`{}`))
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s over the socket: %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
}
