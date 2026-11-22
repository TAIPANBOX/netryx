package rules

import (
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"netryx/internal/enrich"
	"netryx/internal/nflog"
)

// orgApps lists the apps whose servers sit at hard-coded addresses, so their
// DNS answers are not enough (spec 6.2). Their sets app_<id>_org_v4/_v6 are
// filled from the owner organisation's address ranges.
const orgApps = "telegram"

// SubChain is the regular chain of a rule that has allowances: rules_fwd jumps
// to it, it starts with the allowance's return and then holds the rule's lines.
func SubChain(id string) string { return "r_" + id }

func isOrgApp(id string) bool {
	for _, a := range strings.Fields(orgApps) {
		if a == id {
			return true
		}
	}
	return false
}

// Targets answers what a rule's targets stand for.
type Targets interface {
	CountryRanges(cc string) (v4, v6 []enrich.Range)
	OrgRanges(app string) (v4, v6 []enrich.Range)
	AppsIn(category string) []string
	// AppAddrs are an app's static ranges (catalog @addr lines), AppPorts its
	// destination ports (@port lines), HouseIfaces the house-side devices
	// DNS redirection and the DoT block are limited to.
	AppAddrs(app string) (v4, v6 []enrich.Range)
	AppPorts(app string) []enrich.Port
	HouseIfaces() []string
	// HousePrefixes are the networks the router serves; an isolated device
	// is kept from them.
	HousePrefixes() []netip.Prefix
}

// Extra is what the engine knows that the config does not: which members of
// each limit rule have spent their minutes and until when (the next local
// midnight), and whether the safe-search resolver answers. Only then do
// filtered devices get redirected to it; otherwise they keep the main
// resolver rather than lose DNS.
type Extra struct {
	Spent   map[string]map[string]time.Time // rule -> MAC -> until
	SafeDNS bool
	// Isolated are the devices kept from the rest of the house (spec 6.7);
	// Approved the MACs that are not in quarantine, and Quarantine whether
	// the quarantine holds everything else (spec 6.8).
	Isolated   []string
	Approved   []string
	Quarantine bool
	// LogGroup is the capture's nflog group: a country rule copies a try
	// there for the alerts (0: the capture is not reading, nothing copied).
	LogGroup uint16
}

// InChain is the input chain isolation adds to inet netryx_rules, IsoTable
// the bridge table it adds beside it; isoIn and isoFwd are the chains an
// isolated or held device jumps to, isoCounter their counter.
const (
	InChain    = "rules_in"
	IsoTable   = "bridge netryx_iso"
	isoIn      = "iso_in"
	isoFwd     = "iso_fwd"
	isoCounter = "hits_iso"
)

// tryRate bounds the copies of a country rule's tries; the alerts tell a
// device and country once a day anyway.
const tryRate = "10/minute"

// MaxIsolated and MaxApproved bound the isolation sets.
const (
	MaxIsolated = 4096
	MaxApproved = 10000
)

// DNSChain is the dstnat chain that carries forced DNS and the redirect of
// filtered devices to the safe-search resolver, and SafePort that resolver's
// port.
const (
	DNSChain = "rules_dns"
	SafePort = 553
)

// FilterDoH is the implicit rule filters.block_doh compiles to, and dotCounter
// the counter of the DoT block. Rule names starting with f_ are kept for them.
const (
	FilterDoH  = "f_doh"
	dotCounter = "hits_f_dot"
	// filterDoHFiltered closes DoH for the devices sent to the safe resolver
	// when the house's DoH is not blocked; whoFiltered is its "who".
	filterDoHFiltered = "f_dohf"
	whoFiltered       = "filtered"
)

const (
	// Table and Chain are the names the engine flushes and the API reads.
	// The chain cannot be called fwd: that is an nft keyword and fails to parse.
	Table = "inet netryx_rules"
	Chain = "rules_fwd"

	tbl       = Table
	elemChunk = 500
)

// Script compiles the rules into one nft transaction for the table
// inet netryx_rules. It only ever adds: the table, its sets and its counters
// outlive the process, and the chain is flushed and refilled, so set contents
// and today's hits survive a rule change.
//
// Chain order (spec 6.1): country rules (never lifted), then
// "ether saddr @allow_all return", then pauses, then app and category rules.
// A rule with allowances of its own lives in its own chain r_<id>: rules_fwd
// jumps to it, and its first line returns for the allowed devices, so the
// allowance skips that rule's lines and no other rule's.
//
// fresh names the country codes whose sets get their elements written; nil
// means all of them. A country set that is not fresh is declared but left as
// the kernel has it.
func Script(cfg Config, t Targets, loc *time.Location, at time.Time, kernelEast int, fresh map[string]bool, x Extra) string {
	s, _ := compile(cfg, t, loc, at, kernelEast, fresh, nil, nil, x)
	return s
}

