// netryx watches a home network from the router it runs on: which device went
// where, what answered, and what is worth keeping. Phase 1.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"netryx/internal/auth"
	"netryx/internal/collect"
	"netryx/internal/ct"
	"netryx/internal/device"
	"netryx/internal/export"
	"netryx/internal/neigh"
	"netryx/internal/nflog"
	"netryx/internal/preflight"
	"netryx/internal/sni"
)

// sessionsFile keeps the console's sessions across a restart of netryx.
const sessionsFile = "/tmp/netryx/sessions.json"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	if os.Args[1] == "ctl" {
		// The agent's read: its words are exec argv forms, not flags.
		os.Exit(runCtl(ctlSocket, os.Args[2:], os.Stdout, os.Stderr))
	}
	if os.Args[1] == "rpcd" {
		// The ubus object netryx: rpcd runs /usr/libexec/rpcd/netryx, which runs this.
		os.Exit(runRpcd(ctlSocket, os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	f, err := parseArgs(os.Args[1], os.Args[2:], flag.ExitOnError)
	if err != nil {
		fatal(err)
	}
	ctPath, leases, top, addr := f.ctPath, f.leases, f.top, f.addr
	watchIf, watchMode, watchCount, cache := f.watchIf, f.watchMode, f.watchCount, f.cache
	passiveDNS, capture, geoPath, appsPath, ouiPath := f.passiveDNS, f.capture, f.geoPath, f.appsPath, f.ouiPath
	metaPath, fetchIcons, stateDir, wanIface := f.metaPath, f.fetchIcons, f.stateDir, f.wanIface
	syncEvery, pingTarget := f.syncEvery, f.pingTarget

	switch os.Args[1] {
	case "rule":
		// Print what would be installed, without installing it. Somebody whose
		// router this is should be able to read the rule before it runs, and
		// pipe it to nft themselves if they would rather.
		ifs := strings.Split(*watchIf, ",")
		if err := nflog.CheckInterfaces(ifs); err != nil {
			fatal(err)
		}
		o := ruleFor(*watchMode, ifs, *passiveDNS, uint16(*f.group))
		if o.Mode == nflog.Ingress {
			devices, err := nflog.DevicesFor("/sys/class/net", ifs)
			if err != nil {
				fatal(err)
			}
			fmt.Print(nflog.NetdevRuleSet(o, devices))
		} else {
			fmt.Print(nflog.RuleSet(o))
		}
	case "watch":
		// Install the rule, read what the kernel selects, and name each flow.
		// Everything here is temporary on purpose: the table is removed on the
		// way out, so a run leaves the router exactly as it found it.
		rule := ruleFor(*watchMode, strings.Split(*watchIf, ","), *passiveDNS, uint16(*f.group))
		// The socket first: binding the group is what makes copies start
		// arriving, and a rule installed before there is anything listening
		// throws away the first handshakes of the run.
		r, err := nflog.Open(rule.Group, uint32(rule.Snaplen))
		if err != nil {
			fatal(err)
		}
		defer r.Close()
		out, err := nflog.Apply(rule)
		if err != nil {
			fatal(fmt.Errorf("installing the rule: %v: %s", err, out))
		}
		cleanup := func() {
			if o, err := nflog.Remove(rule.Table); err != nil {
				fmt.Fprintf(os.Stderr, "could not remove the rule: %v: %s\n", err, o)
			}
		}
		defer cleanup()
		// Ctrl-C must not leave the table behind: a defer does not run on a
		// signal, and the rule would then keep copying packets to nobody.
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sig
			cleanup()
			os.Exit(130)
		}()
		fmt.Printf("listening to group %d on %s, the first %d packets of each connection\n", rule.Group, *watchIf, rule.FirstPackets)
		reg := neigh.NewRegistry()
		if entries, err := neigh.Dump(); err == nil {
			for _, e := range entries {
				reg.Observe(e, time.Now())
			}
		}
		buf := make([]byte, 1<<20)
		seen := 0
		// ENOBUFS is what the kernel says when copies arrived faster than they
		// were read, and it is the likeliest error on a busy minute. It must
		// not be fatal: fatal() is os.Exit, os.Exit runs no defers, and the
		// process would leave the table installed, copying packets to a group
		// nobody reads. That is the exact leftover this watcher exists to
		// avoid. Losing copies is not losing the run.
		lost := 0
		for seen < *watchCount {
			pkts, err := r.Receive(buf)
			if nflog.Recoverable(err) {
				lost++
				fmt.Fprintf(os.Stderr, "the kernel lost copies (%d times), reading on\n", lost)
				continue
			}
			if err != nil {
				// Any other error ends the run through the cleanup path, never
				// through os.Exit.
				cleanup()
				fatal(err)
			}
			for _, p := range pkts {
				payload := nflog.TransportPayload(p.Payload)
				name := sni.FromTLS(payload)
				res := sni.Inspect(payload)
				dev := p.Device()
				who := dev.String()
				// At ingress there is no connection attached, but the frame
				// carries its own source MAC, which is the better identity
				// anyway: it does not rotate the way an IPv6 address does.
				mac := p.HWAddr
				if mac == "" {
					mac = reg.MACOf(dev)
				}
				if mac != "" {
					who = mac + " " + who
				}
				switch {
				case res.ECH:
					fmt.Printf("%-46s -> hidden by ECH (outer %s)\n", who, res.Name)
				case name != "":
					fmt.Printf("%-46s -> %s\n", who, name)
				default:
					continue // not a handshake we can read; the counters have it
				}
				seen++
			}
		}
	case "check":
		// What a person runs when the dashboard is empty. Every question here
		// is asked of the running kernel, and the ruleset one is asked with a
		// dry run, so the check cannot pass on a router where the watcher
		// would then fail.
		_, ctErr := ct.Dump()
		_, nErr := neigh.Dump()
		ifs := strings.Split(*watchIf, ",")
		ruleErr := nflog.Validate(ruleFor(*watchMode, ifs, *passiveDNS, uint16(*f.group)))
		off, offErr := preflight.ReadOffload(offloadConfig)
		lan, lanErr := preflight.LANDevices(offloadConfig, networkConfig)
		fmt.Printf("%-8s flow offloading: %s\n", "info", off)
		rep := preflight.Check(
			preflight.Explain("connection tracking over netlink", "nf_conntrack_netlink", "kmod-nf-conntrack-netlink", ctErr),
			preflight.Explain("the neighbour table", "rtnetlink", "built in", nErr),
			preflight.RuleNeed(*watchMode, *watchIf, ruleErr),
			preflight.ModeNeed(*watchMode, off, offErr),
			preflight.SideNeed(ifs, lan, lanErr),
		)
		for _, n := range rep.Needs {
			mark := "ok"
			if !n.OK {
				mark = "MISSING"
			}
			fmt.Printf("%-8s %s\n", mark, n.What)
		}
		if err := rep.Error(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("all in place")
	case "flows":
		// The same question as "devices", asked of netlink instead of the text
		// file: exact tuples, per-direction counters, and the offload status
		// that says whether those counters can be trusted to the byte.
		events, err := ct.Dump()
		if err != nil {
			fatal(err)
		}
		var nated, offloaded int
		fmt.Printf("%-24s %-24s %-9s %10s %10s  %s\n", "from (before NAT)", "to", "port", "up", "down", "state")
		for i, e := range events {
			if e.NATed() {
				nated++
			}
			if e.Offloaded() {
				offloaded++
			}
			if i >= *top {
				continue
			}
			state := ""
			if e.NATed() {
				state += "NAT "
			}
			if e.Offloaded() {
				state += "offloaded(counters approximate) "
			}
			if e.Assured() {
				state += "assured"
			}
			fmt.Printf("%-24s %-24s %-9s %10d %10d  %s\n",
				e.Orig.SrcIP, e.Orig.DstIP, fmt.Sprintf("%d/%d", e.Orig.Proto, e.Orig.DstPort),
				e.Up.Bytes, e.Down.Bytes, state)
		}
		fmt.Printf("\n%d flows, %d behind NAT, %d offloaded\n", len(events), nated, offloaded)
	case "neighbours":
		// The join that makes a rotating IPv6 address one device.
		entries, err := neigh.Dump()
		if err != nil {
			fatal(err)
		}
		reg := neigh.NewRegistry()
		now := time.Now()
		for _, e := range entries {
			reg.Observe(e, now)
		}
		fmt.Printf("%-20s %-8s %s\n", "device (MAC)", "count", "addresses")
		seen := map[string]bool{}
		for _, e := range entries {
			if !e.Usable() || seen[e.MAC] {
				continue
			}
			seen[e.MAC] = true
			addrs := reg.AddressesOf(e.MAC)
			sort.Strings(addrs)
			fmt.Printf("%-20s %-8d %s\n", e.MAC, len(addrs), strings.Join(addrs, " "))
		}
		fmt.Printf("\n%d entries in the kernel, %d devices\n", len(entries), reg.Devices())
	case "devices":
		fh, err := os.Open(*ctPath)
		if err != nil {
			fatal(err)
		}
		defer fh.Close()
		flows, st, err := device.ParseConntrack(fh)
		if err != nil {
			fatal(err)
		}
		if st.AcctOff {
			fmt.Fprintln(os.Stderr, "netryx: the kernel is not counting bytes (nf_conntrack_acct=0); volumes would all read zero")
		}
		var tab *device.Table
		if lf, err := os.Open(*leases); err == nil {
			tab = device.NewTable(device.ParseLeases(lf))
			lf.Close()
		}
		type agg struct {
			name             string
			flows            int
			upPkt, upB       int
			downPkt, downB   int
			nated, offloaded bool
		}
		by := map[string]*agg{}
		for _, f := range flows {
			k := f.SrcIP.String()
			a := by[k]
			if a == nil {
				a = &agg{name: tab.Name(f.SrcIP)}
				if a.name == "" {
					a.name = k
				}
				by[k] = a
			}
			a.flows++
			a.upPkt += f.Up.Packets
			a.upB += f.Up.Bytes
			a.downPkt += f.Down.Packets
			a.downB += f.Down.Bytes
			a.nated = a.nated || f.NATIP != nil
			a.offloaded = a.offloaded || f.Offloaded
		}
		list := make([]string, 0, len(by))
		for k := range by {
			list = append(list, k)
		}
		sort.Slice(list, func(i, j int) bool {
			return by[list[i]].upB+by[list[i]].downB > by[list[j]].upB+by[list[j]].downB
		})
		fmt.Printf("%-40s %-20s %7s %12s %12s  %s\n", "address", "who", "flows", "up", "down", "NAT")
		for i, k := range list {
			if i >= *top {
				break
			}
			a := by[k]
			nat := ""
			if a.nated {
				nat = "yes"
			}
			if a.offloaded {
				nat += " (offloaded, counters approximate)"
			}
			fmt.Printf("%-40s %-20s %7d %12d %12d  %s\n", k, a.name, a.flows, a.upB, a.downB, nat)
		}
		fmt.Printf("\n%d flows, %d devices\n", len(flows), len(by))
	case "serve":
		// The service the package installs. Until now it polled the kernel's
		// connection table and published byte counts, which meant the rule,
		// the log group and the handshake parser were exercised only by a
		// person running `netryx watch` by hand: the installed product never
		// ran the thing this package is for.
		// A reload of the service is a SIGHUP (the init's reload_service).
		// It is caught before anything slow runs, because its default
		// action ends the process; it is acted on once the console exists.
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		col := collect.New()
		ifs := strings.Split(*watchIf, ",")
		stop := make(chan struct{})
		loc, err := time.LoadLocation(*f.zone)
		if err != nil {
			log.Printf("zone %q unknown (%v): rule schedules count in UTC", *f.zone, err)
			loc = time.UTC
		}
		if *stateDir == "" {
			*stateDir = "/etc/netryx"
		}
		// History is a record of what each device did: readable by root only.
		if err := os.MkdirAll(*stateDir, 0o700); err != nil {
			log.Printf("state directory %s: %v; history will not survive a restart", *stateDir, err)
		}
		con, err := buildConsole(Options{
			Geo: *geoPath, Apps: *appsPath, OUI: *ouiPath, State: *stateDir,
			WAN: *wanIface, Leases: *leases, PingTarget: *pingTarget, Listen: *addr,
			LAN: ifs, Sync: *syncEvery, Collector: col,
			Meta: *metaPath, FetchIcons: *fetchIcons,
			RulesConfig: "/etc/config/netryx", Loc: loc,
			Blind:        blindNow(offloadConfig, *capture, *passiveDNS, *watchMode, log.Printf),
			LogGroup:     logGroupFor(*capture, uint16(*f.group)),
			AgentPending: agentPendingPath,
		})
		if err != nil {
			fatal(err)
		}
		var cleanup func()
		if *capture {
			rule := ruleFor(*watchMode, ifs, *passiveDNS, uint16(*f.group))
			r, err := nflog.Open(rule.Group, uint32(rule.Snaplen))
			if err != nil {
				fatal(fmt.Errorf("opening the log group: %w", err))
			}
			out, err := nflog.Apply(rule)
			if err != nil {
				fatal(fmt.Errorf("installing the rule: %v: %s", err, out))
			}
			cleanup = func() {
				if o, err := nflog.Remove(rule.Table); err != nil {
					log.Printf("could not remove the rule: %v: %s", err, o)
				}
			}
			defer cleanup()
			go readInto(r, col, stop)
			if rule.Mode == nflog.Ingress {
				// A netdev chain is attached to devices, and the kernel
				// removes a device's hook without a word when it goes away.
				// Nothing else would ever notice.
				sup := &nflog.Supervisor{
					Want:   func() ([]string, error) { return nflog.WatchedDevices(ifs) },
					Have:   func() ([]string, error) { return nflog.InstalledDevices(rule.Table) },
					Apply:  func() error { _, err := nflog.Apply(rule); return err },
					Report: func(m string) { log.Print(m) },
				}
				go sup.Run(stop, 15*time.Second)
			}
			log.Printf("listening to group %d on %s (%s)", rule.Group, *watchIf, *watchMode)
		}
		// The agent reads through a root-only socket; a failure to open it
		// costs the agent its read and nothing else. It opens here, before
		// con.start begins writing files, because the bind narrows the
		// process-wide umask for an instant.
		closeSock := func() {}
		if cs, err := con.serveSocket(*f.sock); err != nil {
			log.Printf("socket for netryx ctl %s: %v", *f.sock, err)
		} else {
			closeSock = cs
		}
		// On a stop, the history is written first, the open hour included,
		// and then the rule is removed: a run leaves the router as it found
		// it and loses nothing it counted.
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sig
			closeSock()
			close(stop)
			if err := con.shutdown(); err != nil {
				log.Printf("writing history at stop: %v", err)
			}
			if cleanup != nil {
				cleanup()
			}
			os.Exit(0)
		}()
		go func() {
			for range hup {
				log.Print("SIGHUP: reading the rules again")
				con.reloadRules()
			}
		}()
		con.start(stop)
		var mu sync.Mutex
		var last export.Snapshot
		var at time.Time
		snap := func() export.Snapshot {
			mu.Lock()
			defer mu.Unlock()
			if time.Since(at) < *cache {
				return last
			}
			s, err := snapshot(*ctPath, *leases)
			if err != nil {
				// The capture is still worth publishing: a router with
				// accounting off, or no readable connection table, still knows
				// which device asked for what.
				log.Printf("snapshot: %v", err)
				s = export.Snapshot{Taken: time.Now()}
			}
			s.Devices = collect.Merge(s.Devices, col.Devices(), col.MACOf)
			s.LostCopies = int(col.LostCopies())
			last, at = s, time.Now()
			return last
		}
		// Every route, the scrape endpoints included, sits behind the Host
		// check: a page elsewhere must not reach any of it by rebinding a
		// name. Behind it, the LuCI login: every view needs a session.
		// Sessions as long as LuCI's, not rpcd's five minutes.
		rpc := auth.NewRPC(*f.ubusURL, nil, auth.SessionTime(luciSessionTime(os.DirFS("/"))))
		gate := con.newGate(rpc, *f.metricsToken)
		// Sessions outlive a restart of netryx in RAM only: /tmp is gone
		// with a reboot, and rpcd still decides on every request.
		gate.Store = sessionsFile
		listen := con.armListen(*addr, gate, time.Sleep, stop)
		tlsConf := tlsFor(listen, *f.cert, *f.key)
		srv := &http.Server{
			Addr: listen, Handler: con.serveHandler(serveMux(con, snap), gate),
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
			IdleTimeout: 60 * time.Second, TLSConfig: tlsConf,
		}
		if tlsConf != nil {
			log.Printf("netryx on https://%s (console behind the LuCI sign-in, /api/, /metrics, /api/snapshot, /api/zabbix/discovery)", listen)
			fatal(srv.ListenAndServeTLS("", ""))
		}
		if !loopbackListen(listen) {
			log.Printf("no %s or %s: the console on %s is unencrypted, the LuCI password crosses the house network in the clear", *f.cert, *f.key, listen)
		}
		log.Printf("netryx on http://%s (console behind the LuCI sign-in, /api/, /metrics, /api/snapshot, /api/zabbix/discovery)", listen)
		fatal(srv.ListenAndServe())
	default:
		usage()
	}
}

// cliFlags are the command line's settings, shared by every subcommand.
type cliFlags struct {
	ctPath     *string
	leases     *string
	top        *int
	addr       *string
	watchIf    *string
	watchMode  *string
	watchCount *int
	cache      *time.Duration
	passiveDNS *bool
	capture    *bool
	geoPath    *string
	appsPath   *string
	ouiPath    *string
	metaPath   *string
	fetchIcons *bool
	stateDir   *string
	wanIface   *string
	syncEvery  *time.Duration
	pingTarget *string
	group      *uint
	zone       *string
	sock       *string
	// The login: where rpcd answers, uhttpd's certificate and key for a
	// house-side listener, and the token that opens the scrape endpoints
	// from the house side.
	ubusURL      *string
	cert         *string
	key          *string
	metricsToken *string
}

// metricsTokenEnv carries the scrape token from the init to serve.
const metricsTokenEnv = "NETRYX_METRICS_TOKEN"

// parseArgs parses a subcommand's arguments with the one flag set every
// subcommand shares.
func parseArgs(cmd string, args []string, eh flag.ErrorHandling) (*cliFlags, error) {
	fs := flag.NewFlagSet(cmd, eh)
	f := &cliFlags{
		ctPath:       fs.String("conntrack", "/proc/net/nf_conntrack", "the kernel's connection table"),
		leases:       fs.String("leases", "/tmp/dhcp.leases", "DHCP leases, for the names people recognise"),
		top:          fs.Int("top", 15, "how many devices to show"),
		addr:         fs.String("addr", "127.0.0.1:8788", "serve: listen address"),
		watchIf:      fs.String("watch-iface", "br-lan", "watch: which bridges to observe, comma separated"),
		watchMode:    fs.String("mode", "ingress", "watch/rule: where to select packets - ingress (before any flow offload) or forward (needs offloading off, but attaches the connection)"),
		watchCount:   fs.Int("watch-count", 10, "watch: stop after this many named flows"),
		cache:        fs.Duration("cache", 2*time.Second, "serve: how long a snapshot is reused between scrapes"),
		passiveDNS:   fs.Bool("dns", true, "watch/serve/rule: also read the questions devices ask the router's resolver and the answers it gives, which is the only naming that survives Encrypted Client Hello"),
		capture:      fs.Bool("capture", true, "serve: install the rule and read the log group, which is what names the services a device asked for"),
		geoPath:      fs.String("geo", "/usr/share/netryx/ip2asn-combined.tsv.gz", "serve: address ranges to country, ASN and organisation (iptoasn); missing means no countries"),
		appsPath:     fs.String("apps", "/usr/share/netryx/appmap.tsv", "serve: domain and organisation rules to apps (tools/appmap); missing means every app is Unknown"),
		ouiPath:      fs.String("oui", "/usr/share/netryx/oui.txt", "serve: the IEEE list of hardware vendors; missing means no vendor names"),
		metaPath:     fs.String("meta", "/usr/share/netryx/apps-meta.json", "serve: each app's icon source and brand colour (tools/appmap -meta); missing means no favicon is fetched"),
		fetchIcons:   fs.Bool("fetch-icons", true, "serve: let the router fetch, once, the favicons of brands with no freely licensed logo, from each brand's own site; false contacts no brand"),
		stateDir:     fs.String("state", "/etc/netryx", "serve: where history and labels.json are kept"),
		wanIface:     fs.String("wan", "wan", "serve: the ubus name of the internet interface"),
		syncEvery:    fs.Duration("sync", 5*time.Second, "serve: how often the connection table is dumped to read its counters"),
		group:        fs.Uint("group", 7, "watch/serve/rule: the netfilter log group the first packets are copied to; change it when another program (ulogd, an IDS) already reads 7"),
		pingTarget:   fs.String("ping", "", "serve: an internet address or host probed every 10 s for latency, jitter and loss; empty probes the line's own gateway"),
		zone:         fs.String("zone", "UTC", "serve: the router's time zone (system.@system[0].zonename), in which rule schedules are written"),
		sock:         fs.String("sock", ctlSocket, "serve: the root-only unix socket that netryx ctl reads the rules and devices through"),
		ubusURL:      fs.String("ubus-url", "http://127.0.0.1/ubus", "serve: uhttpd's JSON-RPC endpoint, where rpcd checks the LuCI login; asked on loopback only"),
		cert:         fs.String("cert", "/etc/uhttpd.crt", "serve: the certificate (DER or PEM) the console serves https with on a house-side address; uhttpd's own by default"),
		key:          fs.String("key", "/etc/uhttpd.key", "serve: the certificate's private key (DER or PEM)"),
		metricsToken: fs.String("metrics-token", "", "serve: a bearer token that opens /metrics and the Zabbix routes from the house side; empty, they answer on loopback only. The service takes it from $"+metricsTokenEnv+" instead, which ps does not show"),
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	// The init hands the scrape token over in the environment, which only
	// root can read, never on the command line, which every user's ps
	// shows. A flag given by hand wins. The variable is cleared either way,
	// so ubus, ping and nft never inherit it.
	if v, ok := os.LookupEnv(metricsTokenEnv); ok {
		given := false
		fs.Visit(func(fl *flag.Flag) { given = given || fl.Name == "metrics-token" })
		if !given {
			*f.metricsToken = v
		}
		os.Unsetenv(metricsTokenEnv)
	}
	if *f.group > 65535 {
		return nil, fmt.Errorf("%s: -group %d is not a log group: 0 to 65535", cmd, *f.group)
	}
	// The flag package stops at the first word that is not a flag, so a
	// boolean written "-capture false" would leave every option after it at
	// its default without a word. The service refuses that instead.
	if cmd == "serve" && fs.NArg() > 0 {
		return nil, fmt.Errorf("serve: words left over on the command line, from %q: %q (write -name=value)", fs.Arg(0), fs.Args())
	}
	return f, nil
}

// readInto is the service's capture loop. A lost-copies error is survived,
// and anything else ends the loop rather than the process: the HTTP side keeps
// serving what was already learned, and the deferred cleanup still runs.
// receiver is the part of nflog.Reader this loop uses. It is an interface for
// one reason: the loop counts lost copies and decides whether losses are
// visible at all, and neither can be exercised on a machine that has no
// netlink socket. The production call passes *nflog.Reader unchanged.
type receiver interface {
	Receive(buf []byte) ([]nflog.Packet, error)
}

func readInto(r receiver, col *collect.Collector, stop <-chan struct{}) {
	buf := make([]byte, 1<<20)
	lost := 0
	var gap nflog.Gap
	said := false
	for {
		select {
		case <-stop:
			return
		default:
		}
		pkts, err := r.Receive(buf)
		if nflog.Recoverable(err) {
			lost++
			log.Printf("the kernel lost copies (%d)", lost)
			continue
		}
		if err != nil {
			log.Printf("reading the group stopped: %v", err)
			return
		}
		now := time.Now()
		for _, p := range pkts {
			if !said {
				// Whether losses can be seen at all is a property of the
				// kernel, and an operator should not have to guess which of
				// the two "zero losses" they are looking at.
				if p.HasSeq {
					log.Print("the kernel numbers copies, so losses will be seen")
				} else {
					log.Print("the kernel does not number copies: losses will not be counted")
				}
				said = true
			}
			col.Lost(gap.Saw(p.Seq, p.HasSeq))
			col.Observe(p, now)
		}
	}
}

// snapshot reads the kernel tables once and folds them into what the exporters
// publish: a row per device, never a row per flow.
func snapshot(ctPath, leasePath string) (export.Snapshot, error) {
	fh, err := os.Open(ctPath)
	if err != nil {
		return export.Snapshot{}, err
	}
	defer fh.Close()
	flows, st, err := device.ParseConntrack(fh)
	if err != nil {
		return export.Snapshot{}, err
	}
	var tab *device.Table
	if lf, err := os.Open(leasePath); err == nil {
		tab = device.NewTable(device.ParseLeases(lf))
		lf.Close()
	}
	by := map[string]*export.Device{}
	for _, f := range flows {
		k := f.SrcIP.String()
		d := by[k]
		if d == nil {
			d = &export.Device{Name: tab.Name(f.SrcIP), Addr: k}
			if d.Name == "" {
				d.Name = k
			}
			by[k] = d
		}
		d.Flows++
		d.UpPackets += f.Up.Packets
		d.UpBytes += f.Up.Bytes
		d.DownPackets += f.Down.Packets
		d.DownBytes += f.Down.Bytes
		d.NATed = d.NATed || f.NATIP != nil
		d.Offloaded = d.Offloaded || f.Offloaded
	}
	s := export.Snapshot{Taken: time.Now(), Flows: len(flows), AcctOff: st.AcctOff}
	for _, d := range by {
		s.Devices = append(s.Devices, *d)
	}
	return s, nil
}

// ruleFor turns the mode flag into options, refusing anything else rather than
// quietly picking a default that watches the wrong place.
func ruleFor(mode string, ifaces []string, dns bool, group uint16) nflog.RuleOptions {
	o, err := ruleOptions(mode, ifaces, dns)
	if err != nil {
		fatal(err)
	}
	o.Group = group
	return o
}

// blindReason is why the rules engine cannot see names, "" when it can: every
// cause there is, each with what to do, in the config's own option names.
func blindReason(capture, passiveDNS bool, mode string, o preflight.Offload) string {
	var why []string
	if !capture {
		why = append(why, "capture is off (set capture to 1)")
	}
	if !passiveDNS {
		why = append(why, "passive_dns is off (set passive_dns to 1)")
	}
	if mode == "forward" && o.On() {
		why = append(why, "forward mode under flow offloading (set mode to ingress, or turn flow offloading off)")
	}
	if len(why) == 0 {
		return ""
	}
	return "names cannot be seen: " + strings.Join(why, "; ")
}

// blindNow answers the rules engine why it cannot see names ("" when it
// can), reading fw4's offload setting again each time it is asked (every
// reload and tick), since `fw4 reload` can turn offloading on under the
// running service. A setting that cannot be read is taken as off, and said
// once per new error.
func blindNow(path string, capture, passiveDNS bool, mode string, logf func(string, ...any)) func() string {
	var mu sync.Mutex
	last := ""
	return func() string {
		o, err := preflight.ReadOffload(path)
		mu.Lock()
		if err != nil && err.Error() != last {
			logf("offload setting could not be read (%v): taking it as off", err)
		}
		last = ""
		if err != nil {
			last = err.Error()
		}
		mu.Unlock()
		return blindReason(capture, passiveDNS, mode, o)
	}
}

// agentPendingPath is where openwrt-mcp keeps the applies it waits to have
// confirmed (its default state directory).
const agentPendingPath = "/etc/openwrt-mcp/pending.json"

// ruleOptions refuses a mode it does not know rather than quietly picking a
// default that watches the wrong place. An unrecognised word must not become
// a working configuration: that is how a monitor ends up watching the NATed
// side and reporting a silent network.
func ruleOptions(mode string, ifaces []string, dns bool) (nflog.RuleOptions, error) {
	o := nflog.DefaultRule(ifaces...)
	o.DNS = dns
	switch mode {
	case "ingress":
		o.Mode = nflog.Ingress
	case "forward":
		o.Mode = nflog.Forward
	default:
		return nflog.RuleOptions{}, fmt.Errorf("unknown mode %q: ingress or forward", mode)
	}
	return o, nil
}

// offloadConfig is where fw4 keeps the setting that decides whether the
// forward hook can see anything. A constant rather than a flag: it is not a
// choice, it is where the file is.
const (
	offloadConfig = "/etc/config/firewall"
	networkConfig = "/etc/config/network"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: netryx check|devices|flows|neighbours|rule|watch|serve|ctl [-conntrack file] [-leases file] [-addr host:port]")
	os.Exit(2)
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "netryx:", err)
		os.Exit(1)
	}
}

// logGroupFor is the nflog group the capture reads, or 0 when it reads none.
func logGroupFor(capture bool, group uint16) uint16 {
	if !capture {
		return 0
	}
	return group
}
