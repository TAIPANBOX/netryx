package rules

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"netryx/internal/ct"
	"netryx/internal/enrich"
	"netryx/internal/flows"
	"netryx/internal/uciconf"
)

// Runner runs a command with extra environment and standard input and returns
// its standard output; an error carries what the command said on stderr, so
// nft's own message reaches the console.
type Runner func(ctx context.Context, env []string, stdin string, name string, args ...string) ([]byte, error)

// EngineOptions is what the engine is built from. Every outside source is a
// field, so the engine runs off a router.
type EngineOptions struct {
	// ConfigPath is /etc/config/netryx; StateDir holds rules.nft and sets.nft.
	ConfigPath, StateDir string
	Run                  Runner
	Now                  func() time.Time
	// Loc is the router's zone, in which schedules are written.
	Loc  *time.Location
	Geo  *enrich.GeoDB
	Apps *enrich.AppMap
	// Live lists the flow table's live flows; Delete ends a connection by its
	// original tuple and DeleteReply by its reply tuple (ct.Delete and
	// ct.DeleteReply on the router).
	Live        func() []flows.Flow
	Delete      func(ct.Tuple) error
	DeleteReply func(ct.Tuple) error
	// KernelEast is the kernel's offset for meta day (nil: rules.KernelEast);
	// Elapsed is a monotonic clock (nil: time since the engine was built).
	KernelEast func() (int, error)
	Elapsed    func() time.Duration
	Logf       func(string, ...any)
	// IsLocal says whether an address is the house's own (its networks and
	// the router's addresses); such an address never enters an app's set.
	IsLocal func(netip.Addr) bool
	// Blind says why the router cannot see names now, "" when it can (no
	// capture, no passive DNS, or forward mode with flow offloading): an app
	// or category rule that only names can fill is then shown as not applied,
	// with that reason as its problem (R41). It is asked again at every
	// reload and tick, since flow offloading can be turned on under a running
	// service (nil: names are seen).
	Blind func() string
	// Usage counts minutes for the time limits (nil: no limits are counted);
	// SafeDNS keeps the safe-search resolver; HouseIfaces is the house-side
	// devices DNS redirection is limited to.
	Usage       *Usage
	SafeDNS     SafeResolver
	HouseIfaces func() []string
	// HousePrefixes are the networks the router serves, kept from isolated
	// and quarantined devices; nil or empty means the private ranges.
	HousePrefixes func() []netip.Prefix
	// Roster is the quarantine's memory (spec 6.8), Seen the devices the
	// router sees on the house side now, and Event what tells the owner
	// (new_device, baseline_rebuilt, quarantine_full).
	Roster *Roster
	Seen   func() []Seen
	Event  func(Event)
	// Known is what the baseline is taken from when it differs from Seen:
	// the house's devices and the week's history (nil: Seen).
	Known func() []string
	// LogGroup is the capture's nflog group while it reads one: a country
	// rule copies tries there for the alerts (0: none).
	LogGroup uint16
}

// safeEvery is how often the safe resolver is looked at, by its own goroutine:
// a restart and its probes take seconds, and none of that may hold the engine.
const safeEvery = 15 * time.Second

// SafeResolver keeps the safe-search resolver (internal/safedns): with want it
// writes and starts it and says whether it answers, without it stops it.
type SafeResolver interface {
	Ensure(want bool, youtube string) (healthy bool, problem string)
}

// LimitUse is one limit rule for one device today: the sum of its apps'
// minutes, which is what blocks, and the apps that were used.
type LimitUse struct {
	Rule, What  string
	Targets     []string // the rule's targets, for naming it
	Used, Limit int
	SpentUntil  time.Time    // zero while minutes remain
	Apps        []AppMinutes // apps of the rule with minutes today, most used first
}

// AppMinutes is one app's counted minutes today.
type AppMinutes struct {
	App  string
	Used int
}

// RuleState is a rule as the console shows it: whether its window covers now,
// what its counter says, and whether the kernel holds it.
type RuleState struct {
	Rule
	ActiveNow bool
	Hits      uint64
	Applied   bool
	Problem   string
	// Allows are the live allowances that lift this rule now (R42).
	Allows []LiveAllow
	// Used is, for a limit rule, today's minutes per device.
	Used map[string]int
}

// LiveAllow is an allowance as the console and the agent read it: whose, until
// when, and by whom ("agent" for an agent_ section, else "owner").
type LiveAllow struct {
	ID, Member string
	Until      time.Time
	By         string
}

// Problem texts the console shows.
const (
	noGeoText   = "no address table"
	noAppsText  = "no app map"
	waitingText = "another rule is waiting for a data file"
)

const (
	// appTimeout is how long an address from an answer stays in an app's set
	// (the set's own timeout); an answer refreshes it at most once an hour.
	appTimeout = "24h"
	refreshAge = time.Hour
	setSize    = 4096 // the app sets' size in compile.go
	// maxAnswered bounds the memory of resolver answers, and an answer
	// vouches for an address as long as the set would keep it.
	maxAnswered = 8192
	evidenceAge = 24 * time.Hour
	// cutQueue bounds the deletes waiting for the worker.
	cutQueue = 256
	// jump is how far the wall clock may move against the monotonic clock
	// between ticks before the schedules are compiled again.
	jump = 10 * time.Minute
	// retry is when a ruleset nft refused is tried again without a trigger.
	retry = 5 * time.Minute
)

// sharedDomains front unrelated services from the same addresses, so their
// addresses never enter a set: blocking YouTube must not take Google search
// with it (spec 6.2). A handshake naming one is cut, never added.
var sharedDomains = []string{
	"youtube.com", "youtu.be", "ytimg.com", "ggpht.com", "google.com", "gstatic.com",
	"googleapis.com", "googleusercontent.com", "facebook.com", "instagram.com",
	"cdninstagram.com", "fbcdn.net",
}