// CutSet is the set of an app or category rule that holds the connections a
// cut ended (R40), as device, remote address, protocol and the device's own
// port (R43, R45): conntrack picks a cut connection up again on its next packet
// (tcp_loose), on the same port, so for ten minutes the rule's lines reject
// that flow whatever the app sets hold, while a new connection of the device
// to the same shared front takes the normal path. fam is "4" or "6".
func CutSet(id, fam string) string { return "cut_" + id + "_v" + fam }

// OwnerCut is the set of the connections the owner cut with the console's
// button, keyed like a rule's cut set and held as long, but no rule's: it is
// declared by every ruleset and its lines sit with the security rules, before
// every allowance, so a flow the owner ended stays ended for ten minutes while
// a new connection takes the normal path. CutSet never makes this name: a
// rule's id is never empty. fam is "4" or "6".
func OwnerCut(fam string) string { return "cut_v" + fam }

// cutTimeout is how long a cut pair stays in its rule's set, and cutSize the
// set's bound.
const (
	cutTimeout = "10m"
	cutSize    = 1024
)

// compiled is what a script declared, for the engine: which rules it holds,
// which app sets the answers may feed, which country sets it named, and the
// per-rule objects (r_<id> chains, members_/allow_ sets, hits_ counters) that
// any other rule's leftovers can be told apart from.
type compiled struct {
	ids     []string
	apps    []string
	ccs     []string // upper case
	objects map[string]bool
	// isoOver counts the isolated devices past MaxIsolated left out;
	// quarantine says the quarantine lines were compiled.
	isoOver    int
	quarantine bool
}

