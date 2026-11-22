package rules

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netryx/internal/enrich"
)

type fakeTargets struct {
	cc     map[string][2][]enrich.Range
	org    map[string][2][]enrich.Range
	cat    map[string][]string
	addrs  map[string][2][]enrich.Range
	ports  map[string][]enrich.Port
	ifaces []string
	house  []netip.Prefix
}

func (f fakeTargets) AppAddrs(app string) (v4, v6 []enrich.Range) {
	r := f.addrs[app]
	return r[0], r[1]
}
func (f fakeTargets) AppPorts(app string) []enrich.Port { return f.ports[app] }
func (f fakeTargets) HouseIfaces() []string             { return f.ifaces }
func (f fakeTargets) HousePrefixes() []netip.Prefix     { return f.house }

func (f fakeTargets) CountryRanges(cc string) (v4, v6 []enrich.Range) {
	r := f.cc[cc]
	return r[0], r[1]
}
func (f fakeTargets) OrgRanges(app string) (v4, v6 []enrich.Range) {
	r := f.org[app]
	return r[0], r[1]
}
func (f fakeTargets) AppsIn(c string) []string { return f.cat[c] }

func rg(lo, hi string) enrich.Range {
	return enrich.Range{Lo: netip.MustParseAddr(lo), Hi: netip.MustParseAddr(hi)}
}

var targets = fakeTargets{
	cc: map[string][2][]enrich.Range{
		"RU": {{rg("5.8.0.0", "5.8.255.255"), rg("5.9.1.1", "5.9.1.1")},
			{rg("2a00:1000::", "2a00:100f:ffff:ffff:ffff:ffff:ffff:ffff")}},
		"BY": {{rg("37.17.176.0", "37.17.191.255")}, nil},
	},
	org: map[string][2][]enrich.Range{
		"telegram": {{rg("149.154.160.0", "149.154.175.255")}, {rg("2001:b28:f23d::", "2001:b28:f23d:ffff:ffff:ffff:ffff:ffff")}},
	},
	cat: map[string][]string{"video": {"netflix", "youtube"}, "vpn": {"wireguard", "nordvpn"}, "doh": {"doh-google"}},
	addrs: map[string][2][]enrich.Range{
		"doh-google": {{rg("8.8.8.8", "8.8.8.8")}, {rg("2001:4860:4860::8888", "2001:4860:4860::8888")}},
	},
	ports:  map[string][]enrich.Port{"wireguard": {{Proto: 17, Num: 51820}}, "openvpn": {{Proto: 17, Num: 1194}, {Proto: 6, Num: 1194}}},
	ifaces: []string{"br-lan"},
	house:  []netip.Prefix{netip.MustParsePrefix("192.168.50.0/24"), netip.MustParsePrefix("fd21:b397:60f5::/48")},
}

const (
	tv     = "02:4e:58:00:00:01"
	laptop = "02:4e:58:00:00:02"
	phone  = "02:4e:58:00:00:03"
)

// summer is a Wednesday noon in BST.
func summer(t *testing.T) time.Time {
	return time.Date(2026, 7, 15, 12, 0, 0, 0, london(t))
}

func blockRU() Rule {
	return Rule{ID: "blockru", Enabled: true, Action: "block", What: "country", Targets: []string{"RU", "BY"}, Who: "all", From: -1, To: -1}
}

func onlyRU() Rule {
	r := blockRU()
	r.Targets = []string{"RU"}
	return r
}

func kidsYT() Rule {
	return Rule{ID: "kidsyt", Enabled: true, Action: "block", What: "app", Targets: []string{"youtube"},
		Who: "device", Members: []string{laptop}, From: -1, To: -1}
}

func pauseTV() Rule {
	return Rule{ID: "pausetv", Enabled: true, Action: "block", What: "all", Who: "device", Members: []string{tv}, From: -1, To: -1}
}

func script(t *testing.T, cfg Config) string {
	t.Helper()
	return Script(cfg, targets, london(t), summer(t), 0, nil, Extra{})
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".nft")
	if os.Getenv("NETRYX_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s: script differs from the golden.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestTheScriptMatchesTheGolden(t *testing.T) {
	at := summer(t)
	cases := []struct {
		name string
		cfg  Config
	}{
		{"country_everyone", Config{Rules: []Rule{blockRU()}}},
		{"app_device_schedule", Config{Rules: []Rule{func() Rule {
			r := kidsYT()
			r.Days = []time.Weekday{time.Monday}
			r.From, r.To = 21*60, 7*60
			return r
		}()}}},
		{"category_group", Config{Rules: []Rule{{ID: "kidsvideo", Enabled: true, Action: "block", What: "category",
			Targets: []string{"video"}, Who: "group", Group: "kids", Members: []string{phone, tv}, From: -1, To: -1}}}},
		{"pause_until", Config{Rules: []Rule{func() Rule {
			r := pauseTV()
			r.Until = at.Add(time.Hour)
			return r
		}()}}},
		{"days_only_multi", Config{Rules: []Rule{func() Rule {
			r := kidsYT()
			r.Days = []time.Weekday{time.Monday, time.Tuesday}
			return r
		}()}}},
		{"who_all_until", Config{Rules: []Rule{func() Rule {
			r := kidsYT()
			r.Who, r.Members = "all", nil
			r.Until = at.Add(time.Hour)
			return r
		}()}}},
		{"allowance_mixed", Config{
			Rules: []Rule{onlyRU(), pauseTV(), kidsYT()},
			Allows: []Allow{
				{ID: "a1", Rule: "kidsyt", Member: laptop, Until: at.Add(30 * time.Minute)},
				{ID: "a2", Rule: "", Member: phone, Until: at.Add(10 * time.Minute)},
				{ID: "a3", Rule: "blockru", Member: laptop, Until: at.Add(30 * time.Minute)}, // a country is never lifted
				{ID: "a4", Rule: "ghost", Member: laptop, Until: at.Add(30 * time.Minute)},   // no such rule
			},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			golden(t, c.name, script(t, c.cfg))
		})
	}
}

