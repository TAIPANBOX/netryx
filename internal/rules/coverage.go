package rules

import (
	"io"

	"netryx/internal/inventory"
	"netryx/internal/uciconf"
)

// WirelessConfig is the router's Wi-Fi networks as /etc/config/wireless
// declares them (docs/stage2c-measured.md, section 6): the VM has no radio,
// so client isolation is read from the config, matched by the SSID hostapd
// reports for a client.
type WirelessConfig []WifiIface

// WifiIface is one wifi-iface section.
type WifiIface struct {
	SSID, Network string
	Isolate       bool
}

// Isolation says how an isolated device is kept from the rest of the house
// (spec 6.7) and, in Text, what is not covered and what to do about it.
// Routed and Bridged are always true: the rules table and the bridge table
// hold every isolated device.
type Isolation struct {
	Routed, Bridged bool
	SSID            string
	SSIDIsolated    bool
	HardwareSwitch  bool
	// SharedPort is how many devices the bridge learned on the device's
	// wired port: more than one is a switch or an access point behind it.
	SharedPort int
	Text       string
}

// ParseWireless reads /etc/config/wireless.
func ParseWireless(r io.Reader) (WirelessConfig, error) {
	secs, err := uciconf.Parse(r)
	if err != nil {
		return nil, err
	}
	var out WirelessConfig
	for _, s := range secs {
		if s.Type != "wifi-iface" {
			continue
		}
		out = append(out, WifiIface{SSID: first(s, "ssid"), Network: first(s, "network"), Isolate: enabledOff(first(s, "isolate"))})
	}
	return out, nil
}

// ssidIsolated says every wifi-iface carrying ssid isolates its clients; an
// SSID the config does not name is not isolated.
func (w WirelessConfig) ssidIsolated(ssid string) bool {
	found := false
	for _, i := range w {
		if i.SSID != ssid {
			continue
		}
		if !i.Isolate {
			return false
		}
		found = true
	}
	return found
}

// IsolationOf says how an isolated device on link is kept from the house.
// switchPort says a bridge port is on a hardware switch, whose ports talk to
// each other without the CPU. netryx never changes the Wi-Fi or the switch:
// the text says what the owner can do in LuCI.
func IsolationOf(link inventory.Link, w WirelessConfig, switchPort func(port string) bool, onPort int) Isolation {
	iso := Isolation{Routed: true, Bridged: true, Text: "isolated from the rest of the house"}
	switch {
	case link.Kind == "wifi" && link.SSID != "":
		iso.SSID = link.SSID
		iso.SSIDIsolated = w.ssidIsolated(link.SSID)
		if iso.SSIDIsolated {
			iso.Text = "isolated from its Wi-Fi neighbours on " + link.SSID
		} else {
			iso.Text = "not isolated from its Wi-Fi neighbours on " + link.SSID +
				": turn on client isolation for this network or move the device to the guest network"
		}
	case link.Kind == "wired" && link.Iface != "" && switchPort != nil && switchPort(link.Iface):
		iso.HardwareSwitch = true
		iso.Text = "not isolated from wired neighbours on the same switch: move the port to a network of its own"
	case link.Kind == "wired" && onPort > 1:
		// Frames between devices behind one unmanaged switch or access
		// point never reach the router (spec 6.7: complete only with one MAC
		// on the port).
		iso.SharedPort = onPort
		iso.Text = "not isolated from devices behind the same switch or access point on this router port"
	}
	return iso
}