func shared(name string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	for _, d := range sharedDomains {
		if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

// cgnat is shared address space (RFC 6598): a carrier's side of the line,
// never an app's server.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// destination says whether an address can be somewhere an app lives: a
// public unicast address that is not the house's own. A resolver answering
// with a house address (its LAN GUA prefix included), a sinkhole's 0.0.0.0,
// a link-local or a shared-space one must not block the house.
func (e *Engine) destination(a netip.Addr) bool {
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsPrivate() || cgnat.Contains(a) {
		return false
	}
	return e.o.IsLocal == nil || !e.o.IsLocal(a)
}

// flowKey names a live flow between ticks.
type flowKey struct {
	id            uint32
	local, remote netip.AddrPort
	proto         uint8
}

// Engine keeps inet netryx_rules equal to the rules in the config: it applies
// the compiled script in one transaction, feeds the app sets from DNS answers
// and handshakes, cuts the connections a rule starts to cover, and saves the
// sets for the next boot.
type Engine struct {
	o   EngineOptions
	cat *catalog

	// applyMu lets one nft change run at a time: a reload, a feed, a prune
	// or a save. mu guards the fields below and is held only briefly, so the
	// capture loop that calls Answer and Named never waits on nft.
	applyMu sync.Mutex
	mu      sync.Mutex

	shown     Config // the config last read, as the console shows it
	inForce   Config // the config the kernel holds, which cuts follow
	appliedOK bool
	read      bool   // a config has been read at least once
	problem   string // nft's message when the last apply was refused
	// missing names the rules that need a data file this process does not
	// have; while it is not empty the apply is refused (R38).
	missing map[string]string
	// cutSig is what each app or category rule blocked at the last apply,
	// so a change empties its cut sets.
	cutSig map[string]string
	// applied names the rules whose counters this engine knows in the table;
	// nil until the first reload reads the table.
	applied   map[string]bool
	ccDone    map[string]bool // country sets filled in this table
	lastDelta string
	fileSum   [32]byte
	fileRead  bool
	apps      map[string]string // app id -> set base name, for the sets the rules declare
	declared  map[string]bool   // what the applied script declared ("set app_x_v4", ...)
	next      time.Time
	east      int
	eastSet   bool

	// blind is what Blind said at the last look.
	blind string

	ticked   bool
	lastWall time.Time
	lastMono time.Duration
	covered  map[flowKey]bool

	// answered is what the router's resolver said, address -> app, newest
	// last and bounded: the evidence a handshake's destination needs before
	// it may enter a set, since the device chooses the name it sends.
	answered map[netip.Addr]*list.Element
	ansOrder *list.List

	// everApplied: this process applied the rules at least once, so it
	// knows which sets are the rules' and may save them.
	everApplied bool

	// spent is, per limit rule, the members whose minutes are used up and
	// until when (the next local midnight); the kernel holds it in spent_<id>.
	spent       map[string]map[string]time.Time
	usageLoaded bool
	// houseSig is the house networks the rules in force were compiled with,
	// quarantineSig the approved set and whether the quarantine held.
	houseSig      string
	quarantineSig string
	// quarantineOn says the rules in force hold by the quarantine lines,
	// isoHeld the devices held by name through a rebuilt baseline's grace.
	quarantineOn bool
	isoHeld      []string
	// baselineErr is the last baseline error logged.
	baselineErr string
	// safeOK says the safe-search resolver answered at the last look, safeAt
	// when that was; safeProblem why not.
	safeOK      bool
	safeProblem string
	safeAt      time.Time
	// safeWake asks the safe-resolver watcher to look now (a reload may have
	// changed what it should run).
	safeWake chan struct{}

	// cuts carries Named's deletes to one worker, so the capture loop never
	// waits on the kernel; cutWG counts the ones not yet made, cutsDropped
	// the ones refused because the queue was full.
	cuts        chan cutReq
	cutOnce     sync.Once
	cutWG       sync.WaitGroup
	cutsDropped int

	pending map[string]map[string]bool      // set -> elements to add
	elems   map[string]map[string]time.Time // set -> element -> last sent
	dirty   bool
	dropped int
}

// NewEngine builds an engine; Reload (or Run) applies the rules.
func NewEngine(o EngineOptions) *Engine {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Loc == nil {
		o.Loc = time.UTC
	}
	if o.KernelEast == nil {
		o.KernelEast = KernelEast
	}
	if o.Elapsed == nil {
		start := time.Now()
		o.Elapsed = func() time.Duration { return time.Since(start) }
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	cat := newCatalog(o.Geo, o.Apps)
	cat.ifaces = o.HouseIfaces
	cat.house = o.HousePrefixes
	return &Engine{o: o, cat: cat, apps: map[string]string{}, cuts: make(chan cutReq, cutQueue),
		safeWake: make(chan struct{}, 1)}
}

// lookBlind asks Blind whether names can be seen now: at every reload and
// tick, so a change of the firewall under the running service shows on the
// rules within one tick.
func (e *Engine) lookBlind() {
	if e.o.Blind == nil {
		return
	}
	b := e.o.Blind()
	e.mu.Lock()
	e.blind = b
	e.mu.Unlock()
}

func (e *Engine) run(env []string, stdin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return e.o.Run(ctx, env, stdin, "nft", args...)
}

// Run applies the rules, then ticks every 15 s, sends the queued set elements
// every second and saves the sets hourly while they changed, until stop.
func (e *Engine) Run(stop <-chan struct{}) {
	// The first apply does not wait for the safe resolver: until it answers,
	// filtered devices stay on the main resolver, as with the boot rules, and
	// the watcher reloads once it does.
	e.TakeBaseline()
	if err := e.Reload(); err != nil {
		e.o.Logf("rules not applied: %v", err)
	}
	if e.o.SafeDNS != nil {
		go e.watchSafe(stop)
	}
	tick := time.NewTicker(15 * time.Second)
	flush := time.NewTicker(time.Second)
	save := time.NewTicker(time.Hour)
	defer tick.Stop()
	defer flush.Stop()
	defer save.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			e.Tick(e.o.Now())
		case <-flush.C:
			if err := e.Flush(); err != nil {
				e.o.Logf("app addresses not added: %v", err)
			}
		case <-save.C:
			e.mu.Lock()
			dirty := e.dirty
			e.mu.Unlock()
			if dirty {
				if err := e.SaveSets(); err != nil {
					e.o.Logf("app sets not saved: %v", err)
				}
			}
		}
	}
}

// Reload reads the config, compiles it and applies it in one nft transaction
// loaded with TZ=UTC. On a refusal the kernel keeps the previous ruleset, so
// does rules.nft, and every rule says it is not applied, with nft's message.
// A reload with nothing changed runs no transaction. After it, connections
// the new state covers are cut.
func (e *Engine) Reload() error {
	err := e.reload()
	e.cut(e.o.Now())
	return err
}

func (e *Engine) reload() error {
	e.lookBlind()
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	now := e.o.Now()
	east, eerr := e.o.KernelEast()
	e.mu.Lock()
	if !e.ticked {
		// The first tick compares against this reload, so a clock NTP
		// moves between the start and that tick is caught.
		e.ticked, e.lastWall, e.lastMono = true, now, e.o.Elapsed()
	}
	if eerr != nil {
		e.o.Logf("the kernel's time offset could not be read (%v): rule days use the previous one", eerr)
		east = e.east
	}
	e.mu.Unlock()

	b, err := os.ReadFile(e.o.ConfigPath)
	var secs []uciconf.Section
	if err == nil {
		secs, err = uciconf.Parse(bytes.NewReader(b))
	}
	if err != nil {
		// Without a readable config nothing is known; the kernel keeps
		// what it has, which is the safe side.
		e.mu.Lock()
		e.problem = err.Error()
		e.appliedOK = false
		e.next = now.Add(retry)
		e.mu.Unlock()
		return fmt.Errorf("rules config: %w", err)
	}
	cfg := FromSections(secs, e.cat.lenient(), now)
	e.track(cfg, now)
	if missing := e.needsMissing(cfg, now); len(missing) > 0 {
		// A data file is missing (R38): applying would flush rules_fwd
		// without these rules and prune their sets, so a restart would open
		// what was blocked. The kernel and rules.nft keep what they have.
		e.mu.Lock()
		e.shown, e.read = cfg, true
		e.appliedOK = false
		e.problem = waitingText
		e.missing = missing
		e.next = now.Add(retry)
		e.mu.Unlock()
		var why []string
		for id, w := range missing {
			why = append(why, id+": "+w)
		}
		sort.Strings(why)
		return fmt.Errorf("rules are waiting for data files: %s", strings.Join(why, "; "))
	}

	present, hits := e.counters()
	e.mu.Lock()
	if !present {
		// No table (a first start, or someone removed it): every set is
		// written again and every counter is new.
		e.ccDone = map[string]bool{}
		e.lastDelta = ""
		e.applied = map[string]bool{}
	}
	if e.applied == nil {
		// The table from before this process: its rules keep their hits.
		e.applied = map[string]bool{}
		for n := range hits {
			if id, ok := strings.CutPrefix(n, "hits_"); ok {
				e.applied[id] = true
			}
		}
	}
	if e.ccDone == nil {
		e.ccDone = map[string]bool{}
	}
	reset := map[string]bool{}
	fresh := map[string]bool{}
	recut := map[string]bool{}
	sigs := map[string]string{}
	for _, r := range cfg.Rules {
		if !inScript(r, now) {
			continue
		}
		if r.What == "app" || r.What == "category" {
			sig := r.What + "|" + strings.Join(r.Targets, ",") + "|" + r.Who + "|" + strings.Join(r.Members, ",")
			sigs[r.ID] = sig
			if old, ok := e.cutSig[r.ID]; ok && old != sig {
				// What the rule blocks changed: the pairs it cut no longer
				// stand for it.
				recut[r.ID] = true
			}
		}
		if _, inTable := hits["hits_"+r.ID]; inTable && !e.applied[r.ID] {
			// A counter this engine did not put there for this rule: a rule
			// created again under an old name starts from zero.
			reset[r.ID] = true
		}
		if r.What == "country" {
			for _, cc := range r.Targets {
				if !e.ccDone[strings.ToUpper(cc)] {
					fresh[strings.ToUpper(cc)] = true
				}
			}
		}
	}
	last := e.lastDelta
	e.mu.Unlock()

	for id := range reset {
		if _, ok := sigs[id]; ok {
			recut[id] = true
		}
	}
	spent := e.computeSpent(cfg, now)
	e.mu.Lock()
	approved, held, qsig := e.quarantineOf(cfg)
	x := Extra{Spent: spent, SafeDNS: e.safeOK, Isolated: append(isolatedOf(cfg), e.heldInGrace(cfg)...), Approved: approved, Quarantine: held, LogGroup: e.o.LogGroup}
	e.houseSig = houseSig(e.cat)
	e.quarantineSig = qsig
	e.mu.Unlock()
	delta, meta := compile(cfg, e.cat, e.o.Loc, now, east, fresh, reset, recut, x)
	if delta != last || !present {
		if _, err := e.run([]string{"TZ=UTC"}, delta, "-f", "-"); err != nil {
			msg := strings.TrimSpace(err.Error())
			e.mu.Lock()
			e.shown, e.read = cfg, true
			e.appliedOK = false
			e.problem = msg
			e.next = now.Add(retry)
			e.mu.Unlock()
			return fmt.Errorf("nft refused the rules: %s", msg)
		}
	}

	e.mu.Lock()
	e.shown, e.inForce, e.read = cfg, cfg, true
	e.appliedOK, e.problem, e.missing = true, "", nil
	e.cutSig = sigs
	e.applied = map[string]bool{}
	for _, id := range meta.ids {
		e.applied[id] = true
	}
	for _, cc := range meta.ccs {
		e.ccDone[cc] = true
	}
	e.lastDelta = delta
	e.everApplied = true
	e.quarantineOn, e.isoHeld = meta.quarantine, e.heldInGrace(cfg)
	// What the safe resolver should run may have changed: look now.
	select {
	case e.safeWake <- struct{}{}:
	default:
	}
	e.spent = spent
	e.declared = meta.objects
	e.apps = map[string]string{}
	for _, id := range meta.apps {
		e.apps[id] = appName(id)
	}
	// What the engine kept for sets no rule declares any more goes with
	// them (the prune below deletes the sets themselves).
	for set := range e.elems {
		if !meta.objects["set "+set] {
			delete(e.elems, set)
		}
	}
	for set := range e.pending {
		if !meta.objects["set "+set] {
			delete(e.pending, set)
		}
	}
	for cc := range e.ccDone {
		if !meta.objects["set cc_"+strings.ToLower(cc)+"_v4"] {
			delete(e.ccDone, cc)
		}
	}
	e.next = NextRecompile(e.o.Loc, now)
	e.east, e.eastSet = east, true
	e.mu.Unlock()

	// The file the init loads at boot is the whole ruleset, every set
	// written, since the kernel then holds nothing.
	// The safe resolver is forgotten by a reboot (its section lives in
	// uncommitted uci changes), so the boot copy keeps filtered devices on
	// the main resolver until netryx has it answering again.
	boot := x
	boot.SafeDNS = false
	full, _ := compile(cfg, e.cat, e.o.Loc, now, east, nil, nil, nil, boot)
	if err := e.writeRules(full); err != nil {
		e.o.Logf("rules.nft not written (%v): after a reboot the rules wait for netryx", err)
	}
	if err := e.prune(meta); err != nil {
		e.o.Logf("leftovers of deleted rules not removed: %v", err)
	}
	return nil
}

// inScript is the test compile applies to choose the rules it emits.
func inScript(r Rule, at time.Time) bool {
	return r.Enabled && r.Err == "" && ValidID(r.ID) && (r.Until.IsZero() || r.Until.After(at))
}

// needsMissing names the rules the compiler would have to leave out or empty
// for want of a data file: a country rule without the address table, an app
// or category rule without the app map, and one naming an app held by its
// organisation's ranges without the address table.
func (e *Engine) needsMissing(cfg Config, now time.Time) map[string]string {
	noGeo, noApps := e.cat.noGeo(), e.cat.noApps()
	if !noGeo && !noApps {
		return nil
	}
	out := map[string]string{}
	for _, r := range cfg.Rules {
		if !inScript(r, now) {
			continue
		}
		switch r.What {
		case "country":
			if noGeo {
				out[r.ID] = noGeoText
			}
		case "app", "category":
			if noApps {
				out[r.ID] = noAppsText
				continue
			}
			if noGeo {
				for _, a := range e.cat.appsOf(r) {
					if isOrgApp(a) {
						out[r.ID] = noGeoText
					}
				}
			}
		}
	}
	return out
}

// namesOnly says whether some app of an app or category rule has no
// organisation ranges, so only names (answers and handshakes) can fill it.
func (e *Engine) namesOnly(r Rule) bool {
	if r.What != "app" && r.What != "category" {
		return false
	}
	for _, a := range e.cat.appsOf(r) {
		if !isOrgApp(a) {
			return true
		}
		if v4, v6 := e.cat.OrgRanges(a); len(v4)+len(v6) == 0 {
			return true
		}
	}
	return false
}

// writeRules replaces rules.nft by rename when its content changed, so a
// power cut leaves either the old file or the new one.
func (e *Engine) writeRules(s string) error {
	path := filepath.Join(e.o.StateDir, "rules.nft")
	sum := sha256.Sum256([]byte(s))
	e.mu.Lock()
	if !e.fileRead {
		if old, err := os.ReadFile(path); err == nil {
			e.fileSum = sha256.Sum256(old)
		}
		e.fileRead = true
	}
	same := e.fileSum == sum
	e.mu.Unlock()
	if same {
		return nil
	}
	if err := writeFile(path, []byte(s)); err != nil {
		return err
	}
	e.mu.Lock()
	e.fileSum = sum
	e.mu.Unlock()
	return nil
}

func writeFile(path string, b []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// nftObject is the part of an nft JSON object this engine reads.
type nftObject struct {
	Name    string `json:"name"`
	Table   string `json:"table"`
	Family  string `json:"family"`
	Packets uint64 `json:"packets"`
}

// listing reads `nft -j ...` into its objects by kind.
func listing(b []byte) (map[string][]nftObject, error) {
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	out := map[string][]nftObject{}
	for _, item := range doc.Nftables {
		for kind, raw := range item {
			var o nftObject
			if json.Unmarshal(raw, &o) != nil || o.Table != "netryx_rules" || o.Family != "inet" {
				continue
			}
			out[kind] = append(out[kind], o)
		}
	}
	return out, nil
}

// counters reads the table's named counters: whether the table exists, and
// each counter's packets.
func (e *Engine) counters() (bool, map[string]uint64) {
	b, err := e.run(nil, "", "-j", "list", "counters", "table", "inet", "netryx_rules")
	if err != nil {
		return false, nil
	}
	objs, err := listing(b)
	if err != nil {
		return false, nil
	}
	hits := map[string]uint64{}
	for _, c := range objs["counter"] {
		hits[c.Name] = c.Packets
	}
	return true, hits
}

// Hits is every rule's hit counter as the kernel holds it now, by rule id
// (the implicit rules too: iso, f_dot); nil when the table cannot be read.
func (e *Engine) Hits() map[string]uint64 {
	ok, all := e.counters()
	if !ok {
		return nil
	}
	out := map[string]uint64{}
	for n, v := range all {
		if id, ok := strings.CutPrefix(n, "hits_"); ok && id != "" {
			out[id] = v
		}
	}
	return out
}

// prune deletes, in a second small transaction, the chains, sets and counters
// the applied script no longer declares: those of removed rules, and country
// and app sets no rule uses (spec 6.4: they exist while a rule uses them).
// The table and rules_fwd are never touched.
func (e *Engine) prune(m compiled) error {
	b, err := e.run(nil, "", "-t", "-j", "list", "table", "inet", "netryx_rules")
	if err != nil {
		return err
	}
	objs, err := listing(b)
	if err != nil {
		return err
	}
	var chains, sets, counters []string
	for _, c := range objs["chain"] {
		if strings.HasPrefix(c.Name, "r_") && !m.objects["chain "+c.Name] {
			chains = append(chains, c.Name)
		}
	}
	for _, s := range objs["set"] {
		if (strings.HasPrefix(s.Name, "members_") || strings.HasPrefix(s.Name, "allow_") ||
			strings.HasPrefix(s.Name, "cc_") || strings.HasPrefix(s.Name, "app_") ||
			strings.HasPrefix(s.Name, "cut_") || strings.HasPrefix(s.Name, "spent_") ||
			s.Name == "filtered_dns") && !m.objects["set "+s.Name] {
			sets = append(sets, s.Name)
		}
	}
	for _, c := range objs["counter"] {
		if strings.HasPrefix(c.Name, "hits_") && !m.objects["counter "+c.Name] {
			counters = append(counters, c.Name)
		}
	}
	if len(chains)+len(sets)+len(counters) == 0 {
		return nil
	}
	sort.Strings(chains)
	sort.Strings(sets)
	sort.Strings(counters)
	var s strings.Builder
	for _, c := range chains {
		fmt.Fprintf(&s, "flush chain %s %s\ndelete chain %s %s\n", Table, c, Table, c)
	}
	for _, n := range sets {
		fmt.Fprintf(&s, "delete set %s %s\n", Table, n)
	}
	for _, c := range counters {
		fmt.Fprintf(&s, "delete counter %s %s\n", Table, c)
	}
	_, err = e.run([]string{"TZ=UTC"}, s.String(), "-f", "-")
	return err
}

// Known is the catalog the engine validates and compiles with: the countries
// of the address table, the apps and categories of the app map.
func (e *Engine) Known() Known { return e.cat }

// State is every rule read, with its window, hits and whether it is applied.
func (e *Engine) State() []RuleState {
	_, hits := e.counters()
	now := e.o.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]RuleState, 0, len(e.shown.Rules))
	for _, r := range e.shown.Rules {
		s := RuleState{Rule: r, Hits: hits["hits_"+r.ID], Allows: e.liveAllows(r, now)}
		if r.Action == "limit" && e.o.Usage != nil {
			s.Used = e.usedBy(r, e.o.Usage.Snapshot())
		}
		if inScript(r, now) {
			s.ActiveNow = Active(r, e.o.Loc, now)
			switch {
			case e.missing[r.ID] != "":
				s.Problem = "not applied: " + e.missing[r.ID]
			case e.appliedOK && e.blind != "" && e.namesOnly(r):
				s.Problem = e.blind
			case e.appliedOK:
				s.Applied = e.applied[r.ID]
			case e.problem != "":
				s.Problem = "not applied: " + e.problem
			}
		}
		out = append(out, s)
	}
	return out
}