func TestAnInboundForwardFromABlockedCountryIsDropped(t *testing.T) {
	everyone := script(t, Config{Rules: []Rule{blockRU()}})
	for _, w := range []string{
		"ct status dnat ip saddr @cc_ru_v4 counter name hits_blockru drop",
		"ct status dnat ip6 saddr @cc_ru_v6 counter name hits_blockru drop",
		"ct status dnat ip saddr @cc_by_v4 counter name hits_blockru drop",
	} {
		if !strings.Contains(everyone, w) {
			t.Fatalf("a country rule for everyone must drop inbound forwards; missing %q", w)
		}
	}
	dev := blockRU()
	dev.ID, dev.Who, dev.Members = "ru_phone", "device", []string{phone}
	if s := script(t, Config{Rules: []Rule{dev}}); strings.Contains(s, "ct status dnat") {
		t.Fatalf("a device rule must not touch inbound forwards:\n%s", s)
	}
}

// ruleLines are the lines the rules put in the chains, without the ones every
// ruleset carries (isolation and the owner's cut set).
func ruleLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "add rule ") && !strings.HasPrefix(l, "add rule "+Table+" "+isoIn+" ") &&
			!strings.HasPrefix(l, "add rule "+Table+" "+isoFwd+" ") && !strings.HasPrefix(l, "add rule "+IsoTable+" ") &&
			!strings.Contains(l, " @"+OwnerCut("4")+" ") && !strings.Contains(l, " @"+OwnerCut("6")+" ") {
			out = append(out, l)
		}
	}
	return out
}

func TestOrderPutsCountriesBeforeAllowances(t *testing.T) {
	at := summer(t)
	s := script(t, Config{
		Rules:  []Rule{kidsYT(), pauseTV(), blockRU()}, // config order must not matter
		Allows: []Allow{{ID: "a", Rule: "", Member: phone, Until: at.Add(time.Hour)}},
	})
	lines := ruleLines(s)
	lastCountry, ret, firstPause, firstApp := -1, -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "hits_blockru"):
			lastCountry = i
		case strings.HasSuffix(l, "@allow_all return"):
			if ret < 0 {
				ret = i
			}
		case strings.Contains(l, "hits_pausetv"):
			if firstPause < 0 {
				firstPause = i
			}
		case strings.Contains(l, "hits_kidsyt"):
			if firstApp < 0 {
				firstApp = i
			}
		}
	}
	if lastCountry < 0 || ret < 0 || firstPause < 0 || firstApp < 0 {
		t.Fatalf("missing a section: country %d return %d pause %d app %d\n%s", lastCountry, ret, firstPause, firstApp, s)
	}
	if !(lastCountry < ret && ret < firstPause && firstPause < firstApp) {
		t.Fatalf("order must be country < allowance return < pause < app, got %d %d %d %d\n%s", lastCountry, ret, firstPause, firstApp, s)
	}
}

// The flows the owner cut with the console's button are rejected with the
// security rules: after the countries and the DoT block, before the blanket
// allowance's return and before any rule's own chain with its allowance, so
// no allowance lets a cut flow back in.
func TestTheOwnersCutsComeBeforeEveryAllowance(t *testing.T) {
	at := summer(t)
	s := script(t, Config{
		Rules: []Rule{kidsYT(), pauseTV(), blockRU()},
		Allows: []Allow{{ID: "a", Rule: "", Member: phone, Until: at.Add(time.Hour)},
			{ID: "b", Rule: "kidsyt", Member: laptop, Until: at.Add(time.Hour)}},
		Filters: Filters{BlockDoT: true, YouTube: "off"},
	})
	var chain []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "add rule "+Table+" "+Chain+" ") {
			chain = append(chain, l)
		}
	}
	lastSecurity, firstCut, lastCut, firstAllow := -1, -1, -1, -1
	for i, l := range chain {
		switch {
		case strings.Contains(l, "hits_blockru") || strings.Contains(l, dotCounter):
			lastSecurity = i
		case strings.Contains(l, " @"+OwnerCut("4")+" ") || strings.Contains(l, " @"+OwnerCut("6")+" "):
			if firstCut < 0 {
				firstCut = i
			}
			lastCut = i
		case strings.HasSuffix(l, "@allow_all return") || strings.Contains(l, "jump "+SubChain("")):
			if firstAllow < 0 {
				firstAllow = i
			}
		}
	}
	if lastSecurity < 0 || firstCut < 0 || firstAllow < 0 || lastCut-firstCut != 3 {
		t.Fatalf("missing a section: security %d cuts %d-%d allowance %d\n%s", lastSecurity, firstCut, lastCut, firstAllow, s)
	}
	if !(lastSecurity < firstCut && lastCut < firstAllow) {
		t.Fatalf("order must be security < owner's cuts < allowances, got %d < %d-%d < %d\n%s", lastSecurity, firstCut, lastCut, firstAllow, s)
	}
}

