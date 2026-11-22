package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/netip"
	"netryx/internal/alerts"
	"netryx/internal/safedns"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"netryx/internal/api"
	"netryx/internal/auth"
	"netryx/internal/collect"
	"netryx/internal/ct"
	"netryx/internal/device"
	"netryx/internal/enrich"
	"netryx/internal/export"
	"netryx/internal/flows"
	"netryx/internal/icons"
	"netryx/internal/inventory"
	"netryx/internal/neigh"
	"netryx/internal/netview"
	"netryx/internal/rollup"
	"netryx/internal/rules"
	"netryx/internal/wan"
	"netryx/internal/web"
)

// runner runs an external command (ubus, ping) and returns its standard
// output. It is a function so tests answer for the router.
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// maxOutput bounds what one command may hand back: ubus and ping answer in
// kilobytes, and a broken or hostile one must not fill the router's memory.
const maxOutput = 1 << 20

func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// Stdout is a writer, not a pipe we read ourselves, so os/exec runs the
	// copy and WaitDelay can close the pipe 2 s after the deadline even when
	// a grandchild still holds it open.
	out := &capWriter{max: maxOutput}
	cmd.Stdout = out
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	return out.b.Bytes(), err
}

// nftRun runs nft (or any command) with extra environment and standard input
// for the rules engine. An error is what the command said on stderr, so the
// console shows nft's own message.
// mainResolve asks the house's main resolver on the router, for the
// safe-search hosts' addresses.
func mainResolve(ctx context.Context, host string) ([]netip.Addr, error) {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, "127.0.0.1:53")
	}}
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	for i := range addrs {
		addrs[i] = addrs[i].Unmap()
	}
	return addrs, err
}

// probeSafe asks the safe-search resolver for the DoH canary, which it
// answers itself with NXDOMAIN: that answer is the sign it is up.
func probeSafe(ctx context.Context) error {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", strconv.Itoa(safedns.Port)))
	}}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := r.LookupHost(ctx, "use-application-dns.net")
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil
	}
	if err == nil {
		return errors.New("answered the DoH check with an address instead of saying the name does not exist")
	}
	return err
}

func nftRun(ctx context.Context, env []string, stdin, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	// A listing of the rules table runs to a few megabytes at most.
	out := &capWriter{max: 8 * maxOutput}
	msg := &capWriter{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, msg
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if m := strings.TrimSpace(msg.b.String()); m != "" {
			return out.b.Bytes(), errors.New(m)
		}
		return out.b.Bytes(), err
	}
	return out.b.Bytes(), nil
}

