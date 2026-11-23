// Package inventory merges what the router knows about each device into one
// record per hardware address: the name a person recognises, the addresses it
// holds, its Wi-Fi link, when it was first and last seen, and the label and
// group the household gave it.
//
// Every source is optional. A missing lease file or an absent labels file
// leaves the device present with less detail, never absent from the table.
package inventory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"netryx/internal/collect"
	"netryx/internal/device"
	"netryx/internal/neigh"
)

// Link is how a device is attached right now. Kind is "wifi" when hostapd
// reports the client, and "" when nothing says how it is connected.
type Link struct {
	Kind   string
	Iface  string
	SSID   string
	Band   string
	Signal int
}

// Device is one row of the inventory, keyed by its lowercase colon MAC.
type Device struct {
	MAC      string
	Name     string
	Hostname string
	Vendor   string
	Label    string
	Group    string
	Random   bool
	IPv4     []string
	IPv6     []string
	Link     Link
	First    time.Time
	Last     time.Time
	// Reachable says the kernel's neighbour table has the device REACHABLE
	// now: on the network, whatever it was last seen doing.
	Reachable bool
}

// Label is what a person typed in for a device.
type Label struct {
	Name  string `json:"name"`
	Group string `json:"group"`
}

// Sources are the inputs Build merges. Each may be nil or empty.
type Sources struct {
	Collected []collect.Device
	Leases    *device.Table
	Neigh     *neigh.Registry
	OUI       Vendors
	WiFi      map[string]Link
	Labels    map[string]Label
}

// maxLabelsBytes and maxLabels bound the labels file. It is typed by people and
// read on every start, so a huge or hostile file is refused, not loaded.
const (
	maxLabelsBytes = 1 << 20
	maxLabels      = 10000
)

// Build returns one Device per MAC the house has known: seen on the wire, seen
// on Wi-Fi, or holding a DHCP lease. An idle device with a lease is still a row,
// since a client list shows known hardware. A label alone does not list a
// device: a label for hardware the house has never had is not a device.
func Build(s Sources) []Device {
	collected := map[string]collect.Device{}
	for _, c := range s.Collected {
		m := normMAC(c.MAC)
		if m == "" {
			// A row the capture could only key by address: it is the device
			// the neighbour table or a lease says holds that address, or no
			// device at all.
			m = s.macOfAddrs(c.Addrs)
		}
		if m == "" || m == collect.RouterKey {
			continue
		}
		if prev, ok := collected[m]; ok {
			c = joinCollected(prev, c)
		}
		c.MAC = m
		collected[m] = c
	}
	wifi := map[string]Link{}
	for m, l := range s.WiFi {
		wifi[normMAC(m)] = l
	}
	labels := map[string]Label{}
	for m, l := range s.Labels {
		labels[normMAC(m)] = cleanLabel(l)
	}

	macs := map[string]bool{}
	for m := range collected {
		macs[m] = true
	}
	for m := range wifi {
		macs[m] = true
	}
	for _, m := range s.Leases.MACs() {
		macs[normMAC(m)] = true
	}
	order := make([]string, 0, len(macs))
	for m := range macs {
		order = append(order, m)
	}
	sort.Strings(order)

	out := make([]Device, 0, len(order))
	for _, m := range order {
		c := collected[m]
		lab := labels[m]
		d := Device{
			MAC:       m,
			Random:    IsRandom(m),
			Label:     lab.Name,
			Group:     lab.Group,
			Hostname:  s.Leases.NameByMAC(m),
			Link:      wifi[m],
			First:     c.First,
			Last:      c.Last,
			Reachable: s.Neigh.Reachable(m),
		}
		// A random address was invented by the phone, so the first three bytes
		// say nothing about the maker. Showing a vendor there would be a lie.
		if !d.Random {
			d.Vendor = vendorOf(s.OUI, m)
		}
		d.Name = displayName(d, m)
		seen := append([]string(nil), c.Addrs...)
		if ip := s.Leases.IPByMAC(m); ip != "" {
			seen = append(seen, ip)
		}
		d.IPv4, d.IPv6 = splitAddrs(addrsOf(s.Neigh, m, seen))
		out = append(out, d)
	}
	return out
}