// liveAllows are the allowances in force (what the kernel holds, not a
// config the apply refused) that lift rule r now: its own and the blanket ones, for a device the rule holds; a country
// rule is never lifted. The caller holds mu.
func (e *Engine) liveAllows(r Rule, now time.Time) []LiveAllow {
	if r.What == "country" {
		return nil
	}
	var out []LiveAllow
	for _, a := range e.inForce.Allows {
		if !a.Until.After(now) || (a.Rule != "" && a.Rule != r.ID) || (r.Who != "all" && !contains(r.Members, a.Member)) {
			continue
		}
		by := "owner"
		if AgentOwned(a.ID) {
			by = "agent"
		}
		out = append(out, LiveAllow{ID: a.ID, Member: a.Member, Until: a.Until, By: by})
	}
	return out
}

// Answer takes one answer of the router's resolver: an address of a domain
// the catalog gives to an app that a rule blocks goes into that app's set,
// unless the domain is a shared front.
func (e *Engine) Answer(name string, ip net.IP) {
	a, ok := netip.AddrFromSlice(ip)
	if !ok || shared(name) {
		return
	}
	a = a.Unmap()
	if !e.destination(a) {
		return
	}
	app, ok := e.o.Apps.Classify(name)
	if !ok {
		return
	}
	now := e.o.Now()
	e.mu.Lock()
	e.remember(a, app.ID, now)
	e.queue(app.ID, a, now)
	e.mu.Unlock()
}

