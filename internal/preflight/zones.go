package preflight

import (
	"fmt"
	"os"
	"strings"
)

// LANDevices reads which devices the firewall considers the house side.
//
// The default "br-lan" is right on most routers and wrong on some, and when it
// is wrong nothing complains: the rule installs, the hook attaches, and it
// watches the side facing the internet, where every device is already behind
// NAT. @measured 2026-09-15 on a Brume 2: br-lan carried the uplink and the
// routed network was on eth1, so the monitor was looking the wrong way while
// reporting that all was well.
func LANDevices(fwPath, netPath string) ([]string, error) {
	fw, err := os.ReadFile(fwPath)
	if err != nil {
		return nil, err
	}
	var networks []string
	in := false
	for _, line := range strings.Split(string(fw), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		switch {
		case f[0] == "config":
			in = false
		case in && (f[0] == "list" || f[0] == "option") && len(f) >= 3 && f[1] == "network":
			networks = append(networks, unquote(f[2]))
		}
		// A zone is only the lan one once its name says so, which is a line
		// below the "config zone" that opened it.
		if len(f) >= 3 && f[0] == "option" && f[1] == "name" && unquote(f[2]) == "lan" {
			in = true
		}
	}
	if len(networks) == 0 {
		return nil, fmt.Errorf("no lan zone in %s", fwPath)
	}
	return devicesOf(netPath, networks)
}

// devicesOf maps interface names to the devices they are made of.
func devicesOf(netPath string, want []string) ([]string, error) {
	b, err := os.ReadFile(netPath)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, w := range want {
		wanted[w] = true
	}
	var out []string
	name := ""
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if f[0] == "config" {
			name = ""
			if len(f) >= 3 && f[1] == "interface" {
				name = unquote(f[2])
			}
			continue
		}
		if name == "" || !wanted[name] || len(f) < 3 || f[0] != "option" {
			continue
		}
		// "device" is current uci; "ifname" is what older configurations and
		// half the forum posts still use.
		if f[1] == "device" || f[1] == "ifname" {
			if d := unquote(f[2]); d != "" {
				out = append(out, d)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the lan zone names %v, and none of them has a device", want)
	}
	return out, nil
}

func unquote(s string) string { return strings.Trim(s, `'"`) }

// SideNeed says whether the interfaces being watched are the ones the firewall
// calls the house side. It is advice, never a refusal: somebody watching the
// uplink deliberately is not making a mistake, they just have to have meant it.
func SideNeed(watching []string, lan []string, err error) Need {
	n := Need{What: "watching the side where the devices are", Module: "-", Package: "-", OK: true}
	if err != nil {
		n.Detail = "could not read the firewall's lan zone: " + err.Error()
		return n
	}
	have := map[string]bool{}
	for _, w := range watching {
		have[w] = true
	}
	for _, l := range lan {
		if have[l] {
			return n
		}
	}
	n.OK = false
	n.Detail = fmt.Sprintf("watching %v, but the firewall's lan zone is %v: on this router the devices are behind the second list, and everything on the first is already NATed", watching, lan)
	n.FixCmd = fmt.Sprintf("uci set netryx.main.interface='%s'; uci commit netryx; /etc/init.d/netryx restart", strings.Join(lan, " "))
	return n
}
