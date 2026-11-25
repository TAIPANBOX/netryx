package nflog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The rule is the security boundary of the whole design: if it selected
// everything, a bug in this process could not leak bulk traffic, but the
// kernel would be copying a gigabit of it.
func TestRuleSelectsOnlyTheStartOfAConnection(t *testing.T) {
	r := RuleSet(DefaultRule("br-lan"))
	// The whole token, not a prefix: "< 8000" contains "< 8" and would pass a
	// substring check while copying a thousand times as much.
	if !strings.Contains(r, "ct original packets < 8 log group 7 snaplen 1800") {
		t.Fatalf("the selection must be stateful and bounded:\n%s", r)
	}
	if !strings.Contains(r, "log group 7") || !strings.Contains(r, "snaplen 1800") {
		t.Fatalf("group or snaplen missing:\n%s", r)
	}
	if !strings.Contains(r, `iifname "br-lan"`) {
		t.Fatalf("the interface must be named, or every bridge is copied:\n%s", r)
	}
}

// fw4 rewrites its own table on every reload, so anything netryx adds there
// would disappear the next time somebody saves a setting in LuCI.
func TestRuleLivesInItsOwnTableAndNeverDecides(t *testing.T) {
	r := RuleSet(DefaultRule("br-lan"))
	if !strings.Contains(r, "table inet netryx") {
		t.Fatalf("must be its own table:\n%s", r)
	}
	if strings.Contains(r, "fw4") {
		t.Fatal("must not touch the firewall's own table")
	}
	if !strings.Contains(r, "policy accept") {
		t.Fatalf("an observing chain must never drop:\n%s", r)
	}
	for _, verdict := range []string{" drop", " reject", " accept\n"} {
		if strings.Contains(strings.ReplaceAll(r, "policy accept;", ""), verdict) {
			t.Fatalf("the chain must carry no verdict of its own (%q):\n%s", verdict, r)
		}
	}
	if TeardownCommand("netryx") != "delete table inet netryx" {
		t.Fatal("removal must be one command")
	}
}

// Several bridges are the normal case in a house with a guest network.
func TestEveryNamedInterfaceGetsARule(t *testing.T) {
	r := RuleSet(DefaultRule("br-lan", "br-guest"))
	// counted by the interface clause, so the router's own chain does not
	// inflate the number and hide a missing bridge
	if strings.Count(r, "iifname") != 2 {
		t.Fatalf("one rule per interface:\n%s", r)
	}
	if !strings.Contains(r, `iifname "br-guest"`) {
		t.Fatalf("the guest bridge is missing:\n%s", r)
	}
	// with no interface named, the forward rule still has to be there
	o := DefaultRule()
	o.Own = false
	r = RuleSet(o)
	if strings.Count(r, "ct original packets") != 1 || strings.Contains(r, "iifname") {
		t.Fatalf("an unnamed interface means every one, not none:\n%s", r)
	}
}

// A box that is not a gateway forwards nothing, and the router's own traffic
// is the whole question anyway on the tap this grew out of.
func TestOwnTrafficGetsItsOwnChain(t *testing.T) {
	r := RuleSet(DefaultRule("br-lan"))
	if !strings.Contains(r, "hook output") {
		t.Fatalf("the router's own traffic never crosses forward:\n%s", r)
	}
	if strings.Count(r, "ct original packets < 8 log group 7 snaplen 1800") != 2 {
		t.Fatalf("both hooks must carry the same bounded selection:\n%s", r)
	}
	o := DefaultRule("br-lan")
	o.Own = false
	if strings.Contains(RuleSet(o), "hook output") {
		t.Fatal("and it must be possible to watch only what passes through")
	}
}