func TestAnAllowanceLiftsOnlyTheRuleItNames(t *testing.T) {
	at := summer(t)
	other := kidsYT()
	other.ID = "kidsnf"
	other.Targets = []string{"netflix"}
	s := script(t, Config{
		Rules: []Rule{blockRU(), kidsYT(), other, pauseTV()},
		Allows: []Allow{
			{ID: "a", Rule: "kidsyt", Member: laptop, Until: at.Add(time.Hour)},
			{ID: "b", Rule: "blockru", Member: laptop, Until: at.Add(time.Hour)},
		},
	})
	returns := 0
	for _, l := range ruleLines(s) {
		inOwn := strings.HasPrefix(l, "add rule "+Table+" r_kidsyt ")
		switch {
		case strings.HasSuffix(l, " return"):
			returns++
			if l != "add rule "+Table+" r_kidsyt ether saddr @allow_kidsyt return" {
				t.Fatalf("the only return is the first line of the chain of the rule the allowance names: %s", l)
			}
		case strings.Contains(l, "@allow_"), strings.Contains(l, "!="):
			t.Fatalf("an allowance reached a line it should not: %s", l)
		case strings.Contains(l, "hits_kidsyt") && !inOwn:
			t.Fatalf("the rule with an allowance lives in its own chain: %s", l)
		case strings.Contains(l, "jump ") && !strings.HasSuffix(l, "jump r_kidsyt"):
			t.Fatalf("only kidsyt has a chain of its own: %s", l)
		}
	}
	if returns != 1 {
		t.Fatalf("one allowance, one return, got %d", returns)
	}
	if strings.Contains(s, "allow_blockru") || strings.Contains(s, "r_blockru") {
		t.Fatalf("a country rule is never lifted by an allowance:\n%s", s)
	}
	// The jump stands where the rule's lines would: after the pause, before nothing else of the rule.
	lines := ruleLines(s)
	var jump, pause int
	for i, l := range lines {
		if strings.HasSuffix(l, "jump r_kidsyt") {
			jump = i
		}
		if strings.Contains(l, "hits_pausetv") {
			pause = i
		}
	}
	if jump < pause {
		t.Fatalf("an app rule's jump comes after the pauses")
	}
}

func TestAWhoAllRuleWithAnAllowanceStillMatchesWithoutAnEtherCondition(t *testing.T) {
	at := summer(t)
	all := pauseTV()
	all.ID, all.Who, all.Members = "pauseall", "all", nil
	s := script(t, Config{
		Rules:  []Rule{all},
		Allows: []Allow{{ID: "a", Rule: "pauseall", Member: phone, Until: at.Add(time.Hour)}},
	})
	own := 0
	for _, l := range ruleLines(s) {
		if !strings.HasPrefix(l, "add rule "+Table+" r_pauseall ") || strings.HasSuffix(l, " return") {
			continue
		}
		own++
		if strings.Contains(l, "ether") {
			t.Fatalf("a rule for everyone must also catch a tunnel client (no Ethernet header), so it has no ether condition: %s", l)
		}
	}
	if own != 2 {
		t.Fatalf("want the 2 reject lines in r_pauseall, got %d\n%s", own, s)
	}
	if !strings.Contains(s, "add rule "+Table+" r_pauseall ether saddr @allow_pauseall return\n") {
		t.Fatalf("the chain must start by returning the allowed devices:\n%s", s)
	}
}

func TestAnAllowanceAndItsRuleNameNothingElse(t *testing.T) {
	// A name that would be mangled inside an nft name is never emitted, and an
	// allowance naming it matches nothing.
	bad := kidsYT()
	bad.ID = "kids-yt"
	at := summer(t)
	s := script(t, Config{
		Rules:  []Rule{bad, pauseTV()},
		Allows: []Allow{{ID: "a", Rule: "kids-yt", Member: laptop, Until: at.Add(time.Hour)}, {ID: "b", Rule: "kids_yt", Member: laptop, Until: at.Add(time.Hour)}},
	})
	for _, w := range []string{"kids", "allow_", Table + " r_"} {
		if strings.Contains(s, w) {
			t.Fatalf("%q must not appear:\n%s", w, s)
		}
	}
}