// capWriter keeps the first max bytes and drops the rest while reporting it
// written, so the command is never stuck on a full pipe.
type capWriter struct {
	b   bytes.Buffer
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - w.b.Len(); room > 0 {
		w.b.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// Options are what the console part of serve is built from. Every external
// source is a field, so buildConsole runs off a router.
type Options struct {
	// Data files. A missing or unreadable one degrades the console (no
	// country, no app, no vendor), never stops it.
	Geo, Apps, OUI string
	// State is the directory history and labels.json live in.
	State string
	// WAN is the ubus name of the internet interface; its IPv6 twin is
	// WAN+"6" (or WAN+"_6", the name netifd gives a dynamic one).
	WAN        string
	Leases     string
	PingTarget string   // probed with ping every WAN tick; "" probes nothing
	Listen     string   // the address the HTTP server listens on
	LAN        []string // the observed bridges, for the wired links
	Sync       time.Duration

	Run       runner                                    // nil runs the real command
	FS        fs.FS                                     // the root filesystem, for /sys and /proc
	Addrs     func() (map[string][]netip.Prefix, error) // nil asks the kernel
	Now       func() time.Time                          // nil is time.Now
	Collector *collect.Collector                        // nil makes a new one
	Neigh     func() ([]neigh.Entry, error)             // nil reads the kernel's neighbour table
	// Meta is tools/appmap's apps-meta.json: which apps have a favicon to
	// fetch, from which domain. Missing means nothing is fetched.
	Meta string
	// FetchIcons lets the router fetch, once, the favicons of the brands
	// that have no freely licensed logo (fetch_icons in the config).
	FetchIcons bool
	// IconClient fetches the favicons; nil is icons.NewClient, which refuses
	// private addresses. Only tests set it.
	IconClient *http.Client
	// RulesConfig is /etc/config/netryx, whose rule sections the rules
	// engine applies; "" runs no engine. Loc is the zone schedules are
	// written in (nil: UTC), and NFT runs nft (nil: the real one).
	RulesConfig string
	Loc         *time.Location
	NFT         rules.Runner
	// Blind says whether the router cannot see names now (R41): no capture, no passive
	// DNS, or forward mode with flow offloading (nil: names are seen).
	Blind func() string
	// LogGroup is the nflog group the capture reads, 0 when it reads none:
	// the rules copy tries towards a blocked country there for the alerts.
	LogGroup uint16
	// AgentPending is openwrt-mcp's pending.json; while it names netryx the
	// console's writes wait (R39). "" checks nothing.
	AgentPending string
}

// console is everything the new UI reads, wired together.
type console struct {
	o     Options
	col   *collect.Collector
	apps  *enrich.AppMap
	table *flows.Table
	store *rollup.Store
	hist  *history
	local *localNet
	macs  *macResolver
	wan   *wanMonitor
	inv   *inventorySource
	api   http.Handler
	// started is when the console was built; lost counts connection events
	// the kernel could not deliver (written by ct.Watch, read atomically),
	// and lostLogged is the count last logged.
	started    time.Time
	lost       uint64
	lostLogged uint64
	// icons keeps the fetched favicons under <state>/icons; favs are the
	// apps it fetches for, from the meta file.
	icons *icons.Fetcher
	favs  []favApp
	// lastLive is the console clock's UnixNano at the last live poll, 0
	// before the first; wake tells the dump loop that someone started
	// watching, so live mode starts at once rather than after a 5 s wait.
	lastLive atomic.Int64
	wake     chan struct{}
	// after is time.After; tests make the loops' waits theirs.
	after func(time.Duration) <-chan time.Time
	// ensure is icons.Ensure; tests stand in for the sites.
	ensure func(ctx context.Context, id, domain string) error
	// rules applies the rule sections to inet netryx_rules; nil without a
	// config path.
	rules *rules.Engine
	// alerts is what the owner is told (spec 6.9), kept in <state>/alerts.json.
	alerts *alerts.Store
}

// Started and LostEvents report the service to the API.
func (c *console) Started() time.Time { return c.started }
func (c *console) LostEvents() uint64 { return atomic.LoadUint64(&c.lost) }

// logLost logs the lost-event count when it has changed since the last log,
// so a busy evening says so once a tick rather than never.
func (c *console) logLost() {
	if n := c.LostEvents(); n != c.lostLogged {
		log.Printf("the kernel did not deliver %d connection events (buffer overflow): those connections' bytes are counted from dumps only", n)
		c.lostLogged = n
	}
}

// buildConsole loads the data files, builds the flow table, the history and
// the sources of the inventory and the WAN tile, and starts nothing: start
// runs the loops that need netlink.
func buildConsole(o Options) (*console, error) {
	if o.Run == nil {
		o.Run = execRun
	}
	if o.FS == nil {
		o.FS = os.DirFS("/")
	}
	if o.Addrs == nil {
		o.Addrs = interfaceAddrs
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Neigh == nil {
		o.Neigh = neigh.Dump
	}
	if o.Sync <= 0 {
		o.Sync = 5 * time.Second
	}
	c := &console{o: o, col: o.Collector, started: o.Now(), after: time.After, wake: make(chan struct{}, 1)}
	c.icons = &icons.Fetcher{Dir: filepath.Join(o.State, "icons"), Client: o.IconClient, Now: o.Now, Logf: log.Printf}
	c.ensure = c.icons.Ensure
	if favs, err := loadFavs(o.Meta); err != nil {
		if o.FetchIcons {
			log.Printf("no app description (%v): site icons are not fetched", err)
		}
	} else {
		c.favs = favs
	}
	if c.col == nil {
		c.col = collect.New()
	}

	var geo *enrich.GeoDB
	if f, err := os.Open(o.Geo); err != nil {
		log.Printf("no address table (%v): countries and organisations will be unknown", err)
	} else {
		geo, err = enrich.LoadIP2ASN(f)
		f.Close()
		if err != nil {
			log.Printf("address table %s cannot be read (%v): countries and organisations will be unknown", o.Geo, err)
			geo = nil
		}
	}
	if f, err := os.Open(o.Apps); err != nil {
		log.Printf("no app map (%v): apps will be Unknown", err)
	} else {
		c.apps, err = enrich.LoadAppMap(f)
		f.Close()
		if err != nil {
			log.Printf("app map %s cannot be read (%v): apps will be Unknown", o.Apps, err)
			c.apps = nil
		}
	}
	var oui map[string]string
	if f, err := os.Open(o.OUI); err != nil {
		log.Printf("no vendor list (%v)", err)
	} else {
		oui, err = inventory.LoadOUI(f)
		f.Close()
		if err != nil {
			log.Printf("vendor list %s cannot be read (%v)", o.OUI, err)
		}
	}

	c.local = &localNet{addrs: o.Addrs, fsys: o.FS, run: o.Run}
	c.local.refresh()
	c.store = rollup.NewStore(rollup.Options{Now: o.Now})
	dir := filepath.Join(o.State, "history")
	c.hist = &history{s: c.store, dir: dir, saved: o.Now()}
	c.hist.load()
	c.macs = &macResolver{col: c.col, local: c.local, neigh: o.Neigh, leases: o.Leases, now: o.Now}
	c.macs.refresh()
	// One answer to "whose is this address", for the finished totals and
	// the flow table alike.
	c.col.Resolve = c.macs.MACOf
	c.col.IsLocal = c.local.IsLocal
	// The time limits count minutes from the same deltas the rollup takes.
	var usage *rules.Usage
	if o.RulesConfig != "" {
		usage = rules.NewUsage(o.Loc)
	}
	sink := func(d flows.Delta) {
		c.store.Add(d)
		if usage != nil {
			usage.Add(d.MAC, d.App, d.Up, d.Down, d.At)
		}
	}
	c.table = flows.NewTable(flows.Options{
		IsLocal: c.local.IsLocal,
		MACOf:   c.macs.MACOf,
		NameOf:  c.col.NameOf,
		Geo:     geo.Lookup,
		App:     c.apps.Resolve,
		Sink:    sink,
	})
	if o.RulesConfig != "" {
		if o.NFT == nil {
			o.NFT = nftRun
		}
		c.alerts = alerts.NewStore(filepath.Join(o.State, "alerts.json"), o.Now, o.Loc)
		c.rules = rules.NewEngine(rules.EngineOptions{
			ConfigPath: o.RulesConfig, StateDir: o.State, Run: o.NFT, Now: o.Now, Loc: o.Loc,
			Geo: geo, Apps: c.apps, Live: c.table.Live,
			Delete: ct.Delete, DeleteReply: ct.DeleteReply, Logf: log.Printf,
			// The house's networks and the router's addresses are never an
			// app's destination.
			IsLocal: c.local.IsLocal,
			Blind:   o.Blind,
			Usage:   usage,
			SafeDNS: safedns.NewManager(safedns.Options{
				Run: safedns.Runner(o.NFT), Resolve: mainResolve, Probe: probeSafe,
				Ifaces: func() []string { return o.LAN }, Now: o.Now, Logf: log.Printf,
			}),
			HouseIfaces:   func() []string { return o.LAN },
			HousePrefixes: c.local.Prefixes,
			Roster:        rules.NewRoster(filepath.Join(o.State, "known-macs.json"), o.Now),
			// Each tick sees the house's devices; the baseline also takes the
			// week's history, for devices asleep on install day.
			Seen: func() []rules.Seen { return houseSeen(c.inv, nil, inHouse(c.local.IsLocal), o.Now()) },
			Known: func() []string {
				seen := houseSeen(c.inv, func(from, to time.Time) []string {
					var keys []string
					for _, r := range c.store.Top(rollup.Filter{}, "mac", from, to, 512) {
						keys = append(keys, r.Key)
					}
					return keys
				}, inHouse(c.local.IsLocal), o.Now())
				labels, _ := inventory.LoadLabels(filepath.Join(o.State, "labels.json"))
				return knownMACs(seen, labels)
			},
			Event: func(ev rules.Event) {
				if (ev.Kind == alerts.NewDevice || ev.Kind == alerts.NewDevices) && !c.rules.Alerts().NewDevice {
					return
				}
				c.alerts.Raise(alerts.Alert{Kind: ev.Kind, MAC: ev.MAC, Count: ev.Count})
			},
			LogGroup: o.LogGroup,
		})
		// The capture tells the engine what the resolver answered and what
		// each handshake named, outside the collector's lock.
		c.col.OnAnswer = c.rules.Answer
		c.col.OnName = c.rules.Named
		c.col.OnTry = tryHandler(func(a netip.Addr) string {
			info, _ := geo.Lookup(a)
			return info.CC
		}, func(a alerts.Alert) { c.alerts.Raise(a) })
	}
	target := o.PingTarget
	if target != "" && !validTarget(target) {
		log.Printf("probe target %q is neither an address nor a host name: probing the line's gateway", target)
		target = ""
	}
	c.wan = &wanMonitor{run: o.Run, fsys: o.FS, iface: o.WAN, target: target, onDevice: c.local.setWAN}
	c.inv = &inventorySource{
		col: c.col, run: o.Run, fsys: o.FS, now: o.Now, macs: c.macs, live: c.table.Live,
		labels:  filepath.Join(o.State, "labels.json"),
		conf:    o.RulesConfig,
		bridges: o.LAN, oui: oui, every: 30 * time.Second,
	}
	apiOpts := []api.Option{
		api.OnLivePoll(c.livePolled),
		// The Network screen reads the router's wiring only: ubus, sysfs,
		// the UCI files and `wg show`, never a key or a password.
		api.WithNetwork(&netview.Source{FS: o.FS, Run: netview.Runner(o.Run), Bridges: o.LAN, Now: o.Now}),
	}
	if c.rules != nil {
		// The console writes the config the agent writes, and the engine
		// reads it back: names and rules from the page and from Hermes are
		// the same thing.
		apiOpts = append(apiOpts, api.WithRules(&rulesAPI{
			eng: c.rules, conf: o.RulesConfig, labels: filepath.Join(o.State, "labels.json"),
			run: o.NFT, inv: c.inv, agentPending: o.AgentPending,
			alerts: c.alerts, triesSeen: o.LogGroup != 0, fsys: o.FS,
		}))
	}
	c.api = api.New(c.table, c.store, c.inv, c.wan, c.apps, o.Now, c, apiOpts...)
	return c, nil
}

// mount puts the console on a mux: the API under /api/ and the page at /.
// More specific routes registered beside it (/api/snapshot) still win.
func (c *console) mount(mux *http.ServeMux) {
	ui := web.Handler()
	mux.Handle("/api/", c.api)
	mux.Handle("/icons/", c.iconRoute(ui))
	mux.Handle("/", ui)
}

func (c *console) handler() http.Handler {
	mux := http.NewServeMux()
	c.mount(mux)
	return mux
}

// start runs the loops: connection events into the collector and the flow
// table, a dump every Sync to read counters, the WAN tick, and the history
// tick. They stop with stop, except the event watch, which ends with the
// process.
func (c *console) start(stop <-chan struct{}) {
	if b, err := fs.ReadFile(c.o.FS, "proc/sys/net/netfilter/nf_conntrack_acct"); err == nil && strings.TrimSpace(string(b)) != "1" {
		log.Print("the kernel does not count bytes (nf_conntrack_acct=0): volumes in the console will be zero; sysctl -w net.netfilter.nf_conntrack_acct=1")
	}
	go func() {
		evs := make(chan ct.Event, 1024)
		go func() {
			for e := range evs {
				c.event(e, c.o.Now())
			}
		}()
		if err := ct.Watch(evs, &c.lost); err != nil {
			log.Printf("not listening to connection events (%v); volumes will come from dumps only", err)
		}
	}()
	// Dumps are taken and applied one at a time, in this one goroutine: the
	// table relies on it to tell a dump that raced a Destroy.
	go c.syncLoop(stop, func() {
		dump, err := ct.Dump()
		if err != nil {
			log.Printf("connection table dump: %v", err)
			return
		}
		c.table.Sync(dump, c.o.Now())
	})
	c.startIcons(stop)
	go every(stop, 10*time.Second, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		c.wan.tick(ctx, c.o.Now())
		c.local.refresh()
		c.macs.refresh()
		c.logLost()
	})
	go every(stop, time.Minute, func() { c.hist.tick(c.o.Now()) })
	if c.rules != nil && c.alerts != nil {
		// Delivery runs on its own goroutine: a slow or dead Telegram never
		// holds the engine (Review Focus 4).
		sender := alerts.NewSender(alerts.SenderOptions{})
		go every(stop, 30*time.Second, func() {
			c.deliverRound(context.Background(), c.rules.AlertsInForce, func() (alerts.Bot, string) {
				return alerts.ReadBot(alerts.HermesConfig, os.Stat)
			}, sender.Send)
		})
		// Unusual upload (spec 6.9): once an hour, the hour that just closed.
		go every(stop, time.Hour, func() {
			uploadRound(c.rules.AlertsInForce, c.store, c.o.Now(), func(mac string, up uint64) {
				c.alerts.Raise(alerts.Alert{Kind: alerts.Upload, MAC: mac, Bytes: up})
			})
		})
	}
	if c.rules != nil {
		// Applies the rules at once (a failure is logged and the console
		// runs on), then ticks, feeds the app sets and saves them.
		go c.rules.Run(stop)
	}
}

// deliverRound is one round of the delivery loop. It sends nothing until the
// engine has read the owner's config once: before that the settings are the
// defaults (channel telegram), and a router with Hermes's bot configured
// would send before the owner's `channel 'console'` is seen.
func (c *console) deliverRound(ctx context.Context, settings func() (rules.Alerts, bool), readBot func() (alerts.Bot, string), send func(context.Context, alerts.Bot, string) error) {
	set, read := settings()
	if !read {
		return
	}
	bot, why := readBot()
	c.alerts.Deliver(ctx, set.Channel, bot, why, send, c.deviceName)
}

// reloadRules reads the rule sections again: serve calls it on SIGHUP.
func (c *console) reloadRules() {
	if c.rules == nil {
		return
	}
	if err := c.rules.Reload(); err != nil {
		log.Printf("rules not applied: %v", err)
	}
}

// event takes one connection event into the collector and the flow table.
func (c *console) event(e ct.Event, now time.Time) {
	// What has finished, with its final counters, is the only figure that
	// survives a device closing its connections.
	c.col.Finished(e, now)
	c.table.Apply(e, now)
}

// every runs fn now and then every d until stop closes.
func every(stop <-chan struct{}, d time.Duration, fn func()) {
	fn()
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			fn()
		}
	}
}