type answer struct {
	addr netip.Addr
	app  string
	at   time.Time
}

// remember files a resolver answer as evidence for later handshakes. A
// repeat makes it the newest; a full memory lets the oldest go. The caller
// holds mu.
func (e *Engine) remember(a netip.Addr, app string, now time.Time) {
	if e.answered == nil {
		e.answered, e.ansOrder = map[netip.Addr]*list.Element{}, list.New()
	}
	if el := e.answered[a]; el != nil {
		an := el.Value.(*answer)
		an.app, an.at = app, now
		e.ansOrder.MoveToBack(el)
		return
	}
	for len(e.answered) >= maxAnswered {
		old := e.ansOrder.Front()
		delete(e.answered, old.Value.(*answer).addr)
		e.ansOrder.Remove(old)
	}
	e.answered[a] = e.ansOrder.PushBack(&answer{a, app, now})
}

// vouched says whether something other than the device stands behind a
// handshake's destination: the router's resolver gave that address for a
// dedicated domain of this app within the last day (as long as the set would
// keep it), or it lies in the app's organisation ranges. The caller holds mu.
func (e *Engine) vouched(a netip.Addr, app string, now time.Time) bool {
	if el := e.answered[a]; el != nil {
		an := el.Value.(*answer)
		if an.app == app && now.Sub(an.at) < evidenceAge && !now.Before(an.at) {
			return true
		}
	}
	return e.cat.inOrg(app, a)
}

// Named takes a handshake that named its destination. The device chooses the
// name it sends, so the address goes into the app's set only when something
// else vouches for it (an answer of the router's resolver for this app, or
// the app's organisation ranges) and the name is not a shared front. When
// the device is held by a rule for that app now, the connection is cut
// either way, so the race between an answer and the first connection does
// not let it through.
func (e *Engine) Named(mac string, local, remote netip.AddrPort, proto uint8, name string) {
	mac = strings.ToLower(mac)
	if mac == flows.RouterKey {
		// The router's own connection: the forward chain never sees it,
		// and no rule is about it.
		return
	}
	app, ok := e.o.Apps.Classify(name)
	if !ok {
		return
	}
	now := e.o.Now()
	ra := remote.Addr().Unmap()
	if e.o.IsLocal != nil && e.o.IsLocal(ra) {
		// Both ends at home: the forward chain's rules never see it, so no
		// rule is about it.
		return
	}
	dest := !shared(name) && e.destination(ra)
	e.mu.Lock()
	if dest && e.vouched(ra, app.ID, now) {
		e.queue(app.ID, ra, now)
	}
	r, cover := e.covering(mac, app.ID, "", now)
	if cover {
		e.queueCut(r, mac, ra, proto, local.Port())
	}
	e.mu.Unlock()
	if cover {
		e.cutLater(local, remote, proto)
	}
}

// queueCut puts the connection a rule cut (the device, the remote address,
// the protocol and the device's own port) into that rule's cut set (R40, R43), so the
// flow conntrack would pick up again is rejected by the rule's own lines for
// ten minutes. Only an app or category rule has one: a pause or a country
// rule already rejects it. A device without a hardware address (a routed
// client, an unknown neighbour) has no pair: nft would refuse the whole
// batch for it. The caller holds mu.
func (e *Engine) queueCut(r Rule, mac string, a netip.Addr, proto uint8, port uint16) {
	if r.What != "app" && r.What != "category" || !a.IsValid() || port == 0 {
		return
	}
	var pname string
	switch proto {
	case 6:
		pname = "tcp"
	case 17:
		pname = "udp"
	default:
		return // only TCP and UDP have the port the key needs
	}
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return
	}
	set := CutSet(r.ID, "4")
	if a.Is6() {
		set = CutSet(r.ID, "6")
	}
	if !e.declared["set "+set] {
		return
	}
	if e.pending == nil {
		e.pending = map[string]map[string]bool{}
	}
	if e.pending[set] == nil {
		e.pending[set] = map[string]bool{}
	}
	if len(e.pending[set]) >= cutSize {
		e.dropped++
		return
	}
	e.pending[set][fmt.Sprintf("%s . %s . %s . %d", hw, a, pname, port)] = true
}

type cutReq struct {
	t     ct.Tuple
	reply bool
}

// cutLater hands a delete to the worker; a full queue drops it and counts.
func (e *Engine) cutLater(local, remote netip.AddrPort, proto uint8) {
	t, ok := tupleOf(local, remote, proto)
	if !ok {
		return
	}
	e.cutOnce.Do(func() { go e.cutWorker() })
	e.cutWG.Add(1)
	select {
	case e.cuts <- cutReq{t: t}:
	default:
		e.cutWG.Done()
		e.mu.Lock()
		e.cutsDropped++
		n := e.cutsDropped
		e.mu.Unlock()
		if n == 1 || n%100 == 0 {
			e.o.Logf("the cut queue is full: %d cuts skipped", n)
		}
	}
}

func (e *Engine) cutWorker() {
	for r := range e.cuts {
		e.delTuple(r.t, r.reply)
		e.cutWG.Done()
	}
}