func TestAnInactiveRuleIsLeftOut(t *testing.T) {
	at := summer(t)
	off := kidsYT()
	off.ID, off.Enabled = "off", false
	bad := kidsYT()
	bad.ID, bad.Err = "bad", "unknown app"
	gone := pauseTV()
	gone.ID, gone.Until = "gone", at.Add(-time.Minute)
	s := script(t, Config{
		Rules:  []Rule{off, bad, gone, pauseTV()},
		Allows: []Allow{{ID: "old", Rule: "pausetv", Member: laptop, Until: at.Add(-time.Second)}},
	})
	for _, w := range []string{"hits_off", "members_off", "hits_bad", "members_bad", "hits_gone", "allow_pausetv", "r_pausetv"} {
		if strings.Contains(s, w) {
			t.Fatalf("%q must not appear in the script:\n%s", w, s)
		}
	}
	if !strings.Contains(s, "hits_pausetv") {
		t.Fatal("the live rule must stay")
	}
}

func TestACountrySetThatIsNotFreshWritesNoElements(t *testing.T) {
	s := Script(Config{Rules: []Rule{blockRU()}}, targets, london(t), summer(t), 0, map[string]bool{"BY": true}, Extra{})
	if strings.Contains(s, "add element inet netryx_rules cc_ru") {
		t.Fatalf("a set that did not change must keep the kernel's elements:\n%s", s)
	}
	if strings.Contains(s, "flush set inet netryx_rules cc_ru") {
		t.Fatalf("a set that did not change must not be flushed:\n%s", s)
	}
	if !strings.Contains(s, "add set inet netryx_rules cc_ru_v4") {
		t.Fatal("the set is still declared, so the rules can name it")
	}
	if !strings.Contains(s, "add element inet netryx_rules cc_by_v4 { 37.17.176.0-37.17.191.255 }") {
		t.Fatal("a fresh set is written in full")
	}
}

func TestSeveralTimeMatchesShareOneCounter(t *testing.T) {
	r := kidsYT()
	r.Days = []time.Weekday{time.Monday}
	r.From, r.To = 21*60, 7*60
	s := script(t, Config{Rules: []Rule{r}})
	if n := strings.Count(s, "add counter inet netryx_rules hits_kidsyt\n"); n != 1 {
		t.Fatalf("one counter per rule, got %d", n)
	}
	if n := strings.Count(s, "counter name hits_kidsyt "); n != 16 {
		t.Fatalf("2 windows x (app set, cut set) x v4/v6 x tcp/other = 16 lines on that counter, got %d", n)
	}
}

func TestSetNamesUseOnlySafeCharacters(t *testing.T) {
	r := kidsYT()
	r.Targets = []string{"disney.plus", "hbo-max"}
	s := script(t, Config{Rules: []Rule{r}})
	for _, w := range []string{"app_disney_d_plus_v4", "app_hbo_h_max_v6"} {
		if !strings.Contains(s, "@"+w) {
			t.Fatalf("missing %s:\n%s", w, s)
		}
	}
	for _, l := range strings.Split(s, "\n") {
		for _, f := range strings.Fields(l) {
			if strings.HasPrefix(f, "@") && strings.Trim(f[1:], "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
				t.Fatalf("set name %q has an unsafe character", f)
			}
		}
	}
}