// The snaplen has to clear a post-quantum ClientHello, or the copy arrives
// truncated and the name is lost for every modern browser.
func TestSnaplenClearsAModernClientHello(t *testing.T) {
	o := DefaultRule("br-lan")
	if o.Snaplen < 1600 {
		t.Fatalf("a ClientHello with post-quantum key shares runs to 1.8 KB; snaplen %d truncates it", o.Snaplen)
	}
}

// Applying twice must leave one ruleset. nft -f on an existing table appends,
// so without the delete the kernel ends up copying every packet twice, then
// three times, and nothing says so.
func TestApplyingTwiceLeavesOneRuleset(t *testing.T) {
	r := RuleSet(DefaultRule("br-lan"))
	if !strings.Contains(r, "delete table inet netryx") {
		t.Fatalf("the script must clear its own table first:\n%s", r)
	}
	// the delete has to be preceded by a declaration, or the script fails on a
	// router that does not have the table yet
	del := strings.Index(r, "delete table inet netryx")
	decl := strings.Index(r, "table inet netryx {}")
	if decl < 0 || decl > del {
		t.Fatalf("declare before deleting, or a first run fails:\n%s", r)
	}
}

// The interface name goes into a script that runs as root.
func TestInterfaceNamesAreChecked(t *testing.T) {
	if err := CheckInterfaces([]string{"br-lan", "eth0.7", "phy0-ap0"}); err != nil {
		t.Fatalf("ordinary names must pass: %v", err)
	}
	for _, bad := range []string{
		`br-lan" ; drop; "`,
		"br lan",
		"",
		"averyveryverylonginterfacename",
		"br-lan\nflush ruleset",
	} {
		if err := CheckInterfaces([]string{bad}); err == nil {
			t.Fatalf("must refuse %q", bad)
		}
	}
	if _, err := Apply(RuleOptions{Table: "netryx", Interfaces: []string{`x"; flush ruleset; "`}}); err == nil {
		t.Fatal("Apply must refuse it too, not only the checker")
	}
}

// A run that used the ingress hook and was torn down as if it had used the
// forward hook leaves a netdev chain behind, copying every new connection to a
// log group nobody reads. Nothing in the output says so: the watcher is gone,
// the box looks clean, and the kernel keeps working.
func TestRemoveTakesBothHooksAway(t *testing.T) {
	var asked []string
	fail := map[string]bool{}
	restore := nftRun
	nftRun = func(script string) (string, error) {
		asked = append(asked, script)
		if fail[strings.TrimSpace(script)] {
			return "no such file or directory", errors.New("nft: no such file")
		}
		return "", nil
	}
	t.Cleanup(func() { nftRun = restore })

	if _, err := Remove("netryx"); err != nil {
		t.Fatalf("a clean teardown is not an error: %v", err)
	}
	joined := strings.Join(asked, "|")
	if !strings.Contains(joined, "delete table inet netryx") {
		t.Fatalf("the forward hook's table must be removed: %q", joined)
	}
	if !strings.Contains(joined, "delete table netdev netryx") {
		t.Fatalf("the ingress hook's table must be removed: %q", joined)
	}

	// The hook this run did not use has no table, and nft says so. That must
	// not stop the removal of the one that does exist.
	asked = nil
	fail["delete table inet netryx"] = true
	if _, err := Remove("netryx"); err != nil {
		t.Fatalf("one absent table is the normal case, not a failure: %v", err)
	}
	if len(asked) != 2 {
		t.Fatalf("the second removal must still be attempted: %v", asked)
	}

	// Both failing is nft missing or permission denied, and that is worth an
	// error: the caller believes the router was left clean.
	fail["delete table netdev netryx"] = true
	if _, err := Remove("netryx"); err == nil {
		t.Fatal("when nothing could be removed, Remove must say so")
	}
}

