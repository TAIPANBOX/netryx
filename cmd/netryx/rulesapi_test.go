package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"netryx/internal/rules"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"netryx/internal/api"
	"netryx/internal/auth"
	"netryx/internal/ct"
	"netryx/internal/export"
	"netryx/internal/inventory"
	"netryx/internal/uciconf"
)

// router stands in for uci and nft: it records every command with its
// standard input, in order.
type router struct {
	mu    sync.Mutex
	calls []routerCall
	// changes is what `uci -q changes netryx` prints: uncommitted work.
	changes string
}

type routerCall struct {
	name  string
	args  []string
	stdin string
}

func (r *router) run(_ context.Context, _ []string, stdin, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, routerCall{name, args, stdin})
	if name == "uci" && len(args) > 1 && args[1] == "changes" {
		return []byte(r.changes), nil
	}
	return nil, nil
}

func (r *router) all() []routerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]routerCall(nil), r.calls...)
}

const confWithPhone = `
config device 'd_024e58000003'
	option mac '02:4e:58:00:00:03'
	option name 'Кухонний планшет'
`

func rulesOptions(t *testing.T, conf string, rt *router) Options {
	t.Helper()
	o := testOptions(t)
	o.RulesConfig = filepath.Join(t.TempDir(), "netryx")
	if err := os.WriteFile(o.RulesConfig, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	o.NFT = rt.run
	return o
}

// A write from the page is the batch the agent would write: `uci -q batch`
// with the text on standard input and nothing of it in argv, and the engine
// reloads after it, never before.
func TestTheConsoleWritesTheBatchOnStdinThenReloads(t *testing.T) {
	rt := &router{}
	c, err := buildConsole(rulesOptions(t, confWithPhone, rt))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	c.api.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/rules",
		strings.NewReader(`{"what":"all","who":"device","members":["02:4e:58:00:00:03"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/rules: %d %s", rec.Code, rec.Body)
	}
	var got struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &got)
	calls := rt.all()
	uci := -1
	for i, c := range calls {
		if c.name == "uci" && len(c.args) > 1 && c.args[1] == "batch" {
			if uci >= 0 {
				t.Fatal("uci ran twice")
			}
			uci = i
		}
	}
	if uci < 0 {
		t.Fatalf("uci never ran: %+v", calls)
	}
	u := calls[uci]
	if strings.Join(u.args, " ") != "-q batch" {
		t.Errorf("uci argv %v: the batch goes on stdin", u.args)
	}
	want := "delete netryx." + got.ID + "\nset netryx." + got.ID + "=rule\n"
	if !strings.HasPrefix(u.stdin, want) || !strings.HasSuffix(u.stdin, "commit netryx\n") || !strings.Contains(u.stdin, "set netryx."+got.ID+".member='02:4e:58:00:00:03'\n") {
		t.Errorf("stdin:\n%s", u.stdin)
	}
	reload := false
	for _, c := range calls[uci+1:] {
		if c.name == "nft" {
			reload = true
		}
	}
	if !reload {
		t.Errorf("the engine did not reload after the write: %+v", calls)
	}
	for _, c := range calls[:uci] {
		if c.name == "nft" && strings.Contains(c.stdin, got.ID) {
			t.Errorf("the rule reached nft before it was written: %+v", c)
		}
	}
}

// D1, measured on the Flint 2: the console's cut button reached the engine
// without the device's hardware address, so the engine could only delete the
// entry, and tcp_loose revived the connection. The button's path hands the
// MAC on, and the engine puts that flow into the owner's cut set before the
// delete.
func TestTheCutButtonKeepsTheFlowCut(t *testing.T) {
	rt := &router{}
	conf := filepath.Join(t.TempDir(), "netryx")
	if err := os.WriteFile(conf, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var deleted []ct.Tuple
	eng := rules.NewEngine(rules.EngineOptions{ConfigPath: conf, StateDir: t.TempDir(), Run: rt.run,
		Delete: func(tu ct.Tuple) error { deleted = append(deleted, tu); return nil }})
	if err := eng.Reload(); err != nil {
		t.Fatal(err)
	}
	ra := &rulesAPI{eng: eng, conf: conf, run: rt.run}
	if _, err := ra.Cut("02:4e:58:00:00:03", netip.MustParseAddrPort("192.168.50.13:44152"), netip.MustParseAddrPort("31.184.217.1:80"), 6, false); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0].SrcPort != 44152 {
		t.Fatalf("the entry is deleted: %+v", deleted)
	}
	want := "add element inet netryx_rules cut_v4 { 02:4e:58:00:00:03 . 31.184.217.1 . tcp . 44152 timeout 10m }"
	for _, c := range rt.all() {
		if c.name == "nft" && strings.Contains(c.stdin, want) {
			return
		}
	}
	t.Fatalf("the flow never reached the cut set (%q): %+v", want, rt.all())
}

func writeLabels(t *testing.T, path string, l map[string]inventory.Label) {
	t.Helper()
	b, _ := json.Marshal(l)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func uciOf(rt *router) string {
	for _, c := range rt.all() {
		if c.name == "uci" && len(c.args) > 1 && c.args[1] == "batch" {
			return c.stdin
		}
	}
	return ""
}

// R39: a device PUT never rewrites labels.json. The API carries a label's
// name into the change where UCI names nothing; the label itself stays, only
// shadowed by the UCI name, so an agent rollback that erases the section
// cannot erase the owner's name for good. What the adapter writes is exactly
// the change it was given.
func TestADevicePutLeavesLabelsJsonAlone(t *testing.T) {
	const mac, other = "02:4e:58:00:00:05", "02:4e:58:00:00:06"
	cases := []struct {
		name  string
		label inventory.Label
		set   map[string][]string
		stdin string
		notIn string
	}{
		{"name not invented", inventory.Label{Name: "Консоль"}, map[string][]string{"mac": {mac}, "group": {"kids"}},
			"set netryx.d_024e58000005.group='kids'\n", "Консоль"},
		{"name given", inventory.Label{Name: "Консоль"}, map[string][]string{"mac": {mac}, "name": {"Нова"}},
			"set netryx.d_024e58000005.name='Нова'\n", "Консоль"},
		{"group in the label", inventory.Label{Name: "Консоль", Group: "Діти"}, map[string][]string{"mac": {mac}, "group": {"kids"}},
			"set netryx.d_024e58000005.group='kids'\n", "Консоль"},
		{"no label", inventory.Label{}, map[string][]string{"mac": {mac}, "group": {"kids"}},
			"set netryx.d_024e58000005.group='kids'\n", ".name="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &router{}
			c, err := buildConsole(rulesOptions(t, "", rt))
			if err != nil {
				t.Fatal(err)
			}
			labels := filepath.Join(c.o.State, "labels.json")
			l := map[string]inventory.Label{other: {Name: "Інший"}}
			if tc.label != (inventory.Label{}) {
				l[mac] = tc.label
			}
			writeLabels(t, labels, l)
			before, _ := os.ReadFile(labels)
			ra := &rulesAPI{eng: c.rules, conf: c.o.RulesConfig, labels: labels, run: c.o.NFT, inv: c.inv}
			if got := ra.LabelName(mac); got != tc.label.Name {
				t.Errorf("LabelName %q, want %q", got, tc.label.Name)
			}
			if err := ra.Write([]uciconf.Change{{Section: "d_024e58000005", Type: "device", Set: tc.set}}); err != nil {
				t.Fatal(err)
			}
			if in := uciOf(rt); !strings.Contains(in, tc.stdin) || (tc.notIn != "" && strings.Contains(in, tc.notIn)) {
				t.Errorf("stdin:\n%s\nwant %q, without %q", in, tc.stdin, tc.notIn)
			}
			if after, _ := os.ReadFile(labels); string(after) != string(before) {
				t.Errorf("labels.json was rewritten:\n%s\nwas\n%s", after, before)
			}
		})
	}
}

// R39: openwrt-mcp keeps every apply it has not had confirmed in
// <state>/pending.json, and rolls the named configs back to its snapshot if
// the confirmation does not come. While one names netryx, a console write
// would be erased by that rollback, so it is refused before uci runs.
func TestAPendingAgentApplyHoldsTheWrite(t *testing.T) {
	for _, tc := range []struct {
		name, marker string
		held         bool
	}{
		{"netryx pending", `[{"token":"t1","snapshot":"/etc/openwrt-mcp/rollback/x.tar.gz","deadline":"2026-11-21T04:00:00Z","configs":["firewall","netryx"]}]`, true},
		{"another config pending", `[{"token":"t2","configs":["wireless"]}]`, false},
		{"nothing pending", `[]`, false},
		{"no marker", "", false},
		{"marker that does not parse", `{not json`, true},
		{"empty marker", " ", false},
		{"marker that cannot be read", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &router{}
			c, err := buildConsole(rulesOptions(t, confWithPhone, rt))
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "pending.json")
			if tc.name == "marker that cannot be read" {
				if err := os.Mkdir(marker, 0o700); err != nil { // reading a directory fails, not ENOENT
					t.Fatal(err)
				}
			}
			if tc.marker != "" {
				if err := os.WriteFile(marker, []byte(tc.marker), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ra := &rulesAPI{eng: c.rules, conf: c.o.RulesConfig, labels: filepath.Join(c.o.State, "labels.json"),
				run: c.o.NFT, inv: c.inv, agentPending: marker}
			err = ra.Write([]uciconf.Change{{Section: "d_024e58000003", Type: "device", Set: map[string][]string{"mac": {"02:4e:58:00:00:03"}, "name": {"Нова"}}}})
			if tc.held {
				if !errors.Is(err, api.ErrAgentPending) || uciOf(rt) != "" {
					t.Fatalf("held: err %v, uci ran %q", err, uciOf(rt))
				}
				return
			}
			if err != nil || uciOf(rt) == "" {
				t.Fatalf("not held: err %v, uci %q", err, uciOf(rt))
			}
		})
	}
}

// Whatever somebody left uncommitted in uci would be committed with our
// batch and never validated, so it stops the write, and nothing is run.
func TestUncommittedUCIChangesStopTheWrite(t *testing.T) {
	rt := &router{changes: "netryx.x=foo\n"}
	c, err := buildConsole(rulesOptions(t, confWithPhone, rt))
	if err != nil {
		t.Fatal(err)
	}
	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c.api.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/rules",
			strings.NewReader(`{"what":"all","who":"device","members":["02:4e:58:00:00:03"]}`)))
		return rec
	}
	rec := post()
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "uci has unsaved changes: commit or revert them first") {
		t.Fatalf("with foreign changes: %d %s", rec.Code, rec.Body)
	}
	if uciOf(rt) != "" {
		t.Errorf("a batch ran over foreign changes:\n%s", uciOf(rt))
	}
	rt.mu.Lock()
	rt.changes = ""
	rt.mu.Unlock()
	if rec := post(); rec.Code != http.StatusOK || uciOf(rt) == "" {
		t.Errorf("after they are gone: %d %s", rec.Code, rec.Body)
	}
}

// A name in a device section is what the console shows, ahead of labels.json;
// the label is the fallback for a device UCI does not name.
func TestADeviceSectionNamesTheDeviceBeforeLabelsJson(t *testing.T) {
	rt := &router{}
	c, err := buildConsole(rulesOptions(t, confWithPhone+`
config device 'd_x'
	option mac '02:4e:58:00:00:04'
	option group 'kids'
`, rt))
	if err != nil {
		t.Fatal(err)
	}
	writeLabels(t, filepath.Join(c.o.State, "labels.json"), map[string]inventory.Label{
		"02:4e:58:00:00:03": {Name: "Старе", Group: "Діти"},
		"02:4e:58:00:00:04": {Name: "Лише мітка"},
		"02:4e:58:00:00:07": {Name: "Без секції"},
	})
	c.inv.mu.Lock()
	c.inv.refreshLocked()
	l := c.inv.src.Labels
	c.inv.mu.Unlock()
	if l["02:4e:58:00:00:03"].Name != "Кухонний планшет" || l["02:4e:58:00:00:03"].Group != "Діти" {
		t.Errorf("section name wins, label group stays: %+v", l["02:4e:58:00:00:03"])
	}
	if l["02:4e:58:00:00:04"].Name != "Лише мітка" || l["02:4e:58:00:00:04"].Group != "kids" {
		t.Errorf("label name stays, section group joins: %+v", l["02:4e:58:00:00:04"])
	}
	if l["02:4e:58:00:00:07"].Name != "Без секції" {
		t.Errorf("a label for a device UCI does not name: %+v", l["02:4e:58:00:00:07"])
	}
	// A write makes the next read fresh instead of waiting out the 30 s.
	c.inv.at = c.o.Now()
	c.inv.invalidate()
	if !c.inv.at.IsZero() {
		t.Error("invalidate did not clear the read time")
	}
}

// A write route answers only past the gate, in serve's own handler: no
// session is 401, a session without the token 403, and with both the route
// itself answers. The write reaches uci only in the last case.
func TestAWriteRouteIsReachableOnlyThroughTheGate(t *testing.T) {
	rt := &router{}
	c, err := buildConsole(rulesOptions(t, confWithPhone, rt))
	if err != nil {
		t.Fatal(err)
	}
	gate := c.newGate(&fakeRPC{}, "tok")
	h := c.serveHandler(serveMux(c, func() export.Snapshot { return export.Snapshot{} }), gate)
	const house, router = "192.168.50.10:40000", "192.168.50.1:8788"
	login := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"user":"root","password":"pw"}`))
	login.RemoteAddr, login.Host = house, router
	login.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, login)
	handle := ""
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == auth.CookieName {
			handle = ck.Value
		}
	}
	if handle == "" {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	do := func(method, path, body string, cookie, token bool) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr, r.Host = house, router
		if cookie {
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: handle})
		}
		if token {
			r.Header.Set(auth.CSRFHeader, gate.CSRF(handle))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	mac := "02:4e:58:00:00:03"
	rule := `{"what":"all","who":"device","members":["` + mac + `"]}`
	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/api/rules", rule},
		{http.MethodPut, "/api/rules/r_x", rule},
		{http.MethodDelete, "/api/rules/r_x", ""},
		{http.MethodPost, "/api/allow", `{"mac":"` + mac + `","minutes":5}`},
		{http.MethodDelete, "/api/allow/a_x", ""},
		{http.MethodPut, "/api/groups/kids", `{"name":"Діти"}`},
		{http.MethodDelete, "/api/groups/kids", ""},
		{http.MethodPut, "/api/devices/" + mac, `{"name":"x"}`},
		{http.MethodPost, "/api/devices/" + mac + "/conns/cut", `{"local":"192.168.50.10:1","remote":"1.1.1.1:443","proto":6}`},
	}
	for _, w := range writes {
		if code := do(w.method, w.path, w.body, false, false); code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session: %d", w.method, w.path, code)
		}
		if code := do(w.method, w.path, w.body, true, false); code != http.StatusForbidden {
			t.Errorf("%s %s without the token: %d", w.method, w.path, code)
		}
	}
	if len(rt.all()) != 0 {
		t.Fatalf("a refused write ran something: %+v", rt.all())
	}
	if code := do(http.MethodPost, "/api/rules", rule, true, true); code != http.StatusOK {
		t.Errorf("POST /api/rules through the gate: %d", code)
	}
	if uciOf(rt) == "" {
		t.Error("the write did not reach uci through the gate")
	}
	if code := do(http.MethodGet, "/api/rules", "", true, false); code != http.StatusOK {
		t.Errorf("GET /api/rules with a session: %d", code)
	}
	if code := do(http.MethodGet, "/api/rules", "", false, false); code != http.StatusUnauthorized {
		t.Errorf("GET /api/rules without a session: %d", code)
	}
}