// macOfAddrs is the hardware address the neighbour table, or else a lease,
// gives the first of these addresses it knows, or "".
func (s Sources) macOfAddrs(addrs []string) string {
	for _, a := range addrs {
		if m := s.Neigh.MACOf(net.ParseIP(a)); m != "" {
			return normMAC(m)
		}
		if m := s.Leases.MACByIP(a); m != "" {
			return normMAC(m)
		}
	}
	return ""
}

// joinCollected folds two collector rows of one device into one: the earliest
// first sighting, the latest last one, every address and every count.
func joinCollected(a, b collect.Device) collect.Device {
	if a.First.IsZero() || (!b.First.IsZero() && b.First.Before(a.First)) {
		a.First = b.First
	}
	if b.Last.After(a.Last) {
		a.Last = b.Last
	}
	addrs := append([]string(nil), a.Addrs...)
	for _, x := range b.Addrs {
		if !contains(addrs, x) {
			addrs = append(addrs, x)
		}
	}
	a.Addrs = addrs
	a.Handshakes += b.Handshakes
	a.Hidden += b.Hidden
	a.Unnamed += b.Unnamed
	a.TotalUp += b.TotalUp
	a.TotalDown += b.TotalDown
	a.Closed += b.Closed
	return a
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// displayName is the first of: the label, the DHCP name, the vendor with the
// last three bytes of the address, and the address itself. The last three
// bytes tell two identical vendors apart in a list.
func displayName(d Device, mac string) string {
	switch {
	case d.Label != "":
		return d.Label
	case d.Hostname != "":
		return d.Hostname
	case d.Vendor != "" && len(mac) == 17:
		return d.Vendor + " " + mac[9:]
	default:
		return mac
	}
}

// addrsOf joins the addresses the capture saw with those the neighbour table
// holds for the same MAC, so an IPv6 temporary address that only the neighbour
// table has still lands on the device.
func addrsOf(reg *neigh.Registry, mac string, seen []string) []string {
	all := append([]string(nil), seen...)
	if reg != nil {
		all = append(all, reg.AddressesOf(mac)...)
	}
	return all
}

// splitAddrs separates IPv4 from IPv6, drops duplicates and anything that is
// not an address, and sorts both lists so the table does not reorder on refresh.
func splitAddrs(all []string) (v4, v6 []string) {
	seen := map[string]bool{}
	for _, a := range all {
		ip := net.ParseIP(a)
		if ip == nil || seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		if ip.To4() != nil {
			v4 = append(v4, ip.String())
		} else {
			v6 = append(v6, ip.String())
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	return v4, v6
}

// vendorOf looks the first three bytes of a MAC up in the registry.
func vendorOf(oui Vendors, mac string) string {
	hex := strings.ReplaceAll(mac, ":", "")
	if len(hex) < 6 || oui == nil {
		return ""
	}
	return oui.Vendor(strings.ToUpper(hex[:6]))
}

// IsRandom reports whether a MAC is locally administered. Phones use such
// addresses to stop being tracked across networks, so the flag tells a person
// why one phone may show up under several addresses over time.
func IsRandom(mac string) bool {
	m := normMAC(mac)
	if len(m) < 2 {
		return false
	}
	b, err := strconv.ParseUint(m[:2], 16, 8)
	if err != nil {
		return false
	}
	return b&0x02 != 0
}

// LoadLabels reads the labels file, a JSON object keyed by MAC. A missing file
// is an empty set of labels, because a router that has never been labelled is
// a normal state, not an error.
func LoadLabels(path string) (map[string]Label, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Label{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxLabelsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxLabelsBytes {
		return nil, fmt.Errorf("labels: file is larger than %d bytes", maxLabelsBytes)
	}
	var raw map[string]Label
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if len(raw) > maxLabels {
		return nil, fmt.Errorf("labels: more than %d entries", maxLabels)
	}
	out := make(map[string]Label, len(raw))
	for k, v := range raw {
		out[normMAC(k)] = cleanLabel(v)
	}
	return out, nil
}

// cleanLabel removes control characters, newlines included, and surrounding
// space from a label typed by a person. A name left empty is then absent, so
// the next name in the precedence wins instead of a blank row.
func cleanLabel(l Label) Label {
	return Label{Name: cleanText(l.Name), Group: cleanText(l.Group)}
}

func cleanText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}

// normMAC is the one form every MAC in the package takes: lowercase, colon
// separated, so that a key typed as "8C-79-F5-..." joins the one from the kernel.
func normMAC(s string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(s, "-", ":")))
}