// Choosing the ingress hook must not quietly answer a different question. The
// router's own traffic never arrives on a bridge port, so without a second
// table the mode that was meant to see more sees less, and no test of the
// netdev ruleset alone can notice.
func TestIngressStillWatchesTheRouterItself(t *testing.T) {
	own := OwnRuleSet(DefaultRule("br-lan"))
	if !strings.Contains(own, "hook output") {
		t.Fatalf("own traffic is an output hook:\n%s", own)
	}
	if strings.Contains(own, "hook forward") || strings.Contains(own, "netdev") {
		t.Fatalf("and nothing else, or ingress mode would also watch the forward path:\n%s", own)
	}
	if !strings.Contains(own, "ct original packets < 8") {
		t.Fatalf("the router's own flows do have a connection to count:\n%s", own)
	}

	var scripts []string
	restore := nftRun
	nftRun = func(s string) (string, error) { scripts = append(scripts, s); return "", nil }
	t.Cleanup(func() { nftRun = restore })
	sysfs := fakeSysfs(t, map[string][]string{"br-lan": {"lan2"}})
	restoreFS := sysfsNet
	sysfsNet = sysfs
	t.Cleanup(func() { sysfsNet = restoreFS })

	o := DefaultRule("br-lan")
	o.Mode = Ingress
	if _, err := Apply(o); err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 2 {
		t.Fatalf("ingress installs the netdev table and the output chain, got %d", len(scripts))
	}
	if !strings.Contains(scripts[0], "table netdev") || !strings.Contains(scripts[1], "hook output") {
		t.Fatalf("wrong pair installed: %v", scripts)
	}

	// And a router where the operator does not want that gets one table only.
	scripts = nil
	o.Own = false
	if _, err := Apply(o); err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 1 {
		t.Fatalf("own traffic off means one table, got %d", len(scripts))
	}
}

// The resolver is the only naming that survives Encrypted Client Hello, and it
// takes both halves: the question arrives at the bridge, the answer leaves
// from the router itself and is seen at no other hook.
func TestTheResolverIsReadOnBothSides(t *testing.T) {
	o := DefaultRule("br-lan")
	o.Mode = Ingress
	r := NetdevRuleSet(o, []string{"lan2"})
	for _, guard := range []string{"meta protocol ip ", "meta protocol ip6 "} {
		q := ruleLine(r, guard+"meta l4proto udp udp dport 53")
		if q == "" {
			t.Fatalf("no rule reads the questions for %s:\n%s", guard, r)
		}
		if !strings.Contains(q, "limit rate") {
			t.Fatalf("a rule with no set behind it needs its own bound: %q", q)
		}
		if !strings.Contains(q, "snaplen 512") {
			t.Fatalf("a question is small and only the question is wanted: %q", q)
		}
	}

	own := OwnRuleSet(o)
	a := ruleLine(own, "udp sport { 53, 553 }")
	if a == "" {
		t.Fatalf("the answers are what give an address a name:\n%s", own)
	}
	if !strings.Contains(a, "snaplen 1500") {
		t.Fatalf("a truncated answer names nothing: %q", a)
	}
	// The rules engine believes an answer only by the rule that copied it
	// (R18): the output hook's prefix, which no packet a device sends can carry.
	if !strings.Contains(a, `log prefix "`+AnswerPrefix+`" group`) {
		t.Fatalf("the answer rule must mark its copies with %q: %q", AnswerPrefix, a)
	}
	for _, other := range []string{NetdevRuleSet(o, []string{"lan2"}), RuleSet(o)} {
		if strings.Contains(other, AnswerPrefix) {
			t.Fatalf("only the router's own answer rule carries the answer prefix:\n%s", other)
		}
	}
	// The accept keeps the rule below from copying the same packet again. It
	// ends this chain only, so the firewall's own chains are untouched.
	if !strings.Contains(a, "accept") {
		t.Fatalf("without the accept every answer is copied twice: %q", a)
	}
	if i, j := strings.Index(a, "accept"), strings.Index(a, "comment"); i > j {
		t.Fatalf("nft reads a comment as the end of the rule: %q", a)
	}

	// And it is a setting, because a list of names is a list of interests.
	o.DNS = false
	if strings.Contains(NetdevRuleSet(o, []string{"lan2"}), "dport 53") || strings.Contains(OwnRuleSet(o), "sport 53") {
		t.Fatal("turning it off must remove both halves")
	}
}

