// Package safedns keeps the second dnsmasq instance that answers the members
// of filtered groups (spec 6.5): SafeSearch for Google and Bing, YouTube's
// restricted mode, and the Firefox DoH canary answered NXDOMAIN.
//
// It writes only its own section, dhcp.netryx_safe, and starts, restarts and
// stops only that instance: the house's main resolver is never touched
// (docs/stage2b-measured.md, section 1). The instance would inherit every
// dhcp pool of the house, so the section always says no-dhcp-interface for
// each device it listens on.
//
// The section is never committed. It lives as uncommitted changes, which the
// dnsmasq init reads like the committed config (adblock-fast keeps its own
// override the same way): nothing reaches flash, nobody else's pending dhcp
// change is committed with it, and a reboot forgets it until netryx writes it
// again (docs/stage2b-measured.md, section 5).
package safedns

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Section is the instance's name, Port the port it answers on: below 1024,
// so only root can bind it and a local process cannot take its place.
const (
	Section = "netryx_safe"
	Port    = 553
)

// refreshEvery is how old the safe hosts' addresses may grow; retryEvery how
// soon a failed refresh is tried again.
const (
	refreshEvery = 24 * time.Hour
	retryEvery   = time.Hour
	// resolveTimeout bounds one safe host's lookup.
	resolveTimeout = 3 * time.Second
)

// Runner runs a command with stdin; the same shape as the rules engine's.
type Runner func(ctx context.Context, env []string, stdin, name string, args ...string) ([]byte, error)

// Options are what the manager needs from the router.
type Options struct {
	Run Runner
	// Resolve asks the main resolver; Probe asks the instance itself.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	Probe   func(ctx context.Context) error
	// Ifaces is the house-side devices the instance listens on.
	Ifaces func() []string
	Now    func() time.Time
	Logf   func(string, ...any)
	// Sleep waits between probes of an instance that is starting (nil:
	// time.Sleep).
	Sleep func(time.Duration)
}

// probeTries and probeGap: dnsmasq starts inside ujail and takes a moment to
// bind its port, so a probe after a start is asked up to five times, a second
// apart.
const (
	probeTries = 5
	probeGap   = time.Second
)

// Manager owns dhcp.netryx_safe.
type Manager struct {
	o Options

	mu      sync.Mutex
	addrs   map[string][]netip.Addr // safe host -> addresses
	got     time.Time               // when addrs were last resolved
	tried   time.Time               // when a refresh was last tried
	written string                  // the batch last written; "" = section absent
	known   bool                    // written reflects the router (after the first Ensure)
}

// NewManager returns a manager; nothing is written until Ensure.
func NewManager(o Options) *Manager {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	return &Manager{o: o, addrs: map[string][]netip.Addr{}}
}

// fronts maps each front name to the safe host whose addresses it is given:
// every Google search domain (googleDomains), Bing, and YouTube's names, which
// depend on the level. The names given one address share lines.

var youtubeFronts = []string{
	"www.youtube.com", "m.youtube.com", "youtubei.googleapis.com",
	"youtube.googleapis.com", "www.youtube-nocookie.com",
}

const canary = "use-application-dns.net"

// privateRelay is the pair of names Apple documents for a network to turn
// iCloud Private Relay off: answered with no address, Safari stops relaying
// around the filter.
var privateRelay = []string{"mask.icloud.com", "mask-h2.icloud.com"}

func fronts(youtube string) map[string]string {
	f := map[string]string{"www.bing.com": "strict.bing.com"}
	for _, d := range googleDomains {
		f["www."+d] = "forcesafesearch.google.com"
	}
	host := ""
	switch youtube {
	case "moderate":
		host = "restrictmoderate.youtube.com"
	case "strict":
		host = "restrict.youtube.com"
	}
	if host != "" {
		for _, n := range youtubeFronts {
			f[n] = host
		}
	}
	return f
}

// Ensure makes the router match want: with want, the section is written (only
// when it changed) and the instance restarted, then probed; without, the
// instance is stopped and the section removed. It answers whether the
// instance answers and, if not, why.
func (m *Manager) Ensure(want bool, youtube string) (healthy bool, problem string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if !want {
		if m.known && m.written == "" {
			return false, ""
		}
		if !m.known {
			// At the first look, ask whether the section is there at all.
			out, err := m.o.Run(ctx, nil, "", "uci", "-q", "get", "dhcp."+Section)
			if err != nil || strings.TrimSpace(string(out)) == "" {
				m.written, m.known = "", true
				return false, ""
			}
		}
		if _, err := m.o.Run(ctx, nil, "", "/etc/init.d/dnsmasq", "stop", Section); err != nil {
			m.o.Logf("DNS filter not stopped: %v", err)
		}
		// Our own changes go; a copy someone committed is hidden by an
		// uncommitted delete.
		if _, err := m.o.Run(ctx, nil, "revert dhcp."+Section+"\ndelete dhcp."+Section+"\n", "uci", "-q", "batch"); err != nil {
			return false, "DNS filter section not removed: " + err.Error()
		}
		m.written, m.known = "", true
		return false, ""
	}

	m.refresh(ctx, youtube)
	for _, host := range fronts(youtube) {
		if len(m.addrs[host]) == 0 {
			// An instance without these lines would answer unfiltered.
			return false, "safe search addresses not received (" + host + ")"
		}
	}
	batch, err := m.batch(youtube)
	if err != nil {
		return false, err.Error()
	}
	if batch == m.written {
		if m.o.Probe == nil || m.o.Probe(ctx) == nil {
			return true, ""
		}
		// The instance died, or its section was taken away (it lives in
		// uncommitted changes, so anyone's `uci revert dhcp` removes it and a
		// restart alone would find nothing): write it again and start it, at
		// most once a look.
	}
	if _, err := m.o.Run(ctx, nil, batch, "uci", "-q", "batch"); err != nil {
		return false, "DNS filter section not written: " + err.Error()
	}
	// The section is there now, whether or not the instance answers.
	m.written, m.known = batch, true
	if _, err := m.o.Run(ctx, nil, "", "/etc/init.d/dnsmasq", "restart", Section); err != nil {
		return false, "DNS filter did not start: " + err.Error()
	}
	if err := m.probeStarting(ctx); err != nil {
		return false, "DNS filter does not answer: " + err.Error()
	}
	return true, ""
}