// compile is Script, and it also says what it declared. reset names the rules
// whose counter is new to the table: it is reset in the same transaction, so a
// rule that is created again under an old name does not inherit its hits.
// recut names the rules whose cut sets are emptied in the same transaction,
// because what the rule blocks changed.
func compile(cfg Config, t Targets, loc *time.Location, at time.Time, kernelEast int, fresh, reset, recut map[string]bool, x Extra) (string, compiled) {
	// FromSections guarantees valid, unique names (ValidID); a rule whose name
	// is not usable inside an nft name is never emitted.
	var active []Rule
	for _, r := range cfg.Rules {
		if !r.Enabled || r.Err != "" || !ValidID(r.ID) || (!r.Until.IsZero() && !r.Until.After(at)) {
			continue
		}
		active = append(active, r)
	}
	ifaces := houseIfaces(t)
	isolated, isoOver := macList(x.Isolated, MaxIsolated)
	approved, _ := macList(x.Approved, MaxApproved)
	// An empty approved set would hold the whole house: the quarantine is on
	// only with something approved and a house interface to scope it to.
	quarantine := x.Quarantine && len(approved) > 0 && len(ifaces) > 0
	var filtered []string
	if x.SafeDNS && len(ifaces) > 0 && (cfg.Filters.SafeSearch || cfg.Filters.YouTube != "off") {
		filtered = filteredMembers(cfg)
	}
	if cfg.Filters.BlockDoH {
		active = append(active, Rule{ID: FilterDoH, Enabled: true, Action: "block", What: "category",
			Targets: []string{"doh"}, Who: "all", From: -1, To: -1})
	} else if len(filtered) > 0 {
		// A filtered device that keeps DoH walks around the safe resolver.
		active = append(active, Rule{ID: filterDoHFiltered, Enabled: true, Action: "block", What: "category",
			Targets: []string{"doh"}, Who: whoFiltered, From: -1, To: -1})
	}

	// Allowances: MAC -> latest end, per rule and for every rule.
	allowAll := map[string]time.Time{}
	allowFor := map[string]map[string]time.Time{}
	liftable := map[string]bool{}
	for _, r := range active {
		// Country rules and the owner's filters (f_*) are never lifted.
		if r.What != "country" && !strings.HasPrefix(r.ID, "f_") {
			liftable[r.ID] = true
		}
	}
	for _, a := range cfg.Allows {
		if !a.Until.After(at) {
			continue
		}
		dst := allowAll
		if a.Rule != "" {
			id := a.Rule
			if !liftable[id] {
				continue
			}
			if allowFor[id] == nil {
				allowFor[id] = map[string]time.Time{}
			}
			dst = allowFor[id]
		}
		if a.Until.After(dst[a.Member]) {
			dst[a.Member] = a.Until
		}
	}

	c := &comp{t: t, loc: loc, at: at, east: kernelEast, allowFor: allowFor, ccs: map[string]bool{}, apps: map[string]bool{}, addrApps: map[string]bool{}}
	if cfg.Alerts.BlockedCountry {
		c.logGroup = x.LogGroup
	}
	var country, pause, app []string
	sub := map[string][]string{} // r_<id> chain contents, for rules with allowances
	for _, r := range active {
		inline, own := c.rule(r)
		if own != nil {
			sub[r.ID] = own
		}
		switch {
		case r.What == "country" || strings.HasPrefix(r.ID, "f_"):
			// Security rules: before every allowance.
			country = append(country, inline...)
		case r.What == "all":
			pause = append(pause, inline...)
		default:
			app = append(app, inline...)
		}
	}

	var b strings.Builder
	line := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	line("# netryx rules, compiled by netryx, loaded with TZ=UTC nft -f")
	line("add table %s", tbl)

	ccNames := sortedKeys(c.ccs)
	appIDs := sortedKeys(c.apps)
	var orgIDs []string
	for _, id := range appIDs {
		if isOrgApp(id) {
			orgIDs = append(orgIDs, id)
		}
	}
	memberRules := map[string]Rule{}
	spentRules := map[string]bool{}
	for _, r := range active {
		switch {
		case r.Action == "limit":
			spentRules[r.ID] = true
		case r.Who != "all" && r.Who != whoFiltered:
			memberRules[r.ID] = r
		}
	}
	spentIDs := sortedKeys(spentRules)
	addrIDs := sortedKeys(c.addrApps)
	memberIDs := sortedKeys(memberRules)
	allowIDs := sortedKeys(allowFor)
	cutRules := map[string]bool{}
	for _, r := range active {
		if r.What == "app" || r.What == "category" {
			cutRules[r.ID] = true
		}
	}
	cutIDs := sortedKeys(cutRules)

	ccDecl := func(name, typ string) string {
		return fmt.Sprintf("add set %s %s { type %s; flags interval; auto-merge; }", tbl, name, typ)
	}
	for _, cc := range ccNames {
		line("%s", ccDecl("cc_"+cc+"_v4", "ipv4_addr"))
		line("%s", ccDecl("cc_"+cc+"_v6", "ipv6_addr"))
	}
	for _, id := range appIDs {
		n := appName(id)
		line("add set %s app_%s_v4 { type ipv4_addr; flags timeout; timeout 24h; size 4096; }", tbl, n)
		line("add set %s app_%s_v6 { type ipv6_addr; flags timeout; timeout 24h; size 4096; }", tbl, n)
	}
	for _, id := range orgIDs {
		n := appName(id)
		line("%s", ccDecl("app_"+n+"_org_v4", "ipv4_addr"))
		line("%s", ccDecl("app_"+n+"_org_v6", "ipv6_addr"))
	}
	etherSet := func(name string) {
		line("add set %s %s { type ether_addr; flags timeout; }", tbl, name)
	}
	for _, id := range addrIDs {
		n := appName(id)
		line("%s", ccDecl("app_"+n+"_addr_v4", "ipv4_addr"))
		line("%s", ccDecl("app_"+n+"_addr_v6", "ipv6_addr"))
	}
	for _, id := range memberIDs {
		etherSet("members_" + id)
	}
	for _, id := range spentIDs {
		etherSet("spent_" + id)
	}
	if len(allowAll) > 0 {
		etherSet("allow_all")
	}
	for _, id := range allowIDs {
		etherSet("allow_" + id)
	}
	cutDecl := func(name, fam string) {
		line("add set %s %s { type ether_addr . ipv%s_addr . inet_proto . inet_service; flags timeout; timeout %s; size %d; }", tbl, name, fam, cutTimeout, cutSize)
	}
	for _, id := range cutIDs {
		cutDecl(CutSet(id, "4"), "4")
		cutDecl(CutSet(id, "6"), "6")
	}
	cutDecl(OwnerCut("4"), "4")
	cutDecl(OwnerCut("6"), "6")
	if len(filtered) > 0 {
		line("add set %s filtered_dns { type ether_addr; }", tbl)
	}
	line("add set %s isolated { type ether_addr; size %d; }", tbl, MaxIsolated)
	line("add set %s approved { type ether_addr; size %d; }", tbl, MaxApproved)
	line("%s", ccDecl("house_v4", "ipv4_addr"))
	line("%s", ccDecl("house_v6", "ipv6_addr"))

	fill := func(name string, v4, v6 []enrich.Range, fam4 string) {
		line("flush set %s %s", tbl, name)
		rs := v4
		if fam4 == "6" {
			rs = v6
		}
		var parts []string
		for _, r := range rs {
			if r.Lo == r.Hi {
				parts = append(parts, r.Lo.String())
			} else {
				parts = append(parts, r.Lo.String()+"-"+r.Hi.String())
			}
		}
		for len(parts) > 0 {
			n := min(elemChunk, len(parts))
			line("add element %s %s { %s }", tbl, name, strings.Join(parts[:n], ", "))
			parts = parts[n:]
		}
	}
	for _, cc := range ccNames {
		if fresh != nil && !fresh[strings.ToUpper(cc)] {
			continue
		}
		v4, v6 := t.CountryRanges(strings.ToUpper(cc))
		fill("cc_"+cc+"_v4", v4, v6, "4")
		fill("cc_"+cc+"_v6", v4, v6, "6")
	}
	for _, id := range orgIDs {
		v4, v6 := t.OrgRanges(id)
		fill("app_"+appName(id)+"_org_v4", v4, v6, "4")
		fill("app_"+appName(id)+"_org_v6", v4, v6, "6")
	}
	for _, id := range addrIDs {
		v4, v6 := t.AppAddrs(id)
		fill("app_"+appName(id)+"_addr_v4", v4, v6, "4")
		fill("app_"+appName(id)+"_addr_v6", v4, v6, "6")
	}
	for _, id := range memberIDs {
		r := memberRules[id]
		line("flush set %s members_%s", tbl, id)
		var parts []string
		seenMAC := map[string]bool{}
		for _, m := range r.Members {
			if seenMAC[m] {
				continue
			}
			seenMAC[m] = true
			parts = append(parts, m+timeout(r.Until, at))
		}
		if len(parts) > 0 {
			line("add element %s members_%s { %s }", tbl, id, strings.Join(parts, ", "))
		}
	}
	for _, id := range spentIDs {
		line("flush set %s spent_%s", tbl, id)
		var parts []string
		for _, mac := range sortedKeys(x.Spent[id]) {
			if _, ok := normMAC(mac); !ok {
				// One element that is not a MAC would make nft refuse the
				// whole transaction.
				continue
			}
			if until := x.Spent[id][mac]; until.After(at) {
				parts = append(parts, mac+timeout(until, at))
			}
		}
		if len(parts) > 0 {
			line("add element %s spent_%s { %s }", tbl, id, strings.Join(parts, ", "))
		}
	}
	allowFill := func(name string, m map[string]time.Time) {
		line("flush set %s %s", tbl, name)
		var parts []string
		for _, mac := range sortedKeys(m) {
			parts = append(parts, mac+timeout(m[mac], at))
		}
		line("add element %s %s { %s }", tbl, name, strings.Join(parts, ", "))
	}
	if len(allowAll) > 0 {
		allowFill("allow_all", allowAll)
	}
	for _, id := range allowIDs {
		allowFill("allow_"+id, allowFor[id])
	}
	for _, id := range cutIDs {
		if recut[id] {
			line("flush set %s %s", tbl, CutSet(id, "4"))
			line("flush set %s %s", tbl, CutSet(id, "6"))
		}
	}
	if len(filtered) > 0 {
		line("flush set %s filtered_dns", tbl)
		line("add element %s filtered_dns { %s }", tbl, strings.Join(filtered, ", "))
	}
	elements := func(table, name string, items []string) {
		line("flush set %s %s", table, name)
		for len(items) > 0 {
			n := min(elemChunk, len(items))
			line("add element %s %s { %s }", table, name, strings.Join(items[:n], ", "))
			items = items[n:]
		}
	}
	elements(tbl, "isolated", isolated)
	elements(tbl, "approved", approved)
	h4, h6 := housePrefixes(t)
	elements(tbl, "house_v4", h4)
	elements(tbl, "house_v6", h6)

	var ids []string
	for _, r := range active {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		line("add counter %s hits_%s", tbl, id)
		if reset[id] {
			line("reset counter %s hits_%s", tbl, id)
		}
	}
	if cfg.Filters.BlockDoT || len(filtered) > 0 {
		line("add counter %s %s", tbl, dotCounter)
	}
	line("add counter %s %s", tbl, isoCounter)
	line("add chain %s %s { type filter hook forward priority -5; policy accept; }", tbl, Chain)
	line("add chain %s %s { type nat hook prerouting priority dstnat - 5; policy accept; }", tbl, DNSChain)
	line("add chain %s %s { type filter hook input priority filter - 5; policy accept; }", tbl, InChain)
	line("add chain %s %s", tbl, isoIn)
	line("add chain %s %s", tbl, isoFwd)
	subIDs := sortedKeys(sub)
	for _, id := range subIDs {
		line("add chain %s %s", tbl, SubChain(id))
	}
	line("flush chain %s %s", tbl, Chain)
	line("flush chain %s %s", tbl, DNSChain)
	line("flush chain %s %s", tbl, InChain)
	line("flush chain %s %s", tbl, isoIn)
	line("flush chain %s %s", tbl, isoFwd)
	for _, id := range subIDs {
		line("flush chain %s %s", tbl, SubChain(id))
	}

	in := ""
	if len(ifaces) > 0 {
		in = "iifname { " + quoteAll(ifaces) + " } "
	}
	// Isolation (spec 6.7) and quarantine (6.8): a security rule, first of
	// all, never lifted. iso_in lets the device reach the router's DNS (53,
	// and the safe resolver's 553 after a redirect), DHCP, NTP and IPv6
	// neighbour discovery only; iso_fwd keeps it from the house's networks.
	line("add rule %s %s meta l4proto { tcp, udp } th dport { 53, %d } return", tbl, isoIn, SafePort)
	line("add rule %s %s udp dport { 67, 123, 547 } return", tbl, isoIn)
	line("add rule %s %s meta l4proto ipv6-icmp return", tbl, isoIn)
	line("add rule %s %s ct state established,related return", tbl, isoIn)
	line("add rule %s %s counter name %s drop", tbl, isoIn, isoCounter)
	line("add rule %s %s ip daddr @house_v4 counter name %s drop", tbl, isoFwd, isoCounter)
	line("add rule %s %s ip6 daddr @house_v6 counter name %s drop", tbl, isoFwd, isoCounter)
	// A quarantine match carries meta iiftype ether: a tunnel (WireGuard)
	// has no Ethernet header and is never held.
	held := "meta iiftype ether " + in + "ether saddr != @approved"
	if len(isolated) > 0 {
		line("add rule %s %s ether saddr @isolated jump %s", tbl, InChain, isoIn)
		line("add rule %s %s ether saddr @isolated jump %s", tbl, Chain, isoFwd)
	}
	if quarantine {
		line("add rule %s %s %s jump %s", tbl, InChain, held, isoIn)
		line("add rule %s %s %s jump %s", tbl, Chain, held, isoFwd)
	}
	if cfg.Filters.BlockDoT {
		// DoT (and DNS over QUIC on the same port) is a security rule: never lifted.
		line("add rule %s %s %smeta l4proto tcp tcp dport 853 counter name %s reject with tcp reset", tbl, Chain, in, dotCounter)
		line("add rule %s %s %sudp dport 853 counter name %s reject with icmpx admin-prohibited", tbl, Chain, in, dotCounter)
	} else if len(filtered) > 0 {
		// Filtered devices only: their DoT would walk around the safe resolver.
		line("add rule %s %s ether saddr @filtered_dns meta l4proto tcp tcp dport 853 counter name %s reject with tcp reset", tbl, Chain, dotCounter)
		line("add rule %s %s ether saddr @filtered_dns udp dport 853 counter name %s reject with icmpx admin-prohibited", tbl, Chain, dotCounter)
	}
	for _, l := range country {
		line("add rule %s %s %s", tbl, Chain, l)
	}
	// The flows the owner cut, matched as a rule's cut pairs are (R45).
	for _, f := range []struct{ fam, ip string }{{"4", "ip"}, {"6", "ip6"}} {
		m := "meta l4proto { tcp, udp } ether saddr . " + f.ip + " daddr . meta l4proto . th sport @" + OwnerCut(f.fam)
		line("add rule %s %s %s meta l4proto tcp reject with tcp reset", tbl, Chain, m)
		line("add rule %s %s %s reject with icmpx admin-prohibited", tbl, Chain, m)
	}
	if len(allowAll) > 0 {
		line("add rule %s %s ether saddr @allow_all return", tbl, Chain)
	}
	for _, l := range pause {
		line("add rule %s %s %s", tbl, Chain, l)
	}
	for _, l := range app {
		line("add rule %s %s %s", tbl, Chain, l)
	}
	for _, id := range subIDs {
		for _, l := range sub[id] {
			line("add rule %s %s %s", tbl, SubChain(id), l)
		}
	}
	if in != "" {
		if len(filtered) > 0 {
			line("add rule %s %s %sether saddr @filtered_dns meta l4proto { tcp, udp } th dport 53 redirect to :%d", tbl, DNSChain, in, SafePort)
		}
		if cfg.Filters.ForceDNS {
			line("add rule %s %s %sfib daddr type != local meta l4proto { tcp, udp } th dport 53 redirect to :53", tbl, DNSChain, in)
		}
	}
	// The bridge table drops frames between an isolated or held device and
	// the rest of the house on the bridges, where frames never reach the inet
	// forward hook (docs/stage2c-measured.md, sections 1 and 2). Frames to
	// and from the router use the bridge's input and output hooks.
	line("add table %s", IsoTable)
	line("add set %s isolated { type ether_addr; size %d; }", IsoTable, MaxIsolated)
	line("add set %s approved { type ether_addr; size %d; }", IsoTable, MaxApproved)
	line("add counter %s %s", IsoTable, isoCounter)
	line("add chain %s %s { type filter hook forward priority -5; policy accept; }", IsoTable, isoFwd)
	line("flush chain %s %s", IsoTable, isoFwd)
	elements(IsoTable, "isolated", isolated)
	elements(IsoTable, "approved", approved)
	if len(isolated) > 0 {
		line("add rule %s %s ether saddr @isolated counter name %s drop", IsoTable, isoFwd, isoCounter)
		line("add rule %s %s ether daddr @isolated counter name %s drop", IsoTable, isoFwd, isoCounter)
	}
	if quarantine {
		line("add rule %s %s meta ibrname { %s } ether saddr != @approved counter name %s drop", IsoTable, isoFwd, quoteAll(ifaces), isoCounter)
	}

	m := compiled{ids: ids, apps: appIDs, objects: map[string]bool{}, isoOver: isoOver, quarantine: quarantine}
	m.objects["counter "+isoCounter] = true
	for _, cc := range ccNames {
		m.ccs = append(m.ccs, strings.ToUpper(cc))
	}
	for _, id := range ids {
		m.objects["counter hits_"+id] = true
	}
	for _, cc := range ccNames {
		m.objects["set cc_"+cc+"_v4"], m.objects["set cc_"+cc+"_v6"] = true, true
	}
	for _, id := range appIDs {
		m.objects["set app_"+appName(id)+"_v4"], m.objects["set app_"+appName(id)+"_v6"] = true, true
	}
	for _, id := range orgIDs {
		m.objects["set app_"+appName(id)+"_org_v4"], m.objects["set app_"+appName(id)+"_org_v6"] = true, true
	}
	for _, id := range addrIDs {
		m.objects["set app_"+appName(id)+"_addr_v4"], m.objects["set app_"+appName(id)+"_addr_v6"] = true, true
	}
	for _, id := range spentIDs {
		m.objects["set spent_"+id] = true
	}
	if len(filtered) > 0 {
		m.objects["set filtered_dns"] = true
	}
	if cfg.Filters.BlockDoT || len(filtered) > 0 {
		m.objects["counter "+dotCounter] = true
	}
	for _, id := range memberIDs {
		m.objects["set members_"+id] = true
	}
	if len(allowAll) > 0 {
		m.objects["set allow_all"] = true
	}
	for _, id := range allowIDs {
		m.objects["set allow_"+id] = true
	}
	for _, id := range cutIDs {
		m.objects["set "+CutSet(id, "4")], m.objects["set "+CutSet(id, "6")] = true, true
	}
	m.objects["set "+OwnerCut("4")], m.objects["set "+OwnerCut("6")] = true, true
	for _, id := range subIDs {
		m.objects["chain "+SubChain(id)] = true
	}
	return b.String(), m
}