// shutdown writes the history, open hour included, and what the app sets
// hold. serve calls it on SIGTERM, before it removes the capture rule. The
// rules table itself is never removed: it keeps enforcing without netryx.
func (c *console) shutdown() error {
	err := c.hist.shutdown()
	if c.rules != nil {
		if e := c.rules.Flush(); e != nil {
			log.Printf("app addresses at stop: %v", e)
		}
		if e := c.rules.SaveSets(); e != nil {
			log.Printf("app sets at stop: %v", e)
		}
		if e := c.rules.SaveUsage(); e != nil {
			log.Printf("minute usage at stop: %v", e)
		}
	}
	return err
}

// history owns the rollup's directory. One mutex serialises every load and
// save: two saves at once may both write a bucket, and a load racing a save
// may read a file twice.
type history struct {
	logf    func(string, ...any) // nil is log.Printf
	mu      sync.Mutex
	s       *rollup.Store
	dir     string
	pending bool // the last load set files aside, as later than the clock
	lastErr string
	saved   time.Time
	closed  bool // the stop has written it; nothing is written after that
}

// saveLag is how long after an hour's end the tick writes it: a bucket
// closes a minute after its end, and the tick runs every minute.
const saveLag = 2 * time.Minute

func (h *history) log(f string, a ...any) {
	if h.logf != nil {
		h.logf(f, a...)
		return
	}
	log.Printf(f, a...)
}

func (h *history) load() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loadLocked()
}

// loadLocked runs LoadDir. After the first call LoadDir reads only the files
// it set aside as later than the clock, so an error from a repeat means some
// are still in the future, and the next tick asks again. The same error every
// minute is not news, so it is logged when it changes.
func (h *history) loadLocked() {
	err := rollup.LoadDir(h.s, h.dir)
	h.pending = err != nil
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if msg != "" && msg != h.lastErr {
		h.log("history: %v", err)
	}
	h.lastErr = msg
}

// tick runs every minute. Files set aside before the clock was set are
// loaded as soon as it passes them, and each hour is written by the first
// tick after it closes, so a power cut loses at most the hour in progress. A
// clock that went back writes at once.
func (h *history) tick(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	if h.pending {
		h.loadLocked()
	}
	due := now.Add(-saveLag).Truncate(time.Hour).After(h.saved.Add(-saveLag).Truncate(time.Hour))
	if !due && !now.Before(h.saved) {
		return
	}
	h.saved = now
	if _, err := rollup.SaveClosed(h.s, h.dir); err != nil {
		h.log("writing history: %v", err)
	}
}

func (h *history) shutdown() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	return rollup.SaveOpen(h.s, h.dir)
}

// localNet answers whether an address is at home: inside a network the
// router serves, or one of the router's own addresses. The networks are read
// from the router's interfaces, all but those facing the internet, so a
// guest bridge or a VLAN is home and the WAN subnet (the ISP's gateway) is
// not. It is read on every WAN tick, since a delegated IPv6 prefix changes.
type localNet struct {
	addrs func() (map[string][]netip.Prefix, error)
	fsys  fs.FS
	run   runner                 // nil reads no local domain: lan
	wan   atomic.Pointer[string] // the WAN device ubus reports
	cur   atomic.Pointer[localSet]
	// names are the router's own host name and that name in its local
	// domain, lower case: the names a person may type to reach the console.
	// Empty when the router has no usable host name.
	names atomic.Pointer[map[string]bool]
	// domain was read for host domainFor at domainTime: uci is asked again
	// only when the host name changes or after a minute.
	domMu             sync.Mutex
	domainFor, domain string
	domainTime        time.Time
}