func TestAnAppWithHardCodedAddressesAlsoGetsItsOrganisationsRanges(t *testing.T) {
	r := kidsYT()
	r.Targets = []string{"telegram", "youtube"}
	s := script(t, Config{Rules: []Rule{r}})
	for _, w := range []string{
		"add set inet netryx_rules app_telegram_org_v4 { type ipv4_addr; flags interval; auto-merge; }",
		"add element inet netryx_rules app_telegram_org_v4 { 149.154.160.0-149.154.175.255 }",
		"add element inet netryx_rules app_telegram_org_v6 { 2001:b28:f23d::-2001:b28:f23d:ffff:ffff:ffff:ffff:ffff }",
		"ip daddr @app_telegram_v4 ",
		"ip daddr @app_telegram_org_v4 ",
		"ip6 daddr @app_telegram_org_v6 ",
	} {
		if !strings.Contains(s, w) {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
	if strings.Contains(s, "app_youtube_org") {
		t.Fatal("only apps named in orgApps carry organisation sets")
	}
}

func TestTheScriptNeverDeletesTheTable(t *testing.T) {
	s := script(t, Config{Rules: []Rule{blockRU(), kidsYT(), pauseTV()}})
	if strings.Contains(s, "delete") || strings.Contains(s, "destroy") {
		t.Fatalf("the table outlives the process; the script must only add:\n%s", s)
	}
}

func TestAnEmptyConfigStillDeclaresTheTableAndEmptiesTheChain(t *testing.T) {
	s := script(t, Config{})
	if !strings.Contains(s, "add table inet netryx_rules\n") || !strings.Contains(s, "flush chain inet netryx_rules rules_fwd\n") {
		t.Fatalf("an empty config must still clear the chain, or a removed rule would keep blocking:\n%s", s)
	}
	if len(ruleLines(s)) != 0 {
		t.Fatal("no rules, no rule lines")
	}
}

func TestADeviceRuleNeverAppliesToEveryone(t *testing.T) {
	dev := blockRU()
	dev.ID, dev.Who, dev.Members = "ru_phone", "device", []string{phone}
	s := script(t, Config{Rules: []Rule{dev, kidsYT(), pauseTV()}})
	for _, l := range ruleLines(s) {
		if !strings.Contains(l, "ether saddr @members_") {
			t.Fatalf("a rule for a device must match its members: %s", l)
		}
	}
	// and a rule for everyone has no device match at all
	for _, l := range ruleLines(script(t, Config{Rules: []Rule{blockRU()}})) {
		if strings.Contains(l, "members_") {
			t.Fatalf("a rule for everyone has no member set: %s", l)
		}
	}
}

// A set that is filled again on a table that already exists must be emptied in
// the same transaction first, or an interval set rejects a range that overlaps
// the old one and the whole reload fails (loaded twice and with changed
// overlapping ranges in the lab VM, nft 1.1.6).
func TestEverySetIsFlushedBeforeItIsFilled(t *testing.T) {
	r := kidsYT()
	r.Targets = []string{"telegram"}
	at := summer(t)
	s := script(t, Config{
		Rules:  []Rule{r, blockRU(), pauseTV()},
		Allows: []Allow{{ID: "a", Rule: "kidsyt", Member: laptop, Until: at.Add(time.Hour)}, {ID: "b", Rule: "", Member: phone, Until: at.Add(time.Hour)}},
	})
	flushed := map[string]bool{}
	filled := 0
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		switch {
		case strings.HasPrefix(l, "flush set "+Table+" "):
			flushed[f[4]] = true
		case strings.HasPrefix(l, "add element "+Table+" "):
			filled++
			if !flushed[f[4]] {
				t.Fatalf("%s is filled without being flushed first:\n%s", f[4], s)
			}
		}
	}
	if filled < 7 {
		t.Fatalf("only %d sets filled; the case should cover countries, the org set, members and allowances", filled)
	}
}

func TestForcedDNSRedirectsTheHouseAndSparesTheRouter(t *testing.T) {
	cfg := Config{Filters: Filters{ForceDNS: true, BlockDoT: true, YouTube: "off"}}
	golden(t, "dns_forced", Script(cfg, targets, london(t), summer(t), 0, nil, Extra{}))
}

func filteredConfig() Config {
	return Config{
		Filters: Filters{ForceDNS: true, SafeSearch: true, YouTube: "moderate"},
		Groups:  []Group{{ID: "kids", Name: "Діти", FilteredDNS: true}, {ID: "adults", Name: "Дорослі"}},
		Devices: []Device{{MAC: phone, Group: "kids"}, {MAC: laptop, Group: "adults"}, {MAC: tv}},
	}
}

func TestFilteredMembersGoToTheSafeResolverOnlyWhileItAnswers(t *testing.T) {
	golden(t, "dns_filtered", Script(filteredConfig(), targets, london(t), summer(t), 0, nil, Extra{SafeDNS: true}))
	down := Script(filteredConfig(), targets, london(t), summer(t), 0, nil, Extra{SafeDNS: false})
	if strings.Contains(down, ":553") || strings.Contains(down, "filtered_dns") {
		t.Fatalf("the safe resolver is down but its redirect is in the script:\n%s", down)
	}
	if !strings.Contains(down, "redirect to :53\n") {
		t.Fatalf("with the safe resolver down, forced DNS must still reach the main resolver:\n%s", down)
	}
	off := filteredConfig()
	off.Filters.SafeSearch, off.Filters.YouTube = false, "off"
	if s := Script(off, targets, london(t), summer(t), 0, nil, Extra{SafeDNS: true}); strings.Contains(s, ":553") {
		t.Fatalf("no safe search and no YouTube level, yet a redirect to the safe resolver:\n%s", s)
	}
}

func TestDoTIsRejectedAsASecurityRule(t *testing.T) {
	cfg := Config{
		Rules:   []Rule{kidsYT()},
		Allows:  []Allow{{ID: "a1", Rule: "", Member: laptop, Until: summer(t).Add(time.Hour)}},
		Filters: Filters{BlockDoT: true, YouTube: "off"},
	}
	lines := ruleLines(Script(cfg, targets, london(t), summer(t), 0, nil, Extra{}))
	dot, ret := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "dport 853") && dot < 0 {
			dot = i
		}
		if strings.Contains(l, "@allow_all return") {
			ret = i
		}
	}
	if dot < 0 || ret < 0 || dot > ret {
		t.Fatalf("the DoT line (%d) must come before every allowance (%d):\n%s", dot, ret, strings.Join(lines, "\n"))
	}
}

func TestVPNPortsAndDoHAddressesShareTheRulesCounter(t *testing.T) {
	novpn := Rule{ID: "novpn", Enabled: true, Action: "block", What: "category", Targets: []string{"vpn"}, Who: "all", From: -1, To: -1}
	nodoh := Rule{ID: "nodoh", Enabled: true, Action: "block", What: "category", Targets: []string{"doh"}, Who: "all", From: -1, To: -1}
	s := script(t, Config{Rules: []Rule{novpn, nodoh}})
	for _, want := range []string{
		"add rule inet netryx_rules rules_fwd udp dport { 51820 } counter name hits_novpn reject with icmpx admin-prohibited",
		"add set inet netryx_rules app_doh_h_google_addr_v4 { type ipv4_addr; flags interval; auto-merge; }",
		"add element inet netryx_rules app_doh_h_google_addr_v4 { 8.8.8.8 }",
		"add element inet netryx_rules app_doh_h_google_addr_v6 { 2001:4860:4860::8888 }",
		"add rule inet netryx_rules rules_fwd ip daddr @app_doh_h_google_addr_v4 meta l4proto { tcp, udp } th dport { 443, 853 } meta l4proto tcp counter name hits_nodoh reject with tcp reset",
	} {
		if !strings.Contains(s, want+"\n") {
			t.Fatalf("missing line:\n%s\n--- script ---\n%s", want, s)
		}
	}
	if strings.Contains(s, "udp dport { 51820 } meta l4proto tcp") {
		t.Fatalf("a UDP port line carries a TCP reset that can never match:\n%s", s)
	}
}

