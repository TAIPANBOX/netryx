package nflog

import (
	"os"
	"path/filepath"
	"sort"
)

// sysfsNet is where the kernel publishes what a bridge is made of. It is a
// variable rather than a constant only so that a test can point it at a tree
// it built: the ingress branch is otherwise unreachable anywhere but a Linux
// router, which is how it stayed untested until a mutant walked through it.
var sysfsNet = "/sys/class/net"

// BridgeMembers lists the devices a bridge is made of.
//
// An ingress chain attaches to the members rather than to the bridge itself.
// Not because a hook on "br-lan" sees nothing - it does see what the bridge
// passes up, which is every routed and local-bound packet - but because the
// members see three things the bridge does not: traffic bridged between two
// members, traffic on a port that a flowtable would otherwise claim first,
// and frames the bridge is about to discard.
//
// The members change when a Wi-Fi interface comes up or a cable is plugged,
// so this is read again whenever the ruleset is applied rather than once at
// start. It is NOT yet re-read when a member appears or disappears while the
// watcher runs: the kernel removes that device's hook and says nothing, so
// Wi-Fi goes quiet after a `wifi reload`. That is the next thing to fix.
func BridgeMembers(sysfs, bridge string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(sysfs, bridge, "brif"))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	// os.ReadDir already returns entries sorted by name, so this line changes
	// nothing today and a mutation of it cannot be caught. It stays because
	// the order is a contract here, not an accident: the device list is
	// interpolated into the ruleset, and a ruleset whose text changes between
	// two identical applications cannot be compared with the running one.
	sort.Strings(out)
	return out, nil
}

// WatchedDevices is what Apply would attach to right now. Callers use this
// rather than DevicesFor with a path of their own: a supervisor comparing a
// different list from the one Apply installs would reinstall the ruleset for
// ever, every pass.
func WatchedDevices(ifaces []string) ([]string, error) { return DevicesFor(sysfsNet, ifaces) }

// DevicesFor turns a list of interfaces into the devices an ingress chain can
// attach to: a bridge becomes its members, anything else stands for itself.
func DevicesFor(sysfs string, ifaces []string) ([]string, error) {
	var out []string
	for _, i := range ifaces {
		members, err := BridgeMembers(sysfs, i)
		if err != nil {
			out = append(out, i) // not a bridge: take the name as given
			continue
		}
		// A bridge with no members contributes nothing. That is different
		// from not being a bridge, and the difference matters: attaching the
		// hook to the bridge device itself during a reload would look like a
		// working installation while the ports came back unwatched.
		out = append(out, members...)
	}
	return out, CheckInterfaces(out)
}