type localSet struct {
	nets []netip.Prefix
	own  map[netip.Addr]bool
	// lanOwn is the router's own addresses on the house side: the ones a
	// person may type to reach the console.
	lanOwn map[netip.Addr]bool
}

func (l *localNet) setWAN(dev string) {
	if p := l.wan.Load(); p == nil || *p != dev {
		l.wan.Store(&dev)
		l.refresh()
	}
}

// readNames reads the host name from /proc/sys/kernel/hostname and the local
// domain from dnsmasq's UCI (dhcp.@dnsmasq[0].domain, lan when unset or
// unreadable, as on OpenWrt). A host name that is not one DNS label admits
// nothing. It runs with the interfaces, every 10 s.
func (l *localNet) readNames() {
	set := map[string]bool{}
	defer func() { l.names.Store(&set) }()
	b, err := fs.ReadFile(l.fsys, "proc/sys/kernel/hostname")
	if err != nil {
		return
	}
	host := strings.ToLower(strings.TrimSpace(string(b)))
	if host == "" || strings.Contains(host, ".") || !validTarget(host) {
		return
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return
	}
	set[host] = true
	l.domMu.Lock()
	defer l.domMu.Unlock()
	if l.domainFor != host || time.Since(l.domainTime) >= time.Minute {
		domain := "lan"
		if l.run != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			out, err := l.run(ctx, "uci", "-q", "get", "dhcp.@dnsmasq[0].domain")
			cancel()
			if d := strings.Trim(strings.ToLower(strings.TrimSpace(string(out))), "."); err == nil && d != "" && validTarget(d) {
				domain = d
			}
		}
		l.domainFor, l.domain, l.domainTime = host, domain, time.Now()
	}
	domain := l.domain
	set[host+"."+domain] = true
}

// isOwnName says whether a host name (lower case, no trailing dot) is the
// router's own, or that name in its local domain.
func (l *localNet) isOwnName(h string) bool {
	m := l.names.Load()
	return m != nil && (*m)[h]
}

func (l *localNet) refresh() {
	l.readNames()
	all, err := l.addrs()
	if err != nil {
		log.Printf("interface addresses: %v", err)
		return
	}
	outside := defaultRouteIfaces(l.fsys)
	if p := l.wan.Load(); p != nil && *p != "" {
		outside[*p] = true
	}
	s := &localSet{own: map[netip.Addr]bool{}, lanOwn: map[netip.Addr]bool{}}
	for name, ps := range all {
		for _, p := range ps {
			a := p.Addr().Unmap()
			s.own[a] = true
			if outside[name] {
				continue
			}
			s.lanOwn[a] = true
			if a.IsLoopback() || a.IsLinkLocalUnicast() {
				continue
			}
			s.nets = append(s.nets, netip.PrefixFrom(a, p.Bits()).Masked())
		}
	}
	l.cur.Store(s)
}

// Prefixes are the networks the router serves, for the rules' isolation.
// IsLocal's fallback stands when they could not be read: an empty answer,
// which the compiler reads as the private ranges.
func (l *localNet) Prefixes() []netip.Prefix {
	s := l.cur.Load()
	if s == nil {
		return nil
	}
	return append([]netip.Prefix(nil), s.nets...)
}

// isOwn says whether an address is one of the router's own, on any side.
func (l *localNet) isOwn(a netip.Addr) bool {
	s := l.cur.Load()
	return s != nil && s.own[a.Unmap()]
}

// IsLocal is called by the flow table under its lock, so it only reads an
// immutable set.
func (l *localNet) IsLocal(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsLinkLocalUnicast() {
		return true
	}
	s := l.cur.Load()
	if s == nil {
		return a.IsPrivate()
	}
	if s.own[a] {
		return true
	}
	if len(s.nets) == 0 {
		// The interfaces could not be read: the private ranges are the
		// best guess left.
		return a.IsPrivate()
	}
	for _, p := range s.nets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// defaultRouteIfaces names the interfaces holding a default route, IPv4 or
// IPv6: those face the internet, whatever ubus calls them. lo holds the
// unreachable IPv6 default every OpenWrt installs and is not one.
func defaultRouteIfaces(fsys fs.FS) map[string]bool {
	out := map[string]bool{}
	if b, err := fs.ReadFile(fsys, "proc/net/route"); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) >= 8 && f[1] == "00000000" && f[7] == "00000000" {
				out[f[0]] = true
			}
		}
	}
	if b, err := fs.ReadFile(fsys, "proc/net/ipv6_route"); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) >= 10 && f[1] == "00" && strings.Trim(f[0], "0") == "" && f[9] != "lo" {
				out[f[9]] = true
			}
		}
	}
	return out
}

// interfaceAddrs is the kernel's list of addresses per interface.
func interfaceAddrs() (map[string][]netip.Prefix, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := map[string][]netip.Prefix{}
	for _, i := range ifs {
		as, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range as {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(n.IP)
			if !ok {
				continue
			}
			ones, _ := n.Mask.Size()
			out[i.Name] = append(out[i.Name], netip.PrefixFrom(ip.Unmap(), ones))
		}
	}
	return out, nil
}

// wanMonitor keeps the internet tile: the line's state from ubus, the rate
// from its counters, and latency, jitter and loss from one ping per tick.
type wanMonitor struct {
	run      runner
	fsys     fs.FS
	iface    string
	target   string
	onDevice func(string)

	mu      sync.Mutex
	st      wan.Status
	prober  wan.Prober
	counter *wan.Counter
	dev     string
	probed  string
}

func (w *wanMonitor) Status() wan.Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.st
}