func TestALimitBlocksOnlyTheSpentMembersUntilMidnight(t *testing.T) {
	loc := london(t)
	// 2026-10-25 is the 25-hour day in London; 00:30 BST is 2026-10-24 23:30 UTC.
	at := time.Date(2026, 10, 25, 0, 30, 0, 0, loc)
	until := NextMidnight(loc, at)
	if got := until.Sub(at); got != 24*time.Hour+30*time.Minute {
		t.Fatalf("NextMidnight on the 25-hour day is %v ahead; want 24h30m", got)
	}
	lim := Rule{ID: "ttlimit", Enabled: true, Action: "limit", What: "app", Targets: []string{"youtube"},
		Who: "device", Members: []string{phone, laptop}, Minutes: 60, From: -1, To: -1}
	s := Script(Config{Rules: []Rule{lim}}, targets, loc, at, 0, nil, Extra{Spent: map[string]map[string]time.Time{"ttlimit": {phone: until}}})
	golden(t, "limit_spent", s)
	if strings.Contains(s, laptop) {
		t.Fatalf("a member that has not spent its minutes is in the script:\n%s", s)
	}
}

func TestBlockDoHIsAnImplicitRule(t *testing.T) {
	s := script(t, Config{Filters: Filters{BlockDoH: true, YouTube: "off"}})
	for _, want := range []string{
		"add counter inet netryx_rules hits_f_doh",
		"add rule inet netryx_rules rules_fwd ip daddr @app_doh_h_google_addr_v4 meta l4proto { tcp, udp } th dport { 443, 853 } meta l4proto tcp counter name hits_f_doh reject with tcp reset",
	} {
		if !strings.Contains(s, want+"\n") {
			t.Fatalf("missing line:\n%s\n--- script ---\n%s", want, s)
		}
	}
}

func TestASpentKeyThatIsNotAMACNeverReachesTheScript(t *testing.T) {
	lim := Rule{ID: "ttlimit", Enabled: true, Action: "limit", What: "app", Targets: []string{"youtube"},
		Who: "all", Minutes: 60, From: -1, To: -1}
	until := summer(t).Add(time.Hour)
	s := Script(Config{Rules: []Rule{lim}}, targets, london(t), summer(t), 0, nil,
		Extra{Spent: map[string]map[string]time.Time{"ttlimit": {"192.168.1.5": until, "router": until, phone: until}}})
	if strings.Contains(s, "192.168.1.5") || strings.Contains(s, "router timeout") || !strings.Contains(s, phone+" timeout") {
		t.Fatalf("spent_ttlimit must hold MACs only:\n%s", s)
	}
}

// A DoH resolver's address also answers plain DNS on 53. Without forced DNS a
// device that has 8.8.8.8 written in it would lose DNS altogether, so the
// address sets of the doh category match only the ports DoH and DoT use.
func TestBlockingDoHByAddressLeavesPlainDNSAlone(t *testing.T) {
	s := script(t, Config{Filters: Filters{BlockDoH: true, YouTube: "off"}})
	if strings.Contains(s, "ip daddr @app_doh_h_google_addr_v4 meta l4proto tcp counter") ||
		strings.Contains(s, "ip daddr @app_doh_h_google_addr_v4 counter") {
		t.Fatalf("a DoH address is blocked on every port, plain DNS on 53 included:\n%s", s)
	}
	want := "ip daddr @app_doh_h_google_addr_v4 meta l4proto { tcp, udp } th dport { 443, 853 }"
	if !strings.Contains(s, want) {
		t.Fatalf("want %q in\n%s", want, s)
	}
}

// The DoH block is one of the owner's filters: no allowance lifts it, not a
// blanket "30 more minutes" and not the agent's, so it sits with the security
// rules, before every allowance.
func TestNoAllowanceLiftsTheDoHFilter(t *testing.T) {
	cfg := Config{
		Filters: Filters{BlockDoH: true, YouTube: "off"},
		Allows: []Allow{
			{ID: "a1", Rule: "", Member: laptop, Until: summer(t).Add(time.Hour)},
			{ID: "agent_x", Rule: FilterDoH, Member: phone, Until: summer(t).Add(time.Hour)},
		},
	}
	s := script(t, cfg)
	lines := ruleLines(s)
	doh, ret := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "hits_f_doh") && doh < 0 {
			doh = i
		}
		if strings.Contains(l, "@allow_all return") {
			ret = i
		}
	}
	if doh < 0 || ret < 0 || doh > ret {
		t.Fatalf("the DoH filter (%d) must come before every allowance (%d):\n%s", doh, ret, s)
	}
	if strings.Contains(s, "allow_f_doh") || strings.Contains(s, "r_f_doh") {
		t.Fatalf("an allowance names the DoH filter and is honoured:\n%s", s)
	}
}