// A bridge with no port up (every cable out, the lab's fake house not raised)
// gave nft `devices = {  }`, a syntax error, and serve ended on it, so procd
// restarted it for ever and the console never answered. Found in netryx-lab,
// 2026-11-25. With no device the netdev table carries no chain, the router's
// own chain still goes in, and Apply says there is nothing to listen on.
func TestABridgeWithNoPortsInstallsNoChainAndSaysSo(t *testing.T) {
	var scripts []string
	restore := nftRun
	nftRun = func(s string) (string, error) { scripts = append(scripts, s); return "", nil }
	t.Cleanup(func() { nftRun = restore })
	sysfs := fakeSysfs(t, map[string][]string{"br-lan": nil})
	restoreFS := sysfsNet
	sysfsNet = sysfs
	t.Cleanup(func() { sysfsNet = restoreFS })

	o := DefaultRule("br-lan")
	o.Mode = Ingress
	_, err := Apply(o)
	if !errors.Is(err, ErrNoDevices) {
		t.Fatalf("an empty bridge must say there is nothing to listen on, got %v", err)
	}
	if len(scripts) != 2 {
		t.Fatalf("the netdev table and the router's own chain go in, got %d: %v", len(scripts), scripts)
	}
	if !strings.Contains(scripts[0], "table netdev netryx {") {
		t.Fatalf("the netdev table must exist, so the supervisor can read it back:\n%s", scripts[0])
	}
	if strings.Contains(scripts[0], "hook ingress") || strings.Contains(scripts[0], "devices = {") {
		t.Fatalf("no chain may be written over an empty device list:\n%s", scripts[0])
	}
	if !strings.Contains(scripts[1], "hook output") {
		t.Fatalf("the router's own traffic is watched whatever the bridge holds:\n%s", scripts[1])
	}
}

// Once a port comes up, the supervisor reads the chain-less table back as
// attached to nothing and installs the chain on the port.
func TestAPortThatComesUpAfterAnEmptyStartIsWatched(t *testing.T) {
	var scripts []string
	restore := nftRun
	nftRun = func(s string) (string, error) { scripts = append(scripts, s); return "", nil }
	t.Cleanup(func() { nftRun = restore })
	sysfs := fakeSysfs(t, map[string][]string{"br-lan": nil})
	restoreFS := sysfsNet
	sysfsNet = sysfs
	t.Cleanup(func() { sysfsNet = restoreFS })

	o := DefaultRule("br-lan")
	o.Mode = Ingress
	if _, err := Apply(o); !errors.Is(err, ErrNoDevices) {
		t.Fatal(err)
	}
	restoreList := nftList
	nftList = func(string) (string, error) { return "table netdev netryx {\n}\n", nil }
	t.Cleanup(func() { nftList = restoreList })
	s := &Supervisor{
		Want:  func() ([]string, error) { return WatchedDevices([]string{"br-lan"}) },
		Have:  func() ([]string, error) { return InstalledDevices("netryx") },
		Apply: func() error { _, err := Apply(o); return err },
	}
	if _, err := s.Check(); err == nil {
		t.Fatal("still no port: nothing to install")
	}
	if err := os.Mkdir(filepath.Join(sysfs, "br-lan", "brif", "lan2"), 0o755); err != nil {
		t.Fatal(err)
	}
	scripts = nil
	did, err := s.Check()
	if err != nil || !did {
		t.Fatalf("a port that came up must be watched: %v %v", did, err)
	}
	if !strings.Contains(scripts[0], `devices = { lan2 }`) {
		t.Fatalf("the chain must go on the port:\n%s", scripts[0])
	}
}