// tick is called every 10 s by one goroutine.
func (w *wanMonitor) tick(ctx context.Context, now time.Time) {
	var st wan.Status
	gw := ""
	if out, err := w.run(ctx, "ubus", "call", "network.interface."+w.iface, "status"); err == nil {
		if s, err := wan.ParseUbusInterface(out); err == nil {
			st = s
		}
		gw = gatewayOf(out)
	}
	// The IPv6 address is on the wan6 interface, not on wan.
	for _, name := range []string{w.iface + "6", w.iface + "_6"} {
		out, err := w.run(ctx, "ubus", "call", "network.interface."+name, "status")
		if err != nil {
			continue
		}
		if s, err := wan.ParseUbusInterface(out); err == nil && s.IPv6 != "" {
			st.IPv6 = s.IPv6
			break
		}
	}
	// The internet target if one is configured, else the line's own
	// gateway: by default no third party is contacted.
	target := w.target
	if target == "" {
		target = gw
	}
	var ms float64
	var ok bool
	if target != "" {
		// -c 1: one output line is one sample; -W 1 bounds a dead line; --
		// ends the options, so a target is never read as one. A lost probe
		// exits non-zero and still prints, so the output is read whatever
		// the exit status.
		out, _ := w.run(ctx, "ping", "-c", "1", "-W", "1", "--", target)
		ms, ok = wan.ParsePing(string(out))
	}
	if st.Device != "" && st.Device != w.dev {
		w.counter = wan.NewCounter(w.fsys, st.Device)
		w.dev = st.Device
		if w.onDevice != nil {
			w.onDevice(st.Device)
		}
	}
	if w.counter != nil {
		if rx, tx, err := w.counter.Sample(now); err == nil {
			st.RxRate, st.TxRate = rx, tx
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if target != w.probed {
		// A new target is a new line to measure; the old window says
		// nothing about it.
		w.prober = wan.Prober{}
		w.probed = target
	}
	if target != "" {
		w.prober.Add(ms, ok)
	}
	st.LatencyMs, st.JitterMs, st.LossPct, st.LatencyOK = w.prober.Stats()
	w.st = st
}

// macResolver is the console's one answer to "which device is this
// address". The router's own addresses are the router; anything else is the
// hardware address the capture saw using it, else the one the neighbour table
// holds for it, else the one a DHCP lease gives it. Nobody knowing is "".
// MACOf is called under the flow table's lock, so it only reads tables that
// are never written after they are published.
type macResolver struct {
	col    *collect.Collector
	local  *localNet
	neigh  func() ([]neigh.Entry, error)
	leases string
	now    func() time.Time
	cur    atomic.Pointer[macTables]
}

type macTables struct {
	reg    *neigh.Registry
	leases *device.Table
}

// refresh re-reads the neighbour table and the leases, publishes them and
// returns them. A source that cannot be read is absent, never fatal.
func (r *macResolver) refresh() *macTables {
	t := &macTables{}
	if f, err := os.Open(r.leases); err == nil {
		t.leases = device.NewTable(device.ParseLeases(f))
		f.Close()
	}
	// A fresh registry each time: it is read from several goroutines at
	// once, and a registry is never written after it is published.
	if entries, err := r.neigh(); err == nil {
		reg := neigh.NewRegistry()
		now := r.now()
		for _, e := range entries {
			reg.Observe(e, now)
		}
		t.reg = reg
	}
	r.cur.Store(t)
	return t
}

func (r *macResolver) MACOf(addr string) string {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return ""
	}
	a = a.Unmap()
	if r.local != nil && r.local.isOwn(a) {
		return collect.RouterKey
	}
	if m := r.col.MACOf(a.String()); m != "" {
		return m
	}
	t := r.cur.Load()
	if t == nil {
		return ""
	}
	if m := t.reg.MACOf(a.AsSlice()); m != "" {
		return m
	}
	return t.leases.MACByIP(a.String())
}

// inventorySource builds the client list on every API call from sources it
// re-reads at most every 30 s: leases, the neighbour table, Wi-Fi clients,
// the bridge's forwarding table and the labels file.
type inventorySource struct {
	col    *collect.Collector
	run    runner
	fsys   fs.FS
	now    func() time.Time
	macs   *macResolver
	live   func() []flows.Flow // nil: no flow table
	labels string
	// conf is /etc/config/netryx: a device section's name and group win over
	// labels.json. "" reads none.
	conf    string
	bridges []string
	oui     map[string]string
	every   time.Duration

	mu     sync.Mutex
	at     time.Time
	src    inventory.Sources
	logged bool
	// confLogged is the device-section error last logged.
	confLogged string
}

// invalidate makes the next Devices read the sources again, so a name just
// written shows at once instead of in up to 30 s.
func (s *inventorySource) invalidate() {
	s.mu.Lock()
	s.at = time.Time{}
	s.mu.Unlock()
}

func (s *inventorySource) Devices() []inventory.Device {
	s.mu.Lock()
	now := s.now()
	if s.at.IsZero() || now.Sub(s.at) >= s.every || now.Before(s.at) {
		s.refreshLocked()
		s.at = now
	}
	src := s.src
	s.mu.Unlock()
	src.Collected = append(s.col.Devices(), s.moving()...)
	return inventory.Build(src)
}

// moving lists, as collector rows, the devices whose flows are open now: a
// device on a fixed address that holds no lease and whose handshakes were
// never read is still a device while it downloads, not only once a
// connection of its has finished.
func (s *inventorySource) moving() []collect.Device {
	if s.live == nil {
		return nil
	}
	by := map[string]*collect.Device{}
	for _, f := range s.live() {
		if _, err := net.ParseMAC(f.MAC); err != nil {
			continue // the router, or an address nobody could name
		}
		d := by[f.MAC]
		if d == nil {
			d = &collect.Device{MAC: f.MAC, First: f.Start}
			by[f.MAC] = d
		}
		if f.Start.Before(d.First) {
			d.First = f.Start
		}
		if f.Updated.After(d.Last) {
			d.Last = f.Updated
		}
	}
	out := make([]collect.Device, 0, len(by))
	for _, d := range by {
		out = append(out, *d)
	}
	return out
}

func (s *inventorySource) refreshLocked() {
	src := inventory.Sources{OUI: s.oui}
	tabs := s.macs.refresh()
	src.Leases, src.Neigh = tabs.leases, tabs.reg
	labels, err := inventory.LoadLabels(s.labels)
	if err != nil && !s.logged {
		log.Printf("device labels: %v", err)
		s.logged = true
	}
	if s.conf != "" {
		devs, err := deviceSections(s.conf)
		if err != nil {
			if msg := err.Error(); msg != s.confLogged {
				log.Printf("device names from the config: %v", err)
				s.confLogged = msg
			}
		}
		if labels == nil {
			labels = map[string]inventory.Label{}
		}
		for mac, d := range devs {
			l := labels[mac]
			if d.Name != "" {
				l.Name = d.Name
			}
			if d.Group != "" {
				l.Group = d.Group
			}
			labels[mac] = l
		}
	}
	src.Labels = labels

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wifi, aps := s.wifi(ctx)
	links := map[string]inventory.Link{}
	for _, br := range s.bridges {
		for mac, l := range wiredLinks(s.fsys, br, aps) {
			links[mac] = l
		}
	}
	// hostapd knows a Wi-Fi client for certain; the forwarding table only
	// knows which port its frames came in on.
	for mac, l := range wifi {
		links[mac] = l
	}
	src.WiFi = links
	s.src = src
}

// wifi asks every hostapd instance for its clients and its network's name.
// It also returns the access point interfaces, which are never wired ports.
func (s *inventorySource) wifi(ctx context.Context) (map[string]inventory.Link, map[string]bool) {
	links := map[string]inventory.Link{}
	aps := map[string]bool{}
	out, err := s.run(ctx, "ubus", "list", "hostapd.*")
	if err != nil {
		return links, aps
	}
	for _, obj := range strings.Fields(string(out)) {
		iface, ok := strings.CutPrefix(obj, "hostapd.")
		if !ok || iface == "" {
			continue
		}
		aps[iface] = true
		js, err := s.run(ctx, "ubus", "call", obj, "get_clients")
		if err != nil {
			continue
		}
		var ssid string
		if st, err := s.run(ctx, "ubus", "call", obj, "get_status"); err == nil {
			var v struct {
				SSID string `json:"ssid"`
			}
			if json.Unmarshal(st, &v) == nil {
				ssid = v.SSID
			}
		}
		for mac, l := range inventory.ParseHostapdClients(iface, js) {
			l.SSID = ssid
			links[mac] = l
		}
	}
	return links, aps
}

// fdbEntrySize is the size of struct __fdb_entry, the record the kernel
// writes to /sys/class/net/<bridge>/brforward.
const fdbEntrySize = 16

// wiredLinks reads the bridge's forwarding table from sysfs, which needs no
// tool on the router (OpenWrt ships neither bridge nor brctl by default), and
// returns the address learned behind each port that is not a radio. The
// bridge's own addresses are flagged local and skipped.
func wiredLinks(fsys fs.FS, bridge string, aps map[string]bool) map[string]inventory.Link {
	out := map[string]inventory.Link{}
	base := "sys/class/net/" + bridge
	ports, err := fs.ReadDir(fsys, base+"/brif")
	if err != nil {
		return out
	}
	byNo := map[uint16]string{}
	for _, p := range ports {
		name := p.Name()
		if aps[name] || isRadio(fsys, name) {
			continue
		}
		b, err := fs.ReadFile(fsys, base+"/brif/"+name+"/port_no")
		if err != nil {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 0, 16)
		if err != nil {
			continue
		}
		byNo[uint16(n)] = name
	}
	b, err := fs.ReadFile(fsys, base+"/brforward")
	if err != nil {
		return out
	}
	for i := 0; i+fdbEntrySize <= len(b); i += fdbEntrySize {
		e := b[i : i+fdbEntrySize]
		if e[7] != 0 { // is_local
			continue
		}
		port, ok := byNo[uint16(e[12])<<8|uint16(e[6])]
		if !ok {
			continue
		}
		mac := net.HardwareAddr(e[:6]).String()
		out[mac] = inventory.Link{Kind: "wired", Iface: port}
	}
	return out
}

// isRadio says whether a port is a Wi-Fi interface: mac80211 gives each one
// a phy80211 link, and older drivers a wireless directory.
func isRadio(fsys fs.FS, port string) bool {
	for _, p := range []string{"phy80211", "wireless"} {
		if _, err := fs.Stat(fsys, "sys/class/net/"+port+"/"+p); err == nil {
			return true
		}
	}
	return false
}

// gatewayOf returns the IPv4 next hop of the default route in a
// `ubus call network.interface.<wan> status` document, or "".
func gatewayOf(js []byte) string {
	var v struct {
		Route []struct {
			Target  string `json:"target"`
			Mask    int    `json:"mask"`
			Nexthop string `json:"nexthop"`
		} `json:"route"`
	}
	if json.Unmarshal(js, &v) != nil {
		return ""
	}
	for _, r := range v.Route {
		if r.Target != "0.0.0.0" || r.Mask != 0 {
			continue
		}
		if a, err := netip.ParseAddr(r.Nexthop); err == nil && !a.IsUnspecified() {
			return a.String()
		}
	}
	return ""
}

// validTarget accepts an address or a host name and nothing else: the value
// comes from a config file and goes on a command line.
func validTarget(s string) bool {
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// guard refuses a request whose Host is not a name of this router. A page
// elsewhere can point its own name at the router's address (DNS rebinding)
// and then read the API as same-origin; the browser still sends that name in
// Host, so it is refused before any route runs. Allowed: localhost and the
// loopback addresses, the listen address, the router's own addresses on the
// house side, and its own host name alone or in its local domain, on any port.
func (c *console) guard(h http.Handler) http.Handler {
	listen := ""
	if host, _, err := net.SplitHostPort(c.o.Listen); err == nil {
		listen = normHost(host)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !c.hostAllowed(normHost(hostOnly(r.Host)), listen) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusMisdirectedRequest)
			io.WriteString(w, "misdirected request\n")
			return
		}
		h.ServeHTTP(w, r)
	})
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
}

func normHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

func (c *console) hostAllowed(host, listen string) bool {
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		// A name passes only if it is the name the server listens on, or
		// the router's own host name, alone or in its local domain.
		return listen != "" && host == listen || c.local.isOwnName(host)
	}
	a = a.Unmap()
	if a.IsLoopback() {
		return true
	}
	if la, err := netip.ParseAddr(listen); err == nil && la.Unmap() == a {
		return true
	}
	if s := c.local.cur.Load(); s != nil && s.lanOwn[a] {
		return true
	}
	return false
}

// The live view is a client card open in a browser, polling every 2 s. While
// one polled within liveWindow the connection table is dumped every liveSync,
// so the card moves at the resolution it draws; with nobody watching, the
// router goes back to the configured Sync and spends nothing extra.
const (
	liveSync   = 2 * time.Second
	liveWindow = 10 * time.Second
)

// livePolled is the API's OnLivePoll: it stamps the poll by the console
// clock, and wakes the dump loop when nobody was watching before.
func (c *console) livePolled() {
	now := c.o.Now()
	prev := c.lastLive.Swap(now.UnixNano())
	if since := now.Sub(time.Unix(0, prev)); prev == 0 || since < 0 || since >= liveWindow {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

// syncInterval is the wait before the next dump. A poll stamped later than
// now (the clock went back) does not count as watching; the next poll stamps
// again.
func (c *console) syncInterval(now time.Time) time.Duration {
	d := c.o.Sync
	if at := c.lastLive.Load(); at != 0 {
		since := now.Sub(time.Unix(0, at))
		if since >= 0 && since < liveWindow && liveSync < d {
			d = liveSync
		}
	}
	return d
}

// syncLoop runs dump now and then after every syncInterval until stop
// closes. It is the one goroutine that dumps, so dumps stay serialised
// whatever the interval.
func (c *console) syncLoop(stop <-chan struct{}, dump func()) {
	for {
		dump()
		select {
		case <-stop:
			return
		case <-c.after(c.syncInterval(c.o.Now())):
		case <-c.wake:
		}
	}
}

// favApp is one app whose icon is a favicon the router fetches.
type favApp struct{ id, domain string }

// maxMeta bounds the meta file read: about 200 apps take some 40 KB.
const maxMeta = 4 << 20

// loadFavs reads the apps whose meta icon is "fav", sorted by id so passes
// run in the same order. Ids and domains are checked by the fetcher.
func loadFavs(path string) ([]favApp, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var meta map[string]struct {
		Icon   string `json:"icon"`
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(io.LimitReader(f, maxMeta)).Decode(&meta); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []favApp
	for id, a := range meta {
		if a.Icon == "fav" && a.Domain != "" {
			out = append(out, favApp{id: id, domain: a.Domain})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, nil
}

const (
	iconTimeout = 10 * time.Second // one Ensure, both URLs and redirects
	iconPause   = 2 * time.Second  // between two sites asked
	iconEvery   = 24 * time.Hour   // a pass a day; the fetcher skips what it has
	clockCheck  = time.Minute
	// iconSettle is the wait after the start: a booting router's line is
	// often not up yet, and its clock may already read past clockFloor
	// (sysfixtime sets it from the newest file in /etc) before NTP answers.
	iconSettle = 2 * time.Minute
	// iconOffline network errors in a row end a pass: the line is down, and
	// asking the rest of the list would only spend the pass.
	iconOffline = 3
)

// clockFloor is a time no correct clock reads before. A router without a
// real-time clock boots in the past until NTP answers, and every certificate
// would then look not yet valid, recording a week's failure for each brand.
var clockFloor = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// startIcons starts the favicon loop when fetch_icons is on and there is
// something to fetch. It is the only place fetching starts.
func (c *console) startIcons(stop <-chan struct{}) bool {
	if !c.o.FetchIcons || len(c.favs) == 0 {
		return false
	}
	go c.iconLoop(stop)
	return true
}

// iconLoop waits for the clock to look set, then makes a pass now and one a
// day. It is the one goroutine that calls Ensure.
func (c *console) iconLoop(stop <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	select {
	case <-stop:
		return
	case <-c.after(iconSettle):
	}
	for c.o.Now().Before(clockFloor) {
		select {
		case <-stop:
			return
		case <-c.after(clockCheck):
		}
	}
	for {
		c.iconPass(ctx, stop)
		select {
		case <-stop:
			return
		case <-c.after(iconEvery):
		}
	}
}

// iconPass asks each fav app's site in turn, with a pause between sites
// actually asked; an icon already kept, or a site whose failure still holds,
// costs no request and no pause. Network errors are not the sites' failures
// (the fetcher does not record them), and iconOffline of them in a row end
// the pass: the next one is tomorrow's.
func (c *console) iconPass(ctx context.Context, stop <-chan struct{}) {
	tried, kept, offline := 0, 0, 0
	for _, a := range c.favs {
		if !c.icons.Due(a.id) {
			continue
		}
		if tried > 0 {
			select {
			case <-stop:
				return
			case <-c.after(iconPause):
			}
		}
		tried++
		actx, cancel := context.WithTimeout(ctx, iconTimeout)
		err := c.ensure(actx, a.id, a.domain) // a site's failure is recorded by the fetcher
		cancel()
		if ctx.Err() != nil {
			return
		}
		switch {
		case err == nil:
			kept++
			offline = 0
		case errors.Is(err, icons.ErrNetwork):
			offline++
		default:
			offline = 0
		}
		if offline >= iconOffline {
			log.Printf("site icons: %d network errors in a row, next try in a day", offline)
			break
		}
	}
	if tried > 0 {
		log.Printf("site icons: %d of %d fetched", kept, tried)
	}
}

// iconRoute serves /icons/. The icon pack (<id>.svg) and its manifest are
// files of the page built into the binary and go to ui; /icons/<id> with no
// extension is a favicon the router fetched, read from <state>/icons by the
// fetcher, which admits only a valid id and a regular file carrying its
// format's magic. Anything else is 404.
func (c *console) iconRoute(ui http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/icons/")
		// Ids may carry a dot (news.site), so the extension is no guide:
		// exactly manifest.json and <id>.svg are the page's, the rest the
		// fetcher's. No id holds "..".
		if !strings.Contains(name, "..") && (name == "manifest.json" ||
			(strings.HasSuffix(name, ".svg") && iconID.MatchString(strings.TrimSuffix(name, ".svg")))) {
			ui.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !iconID.MatchString(name) || strings.Contains(name, "..") {
			http.NotFound(w, r)
			return
		}
		body, ctype, mod, ok := c.icons.Open(name)
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", ctype)
		h.Set("Cache-Control", "max-age=86400")
		// The magic is checked as a prefix only, so the rest of the file is
		// the site's: it must never be sniffed or rendered as a document.
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'")
		http.ServeContent(w, r, "", mod, body)
	})
}

// iconID is the fetcher's id rule, applied before any file is looked for.
var iconID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// serveMux is everything serve answers: the console (its page at / and its
// API under /api/) and the scrape endpoints, which keep their more specific
// paths.
func serveMux(c *console, snap func() export.Snapshot) *http.ServeMux {
	mux := http.NewServeMux()
	c.mount(mux)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_ = export.Prometheus(w, snap())
	})
	mux.HandleFunc("/api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = export.JSON(w, snap())
	})
	mux.HandleFunc("/api/zabbix/discovery", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = export.Discovery(w, snap())
	})
	return mux
}