// queue adds an address to the batch for its app's set, when a rule declares
// that set. The caller holds mu.
func (e *Engine) queue(app string, a netip.Addr, now time.Time) {
	base, ok := e.apps[app]
	if !ok {
		return
	}
	set := "app_" + base + "_v4"
	if a.Is6() {
		set = "app_" + base + "_v6"
	}
	if e.elems == nil {
		e.elems = map[string]map[string]time.Time{}
	}
	sent := e.elems[set]
	if sent == nil {
		sent = map[string]time.Time{}
		e.elems[set] = sent
	}
	s := a.String()
	if t, ok := sent[s]; ok && now.Sub(t) < refreshAge && !now.Before(t) {
		return
	}
	if e.pending[set][s] {
		return
	}
	if _, ok := sent[s]; !ok && len(sent)+len(e.pending[set]) >= setSize {
		// The kernel's set holds 4096; what timed out there is let go here.
		for k, t := range sent {
			if now.Sub(t) >= 24*time.Hour {
				delete(sent, k)
			}
		}
		if len(sent)+len(e.pending[set]) >= setSize {
			e.dropped++
			return
		}
	}
	// Taken as sent only once nft has it (Flush), so a refused batch is
	// sent again with the next answer.
	if e.pending == nil {
		e.pending = map[string]map[string]bool{}
	}
	if e.pending[set] == nil {
		e.pending[set] = map[string]bool{}
	}
	e.pending[set][s] = true
}

// Flush sends the queued addresses in one nft transaction. If nft refuses it
// (a set that is full refuses the whole batch), each set is sent on its own,
// so one full set does not keep the others from filling.
func (e *Engine) Flush() error {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	e.mu.Lock()
	p := e.pending
	e.pending = nil
	e.mu.Unlock()
	if len(p) == 0 {
		return nil
	}
	sets := sortedKeys(p)
	lines := map[string]string{}
	var all strings.Builder
	for _, set := range sets {
		var b strings.Builder
		els := sortedKeys(p[set])
		for len(els) > 0 {
			n := min(elemChunk, len(els))
			parts := make([]string, n)
			to := appTimeout
			if strings.HasPrefix(set, "cut_") {
				to = cutTimeout
			}
			for i, x := range els[:n] {
				parts[i] = x + " timeout " + to
			}
			fmt.Fprintf(&b, "add element %s %s { %s }\n", Table, set, strings.Join(parts, ", "))
			els = els[n:]
		}
		lines[set] = b.String()
		all.WriteString(b.String())
	}
	sentAt := e.o.Now()
	// sent records what nft took, so the next answer for an address in a
	// refused batch sends it again.
	sent := func(set string) {
		if strings.HasPrefix(set, "cut_") {
			return // short-lived pairs, never saved and never refreshed
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.dirty = true
		if !e.declared["set "+set] {
			return // the set went with its last rule meanwhile
		}
		if e.elems == nil {
			e.elems = map[string]map[string]time.Time{}
		}
		if e.elems[set] == nil {
			e.elems[set] = map[string]time.Time{}
		}
		for x := range p[set] {
			e.elems[set][x] = sentAt
		}
	}
	_, err := e.run(nil, all.String(), "-f", "-")
	if err == nil {
		for _, set := range sets {
			sent(set)
		}
		return nil
	}
	if len(sets) == 1 {
		return err
	}
	var first error
	for _, set := range sets {
		if _, err := e.run(nil, lines[set], "-f", "-"); err != nil {
			if first == nil {
				first = fmt.Errorf("%s: %w", set, err)
			}
			continue
		}
		sent(set)
	}
	return first
}

// Tick cuts the connections a rule now covers that it did not cover at the
// previous tick, and compiles again when the zone's next transition has
// passed, the wall clock jumped against the monotonic clock, or the kernel's
// offset changed. It also asks again whether names can be seen; that changes
// only what the rules say, never the ruleset, so it needs no compile.
func (e *Engine) Tick(now time.Time) {
	e.lookBlind()
	if e.o.Usage != nil {
		e.o.Usage.Close(now)
	}
	e.mu.Lock()
	inForce := e.inForce
	old := e.spent
	e.mu.Unlock()
	spentChanged := !sameSpent(old, e.computeSpent(inForce, now))
	if e.o.Roster != nil && !e.o.Roster.Good() {
		e.TakeBaseline()
	}
	e.lookQuarantine(inForce)
	_, _, qsig := e.quarantineOf(inForce)
	mono := e.o.Elapsed()
	east, eerr := e.o.KernelEast()
	e.mu.Lock()
	need := !e.next.IsZero() && !now.Before(e.next)
	if e.ticked {
		d := now.Round(0).Sub(e.lastWall.Round(0)) - (mono - e.lastMono)
		if d > jump || d < -jump {
			e.o.Logf("the clock moved by %v: rules are compiled again", d.Round(time.Second))
			need = true
		}
	}
	if eerr == nil && e.eastSet && east != e.east {
		need = true
	}
	if spentChanged {
		need = true
	}
	if qsig != e.quarantineSig {
		// An approval, a device added during the grace, or the grace's end.
		need = true
	}
	if houseSig(e.cat) != e.houseSig {
		// A house network came or went: isolated devices are kept from
		// the new list.
		need = true
	}
	e.ticked, e.lastWall, e.lastMono = true, now, mono
	e.mu.Unlock()
	if need {
		if err := e.reload(); err != nil {
			e.o.Logf("rules not applied: %v", err)
		}
	}
	e.cut(now)
}

// cut ends every live connection a rule in force covers now and did not at
// the previous look. Flows are chosen by the hardware address the flow table
// filed them under, never by an address, so a device's IPv4 and IPv6 flows
// go together and an address another device now holds is not taken.
func (e *Engine) cut(now time.Time) {
	if e.o.Live == nil {
		return
	}
	live := e.o.Live()
	e.mu.Lock()
	cov := map[flowKey]bool{}
	var todo []flows.Flow
	for _, f := range live {
		if f.MAC == flows.RouterKey || f.Dir == flows.Local || (f.Proto != 6 && f.Proto != 17) {
			continue
		}
		cc := f.Geo.CC
		if cc == "" {
			// The flow table places a flow only once it is named or its
			// name wait is over; its country is its remote address's
			// either way, so a young unnamed flow is cut at the write too.
			if g, found := e.o.Geo.Lookup(f.Remote.Addr()); found {
				cc = g.CC
			}
		}
		r, ok := e.covering(f.MAC, f.App.ID, cc, now)
		if !ok {
			continue
		}
		k := flowKey{f.ID, f.Local, f.Remote, f.Proto}
		cov[k] = true
		if !e.covered[k] {
			todo = append(todo, f)
			e.queueCut(r, f.MAC, f.Remote.Addr().Unmap(), f.Proto, f.Local.Port())
		}
	}
	e.covered = cov
	e.mu.Unlock()
	for _, f := range todo {
		e.del(f.Local, f.Remote, f.Proto, f.Dir == flows.In)
	}
}

// del ends one connection. The device's side is local: for a connection the
// device opened that is the original tuple, for one opened from outside the
// reply tuple.
func (e *Engine) del(local, remote netip.AddrPort, proto uint8, reply bool) {
	if t, ok := tupleOf(local, remote, proto); ok {
		e.delTuple(t, reply)
	}
}

// tupleOf is the connection's tuple seen from the device's side; only TCP
// and UDP are cut.
func tupleOf(local, remote netip.AddrPort, proto uint8) (ct.Tuple, bool) {
	if proto != 6 && proto != 17 {
		return ct.Tuple{}, false
	}
	return ct.Tuple{
		SrcIP: net.IP(local.Addr().Unmap().AsSlice()), DstIP: net.IP(remote.Addr().Unmap().AsSlice()),
		Proto: proto, SrcPort: local.Port(), DstPort: remote.Port(),
	}, true
}

// Cut ends one connection the owner chose: local is the device's side and
// mac its hardware address. conntrack would take the connection up again on
// its next packet (tcp_loose), on the same port, so the flow first goes into
// the owner's cut set (OwnerCut), whose lines reject it for ten minutes, and
// only then is its entry deleted, by the delete a rule uses, with the error
// handed back. A device without a hardware address has no key and is only
// deleted. Only TCP and UDP can be cut. The note is "" when the cut is held,
// and otherwise says, for the console, why the connection may come back.
func (e *Engine) Cut(mac string, local, remote netip.AddrPort, proto uint8, reply bool) (string, error) {
	t, ok := tupleOf(local, remote, proto)
	if !ok {
		return "", errors.New("only TCP and UDP can be cut")
	}
	fn := e.o.Delete
	if reply {
		fn = e.o.DeleteReply
	}
	if fn == nil {
		return "", errors.New("nothing to cut connections with")
	}
	note := e.holdCut(mac, remote.Addr().Unmap(), proto, local.Port())
	return note, fn(t)
}

// comesBack ends every note of a cut that is not held.
const comesBack = ": the connection may come back on its next packet"

// holdCut adds one flow the owner cut to OwnerCut at once, in its own nft
// transaction: through the second Flush waits the reborn flow could pass, and
// under flow offloading it would then never meet the forward chain again.
// It answers "" when the flow is held, else why not.
func (e *Engine) holdCut(mac string, a netip.Addr, proto uint8, port uint16) string {
	pname := map[uint8]string{6: "tcp", 17: "udp"}[proto]
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return "the device has no hardware address, so the cut cannot be held" + comesBack
	}
	if pname == "" || port == 0 || !a.IsValid() {
		return "this connection has no port to hold it by" + comesBack
	}
	set := OwnerCut("4")
	if a.Is6() {
		set = OwnerCut("6")
	}
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	e.mu.Lock()
	declared := e.declared["set "+set]
	e.mu.Unlock()
	if !declared {
		return "the rules are not applied yet, so the cut cannot be held" + comesBack
	}
	elem := fmt.Sprintf("add element %s %s { %s . %s . %s . %d timeout %s }\n", Table, set, hw, a, pname, port, cutTimeout)
	if _, err := e.run(nil, elem, "-f", "-"); err != nil {
		e.o.Logf("the cut of %s:%d is not held, conntrack may take it up again: %v", a, port, err)
		return "the router could not hold the cut (" + firstLine(err.Error()) + ")" + comesBack
	}
	return ""
}

