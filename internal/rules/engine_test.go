package rules

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"netryx/internal/ct"
	"netryx/internal/enrich"
	"netryx/internal/flows"
)

// fakeNFT stands in for nft: it records every call and keeps a small model of
// the table (which counters, chains and per-rule sets exist, and the hits of
// each counter), so a reload, a prune and a counter reset can be observed the
// way the kernel would show them.
type fakeNFT struct {
	mu      sync.Mutex
	calls   []nftCall
	present bool
	objects map[string]bool   // "counter hits_x", "chain r_x", "set members_x"
	hits    map[string]uint64 // counter name -> packets
	listSet map[string]string // what `nft list set` prints per set
	// fail, when set, refuses a `nft -f -` whose script it returns an error
	// for, and the model is left as it was: nft applies all or nothing.
	fail func(script string) error
}

type nftCall struct {
	env   []string
	stdin string
	args  string
}

func newFakeNFT() *fakeNFT {
	return &fakeNFT{objects: map[string]bool{}, hits: map[string]uint64{}, listSet: map[string]string{}}
}

func (f *fakeNFT) run(ctx context.Context, env []string, stdin string, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := strings.Join(args, " ")
	f.calls = append(f.calls, nftCall{env: env, stdin: stdin, args: a})
	if name != "nft" {
		return nil, fmt.Errorf("not nft: %s", name)
	}
	switch {
	case a == "-f -":
		if f.fail != nil {
			if err := f.fail(stdin); err != nil {
				return nil, err
			}
		}
		f.apply(stdin)
		return nil, nil
	case a == "-j list counters table inet netryx_rules":
		if !f.present {
			return nil, errors.New("Error: No such file or directory")
		}
		var parts []string
		for o := range f.objects {
			if n, ok := strings.CutPrefix(o, "counter "); ok {
				parts = append(parts, fmt.Sprintf(`{"counter": {"family": "inet", "name": %q, "table": "netryx_rules", "handle": 1, "packets": %d, "bytes": %d}}`, n, f.hits[n], f.hits[n]*100))
			}
		}
		sort.Strings(parts)
		return []byte(`{"nftables": [{"metainfo": {"version": "1.1.6"}}` + prefixComma(parts) + `]}`), nil
	case a == "-t -j list table inet netryx_rules":
		if !f.present {
			return nil, errors.New("Error: No such file or directory")
		}
		parts := []string{`{"table": {"family": "inet", "name": "netryx_rules", "handle": 1}}`,
			`{"chain": {"family": "inet", "table": "netryx_rules", "name": "rules_fwd", "handle": 2, "type": "filter", "hook": "forward", "prio": -5, "policy": "accept"}}`}
		for o := range f.objects {
			kind, n, _ := strings.Cut(o, " ")
			switch kind {
			case "counter":
				parts = append(parts, fmt.Sprintf(`{"counter": {"family": "inet", "name": %q, "table": "netryx_rules", "handle": 3, "packets": 0, "bytes": 0}}`, n))
			case "chain":
				parts = append(parts, fmt.Sprintf(`{"chain": {"family": "inet", "table": "netryx_rules", "name": %q, "handle": 4}}`, n))
			case "set":
				parts = append(parts, fmt.Sprintf(`{"set": {"family": "inet", "name": %q, "table": "netryx_rules", "type": "ether_addr", "handle": 5, "flags": ["timeout"]}}`, n))
			}
		}
		sort.Strings(parts)
		return []byte(`{"nftables": [{"metainfo": {"version": "1.1.6"}}` + prefixComma(parts) + `]}`), nil
	case strings.HasPrefix(a, "list set inet netryx_rules "):
		n := strings.TrimPrefix(a, "list set inet netryx_rules ")
		if out, ok := f.listSet[n]; ok {
			return []byte(out), nil
		}
		return nil, errors.New("Error: No such file or directory")
	}
	return nil, fmt.Errorf("unexpected nft %s", a)
}

func prefixComma(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return ", " + strings.Join(parts, ", ")
}

// apply folds a successful script into the model.
func (f *fakeNFT) apply(script string) {
	for _, l := range strings.Split(script, "\n") {
		w := strings.Fields(l)
		if len(w) < 3 {
			continue
		}
		if w[0] == "add" && w[1] == "table" {
			f.present = true
			continue
		}
		if len(w) < 5 || w[2] != "inet" || w[3] != "netryx_rules" {
			continue
		}
		obj := w[1] + " " + w[4]
		switch w[0] + " " + w[1] {
		case "add counter":
			f.objects[obj] = true
		case "reset counter":
			f.hits[w[4]] = 0
		case "add chain", "add set":
			if w[4] != Chain && (strings.HasPrefix(w[4], "r_") || strings.HasPrefix(w[4], "members_") || strings.HasPrefix(w[4], "allow_") ||
				strings.HasPrefix(w[4], "cc_") || strings.HasPrefix(w[4], "app_") || strings.HasPrefix(w[4], "cut_")) {
				f.objects[obj] = true
			}
		case "delete counter":
			delete(f.objects, obj)
			delete(f.hits, w[4])
		case "delete chain", "delete set":
			delete(f.objects, obj)
		}
	}
}

func (f *fakeNFT) applies() []nftCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []nftCall
	for _, c := range f.calls {
		if c.args == "-f -" {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeNFT) lastApply(t *testing.T) string {
	t.Helper()
	a := f.applies()
	if len(a) == 0 {
		t.Fatal("nft -f was never run")
	}
	return a[len(a)-1].stdin
}

const testApps = "d\tgooglevideo.com\tyoutube\tYouTube\tvideo\n" +
	"d\tyoutube.com\tyoutube\tYouTube\tvideo\n" +
	"d\ttelegram.org\ttelegram\tTelegram\tmessaging\n" +
	"o\ttelegram\ttelegram\tTelegram\tmessaging\n" +
	"d\tnetflix.com\tnetflix\tNetflix\tvideo\n"

const testGeo = "5.8.0.0\t5.8.255.255\t1\tRU\tSOME-RU\n" +
	"149.154.160.0\t149.154.175.255\t62041\tGB\tTelegram Messenger Inc\n" +
	"2001:67c:4e8::\t2001:67c:4e8:ffff:ffff:ffff:ffff:ffff\t62041\tGB\tTelegram Messenger Inc\n"

// rig is an engine over a temporary config and state directory, a fake nft,
// a fake clock and a recorded Delete.
type rig struct {
	t       *testing.T
	nft     *fakeNFT
	e       *Engine
	conf    string
	state   string
	now     time.Time
	mono    time.Duration
	east    int
	live    []flows.Flow
	mu      sync.Mutex
	cut     []ct.Tuple
	cutBack []ct.Tuple
	usage   *Usage
	safe    *fakeSafe
	house   []netip.Prefix
	roster  *Roster
	seen    []Seen
	events  []string
	told    []Intervention
}

// fakeSafe stands in for the safe-search resolver's manager.
type fakeSafe struct {
	mu      sync.Mutex
	healthy bool
	problem string
	calls   []string
	hold    chan struct{} // while set, Ensure waits for it to close
}

func (f *fakeSafe) Ensure(want bool, youtube string) (bool, string) {
	f.mu.Lock()
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("%v %s", want, youtube))
	if !want {
		return false, ""
	}
	return f.healthy, f.problem
}

func newRig(t *testing.T, conf string, now time.Time) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{t: t, nft: newFakeNFT(), conf: filepath.Join(dir, "netryx"), state: filepath.Join(dir, "state"), now: now,
		usage: NewUsage(london(t)), safe: &fakeSafe{},
		house: []netip.Prefix{netip.MustParsePrefix("192.168.50.0/24")}}
	if err := os.MkdirAll(r.state, 0o700); err != nil {
		t.Fatal(err)
	}
	r.write(conf)
	r.roster = NewRoster(filepath.Join(r.state, "known-macs.json"), func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now })
	apps, err := enrich.LoadAppMap(strings.NewReader(testApps))
	if err != nil {
		t.Fatal(err)
	}
	geo, err := enrich.LoadIP2ASN(strings.NewReader(testGeo))
	if err != nil {
		t.Fatal(err)
	}
	r.e = NewEngine(EngineOptions{
		ConfigPath: r.conf, StateDir: r.state, Run: r.nft.run,
		Now: func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now },
		Loc: london(t), Geo: geo, Apps: apps,
		Live: func() []flows.Flow { r.mu.Lock(); defer r.mu.Unlock(); return append([]flows.Flow(nil), r.live...) },
		Delete: func(tu ct.Tuple) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.cut = append(r.cut, tu)
			return nil
		},
		DeleteReply: func(tu ct.Tuple) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.cutBack = append(r.cutBack, tu)
			return nil
		},
		KernelEast: func() (int, error) { r.mu.Lock(); defer r.mu.Unlock(); return r.east, nil },
		Elapsed:    func() time.Duration { r.mu.Lock(); defer r.mu.Unlock(); return r.mono },
		Logf:       t.Logf,
		IsLocal: func(a netip.Addr) bool {
			return netip.MustParsePrefix("192.168.50.0/24").Contains(a) || netip.MustParsePrefix("2a02:c7c::/32").Contains(a)
		},
		Usage:       r.usage,
		SafeDNS:     r.safe,
		HouseIfaces: func() []string { return []string{"br-lan"} },
		Roster:      r.roster,
		Seen: func() []Seen {
			r.mu.Lock()
			defer r.mu.Unlock()
			return append([]Seen(nil), r.seen...)
		},
		Event: func(ev Event) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.events = append(r.events, ev.Kind+" "+ev.MAC)
		},
		Intervene: func(iv Intervention) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.told = append(r.told, iv)
		},
		HousePrefixes: func() []netip.Prefix {
			r.mu.Lock()
			defer r.mu.Unlock()
			return append([]netip.Prefix(nil), r.house...)
		},
	})
	return r
}

// settle waits for the cuts Named handed to the engine's worker.
func (r *rig) settle() { r.e.cutWG.Wait() }

func (r *rig) write(conf string) {
	r.t.Helper()
	if err := os.WriteFile(r.conf, []byte(conf), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) set(now time.Time, mono time.Duration) {
	r.mu.Lock()
	r.now, r.mono = now, mono
	r.mu.Unlock()
}

func (r *rig) cuts() []ct.Tuple {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ct.Tuple(nil), r.cut...)
}

func (r *rig) state1(id string) RuleState {
	r.t.Helper()
	for _, s := range r.e.State() {
		if s.ID == id {
			return s
		}
	}
	r.t.Fatalf("no rule %s in State()", id)
	return RuleState{}
}

const (
	phoneMAC  = "02:4e:58:00:00:03"
	laptopMAC = "02:4e:58:00:00:02"
	tvMAC     = "02:4e:58:00:00:01"
)

const tgForPhone = `
config rule 'tg'
	option enabled '1'
	option action 'block'
	option what 'app'
	list target 'telegram'
	option who 'device'
	list member '02:4e:58:00:00:03'
`

const ytForLaptop = `
config rule 'yt'
	option action 'block'
	option what 'app'
	list target 'youtube'
	option who 'device'
	list member '02:4e:58:00:00:02'
`

func winter(t *testing.T) time.Time { return time.Date(2026, 12, 2, 12, 0, 0, 0, london(t)) }

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func TestReloadAppliesOneTransactionAndKeepsThePreviousOnFailure(t *testing.T) {
	r := newRig(t, tgForPhone, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatalf("first reload: %v", err)
	}
	a := r.nft.applies()
	if len(a) != 1 {
		t.Fatalf("one reload is one nft transaction, got %d", len(a))
	}
	if !hasEnv(a[0].env, "TZ=UTC") {
		t.Fatalf("the script carries UTC hours and must be loaded with TZ=UTC: env %q", a[0].env)
	}
	if !strings.Contains(a[0].stdin, "add rule inet netryx_rules rules_fwd ether saddr @members_tg ip daddr @app_telegram_v4") {
		t.Fatalf("the transaction does not hold the rule:\n%s", a[0].stdin)
	}
	first, err := os.ReadFile(filepath.Join(r.state, "rules.nft"))
	if err != nil {
		t.Fatalf("rules.nft was not written: %v", err)
	}
	if !strings.Contains(string(first), "ether saddr @members_tg ip daddr @app_telegram_v4") {
		t.Fatalf("rules.nft does not hold the applied rule:\n%s", first)
	}
	if s := r.state1("tg"); !s.Applied || s.Problem != "" {
		t.Fatalf("an applied rule says so: %+v", s)
	}

	r.write(tgForPhone + ytForLaptop)
	const msg = "/dev/stdin:12:1-40: Error: Could not process rule: No such file or directory"
	r.nft.fail = func(string) error { return errors.New(msg) }
	if err := r.e.Reload(); err == nil {
		t.Fatal("a refused transaction is an error")
	}
	for _, id := range []string{"tg", "yt"} {
		s := r.state1(id)
		if s.Applied || !strings.HasPrefix(s.Problem, "not applied: ") || !strings.Contains(s.Problem, "Could not process rule") {
			t.Fatalf("rule %s must say it is not applied, with nft's message: %+v", id, s)
		}
	}
	again, err := os.ReadFile(filepath.Join(r.state, "rules.nft"))
	if err != nil || string(again) != string(first) {
		t.Fatalf("a failed reload must leave rules.nft as the last ruleset that loaded (err %v)", err)
	}
	if left, _ := filepath.Glob(filepath.Join(r.state, "rules.nft*")); len(left) != 1 {
		t.Fatalf("nothing beside rules.nft may be left over: %v", left)
	}

	r.nft.fail = nil
	if err := r.e.Reload(); err != nil {
		t.Fatalf("reload after the failure: %v", err)
	}
	if s := r.state1("yt"); !s.Applied || s.Problem != "" {
		t.Fatalf("once nft takes it, the rule is applied: %+v", s)
	}
	now, _ := os.ReadFile(filepath.Join(r.state, "rules.nft"))
	if !strings.Contains(string(now), "@members_yt") {
		t.Fatal("rules.nft follows the applied ruleset")
	}
}

