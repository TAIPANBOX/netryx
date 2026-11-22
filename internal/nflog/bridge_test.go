package nflog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeSysfs(t *testing.T, bridges map[string][]string) string {
	t.Helper()
	root := t.TempDir()
	for br, members := range bridges {
		dir := filepath.Join(root, br, "brif")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, m := range members {
			if err := os.Mkdir(filepath.Join(dir, m), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// An ingress chain attaches to devices. A rule written against the bridge sees
// nothing at all, which is the kind of failure that looks like a quiet network.
func TestABridgeBecomesItsMembers(t *testing.T) {
	sysfs := fakeSysfs(t, map[string][]string{
		"br-lan":   {"lan2", "lan1", "phy0-ap0"},
		"br-guest": {"phy1-ap0"},
	})
	got, err := DevicesFor(sysfs, []string{"br-lan"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "lan1" || got[2] != "phy0-ap0" {
		t.Fatalf("members wrong or unsorted: %v", got)
	}
	both, err := DevicesFor(sysfs, []string{"br-lan", "br-guest"})
	if err != nil || len(both) != 4 {
		t.Fatalf("two bridges give four devices: %v %v", both, err)
	}
	// something that is not a bridge stands for itself
	plain, err := DevicesFor(sysfs, []string{"eth0"})
	if err != nil || len(plain) != 1 || plain[0] != "eth0" {
		t.Fatalf("a plain device must pass through: %v %v", plain, err)
	}
	// A bridge whose ports are all down contributes nothing, and that is not
	// the same as "not a bridge". Taking the bridge itself as the device here
	// would attach the hook to the wrong place during a reload and hide the
	// one case the supervisor exists to notice: nothing left to watch.
	empty := fakeSysfs(t, map[string][]string{"br-empty": {}})
	if got, err := DevicesFor(empty, []string{"br-empty"}); err != nil || len(got) != 0 {
		t.Fatalf("an empty bridge is nothing to attach to: %v %v", got, err)
	}

	// and whatever comes back is still checked before it reaches a script
	bad := fakeSysfs(t, map[string][]string{"br-x": {`lan1" ; flush ruleset; "`}})
	if _, err := DevicesFor(bad, []string{"br-x"}); err == nil {
		t.Fatal("a member name that is not an interface name must be refused")
	}
}

// The ingress form exists so that the capture does not depend on a firewall
// setting. What it must NOT do is copy traffic that is not a handshake:
// @measured on the Flint 2026-09-15, the tuple-novelty form copied 32 of 188
// packets of one long flow, because an nft `add` never refreshes an element's
// timeout and the budget therefore re-opens every time the entry expires. A
// ten-minute plaintext upload would have been copied 14 KB per 30 s.
func TestIngressCopiesHandshakesAndNotTraffic(t *testing.T) {
	r := NetdevRuleSet(DefaultRule("br-lan"), []string{"lan1", "phy0-ap0"})
	if !strings.Contains(r, "table netdev netryx") {
		t.Fatalf("the ingress hook lives in a netdev table:\n%s", r)
	}
	if !strings.Contains(r, "hook ingress devices = { lan1, phy0-ap0 }") {
		t.Fatalf("it attaches to devices, not to the bridge:\n%s", r)
	}
	if !strings.Contains(r, "policy accept") || strings.Contains(r, " drop") {
		t.Fatalf("an observing chain must carry no verdict:\n%s", r)
	}
	if !strings.Contains(r, "delete table netdev netryx") {
		t.Fatalf("applying twice must leave one ruleset:\n%s", r)
	}

	for _, fam := range []struct{ set, guard, addr string }{
		{"hello4", "meta protocol ip ", "ip saddr"},
		{"hello6", "meta protocol ip6 ", "ip6 saddr"},
	} {
		// A packet enters the set only by carrying a TLS handshake record
		// whose first message is a ClientHello: content type 0x16, version
		// major 0x03, handshake type 0x01 at byte five. @measured on the
		// Flint: 3 packets of 39 on a port with three handshakes.
		seed := ruleLine(r, fam.guard+"meta l4proto tcp @ih,0,16 0x1603")
		if seed == "" {
			t.Fatalf("no rule selects a ClientHello for %s:\n%s", fam.set, r)
		}
		if !strings.Contains(seed, "@ih,40,8 0x01") {
			t.Fatalf("a TLS record that is not a ClientHello must not seed: %q", seed)
		}
		// update, never add: an add leaves the timeout at the first packet's
		// clock, the element dies under a live flow, and the next packet
		// starts a fresh budget.
		if !strings.Contains(seed, "update @"+fam.set) {
			t.Fatalf("the budget must be refreshed, not recreated: %q", seed)
		}
		if strings.Contains(seed, "add @"+fam.set) {
			t.Fatalf("add re-opens the budget every timeout: %q", seed)
		}
		if !strings.Contains(seed, "log group 7") || !strings.Contains(seed, "snaplen 1800") {
			t.Fatalf("the handshake itself must be copied: %q", seed)
		}
		// Counted, because "how much of the house did this copy" must be a
		// number an operator can read rather than a claim in a README. A full
		// set also fails silently, and seen-minus-copied is the only place it
		// shows.
		if !strings.Contains(seed, "counter") {
			t.Fatalf("what is copied must be counted: %q", seed)
		}

		// The rest of the handshake is reached by membership only. A plain
		// lookup evaluates the element's own limit (@measured on the Flint),
		// so the budget bounds the whole flow, not each rule.
		cont := ruleLine(r, fam.guard+"meta l4proto tcp "+fam.addr)
		if cont == "" || !strings.Contains(cont, "@"+fam.set) {
			t.Fatalf("a split ClientHello's second segment needs the set:\n%s", r)
		}
		if strings.Contains(cont, "update @") || strings.Contains(cont, "add @") {
			t.Fatalf("the continuation must not create or refresh anything: %q", cont)
		}

		decl := ruleLine(r, "set "+fam.set+" {")
		if decl == "" {
			t.Fatalf("no set %s is declared:\n%s", fam.set, r)
		}
		if !strings.Contains(decl, "flags dynamic, timeout") || !strings.Contains(decl, "timeout 30s") {
			t.Fatalf("set entries must age out: %q", decl)
		}
		// Unbounded is a router with no memory left: a set with no size grows
		// with whatever an attacker sends.
		if !strings.Contains(decl, "size ") {
			t.Fatalf("the set must be bounded: %q", decl)
		}
		// The budget is what stops a flow being copied whole, and it is sized
		// to a ClientHello rather than to a connection: @measured, a budget of
		// eight spent seven of them on the client's data after the handshake
		// was already complete.
		if !strings.Contains(seed, "burst 3 packets") || !strings.Contains(seed, "limit rate") {
			t.Fatalf("the budget must be the handshake's size: %q", seed)
		}
		if strings.Contains(seed, "burst 8 packets") {
			t.Fatalf("eight is the forward hook's number and it copies data here: %q", seed)
		}
	}

	// QUIC carries its ClientHello in an Initial packet, which is
	// self-describing: the long-header form has both top bits set. It is
	// bounded by a rate rather than a set, because anything may be sent to a
	// UDP port and a set would grow with it.
	q := ruleLine(r, "meta protocol ip meta l4proto udp udp dport 443")
	if q == "" || !strings.Contains(q, "0xc0") {
		t.Fatalf("no rule selects a QUIC Initial:\n%s", r)
	}
	if !strings.Contains(q, "udp dport 443") {
		// Without the port, a DNS reply whose first byte happens to have both
		// top bits set is copied: one query in four.
		t.Fatalf("the QUIC rule must be bounded to its port: %q", q)
	}
	if !strings.Contains(q, "limit rate") {
		t.Fatalf("a UDP rule with no set needs its own bound: %q", q)
	}

	if !strings.Contains(q, "counter") {
		t.Fatalf("the QUIC rule must be counted too: %q", q)
	}
	// And everything that entered, so the fraction is knowable.
	if ruleLine(r, "counter comment \"seen\"") == "" {
		t.Fatalf("nothing counts what arrived:\n%s", r)
	}

	// nft reads a comment as the end of a rule, so a statement written after
	// one is a syntax error the whole ruleset dies on. Nothing in Go notices,
	// and the first sign is a router that installs nothing.
	for _, l := range strings.Split(r, "\n") {
		i := strings.Index(l, `comment "`)
		if i < 0 {
			continue
		}
		rest := l[i+len(`comment "`):]
		j := strings.Index(rest, `"`)
		if j < 0 {
			t.Fatalf("unterminated comment: %q", l)
		}
		if strings.TrimSpace(rest[j+1:]) != "" {
			t.Fatalf("a comment ends the rule, so nothing may follow it: %q", l)
		}
	}

	// Neither family may be selected by the header byte that extension
	// headers move: ip6 nexthdr is the first header, not the transport.
	if strings.Contains(r, "ip6 nexthdr") {
		t.Fatalf("use meta l4proto so extension headers do not hide a flow:\n%s", r)
	}
}

// ruleLine returns the rule that starts with prefix, ignoring comments: nft
// reads "#" to end of line, and a commented rule is not a rule.
func ruleLine(script, prefix string) string {
	for _, l := range strings.Split(script, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "#") {
			continue
		}
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}