// firstLine is the first line of an error, trimmed: nft prints the offending
// line and a caret under it after its message, which a note has no use for.
func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}

func (e *Engine) delTuple(t ct.Tuple, reply bool) {
	fn := e.o.Delete
	if reply {
		fn = e.o.DeleteReply
	}
	if fn == nil {
		return
	}
	if err := fn(t); err != nil {
		e.o.Logf("connection %v:%d -> %v:%d not cut: %v", t.SrcIP, t.SrcPort, t.DstIP, t.DstPort, err)
	}
}

// covering says whether a rule in force holds a flow of this device to this
// app or country now, and which. The caller holds mu.
func (e *Engine) covering(mac, app, cc string, now time.Time) (Rule, bool) {
	for _, r := range e.inForce.Rules {
		if !inScript(r, now) || (r.Action != "limit" && !Active(r, e.o.Loc, now)) {
			continue
		}
		if r.Action == "limit" {
			// A limit covers a device only once its minutes are spent.
			if until, ok := e.spent[r.ID][mac]; !ok || !until.After(now) {
				continue
			}
		} else if r.Who != "all" && !contains(r.Members, mac) {
			continue
		}
		if r.What != "country" && e.allowed(r.ID, mac, now) {
			continue
		}
		switch r.What {
		case "all":
			return r, true
		case "app":
			if app != "" && contains(r.Targets, app) {
				return r, true
			}
		case "category":
			for _, c := range r.Targets {
				if e.cat.inCategory(c, app) {
					return r, true
				}
			}
		case "country":
			if cc != "" && contains(r.Targets, cc) {
				return r, true
			}
		}
	}
	return Rule{}, false
}

// track tells Usage which (device, app) pairs the limit rules count, "" for
// every device, and loads the counts a graceful stop saved, once.
func (e *Engine) track(cfg Config, now time.Time) {
	u := e.o.Usage
	if u == nil {
		return
	}
	pairs := map[string]map[string]bool{}
	for _, r := range cfg.Rules {
		if r.Action != "limit" || !inScript(r, now) {
			continue
		}
		macs := r.Members
		if r.Who == "all" {
			macs = []string{""}
		}
		for _, mac := range macs {
			if pairs[mac] == nil {
				pairs[mac] = map[string]bool{}
			}
			for _, app := range e.cat.appsOf(r) {
				pairs[mac][app] = true
			}
		}
	}
	u.Track(pairs)
	e.mu.Lock()
	load := !e.usageLoaded
	e.usageLoaded = true
	e.mu.Unlock()
	if load {
		if err := u.Load(filepath.Join(e.o.StateDir, "usage.json"), now); err != nil {
			e.o.Logf("minute usage could not be read (%v): the day counts from zero", err)
		}
	}
}

// usedBy is today's minutes of a limit rule per device: the sum over the
// rule's apps (a category's minutes add up across its apps).
func (e *Engine) usedBy(r Rule, snap map[string]map[string]int) map[string]int {
	apps := e.cat.appsOf(r)
	out := map[string]int{}
	add := func(mac string) {
		n := 0
		for _, app := range apps {
			n += snap[mac][app]
		}
		if n > 0 {
			out[mac] = n
		}
	}
	if r.Who == "all" {
		for mac := range snap {
			add(mac)
		}
	} else {
		for _, mac := range r.Members {
			add(mac)
		}
	}
	return out
}

// computeSpent is, per limit rule in force today, the devices whose minutes
// are used up, each until the next local midnight. A rule with days counts
// only on those days.
func (e *Engine) computeSpent(cfg Config, now time.Time) map[string]map[string]time.Time {
	if e.o.Usage == nil {
		return nil
	}
	snap := e.o.Usage.Snapshot()
	day := now.In(e.o.Loc).Weekday()
	out := map[string]map[string]time.Time{}
	for _, r := range cfg.Rules {
		if r.Action != "limit" || !inScript(r, now) {
			continue
		}
		if len(r.Days) > 0 && !containsDay(r.Days, day) {
			continue
		}
		for mac, n := range e.usedBy(r, snap) {
			if n >= r.Minutes {
				if out[r.ID] == nil {
					out[r.ID] = map[string]time.Time{}
				}
				out[r.ID][mac] = NextMidnight(e.o.Loc, now)
			}
		}
	}
	return out
}

func containsDay(days []time.Weekday, d time.Weekday) bool {
	for _, x := range days {
		if x == d {
			return true
		}
	}
	return false
}

func sameSpent(a, b map[string]map[string]time.Time) bool {
	count := func(m map[string]map[string]time.Time) int {
		n := 0
		for _, v := range m {
			n += len(v)
		}
		return n
	}
	if count(a) != count(b) {
		return false
	}
	for id, macs := range a {
		for mac, until := range macs {
			if !b[id][mac].Equal(until) {
				return false
			}
		}
	}
	return true
}

// UsageOf is a device's use of each limit rule today, for the console and
// netryx ctl: the rule's total, as computeSpent sums it, and its apps in use.
func (e *Engine) UsageOf(mac string) []LimitUse {
	if e.o.Usage == nil {
		return nil
	}
	snap := e.o.Usage.Snapshot()
	now := e.o.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []LimitUse
	for _, r := range e.inForce.Rules {
		if r.Action != "limit" || !inScript(r, now) || (r.Who != "all" && !contains(r.Members, mac)) {
			continue
		}
		var until time.Time
		if u, ok := e.spent[r.ID][mac]; ok && u.After(now) {
			until = u
		}
		u := LimitUse{Rule: r.ID, What: r.What, Targets: append([]string(nil), r.Targets...), Limit: r.Minutes, SpentUntil: until}
		for _, app := range e.cat.appsOf(r) {
			if n := snap[mac][app]; n > 0 {
				u.Used += n
				u.Apps = append(u.Apps, AppMinutes{App: app, Used: n})
			}
		}
		sort.Slice(u.Apps, func(i, j int) bool {
			if u.Apps[i].Used != u.Apps[j].Used {
				return u.Apps[i].Used > u.Apps[j].Used
			}
			return u.Apps[i].App < u.Apps[j].App
		})
		out = append(out, u)
	}
	return out
}

// SaveUsage writes today's counts for the next start (a graceful stop).
func (e *Engine) SaveUsage() error {
	if e.o.Usage == nil {
		return nil
	}
	return e.o.Usage.Save(filepath.Join(e.o.StateDir, "usage.json"))
}

// watchSafe looks at the safe resolver every safeEvery and whenever a reload
// asks, until stop. A dead resolver leaves filtered devices without DNS until
// it is seen and started again or they are moved off it.
func (e *Engine) watchSafe(stop <-chan struct{}) {
	t := time.NewTicker(safeEvery)
	defer t.Stop()
	for {
		e.safeLook(e.o.Now())
		select {
		case <-stop:
			return
		case <-t.C:
		case <-e.safeWake:
		}
	}
}