// serveHandler is the order every request meets: the Host check first, so a
// rebound name reaches nothing, the login gate next, then the routes.
func (c *console) serveHandler(mux http.Handler, g *auth.Gate) http.Handler {
	return c.guard(g.Wrap(mux))
}

// loopbackListen reports whether addr listens on loopback only.
func loopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Unmap().IsLoopback()
}

// listenAddr is where the console listens given what the rpcd probe said:
// with no rpcd no login is possible, so a house-side address gives way to
// loopback on the same port, where the owner reaches it through an SSH
// tunnel.
func listenAddr(addr string, rpcErr error) string {
	if !errors.Is(rpcErr, auth.ErrNoRPC) || loopbackListen(addr) {
		return addr
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "8788"
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// probeWaits are the pauses between the probes at start: rpcd and uhttpd
// may come up after netryx at boot, so one refusal is not an answer.
var probeWaits = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// probeRPC asks rpcd (a login with empty credentials, never a password),
// again after each of probeWaits while it does not answer.
func probeRPC(rpc auth.RPC, sleep func(time.Duration)) error {
	var err error
	for i := 0; ; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = auth.Probe(ctx, rpc)
		cancel()
		if !errors.Is(err, auth.ErrNoRPC) || i == len(probeWaits) {
			return err
		}
		sleep(probeWaits[i])
	}
}

// decideListen probes rpcd when the console is to listen on the house side,
// and falls back to loopback if it never answers. A loopback listener needs
// no probe to decide.
func decideListen(addr string, rpc auth.RPC, sleep func(time.Duration)) string {
	if loopbackListen(addr) {
		return addr
	}
	err := probeRPC(rpc, sleep)
	got := listenAddr(addr, err)
	if got != addr {
		log.Printf("rpcd is not answering (%v): without it nobody can sign in, so the console listens only on %s, not %s, and is read only", err, got, addr)
	}
	return got
}

// loadKeyPair reads a certificate and its key in PEM or in DER, the form
// uhttpd's own pair is written in (px5g selfsigned -der; openssl -outform
// der leaves the key in PEM). tls.X509KeyPair then checks they belong
// together.
func loadKeyPair(certFile, keyFile string) (tls.Certificate, error) {
	cb, err := os.ReadFile(certFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	kb, err := os.ReadFile(keyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	if b, _ := pem.Decode(cb); b == nil {
		if _, err := x509.ParseCertificate(cb); err != nil {
			return tls.Certificate{}, fmt.Errorf("%s is neither PEM nor a DER certificate: %v", certFile, err)
		}
		cb = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cb})
	}
	if b, _ := pem.Decode(kb); b == nil {
		typ := ""
		if _, err := x509.ParsePKCS8PrivateKey(kb); err == nil {
			typ = "PRIVATE KEY"
		} else if _, err := x509.ParseECPrivateKey(kb); err == nil {
			typ = "EC PRIVATE KEY"
		} else if _, err := x509.ParsePKCS1PrivateKey(kb); err == nil {
			typ = "RSA PRIVATE KEY"
		} else {
			return tls.Certificate{}, fmt.Errorf("%s is neither PEM nor a DER private key", keyFile)
		}
		kb = pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: kb})
	}
	return tls.X509KeyPair(cb, kb)
}

// tlsFor is the TLS the console is served with: on a house-side address,
// with uhttpd's certificate and key when both files exist. Otherwise nil,
// and the console is plain http, as LuCI over http is; the login form says
// the password then crosses the house network unencrypted.
func tlsFor(listen, certFile, keyFile string) *tls.Config {
	if loopbackListen(listen) {
		return nil
	}
	if _, err := os.Stat(certFile); err != nil {
		return nil
	}
	if _, err := os.Stat(keyFile); err != nil {
		return nil
	}
	c, err := loadKeyPair(certFile, keyFile)
	if err != nil {
		log.Printf("certificate %s with key %s cannot be read (%v): the console runs unencrypted", certFile, keyFile, err)
		return nil
	}
	return &tls.Config{Certificates: []tls.Certificate{c}, MinVersion: tls.VersionTLS12}
}

// newGate is the login gate serve puts in front of the console: the house
// is the console's own networks, the same answer the flow table gets, for
// the login rate limit.
func (c *console) newGate(rpc auth.RPC, token string) *auth.Gate {
	zone := "UTC"
	// "Local" is the process's own zone, which means nothing to a browser.
	if c.o.Loc != nil && c.o.Loc.String() != "Local" {
		zone = c.o.Loc.String()
	}
	return &auth.Gate{
		Zone: zone,
		RPC:  rpc, Now: time.Now, Secret: auth.NewSecret(), MetricsToken: token,
		Logf: log.Printf, IsLocal: c.local.IsLocal,
	}
}