// probeStarting asks an instance that was just started, giving it a few
// seconds to bind its port.
func (m *Manager) probeStarting(ctx context.Context) error {
	if m.o.Probe == nil {
		return nil
	}
	var err error
	for i := 0; i < probeTries; i++ {
		if err = m.o.Probe(ctx); err == nil {
			return nil
		}
		if i < probeTries-1 {
			m.o.Sleep(probeGap)
		}
	}
	return err
}

// refresh resolves the safe hosts when their addresses are a day old, at most
// once an hour; on a failure the last addresses stay.
func (m *Manager) refresh(ctx context.Context, youtube string) {
	now := m.o.Now()
	need := map[string]bool{}
	for _, host := range fronts(youtube) {
		if len(m.addrs[host]) == 0 {
			need[host] = true
		}
	}
	stale := m.got.IsZero() || now.Sub(m.got) >= refreshEvery
	if !stale && len(need) == 0 {
		return
	}
	if !m.tried.IsZero() && now.Sub(m.tried) < retryEvery && len(need) == 0 {
		return
	}
	m.tried = now
	hosts := map[string]bool{}
	for _, host := range fronts(youtube) {
		hosts[host] = true
	}
	ok := true
	for host := range hosts {
		// Each host has its own short deadline: a WAN without DNS must not
		// hold the look for a minute.
		rctx, cancel := context.WithTimeout(ctx, resolveTimeout)
		addrs, err := m.o.Resolve(rctx, host)
		cancel()
		if err != nil || len(addrs) == 0 {
			ok = false
			m.o.Logf("addresses of %s not refreshed (%v): the previous ones stay", host, err)
			continue
		}
		m.addrs[host] = addrs
	}
	if ok {
		m.got = now
	}
}

var ifaceName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)

// batch is the whole section, written by delete-then-set so it ends up
// exactly as given. Values are names and addresses this package checks, so
// single quotes cannot appear in them.
func (m *Manager) batch(youtube string) (string, error) {
	var ifaces []string
	if m.o.Ifaces != nil {
		for _, n := range m.o.Ifaces() {
			if ifaceName.MatchString(n) {
				ifaces = append(ifaces, n)
			}
		}
	}
	if len(ifaces) == 0 {
		return "", fmt.Errorf("no house interface for the DNS filter")
	}
	sort.Strings(ifaces)
	byHost := map[string][]string{}
	for n, host := range fronts(youtube) {
		byHost[host] = append(byHost[host], n)
	}
	hosts := make([]string, 0, len(byHost))
	for host := range byHost {
		hosts = append(hosts, host)
		sort.Strings(byHost[host])
	}
	sort.Strings(hosts)

	s := "dhcp." + Section
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	// Drop our earlier changes, hide any committed copy, then the section.
	w("revert %s", s)
	w("delete %s", s)
	w("set %s=dnsmasq", s)
	w("set %s.port='%d'", s, Port)
	w("set %s.noresolv='1'", s)
	w("add_list %s.server='127.0.0.1#53'", s)
	for _, n := range ifaces {
		w("add_list %s.interface='%s'", s, n)
	}
	w("set %s.nonwildcard='1'", s)
	w("set %s.localservice='0'", s)
	// Never the router's own resolver: the stock init would point
	// resolv.conf at this instance when it is stopped.
	w("set %s.localuse='0'", s)
	// The upstream is the main resolver, which answers the house's names with
	// private addresses and already guards what comes from outside.
	w("set %s.rebind_protection='0'", s)
	w("set %s.leasefile='/tmp/%s.leases'", s, Section)
	var nodhcp []string
	for _, n := range ifaces {
		nodhcp = append(nodhcp, "no-dhcp-interface="+n)
	}
	// One line per device: the instance must never serve the house's DHCP.
	w("set %s.extraconftext='%s'", s, strings.Join(nodhcp, `\n`))
	for _, host := range hosts {
		for _, a := range m.addrs[host] {
			for _, names := range lines(byHost[host], a.String()) {
				w("add_list %s.address='/%s/%s'", s, strings.Join(names, "/"), a)
			}
		}
	}
	w("add_list %s.address='/%s/'", s, canary)
	w("add_list %s.address='/%s/'", s, strings.Join(privateRelay, "/"))
	// address= overrides only A and AAAA; an HTTPS or SVCB answer from
	// upstream carries ipv4hint and ipv6hint that browsers connect to, which
	// would walk around the safe hosts. The instance serves only filtered
	// devices, so it answers neither type.
	w("add_list %s.filter_rr='HTTPS'", s)
	w("add_list %s.filter_rr='SVCB'", s)
	return b.String(), nil
}

// lines splits names into groups whose address line, "/a/b/.../addr", stays
// within maxLine bytes.
func lines(names []string, addr string) [][]string {
	var out [][]string
	var cur []string
	size := len(addr) + 2
	for _, n := range names {
		if len(cur) > 0 && size+len(n)+1 > maxLine {
			out = append(out, cur)
			cur, size = nil, len(addr)+2
		}
		cur = append(cur, n)
		size += len(n) + 1
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}