const adblockConf = `
config adblock-fast 'config'
	option enabled '0'
	option dns 'dnsmasq.servers'

config file_url
	option name 'Hagezi - Pro'
	option url 'https://cdn.example/pro.txt'
	option action 'block'
	option enabled '0'

config file_url
	option name 'AdguardTeam - CNAME Trackers'
	option url 'https://cdn.example/cname.txt'
	option action 'block'
	option enabled '1'
`

func adblockRig(t *testing.T, installed bool) (*rulesAPI, *router) {
	t.Helper()
	dir := t.TempDir()
	rt := &router{}
	ra := &rulesAPI{run: rt.run, adblockConf: filepath.Join(dir, "adblock-fast"), adblockInit: filepath.Join(dir, "init.d-adblock-fast"),
		background: func(f func()) { f() }}
	if installed {
		if err := os.WriteFile(ra.adblockConf, []byte(adblockConf), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ra.adblockInit, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return ra, rt
}

func TestAdblockSourcesAreReadAndSwitchedByPosition(t *testing.T) {
	ra, rt := adblockRig(t, true)
	st, err := ra.Adblock()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || len(st.Sources) != 2 || st.Sources[0].ID != "file_url_0" || st.Sources[1].Name != "AdguardTeam - CNAME Trackers" || !st.Sources[1].Enabled {
		t.Fatalf("Adblock() = %+v", st)
	}
	if err := ra.SetAdblock("file_url_0", "https://cdn.example/pro.txt", true); err != nil {
		t.Fatal(err)
	}
	calls := rt.all()
	var batch string
	restarted := false
	for _, c := range calls {
		if c.name == "uci" && strings.Join(c.args, " ") == "-q batch" {
			batch = c.stdin
		}
		if c.name == ra.adblockInit && strings.Join(c.args, " ") == "restart" {
			restarted = true
		}
	}
	want := "set adblock-fast.@file_url[0].enabled='1'\nset adblock-fast.config.enabled='1'\ncommit adblock-fast\n"
	if batch != want || !restarted {
		t.Fatalf("batch %q (want %q), restarted %v; calls %+v", batch, want, restarted, calls)
	}
	if err := ra.SetAdblock("file_url_0", "https://cdn.example/other.txt", true); !errors.Is(err, api.ErrSourceMoved) {
		t.Fatalf("a different url at that place: %v", err)
	}
	if err := ra.SetAdblock("file_url_7", "https://cdn.example/pro.txt", true); !errors.Is(err, api.ErrNotSource) {
		t.Fatalf("no source at that place: %v", err)
	}
	if err := ra.SetAdblock("config", "", true); !errors.Is(err, api.ErrNotSource) {
		t.Fatalf("the config section is not a source: %v", err)
	}
	rt.mu.Lock()
	rt.changes = "adblock-fast.config.enabled='1'\n"
	rt.mu.Unlock()
	if err := ra.SetAdblock("file_url_1", "https://cdn.example/cname.txt", false); !errors.Is(err, api.ErrPendingChanges) {
		t.Fatalf("pending changes in adblock-fast: %v", err)
	}
}

func TestAdblockNotInstalledIsSaidSo(t *testing.T) {
	ra, _ := adblockRig(t, false)
	st, err := ra.Adblock()
	if err != nil || st.Installed || len(st.Sources) != 0 {
		t.Fatalf("Adblock() = %+v, %v", st, err)
	}
	if err := ra.SetAdblock("file_url_0", "https://cdn.example/pro.txt", true); !errors.Is(err, api.ErrNoAdblock) {
		t.Fatalf("SetAdblock without adblock-fast: %v", err)
	}
}

// The console's view of isolation: every isolated device and every held one,
// with how it is covered; a held device keeps its likely-same hint.
func TestTheAPISeesEachIsolatedOrHeldDevice(t *testing.T) {
	cfg := rules.Config{Devices: []rules.Device{{MAC: "02:4e:58:00:00:04", Isolate: true}, {MAC: "02:4e:58:00:00:01"}}}
	pending := []rules.Pending{{MAC: "06:11:22:33:44:55", LikelySame: "02:4e:58:00:00:03"}}
	links := map[string]inventory.Link{
		"02:4e:58:00:00:04": {Kind: "wifi", SSID: "Things"},
		"06:11:22:33:44:55": {Kind: "wired", Iface: "lan2"},
	}
	w := rules.WirelessConfig{{SSID: "Things", Isolate: true}}
	kernel := map[string]bool{"06:11:22:33:44:55": true}
	got := securityOf(cfg, pending, kernel, links, w, func(p string) bool { return p == "lan2" })
	if len(got) != 2 {
		t.Fatalf("security = %+v; want the camera and the held device only", got)
	}
	cam := got["02:4e:58:00:00:04"]
	if !cam.Isolate || cam.Quarantined || cam.Isolation == nil || !cam.Isolation.SSIDIsolated {
		t.Fatalf("camera = %+v", cam)
	}
	held := got["06:11:22:33:44:55"]
	if !held.Quarantined || held.LikelySame != "02:4e:58:00:00:03" || held.Isolation == nil || !held.Isolation.HardwareSwitch {
		t.Fatalf("held = %+v", held)
	}
}

// Final review I1: a listed device the kernel does not hold (quarantine off,
// no house interface) is not shown as held.
func TestAListedDeviceTheKernelDoesNotHoldIsNotShownHeld(t *testing.T) {
	pending := []rules.Pending{{MAC: "06:11:22:33:44:55"}}
	got := securityOf(rules.Config{}, pending, map[string]bool{}, nil, nil, nil)
	if d, ok := got["06:11:22:33:44:55"]; ok && d.Quarantined {
		t.Fatalf("security = %+v; a device the kernel does not hold is shown held", got)
	}
}

// Final review I5: the API counts the devices behind the isolated device's
// wired port.
func TestTheAPICountsDevicesBehindOnePort(t *testing.T) {
	cfg := rules.Config{Devices: []rules.Device{{MAC: "02:4e:58:00:00:04", Isolate: true}}}
	links := map[string]inventory.Link{
		"02:4e:58:00:00:04": {Kind: "wired", Iface: "lan3"},
		"02:4e:58:00:00:05": {Kind: "wired", Iface: "lan3"},
		"02:4e:58:00:00:06": {Kind: "wired", Iface: "lan1"},
	}
	got := securityOf(cfg, nil, nil, links, nil, nil)["02:4e:58:00:00:04"]
	if got.Isolation == nil || got.Isolation.SharedPort != 2 {
		t.Fatalf("camera = %+v; want two devices on lan3", got.Isolation)
	}
}