// A device in a filtered group that keeps DoH or DoT walks around the safe
// resolver (Chrome goes to DoH by itself when 8.8.8.8 is set by hand), so while
// it is sent to the safe resolver its DoT and DoH are closed too.
func TestFilteredDevicesCannotWalkAroundTheFilter(t *testing.T) {
	s := Script(filteredConfig(), targets, london(t), summer(t), 0, nil, Extra{SafeDNS: true})
	for _, want := range []string{
		"ether saddr @filtered_dns meta l4proto tcp tcp dport 853 counter name hits_f_dot reject with tcp reset",
		"ether saddr @filtered_dns ip daddr @app_doh_h_google_addr_v4 meta l4proto { tcp, udp } th dport { 443, 853 }",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
	down := Script(filteredConfig(), targets, london(t), summer(t), 0, nil, Extra{SafeDNS: false})
	if strings.Contains(down, "@filtered_dns") {
		t.Fatalf("with the filter down, filtered devices keep plain DNS and nothing names them:\n%s", down)
	}
}

const camera = "02:4e:58:00:00:04"

// lineIndex is the index of the first script line containing sub, or -1.
func lineIndex(s, sub string) int {
	for i, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

func isoScript(t *testing.T, cfg Config, x Extra) string {
	t.Helper()
	return Script(cfg, targets, london(t), summer(t), 0, nil, x)
}

// spec 6.7: an isolated device keeps the internet and the router's DNS
// (redirected or not, 53 and the safe resolver's 553), DHCP, NTP and IPv6
// neighbour discovery; everything else to the router is dropped, and so is
// everything to the other house networks.
func TestAnIsolatedDeviceReachesOnlyTheRoutersServices(t *testing.T) {
	s := isoScript(t, Config{}, Extra{Isolated: []string{camera}})
	golden(t, "iso", s)
	want := []string{
		"add rule inet netryx_rules iso_in meta l4proto { tcp, udp } th dport { 53, 553 } return",
		"add rule inet netryx_rules iso_in udp dport { 67, 123, 547 } return",
		"add rule inet netryx_rules iso_in meta l4proto ipv6-icmp return",
		"add rule inet netryx_rules iso_in ct state established,related return",
		"add rule inet netryx_rules iso_in counter name hits_iso drop",
	}
	last := -1
	for _, w := range want {
		i := lineIndex(s, w)
		if i <= last {
			t.Fatalf("%q missing or out of order (at %d after %d):\n%s", w, i, last, s)
		}
		last = i
	}
	for _, w := range []string{
		"add chain inet netryx_rules rules_in { type filter hook input priority filter - 5; policy accept; }",
		"add element inet netryx_rules isolated { " + camera + " }",
		"add rule inet netryx_rules rules_in ether saddr @isolated jump iso_in",
		"add rule inet netryx_rules rules_fwd ether saddr @isolated jump iso_fwd",
		"add rule inet netryx_rules iso_fwd ip daddr @house_v4 counter name hits_iso drop",
		"add rule inet netryx_rules iso_fwd ip6 daddr @house_v6 counter name hits_iso drop",
		"add element inet netryx_rules house_v4 { 192.168.50.0/24 }",
	} {
		if !strings.Contains(s, w+"\n") {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
}

func TestIsolationComesBeforeEveryAllowance(t *testing.T) {
	cfg := Config{Rules: []Rule{blockRU(), kidsYT()}, Filters: Filters{BlockDoT: true, YouTube: "off"},
		Allows: []Allow{{ID: "a", Member: camera, Until: summer(t).Add(time.Hour)}}}
	s := isoScript(t, cfg, Extra{Isolated: []string{camera}})
	iso := lineIndex(s, "rules_fwd ether saddr @isolated jump iso_fwd")
	for _, later := range []string{"tcp dport 853", "counter name hits_blockru", "@allow_all return", "counter name hits_kidsyt"} {
		if i := lineIndex(s, later); iso < 0 || i < iso {
			t.Fatalf("isolation (line %d) must come before %q (line %d):\n%s", iso, later, i, s)
		}
	}
}

func TestTheBridgeTableDropsFramesBetweenAnIsolatedDeviceAndTheHouse(t *testing.T) {
	s := isoScript(t, Config{}, Extra{Isolated: []string{camera}})
	for _, w := range []string{
		"add table bridge netryx_iso",
		"add chain bridge netryx_iso iso_fwd { type filter hook forward priority -5; policy accept; }",
		"flush chain bridge netryx_iso iso_fwd",
		"add element bridge netryx_iso isolated { " + camera + " }",
		"add rule bridge netryx_iso iso_fwd ether saddr @isolated counter name hits_iso drop",
		"add rule bridge netryx_iso iso_fwd ether daddr @isolated counter name hits_iso drop",
	} {
		if !strings.Contains(s, w+"\n") {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
}

// Turning isolation off must take its lines out of the kernel, so the chains
// are always declared and flushed; without an isolated device they hold no
// match.
func TestNoIsolatedDeviceMeansNoIsolationLines(t *testing.T) {
	s := isoScript(t, Config{}, Extra{})
	for _, w := range []string{"flush chain inet netryx_rules rules_in", "flush chain inet netryx_rules iso_in",
		"flush chain bridge netryx_iso iso_fwd", "flush set inet netryx_rules isolated"} {
		if !strings.Contains(s, w+"\n") {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
	for _, no := range []string{"jump iso_in", "jump iso_fwd", "@isolated counter", "!= @approved"} {
		if strings.Contains(s, no) {
			t.Fatalf("nothing is isolated but %q is in the script:\n%s", no, s)
		}
	}
}

func TestTheIsolatedSetIsBounded(t *testing.T) {
	var macs []string
	for i := 0; i < 5000; i++ {
		macs = append(macs, fmt.Sprintf("02:00:00:00:%02x:%02x", i/256, i%256))
	}
	macs = append(macs, "not-a-mac")
	s := isoScript(t, Config{}, Extra{Isolated: macs})
	n := strings.Count(s[strings.Index(s, "add element inet netryx_rules isolated"):strings.Index(s, "add table bridge")], "02:00:00:00:")
	if n != MaxIsolated {
		t.Fatalf("%d MACs in the inet isolated set; want %d", n, MaxIsolated)
	}
	if strings.Contains(s, "not-a-mac") {
		t.Fatal("an element that is not a MAC reached the script")
	}
}

// spec 6.8: a MAC never seen before gets the isolation from its first frame:
// everything on the house side that is not approved is held, with no tick to
// wait for and no set that grows.
func TestQuarantineHoldsAnUnknownSourceFromItsFirstFrame(t *testing.T) {
	s := isoScript(t, Config{}, Extra{Approved: []string{tv, laptop}, Quarantine: true})
	golden(t, "quarantine", s)
	for _, w := range []string{
		"add element inet netryx_rules approved { " + tv + ", " + laptop + " }",
		`add rule inet netryx_rules rules_in meta iiftype ether iifname { "br-lan" } ether saddr != @approved jump iso_in`,
		`add rule inet netryx_rules rules_fwd meta iiftype ether iifname { "br-lan" } ether saddr != @approved jump iso_fwd`,
		`add rule bridge netryx_iso iso_fwd meta ibrname { "br-lan" } ether saddr != @approved counter name hits_iso drop`,
	} {
		if !strings.Contains(s, w+"\n") {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
}

// A WireGuard peer or a routed tunnel has no Ethernet header: the quarantine
// must never match it, or the house's own VPN clients would be held.
func TestATunnelIsNeverQuarantined(t *testing.T) {
	s := isoScript(t, Config{}, Extra{Approved: []string{tv}, Quarantine: true})
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "inet netryx_rules") && strings.Contains(l, "!= @approved") && !strings.Contains(l, "meta iiftype ether") {
			t.Fatalf("a quarantine line without meta iiftype ether: %q", l)
		}
	}
}

// An empty approved set would hold the whole house; it never turns the
// quarantine on (spec 3: a restart must not block what was open).
func TestAnEmptyApprovedSetNeverHoldsTheHouse(t *testing.T) {
	s := isoScript(t, Config{}, Extra{Quarantine: true})
	if strings.Contains(s, "!= @approved") {
		t.Fatalf("quarantine with nothing approved:\n%s", s)
	}
}

// spec 6.9: a try towards a blocked country is told; the kernel copies it to
// netryx's log group, rate limited, before the reject (docs/stage2c-measured.md
// section 4), and only while that alert is on.
func TestACountryRuleLogsTriesOnlyWithAlertsOn(t *testing.T) {
	cfg := Config{Rules: []Rule{onlyRU()}, Alerts: Alerts{BlockedCountry: true}}
	s := isoScript(t, cfg, Extra{LogGroup: 7})
	try := `add rule inet netryx_rules rules_fwd ip daddr @cc_ru_v4 limit rate 10/minute log prefix "netryx-try blockru" group 7`
	rej := "add rule inet netryx_rules rules_fwd ip daddr @cc_ru_v4 meta l4proto tcp counter name hits_blockru reject with tcp reset"
	ti, ri := lineIndex(s, try), lineIndex(s, rej)
	if ti < 0 || ri < 0 || ti > ri {
		t.Fatalf("the try line (%d) must come right before the reject (%d):\n%s", ti, ri, s)
	}
	if !strings.Contains(s, `add rule inet netryx_rules rules_fwd ip6 daddr @cc_ru_v6 limit rate 10/minute log prefix "netryx-try blockru" group 7`) {
		t.Fatalf("no try line for IPv6:\n%s", s)
	}
	if strings.Contains(s, "ct status dnat ip saddr @cc_ru_v4 limit") {
		t.Fatal("an inbound drop is not a device's try")
	}
	cfg.Alerts.BlockedCountry = false
	if s := isoScript(t, cfg, Extra{LogGroup: 7}); strings.Contains(s, "netryx-try") {
		t.Fatalf("alerts off, yet tries are logged:\n%s", s)
	}
	cfg.Alerts.BlockedCountry = true
	if s := isoScript(t, cfg, Extra{}); strings.Contains(s, "netryx-try") {
		t.Fatalf("no log group (capture off), yet tries are logged:\n%s", s)
	}
}
