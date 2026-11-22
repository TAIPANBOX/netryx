package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTwo(t *testing.T, fw, net string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	fwp := filepath.Join(dir, "firewall")
	netp := filepath.Join(dir, "network")
	if err := os.WriteFile(fwp, []byte(fw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(netp, []byte(net), 0o644); err != nil {
		t.Fatal(err)
	}
	return fwp, netp
}

// The shape that started this: a router whose br-lan is the uplink and whose
// routed network is somewhere else. Watching br-lan there is watching the
// wrong side, and nothing about it looks wrong: the rule installs and the hook
// attaches.
func TestTheFirewallSaysWhichSideTheDevicesAreOn(t *testing.T) {
	fw := `
config defaults
	option input 'REJECT'

config zone
	option name 'wan'
	list network 'wwan'
	option masq '1'

config zone
	option name 'lan'
	list network 'lan'
	list network 'guest'
`
	net := `
config interface 'loopback'
	option device 'lo'

config interface 'lan'
	option device 'eth1'

config interface 'guest'
	option ifname 'phy1-ap0'

config interface 'wwan'
	option device 'br-lan'
`
	fwp, netp := writeTwo(t, fw, net)
	got, err := LANDevices(fwp, netp)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "eth1" || got[1] != "phy1-ap0" {
		t.Fatalf("both lan networks and both spellings of the option: %v", got)
	}

	// The advice must name the fix and must not refuse: watching the uplink on
	// purpose is a choice somebody may have made.
	n := SideNeed([]string{"br-lan"}, got, nil)
	if n.OK {
		t.Fatal("watching the uplink while the devices are elsewhere must be reported")
	}
	if !strings.Contains(n.Fix(), "eth1") || !strings.Contains(n.Detail, "NATed") {
		t.Fatalf("the advice must say what to do and why: %q / %q", n.Fix(), n.Detail)
	}
	if ok := SideNeed([]string{"eth1"}, got, nil); !ok.OK {
		t.Fatalf("watching the lan side is the normal case: %+v", ok)
	}
	// Watching several, one of which is the lan side, is fine.
	if ok := SideNeed([]string{"br-lan", "phy1-ap0"}, got, nil); !ok.OK {
		t.Fatalf("one of them being right is right: %+v", ok)
	}
	// Not knowing is not a fault: a router without fw4 is still a router.
	if bad := SideNeed([]string{"br-lan"}, nil, os.ErrNotExist); !bad.OK {
		t.Fatalf("an unreadable firewall config must not be reported as a mistake: %+v", bad)
	}
}

func TestAConfigWithoutALanZoneSaysSoRatherThanGuessing(t *testing.T) {
	fwp, netp := writeTwo(t, "config zone\n\toption name 'wan'\n\tlist network 'wan'\n", "config interface 'wan'\n\toption device 'eth0'\n")
	if _, err := LANDevices(fwp, netp); err == nil {
		t.Fatal("no lan zone must be an error, not an empty answer that reads as agreement")
	}
	// A lan zone naming a network that has no device is the same kind of
	// nothing, and must not come back as a silent empty list.
	fwp, netp = writeTwo(t, "config zone\n\toption name 'lan'\n\tlist network 'lan'\n", "config interface 'lan'\n\toption proto 'static'\n")
	if _, err := LANDevices(fwp, netp); err == nil {
		t.Fatal("a network with no device names nothing")
	}
	if _, err := LANDevices("/nope/firewall", netp); err == nil {
		t.Fatal("a missing file is an error")
	}
}
