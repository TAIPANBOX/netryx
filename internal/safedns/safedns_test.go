package safedns

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

type call struct {
	stdin string
	argv  string
}

type rig struct {
	mu         sync.Mutex
	calls      []call
	changes    string // what `uci -q changes dhcp` prints
	resolves   int
	resolveErr error
	probeErr   error
	now        time.Time
	m          *Manager
}

var safeAddrs = map[string][]netip.Addr{
	"forcesafesearch.google.com":   {netip.MustParseAddr("216.239.38.120"), netip.MustParseAddr("2001:4860:4802:32::78")},
	"strict.bing.com":              {netip.MustParseAddr("150.171.27.16")},
	"restrictmoderate.youtube.com": {netip.MustParseAddr("216.239.38.119")},
	"restrict.youtube.com":         {netip.MustParseAddr("216.239.38.120")},
}

func newRig(t *testing.T) *rig {
	r := &rig{now: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}
	r.m = NewManager(Options{
		Run: func(ctx context.Context, env []string, stdin, name string, args ...string) ([]byte, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			argv := strings.Join(append([]string{name}, args...), " ")
			r.calls = append(r.calls, call{stdin: stdin, argv: argv})
			if argv == "uci -q changes dhcp" {
				return []byte(r.changes), nil
			}
			return nil, nil
		},
		Resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.resolves++
			if r.resolveErr != nil {
				return nil, r.resolveErr
			}
			return safeAddrs[host], nil
		},
		Probe: func(ctx context.Context) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.probeErr
		},
		Ifaces: func() []string { return []string{"br-lan"} },
		Now:    func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now },
		Logf:   t.Logf,
		Sleep:  func(time.Duration) {},
	})
	return r
}

func (r *rig) take() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.calls
	r.calls = nil
	return c
}

func TestSafeDNSWritesOnlyItsOwnSectionAndRestartsOnlyItsInstance(t *testing.T) {
	r := newRig(t)
	ok, problem := r.m.Ensure(true, "moderate")
	if !ok || problem != "" {
		t.Fatalf("Ensure = %v, %q; want healthy", ok, problem)
	}
	calls := r.take()
	var batch string
	restarts := 0
	for _, c := range calls {
		switch {
		case c.argv == "uci -q batch":
			batch = c.stdin
		case c.argv == "/etc/init.d/dnsmasq restart netryx_safe":
			restarts++
		case strings.HasPrefix(c.argv, "/etc/init.d/dnsmasq"):
			t.Fatalf("touched another dnsmasq instance: %q", c.argv)
		}
	}
	if restarts != 1 {
		t.Fatalf("restarted netryx_safe %d times; want 1 (calls %v)", restarts, calls)
	}
	for _, l := range strings.Split(strings.TrimSpace(batch), "\n") {
		if !strings.Contains(l, "dhcp.netryx_safe") {
			t.Fatalf("the batch touches something else than dhcp.netryx_safe: %q\n%s", l, batch)
		}
	}
	// Never a commit: the section lives as uncommitted changes, which the
	// dnsmasq init reads, so nothing is written to flash and nobody else's
	// pending dhcp change is committed with it (docs/stage2b-measured.md, 5).
	if strings.Contains(batch, "commit") {
		t.Fatalf("the batch commits:\n%s", batch)
	}
	if !strings.HasPrefix(batch, "revert dhcp.netryx_safe\ndelete dhcp.netryx_safe\n") {
		t.Fatalf("the batch must first drop its own earlier changes and any committed copy:\n%s", batch)
	}
	for _, want := range []string{
		"delete dhcp.netryx_safe",
		"set dhcp.netryx_safe=dnsmasq",
		"set dhcp.netryx_safe.port='553'",
		"add_list dhcp.netryx_safe.interface='br-lan'",
		"set dhcp.netryx_safe.extraconftext='no-dhcp-interface=br-lan'",
		"add_list dhcp.netryx_safe.address='/use-application-dns.net/'",
		// HTTPS and SVCB records carry address hints a browser would use
		// instead of the overridden A and AAAA answers.
		"add_list dhcp.netryx_safe.filter_rr='HTTPS'",
		"add_list dhcp.netryx_safe.filter_rr='SVCB'",
		// The stock init points the router's own resolv.conf at an instance
		// with localuse on when it stops one with noresolv; the router's
		// lookups must stay with the main resolver.
		"set dhcp.netryx_safe.localuse='0'",
		// Its upstream is the main resolver, whose answers for the house's
		// own names are private addresses: rebind protection here would drop
		// them (the main resolver already guards what comes from outside).
		"set dhcp.netryx_safe.rebind_protection='0'",
	} {
		if !strings.Contains(batch, want+"\n") {
			t.Fatalf("missing %q in the batch:\n%s", want, batch)
		}
	}

	answers := addressLines(batch)
	for name, want := range map[string]string{
		"www.google.com":  "216.239.38.120",
		"www.bing.com":    "150.171.27.16",
		"www.youtube.com": "216.239.38.119",
	} {
		if !contains(answers[name], want) {
			t.Fatalf("%s is not answered %s: %v\n%s", name, want, answers[name], batch)
		}
	}
	if !contains(answers["www.google.com"], "2001:4860:4802:32::78") {
		t.Fatalf("www.google.com has no safe AAAA:\n%s", batch)
	}

	// Nothing changed: no write, no restart.
	r.m.Ensure(true, "moderate")
	for _, c := range r.take() {
		if c.argv == "uci -q batch" || strings.HasPrefix(c.argv, "/etc/init.d/") {
			t.Fatalf("an unchanged Ensure ran %q", c.argv)
		}
	}

	// YouTube off: no YouTube lines.
	r.m.Ensure(true, "off")
	for _, c := range r.take() {
		if c.argv == "uci -q batch" && strings.Contains(c.stdin, "youtube") {
			t.Fatalf("YouTube off but its lines are written:\n%s", c.stdin)
		}
	}

	// Not wanted: the instance stops and the section goes.
	ok, _ = r.m.Ensure(false, "off")
	if ok {
		t.Fatal("Ensure(false) says healthy")
	}
	var stopped, deleted bool
	for _, c := range r.take() {
		stopped = stopped || c.argv == "/etc/init.d/dnsmasq stop netryx_safe"
		deleted = deleted || (c.argv == "uci -q batch" && c.stdin == "revert dhcp.netryx_safe\ndelete dhcp.netryx_safe\n")
	}
	if !stopped || !deleted {
		t.Fatalf("Ensure(false): stopped %v, deleted %v", stopped, deleted)
	}
}