func TestAReloadWithNothingChangedTouchesNothing(t *testing.T) {
	r := newRig(t, tgForPhone, winter(t))
	for i := 0; i < 3; i++ {
		if err := r.e.Reload(); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(r.nft.applies()); n != 1 {
		t.Fatalf("a HUP with nothing changed (a network reload sends one) must not reload nft: %d transactions", n)
	}
}

func TestAnAnswerForABlockedAppFillsItsSetAndASharedOneDoesNot(t *testing.T) {
	r := newRig(t, ytForLaptop, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	before := len(r.nft.applies())
	r.e.Answer("rr1.googlevideo.com", net.ParseIP("142.250.1.1"))
	r.e.Answer("rr2.googlevideo.com", net.ParseIP("2a00:1450:4009::1"))
	r.e.Answer("www.youtube.com", net.ParseIP("142.250.2.2"))      // shared front
	r.e.Answer("i.ytimg.com", net.ParseIP("142.250.3.3"))          // shared, and not even this app
	r.e.Answer("api.telegram.org", net.ParseIP("149.154.167.220")) // no rule blocks it
	r.e.Answer("rr3.googlevideo.com", net.ParseIP("192.168.50.9")) // a private answer is no destination
	if err := r.e.Flush(); err != nil {
		t.Fatal(err)
	}
	a := r.nft.applies()
	if len(a) != before+1 {
		t.Fatalf("the answers go to nft in one batch, got %d transactions", len(a)-before)
	}
	got := a[len(a)-1].stdin
	for _, want := range []string{
		"add element inet netryx_rules app_youtube_v4 { 142.250.1.1 timeout 24h }",
		"add element inet netryx_rules app_youtube_v6 { 2a00:1450:4009::1 timeout 24h }",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	for _, never := range []string{"142.250.2.2", "142.250.3.3", "149.154.167.220", "192.168.50.9"} {
		if strings.Contains(got, never) {
			t.Fatalf("%s must not enter a set:\n%s", never, got)
		}
	}
	if err := r.e.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(r.nft.applies()) != before+1 {
		t.Fatal("nothing queued is nothing run")
	}
}

func TestANamedHandshakeOfAMemberIsCut(t *testing.T) {
	r := newRig(t, tgForPhone+ytForLaptop, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	local := netip.MustParseAddrPort("192.168.50.13:51000")
	remote := netip.MustParseAddrPort("149.154.167.220:443")
	r.e.Named(phoneMAC, local, remote, 6, "api.telegram.org")
	r.e.Named(laptopMAC, netip.MustParseAddrPort("192.168.50.12:51001"), remote, 6, "api.telegram.org")
	r.settle()
	cuts := r.cuts()
	if len(cuts) != 1 {
		t.Fatalf("only the member's connection is cut: %+v", cuts)
	}
	c := cuts[0]
	if !c.SrcIP.Equal(net.ParseIP("192.168.50.13")) || !c.DstIP.Equal(net.ParseIP("149.154.167.220")) ||
		c.Proto != 6 || c.SrcPort != 51000 || c.DstPort != 443 || len(c.SrcIP) != 4 {
		t.Fatalf("the cut must name the connection as the device opened it: %+v", c)
	}

	// A shared front is cut for a member and never put in a set.
	r.e.Named(laptopMAC, netip.MustParseAddrPort("192.168.50.12:51002"), netip.MustParseAddrPort("142.250.2.2:443"), 17, "www.youtube.com")
	r.settle()
	if cuts := r.cuts(); len(cuts) != 2 || cuts[1].Proto != 17 || !cuts[1].DstIP.Equal(net.ParseIP("142.250.2.2")) {
		t.Fatalf("a member's handshake to a shared front is cut: %+v", cuts)
	}
	if err := r.e.Flush(); err != nil {
		t.Fatal(err)
	}
	got := r.nft.lastApply(t)
	if !strings.Contains(got, "add element inet netryx_rules app_telegram_v4 { 149.154.167.220 timeout 24h }") {
		t.Fatalf("a dedicated name's address goes into its app's set:\n%s", got)
	}
	if strings.Contains(appLines(got), "142.250.2.2") {
		t.Fatalf("a shared front's address never goes into an app's set:\n%s", got)
	}
	if !strings.Contains(got, "add element inet netryx_rules cut_yt_v4 { 02:4e:58:00:00:02 . 142.250.2.2 . udp . 51002 timeout 10m }") {
		t.Fatalf("the cut QUIC flow goes into the rule's own cut set (R40, R45):\n%s", got)
	}
}

// appLines is the part of a script that adds to the app sets; the cut sets
// (R40) take any address a rule cut, paired with the device.
func appLines(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "add element inet netryx_rules app_") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func TestAnAllowedMemberIsNotCut(t *testing.T) {
	until := winter(t).Add(30 * time.Minute).Unix()
	r := newRig(t, tgForPhone+fmt.Sprintf(`
config allow 'a1'
	option rule 'tg'
	option member '02:4e:58:00:00:03'
	option until '%d'
`, until), winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.Named(phoneMAC, netip.MustParseAddrPort("192.168.50.13:51000"), netip.MustParseAddrPort("149.154.167.220:443"), 6, "api.telegram.org")
	r.settle()
	if c := r.cuts(); len(c) != 0 {
		t.Fatalf("an allowance lifts the cut as it lifts the rule: %+v", c)
	}
}

func flow(id uint32, mac, local, remote string, app, cc string) flows.Flow {
	return flows.Flow{ID: id, MAC: mac, Local: netip.MustParseAddrPort(local), Remote: netip.MustParseAddrPort(remote),
		Proto: 6, Dir: flows.Out, App: enrich.App{ID: app}, Geo: enrich.ASInfo{CC: cc}}
}

// D4, seen once on the Flint 2: a new country rule cut a running download
// only at the engine's next tick, not at the write. The flow table stamps a
// flow's country only once it is named or its name wait (10 s) is over, so a
// young unnamed flow carries no country yet. The country of a connection is
// its remote address's, so the engine asks the address table itself for a
// flow that has none: the write cuts it. A flow somewhere else, and one with
// both ends at home, are left alone.
func TestACountryRuleCutsAFlowNotYetPlacedAtTheWrite(t *testing.T) {
	r := newRig(t, "", winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.live = []flows.Flow{
		flow(1, laptopMAC, "192.168.50.12:44152", "5.8.1.1:80", "", ""),          // RU, not placed yet
		flow(2, laptopMAC, "192.168.50.12:44153", "149.154.167.220:443", "", ""), // GB, not placed yet
		flow(3, laptopMAC, "192.168.50.12:44154", "5.8.1.2:80", "", "RU"),        // placed
	}
	// Both ends at home, yet the remote address is in the geo's RU range, so
	// only the engine's skip of a Local flow keeps it from being cut.
	home := flow(4, laptopMAC, "192.168.50.12:44155", "5.8.1.3:80", "", "")
	home.Dir = flows.Local
	r.live = append(r.live, home)
	r.mu.Unlock()
	r.write(`
config rule 'ru'
	option action 'block'
	option what 'country'
	list target 'RU'
	option who 'device'
	list member '02:4e:58:00:00:02'
`)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	var ports []int
	for _, c := range r.cuts() {
		ports = append(ports, int(c.SrcPort))
	}
	sort.Ints(ports)
	if fmt.Sprint(ports) != "[44152 44154]" {
		t.Fatalf("the write cuts both flows to RU, placed or not, and nothing else: cut %v", ports)
	}
}

func TestAWindowOpeningCutsWhatItNowCovers(t *testing.T) {
	conf := tgForPhone + "\toption from '21:00'\n\toption to '07:00'\n"
	edge := time.Date(2026, 12, 2, 21, 0, 0, 0, london(t))
	r := newRig(t, conf, edge.Add(-10*time.Second))
	r.live = []flows.Flow{
		flow(1, phoneMAC, "192.168.50.13:40001", "149.154.167.220:443", "telegram", "GB"),
		flow(2, phoneMAC, "[fd00::13]:40002", "[2001:67c:4e8:f004::9]:443", "telegram", "GB"),
		flow(3, phoneMAC, "[2a02:c7c::5]:40003", "[2001:67c:4e8:f004::a]:443", "telegram", "GB"),
		flow(4, phoneMAC, "192.168.50.13:40004", "52.1.1.1:443", "netflix", "US"),
		flow(5, laptopMAC, "192.168.50.12:40005", "149.154.167.221:443", "telegram", "GB"),
		// The laptop now holds an address the phone once had: a cut by
		// address would take it, a cut by hardware address does not.
		flow(6, laptopMAC, "[fd00::13]:40006", "[2001:67c:4e8:f004::b]:443", "telegram", "GB"),
	}
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.Tick(edge.Add(-5 * time.Second))
	if c := r.cuts(); len(c) != 0 {
		t.Fatalf("before the window nothing is cut: %+v", c)
	}
	r.set(edge.Add(5*time.Second), 15*time.Second)
	r.e.Tick(edge.Add(5 * time.Second))
	cuts := r.cuts()
	got := map[uint16]bool{}
	for _, c := range cuts {
		got[c.SrcPort] = true
	}
	if len(cuts) != 3 || !got[40001] || !got[40002] || !got[40003] {
		t.Fatalf("the window opening cuts the phone's three telegram flows, IPv4 and both IPv6, and nothing else: %+v", cuts)
	}
	r.set(edge.Add(20*time.Second), 30*time.Second)
	r.e.Tick(edge.Add(20 * time.Second))
	if c := r.cuts(); len(c) != 3 {
		t.Fatalf("a flow is cut once when the rule starts to cover it, not on every tick: %+v", c)
	}
}

func TestACountryRuleForEveryoneCutsByCountryAndAnInboundFlowByItsDeviceSide(t *testing.T) {
	conf := `
config rule 'ru'
	option action 'block'
	option what 'country'
	list target 'RU'
	option who 'all'
`
	r := newRig(t, conf, winter(t))
	in := flow(2, laptopMAC, "192.168.50.12:22", "5.8.1.1:50000", "unknown", "RU")
	in.Dir = flows.In
	r.live = []flows.Flow{
		flow(1, phoneMAC, "192.168.50.13:40001", "5.8.1.2:443", "unknown", "RU"),
		in,
		flow(3, phoneMAC, "192.168.50.13:40003", "52.1.1.1:443", "unknown", "US"),
		flow(4, flows.RouterKey, "192.168.50.1:40004", "5.8.1.3:443", "unknown", "RU"),
	}
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.Tick(winter(t))
	if c := r.cuts(); len(c) != 1 || c[0].SrcPort != 40001 {
		t.Fatalf("the outbound flow to the country is cut by its original tuple, the router's own is not: %+v", c)
	}
	r.mu.Lock()
	back := append([]ct.Tuple(nil), r.cutBack...)
	r.mu.Unlock()
	if len(back) != 1 || back[0].SrcPort != 22 || !back[0].DstIP.Equal(net.ParseIP("5.8.1.1")) {
		t.Fatalf("an inbound flow is cut by its device side, which is the reply tuple: %+v", back)
	}
}

func TestAClockJumpRecompiles(t *testing.T) {
	t0 := winter(t)
	conf := tgForPhone + fmt.Sprintf("\toption until '%d'\n", t0.Add(5*time.Hour).Unix())
	r := newRig(t, conf, t0)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.Tick(t0)
	n := len(r.nft.applies())

	r.set(t0.Add(15*time.Second), 15*time.Second)
	r.e.Tick(t0.Add(15 * time.Second))
	if len(r.nft.applies()) != n {
		t.Fatal("an ordinary tick recompiles nothing")
	}

	// NTP answers: the wall clock moves two hours while 15 s pass.
	r.set(t0.Add(2*time.Hour), 30*time.Second)
	r.e.Tick(t0.Add(2 * time.Hour))
	if len(r.nft.applies()) != n+1 {
		t.Fatal("a wall clock that moved more than 10 minutes against the monotonic clock recompiles")
	}
	if got := r.nft.lastApply(t); !strings.Contains(got, fmt.Sprintf("%s timeout %ds", phoneMAC, 3*3600)) {
		t.Fatalf("the recompile is against the new time:\n%s", got)
	}

	// The kernel's offset changing (a new zone written to the system) also
	// recompiles, because meta day is read in the kernel's frame.
	r.mu.Lock()
	r.east = 60
	r.mu.Unlock()
	r.set(t0.Add(2*time.Hour+15*time.Second), 45*time.Second)
	r.e.Tick(t0.Add(2*time.Hour + 15*time.Second))
	if len(r.nft.applies()) != n+2 {
		t.Fatal("a changed kernel offset recompiles")
	}
}

func TestADSTTransitionRecompiles(t *testing.T) {
	// 2026-10-25 01:00 UTC London leaves BST.
	before := time.Date(2026, 10, 25, 0, 59, 50, 0, time.UTC)
	conf := tgForPhone + "\toption from '21:00'\n\toption to '07:00'\n"
	r := newRig(t, conf, before)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	n := len(r.nft.applies())
	after := before.Add(20 * time.Second)
	r.set(after, 20*time.Second)
	r.e.Tick(after)
	if len(r.nft.applies()) != n+1 {
		t.Fatal("passing the zone's transition recompiles the hours")
	}
	if got := r.nft.lastApply(t); !strings.Contains(got, `meta hour "21:00:00"-"23:59:59"`) {
		t.Fatalf("after the transition the window is compiled at GMT:\n%s", got)
	}
}

func TestANewRuleIDDoesNotInheritOldHits(t *testing.T) {
	r := newRig(t, tgForPhone, winter(t))
	// The table from before this process: the rule ran and counted.
	r.nft.present = true
	r.nft.objects["counter hits_tg"] = true
	r.nft.hits["hits_tg"] = 50
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.nft.lastApply(t), "reset counter") {
		t.Fatal("a restart of netryx keeps the hits of a rule that was already in the table")
	}
	if s := r.state1("tg"); s.Hits != 50 {
		t.Fatalf("hits are read from the table: %+v", s)
	}

	// The rule is removed, and the prune that would delete its counter fails.
	r.nft.fail = func(s string) error {
		if strings.Contains(s, "delete ") {
			return errors.New("Error: Could not process rule: Device or resource busy")
		}
		return nil
	}
	r.write("")
	if err := r.e.Reload(); err != nil {
		t.Fatalf("a failed prune is logged, never fatal: %v", err)
	}
	if !r.nft.objects["counter hits_tg"] {
		t.Fatal("the test needs the old counter left behind")
	}

	// A rule under the same name again is a new rule: zero hits.
	r.nft.fail = nil
	r.write(tgForPhone)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.nft.lastApply(t), "reset counter inet netryx_rules hits_tg") {
		t.Fatalf("a re-created rule resets its counter in the same transaction:\n%s", r.nft.lastApply(t))
	}
	if s := r.state1("tg"); s.Hits != 0 {
		t.Fatalf("a re-created rule must not inherit old hits: %+v", s)
	}
}

func TestWhatARemovedRuleLeftIsPrunedAfterTheApply(t *testing.T) {
	until := winter(t).Add(30 * time.Minute).Unix()
	r := newRig(t, tgForPhone+ytForLaptop+fmt.Sprintf(`
config allow 'a1'
	option rule 'tg'
	option member '02:4e:58:00:00:03'
	option until '%d'
`, until), winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{"chain r_tg", "set members_tg", "set allow_tg", "counter hits_tg"} {
		if !r.nft.objects[o] {
			t.Fatalf("setup: %s should exist, have %v", o, r.nft.objects)
		}
	}
	r.write(ytForLaptop)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	prune := r.nft.lastApply(t)
	if strings.Contains(prune, "add rule") {
		t.Fatalf("the prune is its own small transaction after the apply:\n%s", prune)
	}
	for _, o := range []string{"chain r_tg", "set members_tg", "set allow_tg", "counter hits_tg"} {
		if r.nft.objects[o] {
			t.Fatalf("%s of the removed rule is still there:\n%s", o, prune)
		}
	}
	if !strings.Contains(prune, "flush chain inet netryx_rules r_tg") ||
		strings.Index(prune, "delete chain inet netryx_rules r_tg") > strings.Index(prune, "delete counter inet netryx_rules hits_tg") {
		t.Fatalf("the chain is emptied and deleted before the counter and sets it uses:\n%s", prune)
	}
	for _, o := range []string{"set members_yt", "counter hits_yt"} {
		if !r.nft.objects[o] {
			t.Fatalf("%s belongs to a rule still there and must stay", o)
		}
	}
	if strings.Contains(prune, "delete table") || strings.Contains(prune, Chain) {
		t.Fatalf("the table and rules_fwd are never deleted:\n%s", prune)
	}
}

func TestSetsAreSavedAndLoadedAtBoot(t *testing.T) {
	r := newRig(t, ytForLaptop+tgForPhone, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.nft.listSet["app_youtube_v4"] = "table inet netryx_rules {\n\tset app_youtube_v4 {\n\t\telements = { 142.250.1.1 expires 23h }\n\t}\n}\n"
	r.nft.listSet["app_youtube_v6"] = "table inet netryx_rules {\n\tset app_youtube_v6 {\n\t}\n}\n"
	r.nft.listSet["app_telegram_v4"] = "table inet netryx_rules {\n\tset app_telegram_v4 {\n\t\telements = { 149.154.167.220 expires 23h }\n\t}\n}\n"
	r.nft.listSet["app_telegram_v6"] = "table inet netryx_rules {\n\tset app_telegram_v6 {\n\t}\n}\n"
	r.nft.listSet["app_telegram_org_v4"] = "ORG RANGES"
	if err := r.e.SaveSets(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(r.state, "sets.nft"))
	if err != nil {
		t.Fatalf("sets.nft was not written: %v", err)
	}
	for _, want := range []string{"142.250.1.1 expires 23h", "149.154.167.220 expires 23h", "set app_youtube_v6", "set app_telegram_v6"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("sets.nft lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(string(b), "ORG RANGES") {
		t.Fatal("the organisation's ranges are rebuilt from the address table, not saved")
	}
	if left, _ := filepath.Glob(filepath.Join(r.state, "sets.nft*")); len(left) != 1 {
		t.Fatalf("the file is written by rename, nothing left beside it: %v", left)
	}
}

func TestACountryRuleWithoutTheAddressTableSaysWhy(t *testing.T) {
	conf := `
config rule 'ru'
	option action 'block'
	option what 'country'
	list target 'RU'
	option who 'all'
`
	r := newRig(t, conf, winter(t))
	r.e.o.Geo = nil
	r.e.cat = newCatalog(nil, r.e.o.Apps)
	if err := r.e.Reload(); err == nil {
		t.Fatal("a ruleset that needs a missing data file is a refused apply")
	}
	s := r.state1("ru")
	if s.Err != "" || s.Applied || s.Problem != "not applied: no address table" {
		t.Fatalf("a country rule without the address table is not applied, with its reason: %+v", s)
	}
	if len(r.nft.applies()) != 0 {
		t.Fatalf("nothing is sent to nft: %+v", r.nft.applies())
	}
}

// R38: a data file missing after a restart is an apply refusal. The kernel
// keeps the ruleset it has: rules_fwd is not flushed, the country and app
// sets are not pruned, rules.nft is not rewritten, and the affected rules
// say why. Removing the rule that needs the file applies again.
func TestAMissingDataFileNeverOpensWhatWasBlocked(t *testing.T) {
	const ru = `
config rule 'ru'
	option action 'block'
	option what 'country'
	list target 'RU'
	option who 'all'
`
	for _, tc := range []struct {
		name, conf, problem string
		drop                func(*EngineOptions)
		kept                []string
	}{
		{"address table", ru + ytForLaptop, "not applied: no address table",
			func(o *EngineOptions) { o.Geo = nil }, []string{"set cc_ru_v4", "set cc_ru_v6"}},
		{"address table under an org app", tgForPhone, "not applied: no address table",
			func(o *EngineOptions) { o.Geo = nil }, []string{"set app_telegram_org_v4", "set app_telegram_org_v6"}},
		{"app map", ru + ytForLaptop, "not applied: no app map",
			func(o *EngineOptions) { o.Apps = nil }, []string{"set app_youtube_v4", "set members_yt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tc.conf, winter(t))
			if err := r.e.Reload(); err != nil {
				t.Fatal(err)
			}
			before := len(r.nft.applies())
			rulesNft, _ := os.ReadFile(filepath.Join(r.state, "rules.nft"))
			for _, o := range tc.kept {
				if !r.nft.objects[o] {
					t.Fatalf("setup: %s should exist: %v", o, r.nft.objects)
				}
			}
			// A restart without the file: a new engine on the same kernel.
			o := r.e.o
			tc.drop(&o)
			e2 := NewEngine(o)
			if err := e2.Reload(); err == nil {
				t.Fatal("a refused apply is an error")
			}
			if a := r.nft.applies(); len(a) != before {
				t.Fatalf("nft ran after the restart without the file:\n%s", a[len(a)-1].stdin)
			}
			for _, o := range tc.kept {
				if !r.nft.objects[o] {
					t.Fatalf("%s was pruned", o)
				}
			}
			if b, _ := os.ReadFile(filepath.Join(r.state, "rules.nft")); string(b) != string(rulesNft) {
				t.Fatal("rules.nft was rewritten")
			}
			affected := 0
			for _, s := range e2.State() {
				if s.Applied {
					t.Errorf("%s says applied while the kernel holds the previous ruleset", s.ID)
				}
				if s.Problem == tc.problem && s.Err == "" {
					affected++
				}
			}
			if affected == 0 {
				t.Fatalf("no rule says %q: %+v", tc.problem, e2.State())
			}
			// Removing what needs the file is applied.
			r.write("")
			if err := e2.Reload(); err != nil {
				t.Fatal(err)
			}
			if len(r.nft.applies()) == before {
				t.Fatal("a ruleset that no longer needs the file is applied")
			}
		})
	}
}

// R41: an app or category rule whose apps have no organisation ranges is
// held only by names; when the router cannot see names (no capture, no
// passive DNS, or forward mode with offload) the rule says so and is not
// counted as applied. A rule with org ranges and a country rule are. The
// problem is the reason Blind gives, as given (D3: one text once stood for
// all three causes).
func TestAnAppRuleSaysItCannotSeeNamesWhenBlind(t *testing.T) {
	conf := ytForLaptop + tgForPhone + `
config rule 'vid'
	option action 'block'
	option what 'category'
	list target 'video'
	option who 'all'

config rule 'ru'
	option action 'block'
	option what 'country'
	list target 'RU'
	option who 'all'
`
	r := newRig(t, conf, winter(t))
	r.e.o.Blind = func() string {
		return "names cannot be seen: forward mode under flow offloading (set mode to ingress, or turn flow offloading off)"
	}
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"yt", "vid"} {
		if s := r.state1(id); s.Applied || s.Problem != "names cannot be seen: forward mode under flow offloading (set mode to ingress, or turn flow offloading off)" {
			t.Errorf("%s: %+v", id, s)
		}
	}
	for _, id := range []string{"tg", "ru"} {
		if s := r.state1(id); !s.Applied || s.Problem != "" {
			t.Errorf("%s needs no names: %+v", id, s)
		}
	}
	r.e.o.Blind = func() string { return "" }
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if s := r.state1("yt"); !s.Applied || s.Problem != "" {
		t.Errorf("seeing names, the rule is applied: %+v", s)
	}
}

// Measured on the Flint 2 (D2): in forward mode the offload setting was read
// once at the start, so flow offloading turned on under the running service
// left a rule held only by names applied with no problem. The engine asks
// again at every tick: within one tick the rule says it cannot see names, and
// once names can be seen again it is applied again.
func TestTheEngineLooksAgainWhetherNamesCanBeSeen(t *testing.T) {
	r := newRig(t, ytForLaptop, winter(t))
	var mu sync.Mutex
	blind := ""
	r.e.o.Blind = func() string { mu.Lock(); defer mu.Unlock(); return blind }
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if s := r.state1("yt"); !s.Applied || s.Problem != "" {
		t.Fatalf("setup: names are seen, the rule is applied: %+v", s)
	}
	mu.Lock()
	blind = "names cannot be seen: forward mode under flow offloading (set mode to ingress, or turn flow offloading off)" // fw4 reloaded with flow offloading on
	mu.Unlock()
	r.set(winter(t).Add(15*time.Second), 15*time.Second)
	r.e.Tick(winter(t).Add(15 * time.Second))
	if s := r.state1("yt"); s.Applied || s.Problem == "" {
		t.Fatalf("one tick after offloading came on, the rule says it cannot see names: %+v", s)
	}
	mu.Lock()
	blind = ""
	mu.Unlock()
	r.set(winter(t).Add(30*time.Second), 30*time.Second)
	r.e.Tick(winter(t).Add(30 * time.Second))
	if s := r.state1("yt"); !s.Applied || s.Problem != "" {
		t.Fatalf("one tick after offloading went off, the rule is applied again: %+v", s)
	}
}

// R42: the console and the agent read the live allowances of each rule:
// who, until when and by whom (an agent_ section is the agent's). A blanket
// allowance lifts every rule but a country one; a member outside the rule
// lifts nothing of it.
func TestStateListsTheLiveAllowancesOfEachRule(t *testing.T) {
	at := winter(t)
	conf := tgForPhone + ytForLaptop + fmt.Sprintf(`
config rule 'ru'
	option action 'block'
	option what 'country'
	list target 'RU'
	option who 'all'

config allow 'a1'
	option rule 'tg'
	option member '02:4e:58:00:00:03'
	option until '%d'

config allow 'agent_x'
	option rule '*'
	option member '02:4e:58:00:00:03'
	option until '%d'

config allow 'a2'
	option rule 'tg'
	option member '02:4e:58:00:00:02'
	option until '%d'
`, at.Add(30*time.Minute).Unix(), at.Add(time.Hour).Unix(), at.Add(time.Hour).Unix())
	r := newRig(t, conf, at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	tg := r.state1("tg").Allows
	if len(tg) != 2 {
		t.Fatalf("tg allows: %+v", tg)
	}
	sort.Slice(tg, func(i, j int) bool { return tg[i].ID < tg[j].ID })
	if tg[0] != (LiveAllow{ID: "a1", Member: phoneMAC, Until: time.Unix(at.Add(30*time.Minute).Unix(), 0), By: "owner"}) ||
		tg[1] != (LiveAllow{ID: "agent_x", Member: phoneMAC, Until: time.Unix(at.Add(time.Hour).Unix(), 0), By: "agent"}) {
		t.Errorf("tg allows: %+v", tg)
	}
	if a := r.state1("yt").Allows; len(a) != 0 {
		t.Errorf("the phone is not in yt, and a2 names another rule, so nothing of yt is lifted: %+v", a)
	}
	if a := r.state1("ru").Allows; len(a) != 0 {
		t.Errorf("a country is never lifted: %+v", a)
	}
	r.set(at.Add(45*time.Minute), 45*time.Minute)
	if a := r.state1("tg").Allows; len(a) != 1 || a[0].ID != "agent_x" {
		t.Errorf("an ended allowance is not live: %+v", a)
	}
}

// A handshake between two of the house's own addresses (plex.direct and the
// like) never crosses the forward chain's rules, so it is never cut either.
func TestANamedHandshakeToAHomeAddressIsNotCut(t *testing.T) {
	r := newRig(t, `
config rule 'everyone'
	option action 'block'
	option what 'all'
	option who 'all'
`, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.Named(phoneMAC, netip.MustParseAddrPort("192.168.50.13:40000"), netip.MustParseAddrPort("192.168.50.20:32400"), 6, "api.telegram.org")
	r.e.Named(phoneMAC, netip.MustParseAddrPort("192.168.50.13:40001"), netip.MustParseAddrPort("149.154.167.220:443"), 6, "api.telegram.org")
	r.settle()
	if c := r.cuts(); len(c) != 1 || c[0].SrcPort != 40001 {
		t.Fatalf("only the connection that leaves the house is cut: %+v", c)
	}
}

// R40, measured in the lab: a cut keep-alive connection to a shared front is
// picked up again by conntrack (tcp_loose) on its next packet. So a cut puts
// the device and the address into the rule's own 10-minute set, and the
// rule's lines reject that pair: the re-created flow is refused.
func TestACutKeepsTheDeviceFromThatAddressForTenMinutes(t *testing.T) {
	r := newRig(t, ytForLaptop, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	applied := r.nft.lastApply(t)
	for _, want := range []string{
		"add set inet netryx_rules cut_yt_v4 { type ether_addr . ipv4_addr . inet_proto . inet_service; flags timeout; timeout 10m; size 1024; }",
		"add set inet netryx_rules cut_yt_v6 { type ether_addr . ipv6_addr . inet_proto . inet_service; flags timeout; timeout 10m; size 1024; }",
		"add rule inet netryx_rules rules_fwd ether saddr @members_yt meta l4proto { tcp, udp } ether saddr . ip daddr . meta l4proto . th sport @cut_yt_v4 meta l4proto tcp counter name hits_yt reject with tcp reset",
		"add rule inet netryx_rules rules_fwd ether saddr @members_yt meta l4proto { tcp, udp } ether saddr . ip6 daddr . meta l4proto . th sport @cut_yt_v6 counter name hits_yt reject with icmpx admin-prohibited",
	} {
		if !strings.Contains(applied, want) {
			t.Fatalf("missing %q in\n%s", want, applied)
		}
	}
	// A handshake to a shared front, and a live flow found by the tick.
	r.e.Named(laptopMAC, netip.MustParseAddrPort("192.168.50.12:51002"), netip.MustParseAddrPort("142.250.2.2:443"), 6, "www.youtube.com")
	r.settle()
	r.mu.Lock()
	r.live = []flows.Flow{flow(9, laptopMAC, "[2a02:c7c::12]:51003", "[2a00:1450:4009::2]:443", "youtube", "")}
	r.mu.Unlock()
	r.e.Tick(winter(t).Add(time.Second))
	if err := r.e.Flush(); err != nil {
		t.Fatal(err)
	}
	got := r.nft.lastApply(t)
	for _, want := range []string{
		"add element inet netryx_rules cut_yt_v4 { 02:4e:58:00:00:02 . 142.250.2.2 . tcp . 51002 timeout 10m }",
		"add element inet netryx_rules cut_yt_v6 { 02:4e:58:00:00:02 . 2a00:1450:4009::2 . tcp . 51003 timeout 10m }",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	// Removing the rule takes its cut sets with it.
	for _, o := range []string{"set cut_yt_v4", "set cut_yt_v6"} {
		if !r.nft.objects[o] {
			t.Fatalf("setup: %s should exist: %v", o, r.nft.objects)
		}
	}
	r.write("")
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{"set cut_yt_v4", "set cut_yt_v6"} {
		if r.nft.objects[o] {
			t.Fatalf("%s outlived its rule", o)
		}
	}
}

// A cut pair stands for what the rule blocked when it was cut: when the rule
// is changed to block something else, its cut sets are emptied in the same
// transaction; a reload with nothing changed leaves them.
func TestAChangedAppRuleEmptiesItsCutSet(t *testing.T) {
	r := newRig(t, ytForLaptop, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.nft.lastApply(t), "flush set inet netryx_rules cut_yt_v4") {
		t.Fatal("a first apply has nothing to empty")
	}
	before := len(r.nft.applies())
	r.write(strings.Replace(ytForLaptop, "'youtube'", "'netflix'", 1))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	got := r.nft.applies()[before].stdin
	for _, want := range []string{"flush set inet netryx_rules cut_yt_v4", "flush set inet netryx_rules cut_yt_v6"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q after the rule changed:\n%s", want, got)
		}
	}
	n := len(r.nft.applies())
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, a := range r.nft.applies()[n:] {
		if strings.Contains(a.stdin, "flush set inet netryx_rules cut_yt") {
			t.Fatalf("a reload with nothing changed emptied the cut set:\n%s", a.stdin)
		}
	}
}

// R43: a cut pair names the reborn flow only (device, address and the
// device's own port), and only a device with a hardware address has one: a
// routed client under a rule for everyone, or an unknown neighbour, adds no
// pair, so nft never refuses the batch and the other devices' pairs go in.
func TestACutPairIsTheRebornFlowOnly(t *testing.T) {
	r := newRig(t, `
config rule 'yt'
	option action 'block'
	option what 'app'
	list target 'youtube'
	option who 'all'
`, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.Named("", netip.MustParseAddrPort("10.8.0.2:40000"), netip.MustParseAddrPort("142.250.2.2:443"), 6, "www.youtube.com")
	r.e.Named("not-a-mac", netip.MustParseAddrPort("192.168.50.99:40001"), netip.MustParseAddrPort("142.250.2.3:443"), 6, "www.youtube.com")
	r.e.Named(laptopMAC, netip.MustParseAddrPort("192.168.50.12:51002"), netip.MustParseAddrPort("142.250.2.2:443"), 6, "www.youtube.com")
	r.settle()
	if len(r.cuts()) != 3 {
		t.Fatalf("every covered connection is still cut: %+v", r.cuts())
	}
	// nft refuses a whole batch with an element that is not a hardware address.
	r.nft.fail = func(script string) error {
		if strings.Contains(script, "{  . ") || strings.Contains(script, ",  . ") || strings.Contains(script, "not-a-mac") {
			return errors.New("Error: Could not process rule: invalid ether address")
		}
		return nil
	}
	if err := r.e.Flush(); err != nil {
		t.Fatal(err)
	}
	got := r.nft.lastApply(t)
	if !strings.Contains(got, "add element inet netryx_rules cut_yt_v4 { 02:4e:58:00:00:02 . 142.250.2.2 . tcp . 51002 timeout 10m }") {
		t.Fatalf("the device's pair is added:\n%s", got)
	}
	for _, never := range []string{"{  . ", ",  . ", "not-a-mac", "40000", "40001"} {
		if strings.Contains(got, never) {
			t.Fatalf("a pair without a hardware address (%q) went to nft:\n%s", never, got)
		}
	}
}

// R45: a cut entry is one flow, by protocol and the device's port: another
// connection of the same device to the same address is not in the set, and
// a QUIC flow on the same port is an entry of its own. Nothing that is not
// TCP or UDP enters.
func TestACutEntryIsOneFlowByProtocolAndPort(t *testing.T) {
	r := newRig(t, ytForLaptop, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	front := netip.MustParseAddrPort("142.250.2.2:443")
	r.e.Named(laptopMAC, netip.MustParseAddrPort("192.168.50.12:51002"), front, 6, "www.youtube.com")
	r.e.Named(laptopMAC, netip.MustParseAddrPort("192.168.50.12:51002"), front, 17, "www.youtube.com")
	r.e.Named(laptopMAC, netip.MustParseAddrPort("192.168.50.12:51005"), front, 6, "www.example.org") // not an app: not cut
	r.e.Named(laptopMAC, netip.MustParseAddrPort("192.168.50.12:51006"), front, 1, "www.youtube.com") // no ports to key
	r.settle()
	if err := r.e.Flush(); err != nil {
		t.Fatal(err)
	}
	got := r.nft.lastApply(t)
	for _, want := range []string{"02:4e:58:00:00:02 . 142.250.2.2 . tcp . 51002 timeout 10m", "02:4e:58:00:00:02 . 142.250.2.2 . udp . 51002 timeout 10m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	for _, never := range []string{"51005", "51006"} {
		if strings.Contains(got, never) {
			t.Fatalf("port %s of the same device and address must not be in the cut set:\n%s", never, got)
		}
	}
	if n := strings.Count(got, " . 142.250.2.2 . "); n != 2 {
		t.Fatalf("one entry per flow, got %d:\n%s", n, got)
	}
}

// Measured on the Flint 2 (D1): the console's cut button deleted the
// conntrack entry only, and tcp_loose took the connection up again on its next
// packet, on the same NAT port. So a button cut puts the flow (the device's
// hardware address, the remote address, the protocol and the device's own
// port) into the owner's 10-minute cut set before the delete, and a line of
// rules_fwd rejects that flow; another port to the same remote is not in it.
// The set is declared by every ruleset, the boot copy included, and no prune
// takes it away. A device with no hardware address adds nothing, and its
// connection is still deleted.
func TestAButtonCutStaysCut(t *testing.T) {
	r := newRig(t, "", winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	decl := []string{
		"add set inet netryx_rules cut_v4 { type ether_addr . ipv4_addr . inet_proto . inet_service; flags timeout; timeout 10m; size 1024; }",
		"add set inet netryx_rules cut_v6 { type ether_addr . ipv6_addr . inet_proto . inet_service; flags timeout; timeout 10m; size 1024; }",
	}
	lines := []string{
		"add rule inet netryx_rules rules_fwd meta l4proto { tcp, udp } ether saddr . ip daddr . meta l4proto . th sport @cut_v4 meta l4proto tcp reject with tcp reset",
		"add rule inet netryx_rules rules_fwd meta l4proto { tcp, udp } ether saddr . ip daddr . meta l4proto . th sport @cut_v4 reject with icmpx admin-prohibited",
		"add rule inet netryx_rules rules_fwd meta l4proto { tcp, udp } ether saddr . ip6 daddr . meta l4proto . th sport @cut_v6 meta l4proto tcp reject with tcp reset",
		"add rule inet netryx_rules rules_fwd meta l4proto { tcp, udp } ether saddr . ip6 daddr . meta l4proto . th sport @cut_v6 reject with icmpx admin-prohibited",
	}
	applied := r.nft.lastApply(t)
	boot, err := os.ReadFile(filepath.Join(r.state, "rules.nft"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range append(decl, lines...) {
		if !strings.Contains(applied, want) {
			t.Fatalf("missing %q in the ruleset of a house with no rules:\n%s", want, applied)
		}
		if !strings.Contains(string(boot), want) {
			t.Fatalf("missing %q in the boot copy:\n%s", want, boot)
		}
	}

	// The flow is in the set before conntrack forgets it, so the packet
	// that would revive it already meets the reject.
	var inSetAtDelete []bool
	element := "add element inet netryx_rules cut_v4 { 02:4e:58:00:00:02 . 78.46.170.2 . tcp . 51002 timeout 10m }"
	r.e.o.Delete = func(ct.Tuple) error {
		inSetAtDelete = append(inSetAtDelete, strings.Contains(r.nft.lastApply(t), element))
		return nil
	}
	if _, err := r.e.Cut(laptopMAC, netip.MustParseAddrPort("192.168.50.12:51002"), netip.MustParseAddrPort("78.46.170.2:443"), 6, false); err != nil {
		t.Fatal(err)
	}
	if len(inSetAtDelete) != 1 || !inSetAtDelete[0] {
		t.Fatalf("the flow must be in cut_v4 before its entry is deleted (deleted %d, in set %v):\n%s", len(inSetAtDelete), inSetAtDelete, r.nft.lastApply(t))
	}
	// A connection opened from outside, over IPv6 and UDP, by its device's side.
	if _, err := r.e.Cut(laptopMAC, netip.MustParseAddrPort("[2a02:c7c::12]:51003"), netip.MustParseAddrPort("[2a00:1450:4009::2]:443"), 17, true); err != nil {
		t.Fatal(err)
	}
	if got := r.nft.lastApply(t); !strings.Contains(got, "add element inet netryx_rules cut_v6 { 02:4e:58:00:00:02 . 2a00:1450:4009::2 . udp . 51003 timeout 10m }") {
		t.Fatalf("an inbound UDP flow is keyed by the device's own port:\n%s", got)
	}
	if len(r.cutBack) != 1 {
		t.Fatalf("an inbound flow is deleted by its reply tuple: %+v", r.cutBack)
	}
	// Without a hardware address there is no key: nothing goes to nft, the
	// entry is still deleted.
	before := len(r.nft.applies())
	for _, mac := range []string{"", "192.168.50.99"} {
		if _, err := r.e.Cut(mac, netip.MustParseAddrPort("192.168.50.99:40000"), netip.MustParseAddrPort("78.46.170.2:443"), 6, false); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(r.nft.applies()); n != before {
		t.Fatalf("a device without a hardware address added to the cut set:\n%s", r.nft.lastApply(t))
	}
	if len(inSetAtDelete) != 3 {
		t.Fatalf("its connections are still deleted: %d deletes", len(inSetAtDelete))
	}
	// No rule owns the set, so neither adding nor removing a rule prunes it.
	r.write(ytForLaptop)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.write("")
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{"set cut_v4", "set cut_v6"} {
		if !r.nft.objects[o] {
			t.Fatalf("%s was pruned: %v", o, r.nft.objects)
		}
	}
}

// A button cut netryx could not hold is not reported as held: when nft
// refuses the element (the set is full at 1024, or any other error), when no
// ruleset is applied yet, or when the device has no hardware address, the
// connection is still deleted and Cut says, in English, that it may come
// back. A held cut says nothing.
func TestACutThatIsNotHeldSaysSo(t *testing.T) {
	local, remote := netip.MustParseAddrPort("192.168.50.12:51002"), netip.MustParseAddrPort("78.46.170.2:443")
	r := newRig(t, "", winter(t))
	if note, err := r.e.Cut(laptopMAC, local, remote, 6, false); err != nil || note == "" {
		t.Fatalf("before any ruleset is applied the cut is not held: %q %v", note, err)
	}
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if note, err := r.e.Cut(laptopMAC, local, remote, 6, false); err != nil || note != "" {
		t.Fatalf("a held cut says nothing: %q %v", note, err)
	}
	r.nft.fail = func(script string) error {
		if strings.Contains(script, "add element inet netryx_rules cut_v4") {
			return errors.New("Error: Could not process rule: No space left on device")
		}
		return nil
	}
	note, err := r.e.Cut(laptopMAC, local, remote, 6, false)
	if err != nil || !strings.Contains(note, "may come back") {
		t.Fatalf("a refused hold is said: %q %v", note, err)
	}
	if note, err := r.e.Cut("", local, remote, 6, false); err != nil || note == "" {
		t.Fatalf("a device without a hardware address is not held: %q %v", note, err)
	}
	if n := len(r.cuts()); n != 4 {
		t.Fatalf("every one of them is still deleted: %d deletes", n)
	}
	// nft prints the offending line and a caret after its message; the note
	// carries the first line only.
	r.nft.fail = func(script string) error {
		if strings.Contains(script, "add element inet netryx_rules cut_v4") {
			return errors.New("Error: Could not process rule: No space left on device\nadd element inet netryx_rules cut_v4 { 1 }\n^^^^")
		}
		return nil
	}
	note, err = r.e.Cut(laptopMAC, local, remote, 6, false)
	if err != nil || !strings.Contains(note, "(Error: Could not process rule: No space left on device)") || strings.ContainsAny(note, "\n^") {
		t.Fatalf("a multi-line nft error is told by its first line: %q %v", note, err)
	}
}

// A cut that cannot be made adds nothing to the owner's set: with nothing to
// delete connections with, the hold would only keep a flow out that was
// never cut.
func TestACutThatCannotBeMadeIsNotHeld(t *testing.T) {
	local, remote := netip.MustParseAddrPort("192.168.50.12:51002"), netip.MustParseAddrPort("78.46.170.2:443")
	r := newRig(t, "", winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.o.Delete = nil
	var added bool
	r.nft.fail = func(script string) error {
		if strings.Contains(script, "add element inet netryx_rules cut_v4") {
			added = true
		}
		return nil
	}
	if _, err := r.e.Cut(laptopMAC, local, remote, 6, false); err == nil {
		t.Fatal("with nothing to delete with, Cut is an error")
	}
	if added {
		t.Fatal("no hold is added for a cut that cannot be made")
	}
}

// R43 minor: the allowances a rule lists are the ones in force: one written
// while the apply is refused lifts nothing yet, so it is not shown as live.
func TestListedAllowancesAreTheOnesInForce(t *testing.T) {
	at := winter(t)
	a1 := fmt.Sprintf("\nconfig allow 'a1'\n\toption rule 'tg'\n\toption member '02:4e:58:00:00:03'\n\toption until '%d'\n", at.Add(30*time.Minute).Unix())
	a2 := fmt.Sprintf("\nconfig allow 'a2'\n\toption rule 'tg'\n\toption member '02:4e:58:00:00:03'\n\toption until '%d'\n", at.Add(time.Hour).Unix())
	r := newRig(t, tgForPhone+a1, at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.write(tgForPhone + a1 + a2)
	r.nft.fail = func(string) error { return errors.New("Error: Could not process rule: No such file or directory") }
	if err := r.e.Reload(); err == nil {
		t.Fatal("setup: the apply must be refused")
	}
	if a := r.state1("tg").Allows; len(a) != 1 || a[0].ID != "a1" {
		t.Fatalf("only the allowance in force is live: %+v", a)
	}
}

// SaveSets replaces sets.nft only with a whole listing: if any set could not
// be listed, the file the boot loads stays as it was.
func TestSaveSetsKeepsTheOldFileWhenAListFails(t *testing.T) {
	r := newRig(t, ytForLaptop+tgForPhone, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.state, "sets.nft")
	saved := "table inet netryx_rules {\n\tset app_youtube_v4 {\n\t\telements = { 142.250.1.1 expires 20h }\n\t}\n}\n"
	if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	r.nft.listSet["app_telegram_v4"] = "table inet netryx_rules {\n\tset app_telegram_v4 {\n\t\telements = { 149.154.167.220 expires 23h }\n\t}\n}\n"
	r.nft.listSet["app_telegram_v6"] = "table inet netryx_rules {\n\tset app_telegram_v6 {\n\t}\n}\n"
	// app_youtube_v4 and _v6 are not listed: nft answers an error for them.
	if err := r.e.SaveSets(); err == nil {
		t.Fatal("a failed listing is an error")
	}
	if b, _ := os.ReadFile(path); string(b) != saved {
		t.Fatalf("a partial listing replaced sets.nft:\n%s", b)
	}
}

// A handshake's name is chosen by the device (R18): its destination enters
// the app's set only when the router's resolver gave that address for the
// app, or it lies in the app's organisation ranges. Otherwise only the
// covered device's connection is cut.
func TestANamedHandshakeAddsOnlyWhatAnAnswerOrTheOrgVouchesFor(t *testing.T) {
	r := newRig(t, "", winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	// The resolver answered before any rule declared the set.
	r.e.Answer("rr7.googlevideo.com", net.ParseIP("142.250.7.7"))
	// An answer for another app vouches for nothing of this one.
	r.e.Answer("www.netflix.com", net.ParseIP("198.51.100.8"))
	r.write(ytForLaptop + tgForPhone)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.Named(laptopMAC, netip.MustParseAddrPort("192.168.50.12:50001"), netip.MustParseAddrPort("203.0.113.9:443"), 6, "rr9.googlevideo.com")
	r.e.Named(laptopMAC, netip.MustParseAddrPort("192.168.50.12:50002"), netip.MustParseAddrPort("142.250.7.7:443"), 6, "rr7.googlevideo.com")
	r.e.Named(phoneMAC, netip.MustParseAddrPort("192.168.50.13:50003"), netip.MustParseAddrPort("149.154.167.220:443"), 6, "api.telegram.org")
	r.e.Named(phoneMAC, netip.MustParseAddrPort("192.168.50.13:50004"), netip.MustParseAddrPort("198.51.100.4:443"), 6, "api.telegram.org")
	r.e.Named(laptopMAC, netip.MustParseAddrPort("192.168.50.12:50005"), netip.MustParseAddrPort("198.51.100.8:443"), 6, "rr8.googlevideo.com")
	r.settle()
	if c := r.cuts(); len(c) != 5 {
		t.Fatalf("every covered member's connection is cut, vouched for or not: %+v", c)
	}
	if err := r.e.Flush(); err != nil {
		t.Fatal(err)
	}
	got := r.nft.lastApply(t)
	for _, want := range []string{
		"add element inet netryx_rules app_youtube_v4 { 142.250.7.7 timeout 24h }",
		"add element inet netryx_rules app_telegram_v4 { 149.154.167.220 timeout 24h }",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("an address an answer or the organisation vouches for is added: missing %q in\n%s", want, got)
		}
	}
	for _, never := range []string{"203.0.113.9", "198.51.100.4", "198.51.100.8"} {
		if strings.Contains(appLines(got), never) {
			t.Fatalf("a name the device chose must not put %s in a set:\n%s", never, got)
		}
	}
}

// Fix round 1, item 1: the router's own connections are not the house's.
// The capture copies the router's outbound handshakes too; a rule for
// everyone (a pause, a country, an app) must never cut them, as the kernel
// rules in the forward chain never touch them.
func TestTheRoutersOwnHandshakeIsNeverCut(t *testing.T) {
	r := newRig(t, `
config rule 'everyone'
	option action 'block'
	option what 'all'
	option who 'all'
`, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.Named(flows.RouterKey, netip.MustParseAddrPort("192.168.50.1:40000"), netip.MustParseAddrPort("149.154.167.220:443"), 6, "api.telegram.org")
	r.e.Named("", netip.MustParseAddrPort("192.168.50.99:40001"), netip.MustParseAddrPort("149.154.167.221:443"), 6, "api.telegram.org")
	r.settle()
	c := r.cuts()
	if len(c) != 1 || c[0].SrcPort != 40001 {
		t.Fatalf("the router's own handshake is never cut; a device's under a rule for everyone is: %+v", c)
	}
}

// Item 2: an answer vouches for an address as long as the set would keep
// it, a day, and no longer.
func TestEvidenceExpiresAfterADay(t *testing.T) {
	t0 := winter(t)
	r := newRig(t, "", t0)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.Answer("rr7.googlevideo.com", net.ParseIP("142.250.7.7"))
	r.write(ytForLaptop)
	r.set(t0.Add(25*time.Hour), 25*time.Hour)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.Named(laptopMAC, netip.MustParseAddrPort("192.168.50.12:50002"), netip.MustParseAddrPort("142.250.7.7:443"), 6, "rr7.googlevideo.com")
	r.settle()
	if err := r.e.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, a := range r.nft.applies() {
		if strings.Contains(appLines(a.stdin), "142.250.7.7") {
			t.Fatalf("an answer older than a day vouches for nothing:\n%s", a.stdin)
		}
	}
	if len(r.cuts()) != 1 {
		t.Fatal("the covered member's connection is still cut")
	}
}

// Item 4: sets.nft is what the boot loads; a process that never applied the
// rules knows nothing to save, and an empty listing never replaces saved
// addresses.
func TestSetsNftIsNeverEmptiedByWhatThisProcessDoesNotKnow(t *testing.T) {
	r := newRig(t, ytForLaptop, winter(t))
	path := filepath.Join(r.state, "sets.nft")
	saved := "table inet netryx_rules {\n\tset app_youtube_v4 {\n\t\telements = { 142.250.1.1 expires 20h }\n\t}\n}\n"
	if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	r.nft.fail = func(string) error { return errors.New("Error: Could not process rule: No such file or directory") }
	if err := r.e.Reload(); err == nil {
		t.Fatal("setup: the first apply must fail")
	}
	_ = r.e.SaveSets()
	if b, _ := os.ReadFile(path); string(b) != saved {
		t.Fatalf("a process that never applied must not touch sets.nft:\n%s", b)
	}
	r.nft.fail = nil
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.nft.listSet["app_youtube_v4"] = "table inet netryx_rules {\n\tset app_youtube_v4 {\n\t}\n}\n"
	r.nft.listSet["app_youtube_v6"] = "table inet netryx_rules {\n\tset app_youtube_v6 {\n\t}\n}\n"
	_ = r.e.SaveSets()
	if b, _ := os.ReadFile(path); string(b) != saved {
		t.Fatalf("empty sets must not replace saved addresses:\n%s", b)
	}
	r.nft.listSet["app_youtube_v4"] = "table inet netryx_rules {\n\tset app_youtube_v4 {\n\t\telements = { 142.250.9.9 expires 23h }\n\t}\n}\n"
	if err := r.e.SaveSets(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "142.250.9.9") {
		t.Fatalf("what the sets hold now is saved:\n%s", b)
	}
}

// Item 5: an address whose batch nft refused is not taken as sent.
func TestAFailedFlushDoesNotLoseTheAddress(t *testing.T) {
	r := newRig(t, ytForLaptop, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.nft.fail = func(s string) error {
		if strings.Contains(s, "add element") {
			return errors.New("Error: Could not process rule: Too many open files in system")
		}
		return nil
	}
	r.e.Answer("rr1.googlevideo.com", net.ParseIP("142.250.1.1"))
	if err := r.e.Flush(); err == nil {
		t.Fatal("setup: the batch must be refused")
	}
	r.nft.fail = nil
	n := len(r.nft.applies())
	r.e.Answer("rr1.googlevideo.com", net.ParseIP("142.250.1.1"))
	if err := r.e.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(r.nft.applies()) != n+1 {
		t.Fatal("the address whose batch was refused is sent again with the next answer")
	}
	if got := r.nft.lastApply(t); !strings.Contains(got, "app_youtube_v4 { 142.250.1.1 timeout 24h }") {
		t.Fatalf("the next answer sends the address again:\n%s", got)
	}
}

// Item 7: the first tick compares against the reload, so NTP setting the
// clock back between the start and the first tick recompiles.
func TestABackwardJumpBeforeTheFirstTickRecompiles(t *testing.T) {
	t0 := winter(t)
	r := newRig(t, tgForPhone+fmt.Sprintf("\toption until '%d'\n", t0.Add(5*time.Hour).Unix()), t0)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	n := len(r.nft.applies())
	r.set(t0.Add(-2*time.Hour), 15*time.Second)
	r.e.Tick(t0.Add(-2 * time.Hour))
	if len(r.nft.applies()) != n+1 {
		t.Fatal("a jump between the reload and the first tick recompiles")
	}
}

// Item 9: country and app sets exist only while a rule uses them.
func TestSetsNoRuleUsesArePruned(t *testing.T) {
	ru := `
config rule 'ru'
	option action 'block'
	option what 'country'
	list target 'RU'
	option who 'all'
`
	r := newRig(t, tgForPhone+ytForLaptop+ru, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{"set app_telegram_v4", "set app_telegram_org_v6", "set cc_ru_v4", "set app_youtube_v6"} {
		if !r.nft.objects[o] {
			t.Fatalf("setup: %s should exist: %v", o, r.nft.objects)
		}
	}
	r.e.Answer("api.telegram.org", net.ParseIP("149.154.167.220"))
	r.write(ytForLaptop)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{"set app_telegram_v4", "set app_telegram_v6", "set app_telegram_org_v4", "set app_telegram_org_v6", "set cc_ru_v4", "set cc_ru_v6"} {
		if r.nft.objects[o] {
			t.Fatalf("%s is used by no rule and must go:\n%s", o, r.nft.lastApply(t))
		}
	}
	for _, o := range []string{"set app_youtube_v4", "set app_youtube_v6", "set members_yt"} {
		if !r.nft.objects[o] {
			t.Fatalf("%s is still used", o)
		}
	}
	n := len(r.nft.applies())
	if err := r.e.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, a := range r.nft.applies()[n:] {
		if strings.Contains(a.stdin, "add element inet netryx_rules app_telegram") {
			t.Fatalf("nothing is sent to a set that is gone:\n%s", a.stdin)
		}
	}
}

// Item 11: the capture loop calls Named; a kernel slow to answer a delete
// must not hold it.
func TestNamedDoesNotWaitForTheKernel(t *testing.T) {
	r := newRig(t, tgForPhone, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	r.e.o.Delete = func(ct.Tuple) error { <-release; return nil }
	done := make(chan struct{})
	go func() {
		r.e.Named(phoneMAC, netip.MustParseAddrPort("192.168.50.13:51000"), netip.MustParseAddrPort("149.154.167.220:443"), 6, "api.telegram.org")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Named waited for the delete")
	}
	close(release)
	r.settle()
}

// Item 12: shared address space and the house's own addresses (its LAN GUA
// prefix included) are never an app's destination.
func TestAnAnswerForAHouseOrSharedAddressFillsNothing(t *testing.T) {
	r := newRig(t, ytForLaptop, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	n := len(r.nft.applies())
	r.e.Answer("rr1.googlevideo.com", net.ParseIP("100.64.1.1"))
	r.e.Answer("rr2.googlevideo.com", net.ParseIP("2a02:c7c::9"))
	if err := r.e.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(r.nft.applies()) != n {
		t.Fatalf("an address of the house or of shared address space entered a set:\n%s", r.nft.lastApply(t))
	}
}

// Item 13: the capture loop, the reload, the tick, the flush and the save
// run on different goroutines; under -race this is the proof they share
// nothing unguarded.
func TestTheEngineIsSafeUnderConcurrentUse(t *testing.T) {
	r := newRig(t, tgForPhone+ytForLaptop, winter(t))
	r.live = []flows.Flow{flow(1, phoneMAC, "192.168.50.13:40001", "149.154.167.220:443", "telegram", "GB")}
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				ip := net.IPv4(142, 250, byte(i), byte(n))
				r.e.Answer("rr1.googlevideo.com", ip)
				r.e.Named(phoneMAC, netip.MustParseAddrPort("192.168.50.13:51000"), netip.AddrPortFrom(netip.AddrFrom4([4]byte{149, 154, 167, byte(n)}), 443), 6, "api.telegram.org")
				_ = r.e.State()
			}
		}(i)
	}
	for k := 0; k < 30; k++ {
		_ = r.e.Reload()
		_ = r.e.Flush()
		_ = r.e.SaveSets()
		r.e.Tick(winter(t).Add(time.Duration(k) * time.Second))
	}
	close(stop)
	wg.Wait()
	r.settle()
}

// The API validates a would-be rule with the catalog the engine itself
// applies with, so a rule it lets through is one the engine reads as valid.
func TestTheEngineHandsItsCatalogToTheAPI(t *testing.T) {
	r := newRig(t, tgForPhone, winter(t))
	k := r.e.Known()
	if !k.Country("RU") || !k.Country("GB") || k.Country("XX") {
		t.Errorf("countries: RU %v GB %v XX %v", k.Country("RU"), k.Country("GB"), k.Country("XX"))
	}
	if !k.App("telegram") || k.App("nope") || !k.Category("video") || k.Category("nope") {
		t.Errorf("apps or categories not as loaded")
	}
}

// The console cuts one connection the owner points at, through the same
// delete the engine uses for a rule: by the original tuple for a flow the
// device opened and by the reply tuple for one opened from outside.
func TestCutEndsOneConnectionByTheSideTheDeviceIsOn(t *testing.T) {
	r := newRig(t, tgForPhone, winter(t))
	local := netip.MustParseAddrPort("192.168.50.10:40000")
	remote := netip.MustParseAddrPort("149.154.167.50:443")
	if _, err := r.e.Cut(phoneMAC, local, remote, 6, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.Cut(phoneMAC, local, remote, 17, true); err != nil {
		t.Fatal(err)
	}
	if c := r.cuts(); len(c) != 1 || c[0].Proto != 6 || c[0].SrcPort != 40000 || c[0].DstPort != 443 || c[0].SrcIP.String() != "192.168.50.10" {
		t.Errorf("deleted by the original tuple: %+v", c)
	}
	if len(r.cutBack) != 1 || r.cutBack[0].Proto != 17 || r.cutBack[0].SrcPort != 40000 {
		t.Errorf("deleted by the reply tuple: %+v", r.cutBack)
	}
	if _, err := r.e.Cut(phoneMAC, local, remote, 1, false); err == nil {
		t.Error("ICMP has no ports: it is refused, not deleted")
	}
	if len(r.cuts()) != 1 {
		t.Error("a refused cut deleted something")
	}
}

const ytLimitLaptop = `
config rule 'ytlim'
	option action 'limit'
	option what 'app'
	list target 'youtube'
	option who 'device'
	list member '02:4e:58:00:00:02'
	option minutes '1'
`

// useMinute gives the laptop one counted minute of an app at the rig's time.
func (r *rig) useMinute(mac, app string) {
	r.mu.Lock()
	at := r.now
	r.mu.Unlock()
	r.usage.Add(mac, app, 70000, 0, at)
}

func TestALimitBlocksAtItsMinutesAndCutsThatAppOnly(t *testing.T) {
	at := summer(t)
	r := newRig(t, ytLimitLaptop, at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if s := r.nft.lastApply(t); strings.Contains(s, "add element inet netryx_rules spent_ytlim") {
		t.Fatalf("nothing used yet, but the laptop is spent:\n%s", s)
	}
	r.mu.Lock()
	r.live = []flows.Flow{
		flow(1, laptopMAC, "192.168.50.20:50000", "142.250.1.1:443", "youtube", "US"),
		flow(2, laptopMAC, "192.168.50.20:50001", "149.154.167.50:443", "telegram", "GB"),
	}
	r.mu.Unlock()
	r.useMinute(laptopMAC, "youtube")
	r.set(at.Add(time.Minute), time.Minute)
	r.e.Tick(at.Add(time.Minute))
	r.settle()
	s := r.nft.lastApply(t)
	midnight := NextMidnight(london(t), at.Add(time.Minute))
	want := fmt.Sprintf("add element inet netryx_rules spent_ytlim { %s timeout %ds }", laptopMAC, int(midnight.Sub(at.Add(time.Minute)).Seconds()))
	if !strings.Contains(s, want) {
		t.Fatalf("after its minute the laptop is not spent until midnight; want %q in\n%s", want, s)
	}
	cuts := r.cuts()
	if len(cuts) != 1 || cuts[0].DstPort != 443 || cuts[0].SrcPort != 50000 {
		t.Fatalf("cuts = %+v; want only the YouTube flow", cuts)
	}
	if st := r.state1("ytlim"); st.Used[laptopMAC] != 1 {
		t.Fatalf("State().Used = %v; want the laptop at 1 minute", st.Used)
	}
	if u := r.e.UsageOf(laptopMAC); len(u) != 1 || u[0].Used != 1 || u[0].Limit != 1 || !u[0].SpentUntil.Equal(midnight) {
		t.Fatalf("UsageOf = %+v", u)
	}
}

func TestAnAllowanceLiftsASpentLimit(t *testing.T) {
	at := summer(t)
	conf := ytLimitLaptop + fmt.Sprintf(`
config allow 'a1'
	option rule 'ytlim'
	option member '%s'
	option until '%d'
`, laptopMAC, at.Add(time.Hour).Unix())
	r := newRig(t, conf, at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.useMinute(laptopMAC, "youtube")
	r.usage.Close(at.Add(time.Minute))
	r.set(at.Add(time.Minute), time.Minute)
	r.mu.Lock()
	r.now = at.Add(time.Minute)
	r.live = []flows.Flow{flow(1, laptopMAC, "192.168.50.20:50000", "142.250.1.1:443", "youtube", "US")}
	r.mu.Unlock()
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.cuts()) != 0 {
		t.Fatalf("an allowed spent device was cut: %+v", r.cuts())
	}
	s := r.nft.lastApply(t)
	if !strings.Contains(s, "add element inet netryx_rules spent_ytlim { "+laptopMAC) {
		t.Fatalf("the laptop spent its minute but is not in spent_ytlim:\n%s", s)
	}
	if !strings.Contains(s, "ether saddr @allow_ytlim return") {
		t.Fatalf("the allowance does not lift the limit in the kernel:\n%s", s)
	}
}

const filteredHouse = `
config filters 'filters'
	option force_dns '1'
	option safesearch '1'

config group 'kids'
	option name 'Діти'
	option filtered_dns '1'

config device 'd_024e58000003'
	option mac '02:4e:58:00:00:03'
	option group 'kids'
`

func TestTheFilteredResolverIsUsedOnlyWhileHealthy(t *testing.T) {
	at := summer(t)
	r := newRig(t, filteredHouse, at)
	r.safe.healthy, r.safe.problem = false, "DNS filter does not answer: refused"
	r.e.EnsureSafeDNS(at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if s := r.nft.lastApply(t); strings.Contains(s, ":553") || !strings.Contains(s, "redirect to :53") {
		t.Fatalf("an unhealthy safe resolver must leave filtered devices on the main one:\n%s", s)
	}
	if ok, p := r.e.SafeDNSState(); ok || !strings.Contains(p, "refused") {
		t.Fatalf("SafeDNSState = %v, %q", ok, p)
	}
	r.safe.mu.Lock()
	r.safe.healthy, r.safe.problem = true, ""
	r.safe.mu.Unlock()
	r.set(at.Add(time.Minute), time.Minute)
	r.e.safeLook(at.Add(time.Minute))
	if s := r.nft.lastApply(t); !strings.Contains(s, "ether saddr @filtered_dns meta l4proto { tcp, udp } th dport 53 redirect to :553") {
		t.Fatalf("the resolver answers again but filtered devices are not sent to it:\n%s", s)
	}
	r.safe.mu.Lock()
	calls := append([]string(nil), r.safe.calls...)
	r.safe.mu.Unlock()
	if len(calls) == 0 || calls[len(calls)-1] != "true off" {
		t.Fatalf("Ensure calls %v; want the last to ask for the resolver with YouTube off", calls)
	}
}

func TestUsageSurvivesAGracefulRestart(t *testing.T) {
	at := summer(t)
	r := newRig(t, ytLimitLaptop, at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.useMinute(laptopMAC, "youtube")
	r.usage.Close(at.Add(time.Minute))
	if err := r.e.SaveUsage(); err != nil {
		t.Fatal(err)
	}
	again := newRig(t, ytLimitLaptop, at.Add(2*time.Minute))
	again.state = r.state
	again.e.o.StateDir = r.state
	if err := again.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := again.usage.Minutes(laptopMAC, "youtube"); got != 1 {
		t.Fatalf("after a restart the laptop's minutes are %d; want 1", got)
	}
}

// The safe resolver's section lives in uncommitted uci changes and a reboot
// forgets it; the init loads rules.nft before netryx starts it again, so that
// copy never sends a filtered device to a resolver that is not there yet. The
// redirect comes back with the first apply once the resolver answers.
func TestTheBootRulesNeverSendToTheSafeResolver(t *testing.T) {
	at := summer(t)
	r := newRig(t, filteredHouse, at)
	r.safe.healthy = true
	r.e.EnsureSafeDNS(at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if s := r.nft.lastApply(t); !strings.Contains(s, "redirect to :553") {
		t.Fatalf("the running rules must send filtered devices to the safe resolver:\n%s", s)
	}
	b, err := os.ReadFile(filepath.Join(r.state, "rules.nft"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), ":553") || strings.Contains(string(b), "filtered_dns") {
		t.Fatalf("rules.nft, loaded at boot, sends filtered devices to a resolver a reboot forgot:\n%s", b)
	}
	if !strings.Contains(string(b), "redirect to :53") {
		t.Fatalf("rules.nft must still force DNS to the main resolver:\n%s", b)
	}
}

// A dead safe resolver takes seconds to restart and probe, and a WAN without
// DNS holds every lookup of the safe hosts: none of that may hold the engine's
// first apply, its flushes or its ticks.
func TestTheSafeResolverNeverHoldsTheEngine(t *testing.T) {
	at := summer(t)
	r := newRig(t, filteredHouse, at)
	hold := make(chan struct{})
	r.safe.mu.Lock()
	r.safe.hold = hold
	r.safe.mu.Unlock()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { r.e.Run(stop); close(done) }()
	defer func() { close(stop); close(hold); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for len(r.nft.applies()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the rules were not applied while the safe resolver was being looked at")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const videoLimitLaptop = `
config rule 'vidlim'
	option action 'limit'
	option what 'category'
	list target 'video'
	option who 'device'
	list member '02:4e:58:00:00:02'
	option minutes '2'
`

// A category limit blocks on the sum of its apps' minutes, so the device card
// shows that sum against the limit, and only the apps that were used.
func TestALimitShowsItsTotalAndTheAppsInUse(t *testing.T) {
	at := summer(t)
	r := newRig(t, videoLimitLaptop, at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.useMinute(laptopMAC, "youtube")
	r.useMinute(laptopMAC, "netflix")
	r.usage.Close(at.Add(time.Minute))
	r.set(at.Add(time.Minute), time.Minute)
	r.e.Tick(at.Add(time.Minute))
	u := r.e.UsageOf(laptopMAC)
	if len(u) != 1 {
		t.Fatalf("UsageOf = %+v; want one entry for the rule", u)
	}
	if u[0].Rule != "vidlim" || u[0].What != "category" || u[0].Used != 2 || u[0].Limit != 2 || u[0].SpentUntil.IsZero() {
		t.Fatalf("UsageOf = %+v; want 2 of 2, spent", u[0])
	}
	if len(u[0].Apps) != 2 || u[0].Apps[0].Used != 1 || u[0].Apps[1].Used != 1 {
		t.Fatalf("Apps = %+v; want youtube and netflix at 1 minute each", u[0].Apps)
	}
	other := newRig(t, videoLimitLaptop, at)
	if err := other.e.Reload(); err != nil {
		t.Fatal(err)
	}
	other.useMinute(laptopMAC, "youtube")
	other.usage.Close(at.Add(time.Minute))
	if u := other.e.UsageOf(laptopMAC); len(u) != 1 || len(u[0].Apps) != 1 || u[0].Apps[0].App != "youtube" {
		t.Fatalf("UsageOf = %+v; want only the app that was used", u)
	}
}

const isolatedCamera = `
config device 'd_024e58000004'
	option mac '02:4e:58:00:00:04'
	option isolate '1'
`

// Isolation is a security rule: it holds without netryx, so the copy the
// init loads at boot carries it.
func TestIsolationIsInTheBootRules(t *testing.T) {
	at := summer(t)
	r := newRig(t, isolatedCamera, at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(r.state, "rules.nft"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"add element inet netryx_rules isolated { 02:4e:58:00:00:04 }",
		"rules_in ether saddr @isolated jump iso_in", "add rule bridge netryx_iso iso_fwd ether saddr @isolated"} {
		if !strings.Contains(string(b), w) {
			t.Fatalf("rules.nft lacks %q:\n%s", w, b)
		}
	}
	if s := r.nft.lastApply(t); !strings.Contains(s, "add element inet netryx_rules isolated { 02:4e:58:00:00:04 }") {
		t.Fatalf("the running rules do not isolate the camera:\n%s", s)
	}
}

// The house's networks are re-read every 10 s; a new one (a guest network
// brought up) must be kept from isolated devices at the next tick.
func TestAChangeOfTheHouseNetworksRecompiles(t *testing.T) {
	at := summer(t)
	r := newRig(t, isolatedCamera, at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.house = append(r.house, netip.MustParsePrefix("192.168.60.0/24"))
	r.mu.Unlock()
	r.set(at.Add(15*time.Second), 15*time.Second)
	r.e.Tick(at.Add(15 * time.Second))
	if s := r.nft.lastApply(t); !strings.Contains(s, "add element inet netryx_rules house_v4 { 192.168.50.0/24, 192.168.60.0/24 }") {
		t.Fatalf("a new house network is not kept from isolated devices:\n%s", s)
	}
}

// quarantineRig is a house whose baseline (the tv and the laptop) was taken
// two days before at, so its grace is over.
func quarantineRig(t *testing.T, conf string, at time.Time) *rig {
	t.Helper()
	r := newRig(t, conf, at.Add(-48*time.Hour))
	r.seen = []Seen{{MAC: tvMAC, At: r.now}, {MAC: laptopMAC, At: r.now}}
	r.e.TakeBaseline()
	r.set(at, 48*time.Hour)
	return r
}

func TestQuarantineHoldsInTheBootRules(t *testing.T) {
	at := summer(t)
	r := quarantineRig(t, "", at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(r.state, "rules.nft"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"ether saddr != @approved jump iso_in", "add element inet netryx_rules approved { " + tvMAC + ", " + laptopMAC + " }"} {
		if !strings.Contains(string(b), w) {
			t.Fatalf("rules.nft lacks %q:\n%s", w, b)
		}
	}
}

func TestANewDeviceRaisesOneEvent(t *testing.T) {
	at := summer(t)
	r := quarantineRig(t, "", at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.seen = append(r.seen, Seen{MAC: "02:4e:58:00:00:04", Hostname: "cam", At: at})
	r.mu.Unlock()
	r.set(at.Add(15*time.Second), 48*time.Hour+15*time.Second)
	r.e.Tick(at.Add(15 * time.Second))
	r.set(at.Add(30*time.Second), 48*time.Hour+30*time.Second)
	r.e.Tick(at.Add(30 * time.Second))
	r.mu.Lock()
	ev := append([]string(nil), r.events...)
	r.mu.Unlock()
	if len(ev) != 1 || ev[0] != "new_device 02:4e:58:00:00:04" {
		t.Fatalf("events = %v; want one new_device for the camera", ev)
	}
	if p := r.e.Pending(); len(p) != 1 || p[0].MAC != "02:4e:58:00:00:04" {
		t.Fatalf("Pending = %+v", p)
	}
}

func TestAnApprovedDeviceLeavesQuarantine(t *testing.T) {
	at := summer(t)
	r := quarantineRig(t, "", at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.write(`
config device 'd_024e58000004'
	option mac '02:4e:58:00:00:04'
	option approved '1'
`)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if s := r.nft.lastApply(t); !strings.Contains(s, "add element inet netryx_rules approved { "+tvMAC+", "+laptopMAC+", 02:4e:58:00:00:04 }") {
		t.Fatalf("an approved device is not in the approved set:\n%s", s)
	}
}

func TestQuarantineStartsWhenTheGraceEnds(t *testing.T) {
	at := summer(t)
	r := newRig(t, "", at)
	r.seen = []Seen{{MAC: tvMAC, At: at}}
	r.e.TakeBaseline()
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if s := r.nft.lastApply(t); strings.Contains(s, "!= @approved") {
		t.Fatalf("the quarantine holds during its grace:\n%s", s)
	}
	later := at.Add(Grace + 10*time.Second)
	r.set(later, Grace+10*time.Second)
	r.e.Tick(later)
	if s := r.nft.lastApply(t); !strings.Contains(s, "ether saddr != @approved jump iso_in") {
		t.Fatalf("the grace is over but nothing is held:\n%s", s)
	}
}

func TestQuarantineTurnedOffHoldsNobody(t *testing.T) {
	at := summer(t)
	r := quarantineRig(t, "config quarantine 'quarantine'\n\toption enabled '0'\n", at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if s := r.nft.lastApply(t); strings.Contains(s, "!= @approved") {
		t.Fatalf("quarantine off still holds:\n%s", s)
	}
}

// serve asks the engine which alerts are on: the defaults before the config
// is read, the owner's section after.
func TestTheEngineSaysWhichAlertsAreOn(t *testing.T) {
	at := summer(t)
	r := newRig(t, "config alerts 'alerts'\n\toption upload '0'\n", at)
	if a := r.e.Alerts(); !a.NewDevice || !a.Upload {
		t.Fatalf("before the first read: %+v; want the defaults", a)
	}
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if a := r.e.Alerts(); a.Upload || !a.NewDevice || !a.BlockedCountry {
		t.Fatalf("after the read: %+v; want upload off", a)
	}
}

// The delivery loop and the upload check wait for the config: a fresh engine
// says its alert settings were not read, and after one read says they were,
// with the owner's channel.
func TestTheAlertSettingsAreInForceOnlyOnceRead(t *testing.T) {
	at := summer(t)
	r := newRig(t, "config alerts 'alerts'\n\toption channel 'console'\n", at)
	if _, read := r.e.AlertsInForce(); read {
		t.Fatal("a fresh engine says its alert settings were read")
	}
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if a, read := r.e.AlertsInForce(); !read || a.Channel != "console" {
		t.Fatalf("after the read: %+v, read %v; want channel console, read", a, read)
	}
}

// Found in the stage's live run: a MAC only the history knows (it may not be
// a house device at all) belongs in the baseline, which is about devices
// asleep on install day, never among the newcomers of each tick.
func TestTheHistoryFeedsTheBaselineAndNeverTheTicks(t *testing.T) {
	at := summer(t)
	r := newRig(t, "", at.Add(-48*time.Hour))
	r.seen = []Seen{{MAC: tvMAC, At: r.now}}
	r.e.o.Known = func() []string { return []string{tvMAC, laptopMAC} }
	r.e.TakeBaseline()
	found := false
	for _, m := range r.roster.Approved(nil) {
		found = found || m == laptopMAC
	}
	if !found {
		t.Fatal("a MAC from the history is not in the baseline")
	}
	r.e.o.Known = func() []string { return []string{"02:4e:58:00:00:77"} }
	r.set(at, 48*time.Hour)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.Tick(at)
	r.mu.Lock()
	ev := append([]string(nil), r.events...)
	r.mu.Unlock()
	if len(ev) != 0 {
		t.Fatalf("a MAC only the history holds became a newcomer: %v", ev)
	}
}

// Final review C2: after a damaged baseline is rebuilt, its new grace holds
// nobody by the quarantine lines; a device held before must still be held,
// in both tables, not let into the house for a day.
func TestAHeldDeviceStaysHeldThroughARebuiltBaseline(t *testing.T) {
	at := summer(t)
	r := newRig(t, "", at)
	if err := os.WriteFile(filepath.Join(r.state, "known-macs.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.state, "held-macs.json"), []byte(`["02:4e:58:00:00:04"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	r.seen = []Seen{{MAC: tvMAC, At: at}, {MAC: "02:4e:58:00:00:04", At: at}}
	r.e.TakeBaseline()
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	s := r.nft.lastApply(t)
	for _, w := range []string{"add element inet netryx_rules isolated { 02:4e:58:00:00:04 }", "add element bridge netryx_iso isolated { 02:4e:58:00:00:04 }"} {
		if !strings.Contains(s, w) {
			t.Fatalf("the held camera is let into the house during the new grace; want %q in\n%s", w, s)
		}
	}
	if strings.Contains(s, "!= @approved") {
		t.Fatal("the grace holds nobody else")
	}
}

// Final review I1: the console says a device is held only while the kernel
// holds it; with the quarantine off a newcomer is listed but not held.
func TestTheConsoleSaysHeldOnlyWhileTheKernelHolds(t *testing.T) {
	at := summer(t)
	for _, c := range []struct {
		conf string
		held bool
	}{{"", true}, {"config quarantine 'quarantine'\n\toption enabled '0'\n", false}} {
		r := quarantineRig(t, c.conf, at)
		if err := r.e.Reload(); err != nil {
			t.Fatal(err)
		}
		r.mu.Lock()
		r.seen = append(r.seen, Seen{MAC: "02:4e:58:00:00:04", At: at})
		r.mu.Unlock()
		r.e.Tick(at.Add(15 * time.Second))
		if got := r.e.HeldDevices()["02:4e:58:00:00:04"]; got != c.held {
			t.Fatalf("quarantine conf %q: held = %v; want %v", c.conf, got, c.held)
		}
		active, _, problem := r.e.QuarantineState()
		if active != c.held || (!c.held && problem == "") {
			t.Fatalf("conf %q: state active %v problem %q", c.conf, active, problem)
		}
	}
}

// Final review I2: an unreadable baseline must not switch the quarantine off
// silently and for good: it is tried again at each tick and told once.
func TestAnUnreadableBaselineIsTriedAgainAndTold(t *testing.T) {
	at := summer(t)
	r := newRig(t, "", at)
	path := filepath.Join(r.state, "known-macs.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	r.seen = []Seen{{MAC: tvMAC, At: at}}
	r.e.TakeBaseline()
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.e.Tick(at.Add(15 * time.Second))
	r.mu.Lock()
	ev := append([]string(nil), r.events...)
	r.mu.Unlock()
	n := 0
	for _, e := range ev {
		if strings.HasPrefix(e, "quarantine_off") {
			n++
		}
	}
	if n < 1 {
		t.Fatalf("events = %v; want quarantine_off told", ev)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r.e.Tick(at.Add(30 * time.Second))
	if !r.roster.Good() {
		t.Fatal("once readable the baseline is taken at the next tick")
	}
}

// Final review I3: a flood of made-up MACs is one alert, not one per MAC.
func TestAFloodOfNewcomersIsToldOnce(t *testing.T) {
	at := summer(t)
	r := quarantineRig(t, "", at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	for i := 0; i < 5000; i++ {
		r.seen = append(r.seen, Seen{MAC: fmt.Sprintf("02:00:00:00:%02x:%02x", i/256, i%256), At: at})
	}
	r.mu.Unlock()
	r.e.Tick(at.Add(15 * time.Second))
	r.mu.Lock()
	ev := append([]string(nil), r.events...)
	r.mu.Unlock()
	perMAC, summary := 0, 0
	for _, e := range ev {
		switch {
		case strings.HasPrefix(e, "new_device "):
			perMAC++
		case strings.HasPrefix(e, "new_devices"):
			summary++
		}
	}
	if perMAC != 0 || summary != 1 {
		t.Fatalf("events: %d per MAC, %d summaries; want 0 and 1 (%v)", perMAC, summary, ev[:min(len(ev), 5)])
	}
}

// A damaged baseline that cannot be written again leaves the quarantine off:
// that is told, not only the rebuild (automated security review of 2984e5a).
func TestADamagedBaselineThatCannotBeWrittenIsToldAsOff(t *testing.T) {
	at := summer(t)
	r := newRig(t, "", at)
	if err := os.WriteFile(filepath.Join(r.state, "known-macs.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(r.state, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(r.state, 0o700)
	r.seen = []Seen{{MAC: tvMAC, At: at}}
	r.e.TakeBaseline()
	r.mu.Lock()
	ev := strings.Join(r.events, ",")
	r.mu.Unlock()
	if r.roster.Good() || !strings.Contains(ev, "quarantine_off") {
		t.Fatalf("good %v, events %q; want the quarantine told off", r.roster.Good(), ev)
	}
}

// Final review I6 (spec 6.8 lists labels and devices): a device the owner
// named in UCI is in the baseline even when it was away all week.
func TestADeviceNamedInTheConfigIsInTheBaseline(t *testing.T) {
	at := summer(t)
	r := newRig(t, "config device 'd_laptop'\n\toption mac '02:4E:58:00:00:02'\n\toption name 'Laptop'\n", at)
	r.seen = []Seen{{MAC: tvMAC, At: at}}
	r.e.TakeBaseline()
	found := false
	for _, m := range r.roster.Approved(nil) {
		found = found || m == laptopMAC
	}
	if !found {
		t.Fatal("a device named in the config is not in the baseline")
	}
}

// The daily file keeps every rule's counter at a day's close, by rule id,
// the implicit rules among them; a table that cannot be read gives none.
func TestTheHitsAreEveryRulesCounterByID(t *testing.T) {
	r := newRig(t, tgForPhone, winter(t))
	r.nft.present = true
	for n, v := range map[string]uint64{"hits_tg": 50, "hits_iso": 3, "other_counter": 9} {
		r.nft.objects["counter "+n] = true
		r.nft.hits[n] = v
	}
	got := r.e.Hits()
	if len(got) != 2 || got["tg"] != 50 || got["iso"] != 3 {
		t.Fatalf("hits %v, want tg 50 and iso 3 alone", got)
	}
	r.nft.present = false
	if got := r.e.Hits(); got != nil {
		t.Fatalf("no table, hits %v", got)
	}
}

// interventions takes what the engine told since the last call.
func (r *rig) interventions() []Intervention {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.told
	r.told = nil
	return out
}

func toldOne(t *testing.T, got []Intervention, want Intervention) {
	t.Helper()
	for _, g := range got {
		u := g.Until
		g.Until = want.Until
		if g == want && u.Equal(want.Until) {
			return
		}
	}
	t.Errorf("not told %+v; told %+v", want, got)
}

// The engine tells the journal each intervention it applies: an allowance
// in force, by the owner or the agent; a limit spent, until midnight; a
// connection it cut, with the rule and who wrote it.
func TestTheEngineTellsTheAllowancesLimitsAndBlocksItApplies(t *testing.T) {
	at := summer(t)
	until := at.Add(30 * time.Minute).Truncate(time.Second)
	conf := ytLimitLaptop + fmt.Sprintf(`
config allow 'agent_a1'
	option rule 'ytlim'
	option member '%s'
	option until '%d'
config allow 'old'
	option rule 'ytlim'
	option member '%s'
	option until '%d'
`, phoneMAC, until.Unix(), phoneMAC, at.Add(-time.Minute).Unix())
	r := newRig(t, conf, at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	first := r.interventions()
	toldOne(t, first, Intervention{Kind: "allow", ID: "agent_a1", MAC: phoneMAC, Rule: "ytlim", Name: "youtube", By: "agent", Until: until})
	for _, iv := range first {
		if iv.ID == "old" {
			t.Errorf("an allowance that has ended was told: %+v", iv)
		}
	}
	r.mu.Lock()
	r.live = []flows.Flow{flow(1, laptopMAC, "192.168.50.20:50000", "142.250.1.1:443", "youtube", "US")}
	r.mu.Unlock()
	r.useMinute(laptopMAC, "youtube")
	r.set(at.Add(time.Minute), time.Minute)
	r.e.Tick(at.Add(time.Minute))
	r.settle()
	got := r.interventions()
	midnight := NextMidnight(london(t), at.Add(time.Minute))
	toldOne(t, got, Intervention{Kind: "limit_spent", MAC: laptopMAC, Rule: "ytlim", Name: "youtube", By: "router", Until: midnight})
	toldOne(t, got, Intervention{Kind: "blocked", MAC: laptopMAC, Rule: "ytlim", Name: "youtube", By: "owner"})
}

// Letting a device out of quarantine and isolating it are the owner's (the
// agent cannot write them); the engine tells each change of the config
// after the first apply, which only learns how things stand.
func TestTheEngineTellsApprovalsAndIsolationAfterItsFirstApply(t *testing.T) {
	at := summer(t)
	r := newRig(t, `
config device 'd_024e58000004'
	option mac '02:4e:58:00:00:04'
	option isolate '1'
`, at)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, iv := range r.interventions() {
		if iv.Kind == "isolated" {
			t.Errorf("the first apply told how things already stood: %+v", iv)
		}
	}
	r.write(`
config device 'd_024e58000004'
	option mac '02:4e:58:00:00:04'
	option approved '1'
config device 'd_024e58000005'
	option mac '02:4e:58:00:00:05'
	option isolate '1'
`)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	got := r.interventions()
	toldOne(t, got, Intervention{Kind: "approved", MAC: "02:4e:58:00:00:04", By: "owner"})
	toldOne(t, got, Intervention{Kind: "released", MAC: "02:4e:58:00:00:04", By: "owner"})
	toldOne(t, got, Intervention{Kind: "isolated", MAC: "02:4e:58:00:00:05", By: "owner"})
}