// safeLook makes the resolver match the config and, when whether it answers
// changed, applies the rules again so filtered devices follow it.
func (e *Engine) safeLook(now time.Time) {
	if e.EnsureSafeDNS(now) {
		if err := e.Reload(); err != nil {
			e.o.Logf("rules not applied: %v", err)
		}
	}
}

// EnsureSafeDNS asks the safe-search resolver's manager to match the config
// in force (or the one read, before the first apply) and records whether it
// answers. It says whether that changed.
func (e *Engine) EnsureSafeDNS(now time.Time) bool {
	if e.o.SafeDNS == nil {
		return false
	}
	e.mu.Lock()
	cfg := e.inForce
	read := e.read
	if !e.appliedOK && read {
		cfg = e.shown
	}
	e.mu.Unlock()
	if !read {
		if b, err := os.ReadFile(e.o.ConfigPath); err == nil {
			if secs, err := uciconf.Parse(bytes.NewReader(b)); err == nil {
				cfg = FromSections(secs, e.cat.lenient(), now)
			}
		}
	}
	want := wantsSafe(cfg)
	ok, problem := e.o.SafeDNS.Ensure(want, cfg.Filters.YouTube)
	e.mu.Lock()
	defer e.mu.Unlock()
	changed := ok != e.safeOK
	e.safeOK, e.safeProblem, e.safeAt = ok, problem, now
	return changed
}

// SafeDNSState is whether the safe-search resolver answers and, if not, why.
func (e *Engine) SafeDNSState() (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.safeOK, e.safeProblem
}

// wantsSafe: some group asks for filtered DNS and there is something to filter.
func wantsSafe(cfg Config) bool {
	if !cfg.Filters.SafeSearch && cfg.Filters.YouTube == "off" {
		return false
	}
	for _, g := range cfg.Groups {
		if g.FilteredDNS {
			return true
		}
	}
	return false
}

// allowed says whether a live allowance lifts rule id for this device.
func (e *Engine) allowed(id, mac string, now time.Time) bool {
	for _, a := range e.inForce.Allows {
		if a.Member == mac && a.Until.After(now) && (a.Rule == "" || a.Rule == id) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// SaveSets writes what the app sets hold to <state>/sets.nft by rename, for
// the init to load at boot after rules.nft. The organisations' ranges are not
// saved: rules.nft fills them from the address table.
func (e *Engine) SaveSets() error {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	e.mu.Lock()
	if !e.everApplied {
		// The sets this process would list are not known to be the
		// rules': sets.nft keeps what the last process saved.
		e.mu.Unlock()
		return nil
	}
	var names []string
	for _, base := range e.apps {
		names = append(names, "app_"+base+"_v4", "app_"+base+"_v6")
	}
	e.mu.Unlock()
	sort.Strings(names)
	var buf bytes.Buffer
	var errs []error
	for _, n := range names {
		out, err := e.run(nil, "", "list", "set", "inet", "netryx_rules", n)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", n, err))
			continue
		}
		buf.Write(out)
	}
	path := filepath.Join(e.o.StateDir, "sets.nft")
	if len(errs) > 0 {
		// A partial listing would drop the addresses of the sets that
		// failed: the file the boot loads keeps what it has.
		return errors.Join(errs...)
	}
	if !bytes.Contains(buf.Bytes(), []byte("elements")) {
		// Empty sets never replace saved addresses: an app rule after the
		// next boot would otherwise start blind.
		if old, err := os.ReadFile(path); err == nil && bytes.Contains(old, []byte("elements")) {
			return nil
		}
	}
	if err := writeFile(path, buf.Bytes()); err != nil {
		return err
	}
	e.mu.Lock()
	e.dirty = false
	e.mu.Unlock()
	return nil
}

// catalog answers the compiler and the validator from the loaded data files:
// which countries the address table has, which apps and categories the app
// map knows, and the ranges behind a country or an organisation, kept once
// computed (the files do not change while the process runs).
type catalog struct {
	geo   *enrich.GeoDB
	apps  *enrich.AppMap
	ccs   map[string]bool
	ids   map[string]bool
	cats  map[string][]string
	inCat map[string]map[string]bool

	// ifaces lists the house-side devices (EngineOptions.HouseIfaces).
	ifaces func() []string
	house  func() []netip.Prefix

	mu  sync.Mutex
	cc  map[string][2][]enrich.Range
	org map[string][2][]enrich.Range
}

func newCatalog(geo *enrich.GeoDB, apps *enrich.AppMap) *catalog {
	c := &catalog{geo: geo, apps: apps, ccs: map[string]bool{}, ids: map[string]bool{},
		cats: map[string][]string{}, inCat: map[string]map[string]bool{},
		cc: map[string][2][]enrich.Range{}, org: map[string][2][]enrich.Range{}}
	geo.Ranges(func(a enrich.ASInfo) bool {
		if isCC(a.CC) {
			c.ccs[a.CC] = true
		}
		return false
	})
	for _, a := range apps.Apps() {
		c.ids[a.ID] = true
		if a.Category == "" {
			continue
		}
		c.cats[a.Category] = append(c.cats[a.Category], a.ID)
		if c.inCat[a.Category] == nil {
			c.inCat[a.Category] = map[string]bool{}
		}
		c.inCat[a.Category][a.ID] = true
	}
	return c
}

// noGeo and noApps say a data file is missing (or held nothing).
func (c *catalog) noGeo() bool { return len(c.ccs) == 0 }

// HousePrefixes are the networks the router serves (EngineOptions.HousePrefixes).
func (c *catalog) HousePrefixes() []netip.Prefix {
	if c.house == nil {
		return nil
	}
	return c.house()
}
func (c *catalog) noApps() bool { return len(c.ids) == 0 }

// lenient is the catalog the engine reads the config with: a data file that
// is missing cannot say a target is unknown, so the rule is read whole and
// the apply is refused for it instead (R38).
func (c *catalog) lenient() Known { return lenientKnown{c} }

type lenientKnown struct{ c *catalog }

func (k lenientKnown) Country(cc string) bool { return k.c.Country(cc) || k.c.noGeo() && isCC(cc) }
func (k lenientKnown) App(id string) bool     { return k.c.App(id) || k.c.noApps() }
func (k lenientKnown) Category(x string) bool { return k.c.Category(x) || k.c.noApps() }

// appsOf is every app an app or category rule names.
func (c *catalog) appsOf(r Rule) []string {
	if r.What == "app" {
		return r.Targets
	}
	var out []string
	for _, x := range r.Targets {
		out = append(out, c.AppsIn(x)...)
	}
	return out
}

func (c *catalog) Country(cc string) bool { return c.ccs[cc] }
func (c *catalog) App(id string) bool     { return c.ids[id] }
func (c *catalog) Category(x string) bool { return len(c.cats[x]) > 0 }
func (c *catalog) AppsIn(x string) []string {
	return c.cats[x]
}
func (c *catalog) inCategory(x, id string) bool { return id != "" && c.inCat[x][id] }

// inOrg says whether a lies in the app's organisation ranges, as far as they
// have been computed (the compiler computes them for every org app a rule
// names); it never scans the address table itself.
func (c *catalog) inOrg(app string, a netip.Addr) bool {
	c.mu.Lock()
	r, ok := c.org[app]
	c.mu.Unlock()
	if !ok {
		return false
	}
	rs := r[0]
	if a.Is6() {
		rs = r[1]
	}
	i := sort.Search(len(rs), func(i int) bool { return rs[i].Hi.Compare(a) >= 0 })
	return i < len(rs) && rs[i].Lo.Compare(a) <= 0
}

// AppAddrs is an app's static ranges, split by family.
func (c *catalog) AppAddrs(app string) (v4, v6 []enrich.Range) {
	for _, p := range c.apps.Addrs(app) {
		r := prefixRange(p)
		if p.Addr().Is4() {
			v4 = append(v4, r)
		} else {
			v6 = append(v6, r)
		}
	}
	return v4, v6
}

// AppPorts is an app's destination ports.
func (c *catalog) AppPorts(app string) []enrich.Port { return c.apps.Ports(app) }

// HouseIfaces is the house-side devices, from the engine's options.
func (c *catalog) HouseIfaces() []string {
	if c.ifaces == nil {
		return nil
	}
	return c.ifaces()
}

// prefixRange is the first and last address of a prefix.
func prefixRange(p netip.Prefix) enrich.Range {
	p = p.Masked()
	lo := p.Addr()
	b := lo.AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 1 << (7 - uint(i%8))
	}
	hi, _ := netip.AddrFromSlice(b)
	return enrich.Range{Lo: lo, Hi: hi}
}