// adblock-fast keeps its dhcp override uncommitted (measured); a resolver that
// waited for no pending changes would never start beside it, and one that
// committed would make adblock-fast's runtime change permanent.
func TestPendingDHCPChangesAreNotCommittedForSomeoneElse(t *testing.T) {
	r := newRig(t)
	r.changes = "dhcp.cfg01411c.serversfile='/var/run/adblock-fast/dnsmasq.servers'\n"
	ok, problem := r.m.Ensure(true, "strict")
	if !ok || problem != "" {
		t.Fatalf("Ensure beside someone else's pending dhcp changes = %v, %q; want it to start", ok, problem)
	}
	for _, c := range r.take() {
		if c.argv == "uci -q batch" && strings.Contains(c.stdin, "commit") {
			t.Fatalf("committed dhcp while someone else's changes were pending:\n%s", c.stdin)
		}
	}
}

func TestAnUnhealthyResolverSaysWhy(t *testing.T) {
	r := newRig(t)
	r.probeErr = errors.New("connection refused")
	ok, problem := r.m.Ensure(true, "moderate")
	if ok || !strings.Contains(problem, "connection refused") {
		t.Fatalf("Ensure with a failing probe = %v, %q", ok, problem)
	}
}

func TestSafeHostsAreRefreshedDailyAndKeptOnFailure(t *testing.T) {
	r := newRig(t)
	r.m.Ensure(true, "moderate")
	first := r.resolves
	if first == 0 {
		t.Fatal("no safe host was resolved")
	}
	r.now = r.now.Add(2 * time.Hour)
	r.m.Ensure(true, "moderate")
	if r.resolves != first {
		t.Fatalf("resolved again after 2 h (%d -> %d); want daily", first, r.resolves)
	}
	r.take()
	r.now = r.now.Add(23 * time.Hour)
	r.resolveErr = errors.New("no answer")
	ok, _ := r.m.Ensure(true, "moderate")
	if r.resolves == first {
		t.Fatal("not resolved again after a day")
	}
	if !ok {
		t.Fatal("a failed refresh made the resolver unhealthy; the last addresses must stay")
	}
	for _, c := range r.take() {
		if c.argv == "uci -q batch" {
			t.Fatalf("a failed refresh rewrote the section:\n%s", c.stdin)
		}
	}
	tried := r.resolves
	r.now = r.now.Add(30 * time.Minute)
	r.m.Ensure(true, "moderate")
	if r.resolves != tried {
		t.Fatal("retried a failed refresh within the hour")
	}
}

