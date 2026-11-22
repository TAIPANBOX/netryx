package preflight

import (
	"fmt"
	"os"
	"strings"
)

// Offload is what the firewall does to a flow once it is established.
//
// This is the fact that decides whether selecting packets in the forward hook
// can work at all. With offloading on, the kernel moves a flow to a flowtable
// after its first packets and it never passes a forward chain again. The
// handshake is then invisible and NOTHING reports it: the dashboard shows a
// quiet network, which is the same thing it shows when the house is asleep.
type Offload struct {
	Software bool
	Hardware bool
	Found    bool // the defaults section was there to read
}

// ReadOffload parses /etc/config/firewall. It reads only the `config defaults`
// section: the same option name appears in zones and forwardings, and a zone
// that enables offloading for itself says nothing about the router's default.
// A parser that greps the whole file reports offloading on a router that has
// it off, and the advice that follows is then wrong in the confident direction.
func ReadOffload(path string) (Offload, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Offload{}, err
	}
	var o Offload
	in := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if f[0] == "config" {
			in = len(f) > 1 && f[1] == "defaults"
			if in {
				o.Found = true
			}
			continue
		}
		if !in || f[0] != "option" || len(f) < 3 {
			continue
		}
		switch f[1] {
		case "flow_offloading":
			o.Software = uciTrue(f[2])
		case "flow_offloading_hw":
			o.Hardware = uciTrue(f[2])
		}
	}
	return o, nil
}

// uciTrue reads uci's booleans, which are five spellings of the same thing.
func uciTrue(v string) bool {
	switch strings.ToLower(strings.Trim(v, `'"`)) {
	case "1", "on", "true", "yes", "enabled":
		return true
	}
	return false
}

// On reports whether a flow will leave the forward path.
func (o Offload) On() bool { return o.Software || o.Hardware }

// ModeNeed judges the chosen hook against what this router actually does.
//
// It is deliberately not symmetric. Offloading with ingress selection is fine
// and is not reported as a problem; offloading with forward selection is a
// silent blindness, and silence is what this whole package exists to remove.
func ModeNeed(mode string, o Offload, err error) Need {
	n := Need{What: "packet selection in the " + mode + " hook", Module: "-", Package: "-"}
	switch {
	case err != nil:
		// Not knowing is not the same as being broken: a router without the
		// file may not be running fw4 at all.
		n.OK = true
		n.Detail = "could not read the firewall config, so offloading is unknown: " + err.Error()
	case mode != "forward":
		n.OK = true
		if o.On() {
			n.Detail = "flow offloading is on, which is why this hook is the ingress one"
		}
	case o.Hardware:
		n.Detail = "hardware flow offloading is on: an established flow bypasses the CPU entirely, so the forward hook sees the first packets of a connection and nothing after them"
		n.FixCmd = "run with -mode ingress, or turn off Hardware flow offloading in LuCI: Network > Firewall > Routing/NAT Offloading"
	case o.Software:
		n.Detail = "software flow offloading is on: an established flow is handed to a flowtable and stops passing the forward hook, so a handshake that spans more than the first packets is lost without any error"
		n.FixCmd = "run with -mode ingress, or turn off Software flow offloading in LuCI: Network > Firewall > Routing/NAT Offloading"
	default:
		n.OK = true
	}
	return n
}

// String renders one line for the check command.
func (o Offload) String() string {
	if !o.Found {
		return "no defaults section in the firewall config"
	}
	return fmt.Sprintf("software=%v hardware=%v", o.Software, o.Hardware)
}

// RuleNeed judges the dry run of the selection rule and, unlike a module
// check, tells the operator which of four different things went wrong.
//
// They need different answers. "Install nftables" is the right sentence for
// exactly one of them, and it is the wrong sentence for the most common one:
// a router whose bridge is not called what the flag says it is called.
func RuleNeed(mode, ifaces string, err error) Need {
	n := Need{What: "the rule that selects packets (" + mode + ")", Module: "nf_tables", Package: "nftables"}
	if err == nil {
		n.OK = true
		return n
	}
	msg := err.Error()
	n.Detail = msg
	switch {
	case strings.Contains(msg, "executable file not found"), strings.Contains(msg, "not found in $PATH"):
		n.Detail = "nft is not installed, so no rule can be applied at all"
	case strings.Contains(msg, "No such file or directory"):
		// An ingress chain names real devices, and this is what the kernel
		// says when one of them is not there. On a GL.iNet box the bridge is
		// br-lan; on others it is br0 or lan.
		n.Detail = "the kernel refused the rule because a device does not exist: " + msg
		n.FixCmd = "name interfaces that exist (-watch-iface " + ifaces + " was tried): ls /sys/class/net"
	case strings.Contains(msg, "not permitted"), strings.Contains(msg, "Permission denied"):
		n.Detail = "applying a rule needs CAP_NET_ADMIN: " + msg
		n.FixCmd = "run as root, or as the user procd gives the service"
	case strings.Contains(msg, "no member devices to watch"):
		// Not a missing package: a bridge with nothing plugged into it. The
		// message already says which bridge, so the fix is where to look.
		n.Detail = msg
		n.FixCmd = "plug a port into it, or name the devices directly (-watch-iface lan2,lan3): ip link show master " + ifaces
	case mode == "ingress" && strings.Contains(strings.ToLower(msg), "not supported"):
		// The kernel refused the netdev table itself. That is a build-time
		// choice (CONFIG_NF_TABLES_NETDEV) and no package adds it where it is
		// off, so the install line below would be followed, would succeed, and
		// would change nothing. @measured 2026-09-15 on Container-Optimized
		// OS, kernel 6.6.153+, where the family is compiled out.
		n.Detail = "this kernel has no netdev family, so no ingress hook can be attached: " + msg
		n.FixCmd = "on OpenWrt: opkg install kmod-nft-core. Where the family is compiled out (CONFIG_NF_TABLES_NETDEV), ingress cannot run on this kernel: use -mode forward, with flow offloading off"
	case strings.Contains(msg, "not supported"), strings.Contains(msg, "Operation not supported"),
		strings.Contains(msg, "syntax error"), strings.Contains(msg, "unknown"):
		n.Detail = "this kernel build does not carry an expression the rule needs: " + msg
		n.FixCmd = "opkg install kmod-nft-core nftables  (or: apk add kmod-nft-core nftables)"
	}
	return n
}