func (c *catalog) CountryRanges(cc string) (v4, v6 []enrich.Range) {
	c.mu.Lock()
	r, ok := c.cc[cc]
	c.mu.Unlock()
	if ok {
		return r[0], r[1]
	}
	// Scanned without the lock: inOrg is asked from the capture path.
	v4, v6 = c.geo.Ranges(func(a enrich.ASInfo) bool { return a.CC == cc })
	c.mu.Lock()
	c.cc[cc] = [2][]enrich.Range{v4, v6}
	c.mu.Unlock()
	return v4, v6
}

func (c *catalog) OrgRanges(app string) (v4, v6 []enrich.Range) {
	c.mu.Lock()
	r, ok := c.org[app]
	c.mu.Unlock()
	if ok {
		return r[0], r[1]
	}
	// Scanned without the lock: inOrg is asked from the capture path.
	memo := map[string]bool{}
	v4, v6 = c.geo.Ranges(func(a enrich.ASInfo) bool {
		m, ok := memo[a.Org]
		if !ok {
			x, found := c.apps.ClassifyOrg(a.Org)
			m = found && x.ID == app
			memo[a.Org] = m
		}
		return m
	})
	c.mu.Lock()
	c.org[app] = [2][]enrich.Range{v4, v6}
	c.mu.Unlock()
	return v4, v6
}

// isolatedOf is the MACs of the devices with isolate (spec 6.7).
func isolatedOf(cfg Config) []string {
	var out []string
	for _, d := range cfg.Devices {
		if d.Isolate {
			out = append(out, d.MAC)
		}
	}
	return out
}

// houseSig names the house networks as the compiler sees them.
func houseSig(t Targets) string {
	v4, v6 := housePrefixes(t)
	return strings.Join(v4, ",") + "|" + strings.Join(v6, ",")
}

// TakeBaseline reads the quarantine's baseline, or takes it from the devices
// seen now (Run calls it before the first apply).
func (e *Engine) TakeBaseline() {
	if e.o.Roster == nil {
		return
	}
	var known []string
	if e.o.Known != nil {
		known = e.o.Known()
	} else {
		for _, d := range e.seen() {
			known = append(known, d.MAC)
		}
	}
	// The devices the owner named in the config are the house's too, away
	// or not (spec 6.8).
	if b, err := os.ReadFile(e.o.ConfigPath); err == nil {
		if secs, err := uciconf.Parse(bytes.NewReader(b)); err == nil {
			for _, d := range FromSections(secs, e.cat.lenient(), e.o.Now()).Devices {
				known = append(known, d.MAC)
			}
		}
	}
	err := e.o.Roster.Baseline(known)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	e.mu.Lock()
	changed := msg != e.baselineErr
	e.baselineErr = msg
	e.mu.Unlock()
	if err != nil && changed {
		e.o.Logf("quarantine: %v", err)
	}
	if errors.Is(err, ErrBaselineRebuilt) {
		e.event("baseline_rebuilt", "", "")
	}
	if err != nil && !errors.Is(err, ErrClockNotSet) && !e.o.Roster.Good() {
		// Unreadable or unwritable (a rebuild that could not be saved
		// included): the quarantine holds nobody until it can be, and the
		// owner hears so (once a day, the store's limit); Tick tries again.
		e.event("quarantine_off", "", "")
	}
}

// Pending is the devices in quarantine.
func (e *Engine) Pending() []Pending {
	if e.o.Roster == nil {
		return nil
	}
	return e.o.Roster.Pending()
}

func (e *Engine) seen() []Seen {
	if e.o.Seen == nil {
		return nil
	}
	return e.o.Seen()
}

func (e *Engine) event(kind, mac, host string) {
	if e.o.Event != nil {
		e.o.Event(Event{Kind: kind, MAC: mac, Hostname: host})
	}
}

// Event is what the engine tells the alerts; Count is how many newcomers a
// new_devices summary stands for.
type Event struct {
	Kind, MAC, Hostname string
	Count               int
}

// floodNewcomers is how many newcomers one look tells one by one; more are
// one new_devices summary (a broken switch or an attacker making up MACs).
const floodNewcomers = 5

// lookQuarantine hands the roster what the router sees and raises an event
// for each newcomer and for an overflow.
func (e *Engine) lookQuarantine(cfg Config) {
	if e.o.Roster == nil {
		return
	}
	news, full := e.o.Roster.See(e.seen(), approvedIn(cfg))
	if len(news) > floodNewcomers {
		if e.o.Event != nil {
			e.o.Event(Event{Kind: "new_devices", Count: len(news)})
		}
	} else {
		for _, n := range news {
			e.event("new_device", n.MAC, n.Hostname)
		}
	}
	if full {
		e.event("quarantine_full", "", "")
	}
}

// quarantineOf is what the compiler needs from the roster, and its signature.
func (e *Engine) quarantineOf(cfg Config) (approved []string, on bool, sig string) {
	if e.o.Roster == nil {
		return nil, false, ""
	}
	approved = e.o.Roster.Approved(approvedIn(cfg))
	on = e.o.Roster.Active(cfg.Quarantine.Enabled)
	return approved, on, fmt.Sprintf("%v|%s|%s", on, strings.Join(approved, ","), strings.Join(e.heldInGrace(cfg), ","))
}

// heldInGrace is the devices held before a rebuilt baseline's grace, less
// those approved since: isolated by name while the quarantine lines are off.
func (e *Engine) heldInGrace(cfg Config) []string {
	if e.o.Roster == nil {
		return nil
	}
	ok := map[string]bool{}
	for _, m := range approvedIn(cfg) {
		ok[m] = true
	}
	var out []string
	for _, m := range e.o.Roster.HeldInGrace(cfg.Quarantine.Enabled) {
		if !ok[m] {
			out = append(out, m)
		}
	}
	return out
}

// approvedIn is the MACs of the devices with approved in UCI.
func approvedIn(cfg Config) []string {
	var out []string
	for _, d := range cfg.Devices {
		if d.Approved {
			out = append(out, d.MAC)
		}
	}
	return out
}

// Alerts is the alert settings in force: the defaults until the config is
// read.
func (e *Engine) Alerts() Alerts {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.read {
		return DefaultAlerts
	}
	return e.shown.Alerts
}

// AlertsInForce is the alert settings and whether they were read from the
// config at all: before that they are the defaults, which nobody chose.
func (e *Engine) AlertsInForce() (Alerts, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.read {
		return DefaultAlerts, false
	}
	return e.shown.Alerts, true
}

// HeldDevices is the devices the kernel holds as quarantine now: every one
// listed while the quarantine lines are in force, and those held by name
// through a rebuilt baseline's grace.
func (e *Engine) HeldDevices() map[string]bool {
	e.mu.Lock()
	on, byName := e.quarantineOn, append([]string(nil), e.isoHeld...)
	e.mu.Unlock()
	out := map[string]bool{}
	if on {
		for _, p := range e.Pending() {
			out[p.MAC] = true
		}
	}
	for _, m := range byName {
		out[m] = true
	}
	return out
}

// QuarantineState says whether the quarantine holds, when its grace ends, and
// why it does not hold when it is on (spec 3: nothing is shown as enforced
// that is not).
func (e *Engine) QuarantineState() (active bool, graceEnds time.Time, problem string) {
	e.mu.Lock()
	active = e.quarantineOn
	enabled := e.inForce.Quarantine.Enabled
	if !e.appliedOK {
		enabled = e.shown.Quarantine.Enabled
	}
	e.mu.Unlock()
	if e.o.Roster == nil {
		return false, time.Time{}, "not available"
	}
	if g := e.o.Roster.GraceEnds(); g.After(e.o.Now()) {
		graceEnds = g
	}
	switch {
	case !enabled:
		problem = "turned off, new devices reach the house at once"
	case active:
	case !e.o.Roster.Good():
		problem = "the list of known devices cannot be read"
	case !graceEnds.IsZero():
	default:
		problem = "the router does not know the house interfaces or the approved devices"
	}
	return active, graceEnds, problem
}