func TestWithoutSafeAddressesTheResolverIsNotHealthy(t *testing.T) {
	r := newRig(t)
	r.resolveErr = errors.New("no answer")
	ok, problem := r.m.Ensure(true, "moderate")
	if ok || !strings.Contains(problem, "address") {
		t.Fatalf("Ensure without any safe address = %v, %q; a resolver that answers unfiltered must not count as healthy", ok, problem)
	}
}

// An instance that died on its own (killed, crashed) is started again: the
// section did not change, so without this the group would stay on the main
// resolver until something else changed.
func TestADeadInstanceIsRestarted(t *testing.T) {
	r := newRig(t)
	if ok, _ := r.m.Ensure(true, "moderate"); !ok {
		t.Fatal("not healthy at first")
	}
	r.take()
	r.mu.Lock()
	r.probeErr = errors.New("connection refused")
	r.mu.Unlock()
	restarted := false
	r.m.o.Run = func(ctx context.Context, env []string, stdin, name string, args ...string) ([]byte, error) {
		argv := strings.Join(append([]string{name}, args...), " ")
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, call{stdin: stdin, argv: argv})
		if argv == "/etc/init.d/dnsmasq restart netryx_safe" {
			restarted = true
			r.probeErr = nil
		}
		return nil, nil
	}
	ok, problem := r.m.Ensure(true, "moderate")
	if !restarted || !ok || problem != "" {
		t.Fatalf("a dead instance: restarted %v, Ensure = %v, %q", restarted, ok, problem)
	}
}

// At the first look nothing says whether the section exists; stopping an
// instance that was never there only fills the log, so it is asked first.
func TestNothingIsStoppedThatWasNeverThere(t *testing.T) {
	r := newRig(t)
	r.m.Ensure(false, "off")
	for _, c := range r.take() {
		if strings.HasPrefix(c.argv, "/etc/init.d/") || c.argv == "uci -q batch" {
			t.Fatalf("with no section, Ensure(false) ran %q", c.argv)
		}
	}
}

// dnsmasq starts inside ujail and takes a moment to bind its port (seen in the
// live run): a probe right after a restart fails, and the group would be taken
// off the filter for a minute. The probe is asked again for a few seconds.
func TestAStartingInstanceIsGivenAMoment(t *testing.T) {
	r := newRig(t)
	fails := 2
	r.m.o.Probe = func(ctx context.Context) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if fails > 0 {
			fails--
			return errors.New("connection refused")
		}
		return nil
	}
	var slept time.Duration
	r.m.o.Sleep = func(d time.Duration) { slept += d }
	ok, problem := r.m.Ensure(true, "moderate")
	if !ok || problem != "" {
		t.Fatalf("an instance that answers on the third probe: Ensure = %v, %q", ok, problem)
	}
	if slept == 0 || slept > 5*time.Second {
		t.Fatalf("waited %v between probes; want a few seconds at most", slept)
	}
}