type comp struct {
	t        Targets
	loc      *time.Location
	at       time.Time
	east     int
	allowFor map[string]map[string]time.Time
	ccs      map[string]bool // lower-case codes in use
	apps     map[string]bool // catalog ids in use
	addrApps map[string]bool // catalog ids with static ranges in use
	// logGroup, when not 0, makes a country rule copy each try (rate
	// limited) to the capture's log group for the alerts.
	logGroup uint16
}

// dest is one destination set; in marks the inbound drop for a country rule
// for everyone (source in the set, connection forwarded by a port forward),
// and pair a cut set, matched by the device, the address, the protocol and
// the device's own port together.
type dest struct {
	fam, set string
	in, pair bool
	port     string // "tcp dport { ... }" or "udp dport { ... }": an app's ports
	only     string // a further match on the destination, e.g. resolverPorts
}

// resolverPorts limits a DoH resolver's address to DoH and DoT.
const resolverPorts = "meta l4proto { tcp, udp } th dport { 443, 853 }"

// rule returns the lines the rule puts in rules_fwd (without "add rule ...")
// and, when it has allowances, the lines of its own chain. The allowance is a
// `return` at the top of that chain, so it skips only this rule and the match
// of the rule's own lines has no ether condition that a tunnel client (no
// Ethernet header) would fail.
func (c *comp) rule(r Rule) (inline, own []string) {
	id := r.ID
	var pre []string
	switch {
	case r.Action == "limit":
		// Only the members that spent their minutes, until the next midnight.
		pre = append(pre, "ether saddr @spent_"+id)
	case r.Who == whoFiltered:
		pre = append(pre, "ether saddr @filtered_dns")
	case r.Who != "all":
		pre = append(pre, "ether saddr @members_"+id)
	}
	if !r.Until.IsZero() && r.Who == "all" {
		// Nothing is in a members set to time out, so the kernel ends the rule itself.
		pre = append(pre, fmt.Sprintf("meta time < %d", r.Until.Unix()))
	}
	var dests []dest
	switch r.What {
	case "country":
		for _, cc := range r.Targets {
			n := strings.ToLower(cc)
			c.ccs[n] = true
			dests = append(dests, dest{fam: "ip", set: "cc_" + n + "_v4"}, dest{fam: "ip6", set: "cc_" + n + "_v6"})
			if r.Who == "all" {
				dests = append(dests, dest{fam: "ip", set: "cc_" + n + "_v4", in: true}, dest{fam: "ip6", set: "cc_" + n + "_v6", in: true})
			}
		}
	case "app", "category":
		var ids []string
		var ports []enrich.Port
		if r.What == "app" {
			ids = r.Targets
		} else {
			for _, cat := range r.Targets {
				ids = append(ids, c.t.AppsIn(cat)...)
			}
		}
		done := map[string]bool{}
		for _, a := range ids {
			if done[a] {
				continue
			}
			done[a] = true
			c.apps[a] = true
			n := appName(a)
			dests = append(dests, dest{fam: "ip", set: "app_" + n + "_v4"}, dest{fam: "ip6", set: "app_" + n + "_v6"})
			if isOrgApp(a) {
				dests = append(dests, dest{fam: "ip", set: "app_" + n + "_org_v4"}, dest{fam: "ip6", set: "app_" + n + "_org_v6"})
			}
			if v4, v6 := c.t.AppAddrs(a); len(v4)+len(v6) > 0 {
				c.addrApps[a] = true
				// A static range is a resolver's address, which also answers
				// plain DNS on 53: only the ports DoH and DoT use are matched.
				dests = append(dests, dest{fam: "ip", set: "app_" + n + "_addr_v4", only: resolverPorts},
					dest{fam: "ip6", set: "app_" + n + "_addr_v6", only: resolverPorts})
			}
			ports = append(ports, c.t.AppPorts(a)...)
		}
		dests = append(dests, portDests(ports)...)
		dests = append(dests, dest{fam: "ip", set: CutSet(id, "4"), pair: true}, dest{fam: "ip6", set: CutSet(id, "6"), pair: true})
	case "all":
		dests = []dest{{}}
	}
	matches := Compile(r, c.loc, c.at, c.east)
	if r.Action == "limit" {
		// The engine puts a member in spent_<id> only on the days it counts.
		matches = nil
	}
	if matches == nil {
		matches = []TimeMatch{{}}
	}
	counter := "counter name hits_" + id
	var out []string
	for _, m := range matches {
		parts := append([]string{}, pre...)
		if tm := timeExpr(m); tm != "" {
			parts = append(parts, tm)
		}
		for _, d := range dests {
			p := append([]string{}, parts...)
			if d.in {
				// Not the device's own traffic: the source is in the country.
				out = append(out, strings.Join(append(p, "ct status dnat "+d.fam+" saddr @"+d.set), " ")+" "+counter+" drop")
				continue
			}
			switch {
			case d.port != "":
				// A port names its protocol, so only that protocol's reject.
				base := strings.Join(append(p, d.port), " ")
				if strings.HasPrefix(d.port, "tcp ") {
					out = append(out, base+" meta l4proto tcp "+counter+" reject with tcp reset")
				} else {
					out = append(out, base+" "+counter+" reject with icmpx admin-prohibited")
				}
				continue
			case d.pair:
				// th sport means a port only for TCP and UDP (R45): an ICMPv6
				// echo's type and code would read as 32768.
				p = append(p, "meta l4proto { tcp, udp } ether saddr . "+d.fam+" daddr . meta l4proto . th sport @"+d.set)
			case d.set != "":
				p = append(p, d.fam+" daddr @"+d.set)
				if d.only != "" {
					p = append(p, d.only)
				}
			}
			base := strings.Join(p, " ")
			if r.What == "country" && c.logGroup != 0 {
				// A separate line, so its limit never lets a packet past the
				// reject that follows (docs/stage2c-measured.md, section 4).
				out = append(out, fmt.Sprintf("%s limit rate %s log prefix \"%s%s\" group %d", base, tryRate, nflog.TryPrefix, id, c.logGroup))
			}
			out = append(out,
				base+" meta l4proto tcp "+counter+" reject with tcp reset",
				base+" "+counter+" reject with icmpx admin-prohibited")
		}
	}
	if len(c.allowFor[id]) > 0 && r.What != "country" {
		own = append([]string{"ether saddr @allow_" + id + " return"}, out...)
		return []string{"jump " + SubChain(id)}, own
	}
	return out, nil
}

