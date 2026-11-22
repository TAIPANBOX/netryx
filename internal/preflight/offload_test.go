package preflight

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "firewall")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The option this reads also exists inside zones, where it means something
// else. A check that greps the file tells an operator whose router has
// offloading OFF that their forward-hook capture cannot work, and they turn
// off a setting that was never on.
func TestOnlyTheDefaultsSectionDecides(t *testing.T) {
	cfg := `
config defaults
	option input 'ACCEPT'
	option flow_offloading '0'

config zone
	option name 'lan'
	option flow_offloading '1'
`
	o, err := ReadOffload(write(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if o.Software || o.Hardware {
		t.Fatalf("a zone's setting is not the default: %+v", o)
	}
	if !o.Found {
		t.Fatal("the defaults section was there and must be reported found")
	}
}

func TestOffloadingIsReadFromTheDefaults(t *testing.T) {
	cfg := "config defaults\n\toption flow_offloading '1'\n\toption flow_offloading_hw '1'\n"
	o, err := ReadOffload(write(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if !o.Software || !o.Hardware || !o.On() {
		t.Fatalf("both kinds must be read: %+v", o)
	}

	// uci writes booleans five ways and all of them mean the same thing. A
	// parser that knows only "1" reports offloading off on a router that has
	// it on, which is the dangerous direction: the operator is told the
	// forward hook is fine and it is blind.
	for _, v := range []string{"'1'", "1", "on", "'true'", "yes", "enabled"} {
		o, err := ReadOffload(write(t, "config defaults\n\toption flow_offloading "+v+"\n"))
		if err != nil || !o.Software {
			t.Fatalf("uci spells true as %s: %+v %v", v, o, err)
		}
	}
	for _, v := range []string{"'0'", "0", "off", "false", "no", "''"} {
		o, err := ReadOffload(write(t, "config defaults\n\toption flow_offloading "+v+"\n"))
		if err != nil || o.Software {
			t.Fatalf("uci spells false as %s: %+v %v", v, o, err)
		}
	}
}

// Configuration files come off somebody else's router and are not promised to
// be well formed. None of these may panic or invent a setting.
func TestAConfigFileIsNotPromisedToBeWellFormed(t *testing.T) {
	for _, body := range []string{
		"",
		"config",
		"config defaults",
		"config defaults\n\toption\n",
		"config defaults\n\toption flow_offloading\n",
		"# config defaults\n#\toption flow_offloading '1'\n",
		"config defaults\r\n\toption flow_offloading '1'\r\n",
		strings.Repeat("config defaults\n", 5000),
	} {
		o, err := ReadOffload(write(t, body))
		if err != nil {
			t.Fatalf("a malformed file is not a read error: %q %v", body, err)
		}
		// Only the CRLF file has a real setting in it; everything else is
		// either commented out or has no value at all.
		want := strings.Contains(body, "\r\n\toption flow_offloading '1'")
		if o.Software != want {
			t.Fatalf("%q read as software=%v, wanted %v", body, o.Software, want)
		}
	}
}

// The advice has to be asymmetric: offloading only breaks the forward hook.
// Telling an ingress user to turn off offloading would have them degrade their
// router's throughput for nothing.
func TestOffloadingIsOnlyAProblemForTheForwardHook(t *testing.T) {
	on := Offload{Software: true, Found: true}
	if n := ModeNeed("ingress", on, nil); !n.OK {
		t.Fatalf("ingress is the answer to offloading, not a victim of it: %+v", n)
	}
	n := ModeNeed("forward", on, nil)
	if n.OK {
		t.Fatal("offloading plus the forward hook is silent blindness and must be reported")
	}
	if !strings.Contains(n.Fix(), "-mode ingress") {
		t.Fatalf("the fix must name the mode that works, got %q", n.Fix())
	}
	if strings.Contains(n.Fix(), "opkg") {
		t.Fatalf("nothing here is fixed by installing a package: %q", n.Fix())
	}
	// Hardware offloading is worse and must say so in its own words: the flow
	// leaves the CPU altogether.
	if d := ModeNeed("forward", Offload{Hardware: true, Found: true}, nil).Detail; !strings.Contains(d, "bypasses the CPU") {
		t.Fatalf("hardware offloading needs its own explanation, got %q", d)
	}
	// A router with no firewall config at all is not a broken router.
	if n := ModeNeed("forward", Offload{}, os.ErrNotExist); !n.OK {
		t.Fatalf("not knowing is not the same as being broken: %+v", n)
	}
	if n := ModeNeed("forward", Offload{Found: true}, nil); !n.OK {
		t.Fatalf("offloading off is the case the forward hook was written for: %+v", n)
	}
}

// The check command prints this line, and "false" when the answer is "the
// file had no defaults section" would be a measurement that was never taken
// reported as a measurement that came back negative.
func TestUnknownIsNotTheSameAsOff(t *testing.T) {
	if s := (Offload{}).String(); !strings.Contains(s, "no defaults section") {
		t.Fatalf("not knowing must say so: %q", s)
	}
	if s := (Offload{Found: true, Software: true}).String(); s != "software=true hardware=false" {
		t.Fatalf("a read that happened reports what it read: %q", s)
	}
	// A need that passed has no fix to offer: printing one would send an
	// operator to change a setting that is already right.
	if fix := (Need{OK: true, FixCmd: "do something"}).Fix(); fix != "" {
		t.Fatalf("a satisfied need has no fix: %q", fix)
	}
}

// Four failures, four answers. The one that must never happen is the package
// install line on a router where nftables is already installed and working:
// the operator follows it, nothing changes, and the tool has spent its
// credibility.
func TestTheRuleFailureSaysWhichFailureItWas(t *testing.T) {
	if n := RuleNeed("ingress", "br-lan", nil); !n.OK || n.Fix() != "" {
		t.Fatalf("a rule the kernel accepts is not a problem: %+v", n)
	}
	cases := []struct {
		err         string
		wantInFix   string
		wantNotInFx string
	}{
		{"/dev/stdin:7:40-48: Error: Could not process rule: No such file or directory", "/sys/class/net", "opkg"},
		{`exec: "nft": executable file not found in $PATH`, "apk add nftables", ""},
		{"Error: Could not process rule: Operation not permitted", "root", "opkg"},
		{"Error: syntax error, unexpected flags", "kmod-nft-core", ""},
	}
	// An error this code has never seen before is the most likely one in the
	// field, and it must still reach the operator whole.
	if d := RuleNeed("ingress", "br-lan", errors.New("Error: something nobody predicted")).Detail; !strings.Contains(d, "nobody predicted") {
		t.Fatalf("an unclassified failure must still carry the message: %q", d)
	}

	for _, c := range cases {
		n := RuleNeed("ingress", "br-lan", errors.New(c.err))
		if n.OK {
			t.Fatalf("a refused rule is a failure: %q", c.err)
		}
		if !strings.Contains(n.Fix(), c.wantInFix) {
			t.Fatalf("%q -> fix %q, wanted %q", c.err, n.Fix(), c.wantInFix)
		}
		if c.wantNotInFx != "" && strings.Contains(n.Fix(), c.wantNotInFx) {
			t.Fatalf("%q must not be answered with %q: %q", c.err, c.wantNotInFx, n.Fix())
		}
		// Whatever the verdict, nft's own words survive: they name the line
		// and the expression, and no sentence here can replace that.
		if !strings.Contains(n.Detail, c.err) && !strings.Contains(c.err, "not found") {
			t.Fatalf("the kernel's own message must be kept: %q", n.Detail)
		}
	}
}

// A kernel built without the netdev family refuses the table itself, and no
// package can add it: CONFIG_NF_TABLES_NETDEV is compiled in or it is not.
// Answering that with "install nftables" is the worst kind of wrong, because
// nftables is working on that machine and the operator will watch the install
// succeed and the problem stay.
//
// @measured 2026-09-15 on Google's Container-Optimized OS, kernel 6.6.153+,
// where `CONFIG_NF_TABLES_NETDEV is not set` and the service said exactly this.
func TestTheRuleFailureTellsANetdevlessKernelFromAMissingModule(t *testing.T) {
	kernel := RuleNeed("ingress", "br-lan",
		errors.New("/dev/stdin:1:1-23: Error: Could not process rule: Not supported"))
	if kernel.OK {
		t.Fatal("a kernel that refuses the table is a failure")
	}
	if !strings.Contains(kernel.Detail, "netdev") {
		t.Fatalf("the cause must name the family the kernel lacks: %q", kernel.Detail)
	}
	if !strings.Contains(kernel.Fix(), "forward") {
		t.Fatalf("and the fix must offer the mode that can still run here: %q", kernel.Fix())
	}

	// The teeth: the branch must not swallow the case it sits next to. A build
	// that carries netdev but lacks an expression is still answered with the
	// module, because there installing it does help.
	module := RuleNeed("ingress", "br-lan", errors.New("Error: syntax error, unexpected flags"))
	if !strings.Contains(module.Fix(), "kmod-nft-core") {
		t.Fatalf("a missing expression is still a module problem: %q", module.Fix())
	}
	if strings.Contains(module.Detail, "netdev family") {
		t.Fatalf("and it is not the family: %q", module.Detail)
	}
}

// A bridge with no ports is not a software problem, and the install line is
// the answer to a different question. @measured 2026-09-15: the operator was
// told to install nftables on a machine where nftables had just answered.
func TestAnEmptyBridgeIsNotAnsweredWithAPackage(t *testing.T) {
	n := RuleNeed("ingress", "br-empty",
		errors.New("br-empty has no member devices to watch: an ingress hook attaches to ports, and this bridge has none"))
	if n.OK {
		t.Fatal("a bridge with no ports cannot be watched")
	}
	if strings.Contains(n.Fix(), "install") {
		t.Fatalf("installing something adds no ports to a bridge: %q", n.Fix())
	}
	if !strings.Contains(n.Fix(), "ip link") {
		t.Fatalf("the fix is to look at what the bridge is made of: %q", n.Fix())
	}
}