// addressLines reads the batch's address lines: name -> addresses it is given.
// A line names several domains and one address ("/a/b/c/ip"); the canary's
// line has no address.
func addressLines(batch string) map[string][]string {
	out := map[string][]string{}
	for _, l := range strings.Split(batch, "\n") {
		const pre = "add_list dhcp.netryx_safe.address='/"
		if !strings.HasPrefix(l, pre) || !strings.HasSuffix(l, "'") {
			continue
		}
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(l, pre), "'"), "/")
		addr := parts[len(parts)-1]
		for _, n := range parts[:len(parts)-1] {
			out[n] = append(out[n], addr)
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func batchOf(calls []call) string {
	for _, c := range calls {
		if c.argv == "uci -q batch" {
			return c.stdin
		}
	}
	return ""
}

// Google's search answers on every country domain it runs (its own list,
// google.com/supported_domains); one left out is a way around safe search.
// dnsmasq takes many domains on one address line, so they cost few lines.
func TestEveryGoogleSearchDomainIsSafe(t *testing.T) {
	r := newRig(t)
	if ok, problem := r.m.Ensure(true, "off"); !ok {
		t.Fatal(problem)
	}
	batch := batchOf(r.take())
	answers := addressLines(batch)
	for _, n := range []string{"www.google.com", "www.google.nl", "www.google.ca", "www.google.co.jp", "www.google.com.br", "www.google.ad"} {
		if !contains(answers[n], "216.239.38.120") || !contains(answers[n], "2001:4860:4802:32::78") {
			t.Fatalf("%s is not sent to the safe address: %v", n, answers[n])
		}
	}
	if len(googleDomains) < 180 {
		t.Fatalf("only %d Google domains", len(googleDomains))
	}
	lines := 0
	for _, l := range strings.Split(batch, "\n") {
		if strings.Contains(l, ".address=") {
			lines++
			if len(l) > maxLine+64 {
				t.Fatalf("an address line is %d bytes:\n%s", len(l), l)
			}
		}
	}
	if lines > 40 {
		t.Fatalf("%d address lines; the domains should share lines", lines)
	}
}

// Apple's documented way for a network to turn iCloud Private Relay off is to
// answer its two names with no address; otherwise Safari walks around the
// filter.
func TestPrivateRelayIsTurnedOff(t *testing.T) {
	r := newRig(t)
	r.m.Ensure(true, "off")
	batch := batchOf(r.take())
	if !strings.Contains(batch, "add_list dhcp.netryx_safe.address='/mask.icloud.com/mask-h2.icloud.com/'\n") {
		t.Fatalf("Private Relay's names are not answered NXDOMAIN:\n%s", batch)
	}
}

// The section lives in uncommitted changes, so anyone's `uci revert dhcp`
// takes it away; a restart alone then finds nothing. The look that finds the
// probe dead writes the section again before it starts the instance.
func TestASectionRemovedByOthersIsWrittenAgain(t *testing.T) {
	r := newRig(t)
	if ok, problem := r.m.Ensure(true, "off"); !ok {
		t.Fatal(problem)
	}
	r.take()
	r.probeErr = errors.New("no answer")
	r.m.Ensure(true, "off")
	calls := r.take()
	wrote, restartAfter := -1, false
	for i, c := range calls {
		if c.argv == "uci -q batch" && strings.HasPrefix(c.stdin, "revert dhcp.netryx_safe\ndelete dhcp.netryx_safe\nset ") {
			wrote = i
		}
		if c.argv == "/etc/init.d/dnsmasq restart netryx_safe" && wrote >= 0 && i > wrote {
			restartAfter = true
		}
	}
	if wrote < 0 || !restartAfter {
		t.Fatalf("a dead probe must write the section, then restart: %v", calls)
	}
}

// An instance that never answered is still there: turning the filters off
// stops it and takes the section away.
func TestTurningOffStopsAnInstanceThatNeverAnswered(t *testing.T) {
	r := newRig(t)
	r.probeErr = errors.New("no answer")
	if ok, _ := r.m.Ensure(true, "off"); ok {
		t.Fatal("healthy with a dead probe")
	}
	r.take()
	r.m.Ensure(false, "off")
	var stopped, deleted bool
	for _, c := range r.take() {
		stopped = stopped || c.argv == "/etc/init.d/dnsmasq stop netryx_safe"
		deleted = deleted || (c.argv == "uci -q batch" && strings.Contains(c.stdin, "delete dhcp.netryx_safe"))
	}
	if !stopped || !deleted {
		t.Fatalf("stopped %v, deleted %v", stopped, deleted)
	}
}

// A WAN without DNS must not hold a look for a minute: each safe host is
// asked with its own short deadline.
func TestEachSafeHostLookupHasAShortDeadline(t *testing.T) {
	r := newRig(t)
	var worst time.Duration
	r.m.o.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			worst = time.Hour
		} else if d := time.Until(dl); d > worst {
			worst = d
		}
		return safeAddrs[host], nil
	}
	r.m.Ensure(true, "moderate")
	if worst > 5*time.Second {
		t.Fatalf("a safe host lookup may take %v", worst)
	}
}