// reprobeEvery is how often rpcd is asked again while it is gone.
const reprobeEvery = 30 * time.Second

// armListen decides where serve listens and arms the gate for it, before
// the handler is built. A house-side address waits for the rpcd probe:
// without rpcd it gives way to loopback, and the gate lets loopback read
// without a session (nobody can log in) and refuses every change. A
// loopback address is served at once and the probe runs in the background.
// While rpcd is gone it is asked again every reprobeEvery, and its first
// answer closes the loopback door (rpcd started late at boot, or uhttpd
// restarted in an upgrade); a listener that gave way to loopback stays
// there until netryx restarts. The asking ends with stop. The Host check is
// told the address actually listened on.
func (c *console) armListen(addr string, g *auth.Gate, sleep func(time.Duration), stop <-chan struct{}) string {
	if loopbackListen(addr) {
		c.o.Listen = addr
		go func() {
			if err := probeRPC(g.RPC, sleep); errors.Is(err, auth.ErrNoRPC) {
				g.SetNoRPC(true)
				log.Printf("rpcd is not answering (%v): nobody can sign in, the console on %s is read only, no changes", err, addr)
				c.reprobe(g, stop)
			}
		}()
		return addr
	}
	listen := decideListen(addr, g.RPC, sleep)
	if listen != addr {
		g.SetNoRPC(true)
		go c.reprobe(g, stop)
	}
	c.o.Listen = listen
	return listen
}

// reprobe asks rpcd every reprobeEvery while the gate says it is gone, and
// clears that on the first JSON-RPC answer.
func (c *console) reprobe(g *auth.Gate, stop <-chan struct{}) {
	for g.NoRPC() {
		select {
		case <-stop:
			return
		case <-c.after(reprobeEvery):
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := auth.Probe(ctx, g.RPC)
		cancel()
		if !errors.Is(err, auth.ErrNoRPC) {
			g.SetNoRPC(false)
			log.Print("rpcd answers again: the console is behind the LuCI sign-in again")
		}
	}
}

// houseSeen is what the quarantine sees: the inventory's devices and the
// MACs in the history of the last week (rows that are not a MAC, such as
// other and router, are left out).
func houseSeen(inv interface{ Devices() []inventory.Device }, hist func(from, to time.Time) []string, house func(inventory.Device) bool, now time.Time) []rules.Seen {
	var out []rules.Seen
	seen := map[string]bool{}
	if inv != nil {
		for _, d := range inv.Devices() {
			if house != nil && !house(d) {
				continue
			}
			if !seen[d.MAC] {
				seen[d.MAC] = true
				out = append(out, rules.Seen{MAC: d.MAC, Hostname: d.Hostname, At: d.Last})
			}
		}
	}
	if hist != nil {
		for _, k := range hist(now.Add(-7*24*time.Hour), now) {
			if !isMAC(k) || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, rules.Seen{MAC: k, At: now})
		}
	}
	return out
}

// isMAC is a lower-case colon MAC, the form history keys devices by.
func isMAC(s string) bool {
	if len(s) != 17 {
		return false
	}
	for i := 0; i < 17; i++ {
		c := s[i]
		if i%3 == 2 {
			if c != ':' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// onHardwareSwitch says a bridge port has a switch id: its traffic with the
// other ports of that switch never reaches the CPU.
func onHardwareSwitch(fsys fs.FS, port string) bool {
	if fsys == nil || !validIface(port) {
		return false
	}
	b, err := fs.ReadFile(fsys, "sys/class/net/"+port+"/phys_switch_id")
	return err == nil && len(strings.TrimSpace(string(b))) > 0
}

// readWireless reads /etc/config/wireless; nil when there is none.
func readWireless(fsys fs.FS) rules.WirelessConfig {
	if fsys == nil {
		return nil
	}
	b, err := fs.ReadFile(fsys, "etc/config/wireless")
	if err != nil {
		return nil
	}
	w, err := rules.ParseWireless(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	return w
}

// validIface is a Linux interface name, so it cannot walk out of sysfs.
func validIface(s string) bool {
	if s == "" || len(s) > 15 || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' || c == '@') {
			return false
		}
	}
	return true
}

// tryHandler turns a country rule's try copy into a blocked_country alert.
func tryHandler(country func(netip.Addr) string, raise func(alerts.Alert)) func(rule, mac string, peer net.IP) {
	return func(rule, mac string, peer net.IP) {
		mac = strings.ToLower(mac)
		a, ok := netip.AddrFromSlice(peer)
		if !isMAC(mac) || !ok {
			return
		}
		raise(alerts.Alert{Kind: alerts.BlockedCountry, MAC: mac, Country: country(a.Unmap())})
	}
}

// uploadRound is one hourly upload check. It raises nothing until the engine
// has read the owner's config, since the defaults have uploads on, and nothing
// when the owner turned them off.
func uploadRound(settings func() (rules.Alerts, bool), st *rollup.Store, now time.Time, raise func(mac string, up uint64)) {
	set, read := settings()
	if !read || !set.Upload {
		return
	}
	checkUploads(st, now, raise)
}

// checkUploads raises an upload alert for each device whose last closed hour
// was unusual (alerts.Unusual).
func checkUploads(st *rollup.Store, now time.Time, raise func(mac string, up uint64)) {
	last := now.Truncate(time.Hour).Add(-time.Hour)
	from := last.Add(-7 * 24 * time.Hour)
	for _, r := range st.Top(rollup.Filter{}, "mac", from, now, 512) {
		if !isMAC(r.Key) {
			continue
		}
		var up, weekMax uint64
		var first time.Time
		for _, p := range st.Series(rollup.Filter{MAC: r.Key}, from, last, time.Hour, "") {
			b := p.By[""]
			if b.Up+b.Down > 0 && (first.IsZero() || p.At.Before(first)) {
				first = p.At
			}
			switch {
			case p.At.Equal(last):
				up = b.Up
			case p.At.Before(last) && b.Up > weekMax:
				weekMax = b.Up
			}
		}
		if !first.IsZero() && alerts.Unusual(up, weekMax, last.Sub(first)) {
			raise(r.Key, up)
		}
	}
}

// deviceName is a device's name as the console shows it, "" when it has none.
func (c *console) deviceName(mac string) string {
	if c.inv == nil {
		return ""
	}
	for _, d := range c.inv.Devices() {
		if d.MAC == mac {
			return d.Name
		}
	}
	return ""
}

// inHouse says a device is on the house side: on a house port or Wi-Fi, or
// holding an address of the house (link-local addresses say nothing).
func inHouse(isLocal func(netip.Addr) bool) func(inventory.Device) bool {
	return func(d inventory.Device) bool {
		if d.Link.Kind != "" {
			return true
		}
		for _, list := range [][]string{d.IPv4, d.IPv6} {
			for _, s := range list {
				a, err := netip.ParseAddr(s)
				if err == nil && !a.IsLinkLocalUnicast() && isLocal != nil && isLocal(a) {
					return true
				}
			}
		}
		return false
	}
}

// knownMACs is what the baseline is taken from: the devices seen (inventory
// and the week's history) and the ones labelled in labels.json.
func knownMACs(seen []rules.Seen, labels map[string]inventory.Label) []string {
	set := map[string]bool{}
	for _, s := range seen {
		set[strings.ToLower(s.MAC)] = true
	}
	for mac := range labels {
		if m := strings.ToLower(mac); isMAC(m) {
			set[m] = true
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