// portDests turns an app's ports into one destination per protocol, ports in
// order: "tcp dport { 1194 }", "udp dport { 1194, 51820 }".
func portDests(ports []enrich.Port) []dest {
	by := map[uint8][]int{}
	for _, p := range ports {
		by[p.Proto] = append(by[p.Proto], int(p.Num))
	}
	var out []dest
	for _, pr := range []struct {
		num  uint8
		name string
	}{{6, "tcp"}, {17, "udp"}} {
		nums := by[pr.num]
		if len(nums) == 0 {
			continue
		}
		sort.Ints(nums)
		var s []string
		for i, n := range nums {
			if i > 0 && nums[i-1] == n {
				continue
			}
			s = append(s, strconv.Itoa(n))
		}
		out = append(out, dest{port: pr.name + " dport { " + strings.Join(s, ", ") + " }"})
	}
	return out
}

// houseIfaces is the house-side devices, only those whose names nft can take
// quoted (letters, digits, dot, dash, underscore, at most 15 characters).
func houseIfaces(t Targets) []string {
	var out []string
	for _, n := range t.HouseIfaces() {
		if ifaceName.MatchString(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

var ifaceName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)

func quoteAll(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return strings.Join(q, ", ")
}

// filteredMembers is the MACs of devices in groups that ask for filtered DNS.
func filteredMembers(cfg Config) []string {
	groups := map[string]bool{}
	for _, g := range cfg.Groups {
		if g.FilteredDNS {
			groups[g.ID] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range cfg.Devices {
		if groups[d.Group] && !seen[d.MAC] {
			seen[d.MAC] = true
			out = append(out, d.MAC)
		}
	}
	sort.Strings(out)
	return out
}

func timeExpr(m TimeMatch) string {
	var p []string
	switch len(m.Days) {
	case 0:
	case 1:
		p = append(p, fmt.Sprintf("meta day %q", m.Days[0].String()))
	default:
		var d []string
		for _, x := range m.Days {
			d = append(d, fmt.Sprintf("%q", x.String()))
		}
		p = append(p, "meta day { "+strings.Join(d, ", ")+" }")
	}
	if m.From != "" {
		p = append(p, fmt.Sprintf("meta hour %q-%q", m.From, m.To))
	}
	return strings.Join(p, " ")
}

// timeout is " timeout Ns" for an end in the future, "" for none.
func timeout(until, at time.Time) string {
	if until.IsZero() {
		return ""
	}
	s := int(math.Ceil(until.Sub(at).Seconds()))
	if s < 1 {
		s = 1
	}
	return fmt.Sprintf(" timeout %ds", s)
}

// appName turns a catalog id into set-name text: only [a-z0-9_], "." -> "_d_",
// "-" -> "_h_".
func appName(id string) string {
	id = strings.ToLower(id)
	id = strings.NewReplacer(".", "_d_", "-", "_h_").Replace(id)
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return '_'
	}, id)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// macList normalises, dedupes and sorts MACs, keeping at most max of them; it
// says how many valid ones were left out.
func macList(in []string, max int) ([]string, int) {
	seen := map[string]bool{}
	var out []string
	for _, m := range in {
		if mac, ok := normMAC(m); ok && !seen[mac] {
			seen[mac] = true
			out = append(out, mac)
		}
	}
	sort.Strings(out)
	if len(out) > max {
		return out[:max], len(out) - max
	}
	return out, 0
}

// privateHouse is what the house is taken to be when its networks could not
// be read, as IsLocal does.
var privateHouse = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("fc00::/7"),
}

// housePrefixes are the house's networks as set elements, v4 and v6.
func housePrefixes(t Targets) (v4, v6 []string) {
	ps := t.HousePrefixes()
	if len(ps) == 0 {
		ps = privateHouse
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if p.Addr().Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		p = p.Masked()
		if !p.IsValid() || seen[p.String()] {
			continue
		}
		seen[p.String()] = true
		if p.Addr().Is4() {
			v4 = append(v4, p.String())
		} else {
			v6 = append(v6, p.String())
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	return v4, v6
}
