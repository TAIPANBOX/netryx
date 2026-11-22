package device

import (
	"bufio"
	"io"
	"net"
	"sort"
	"strings"
)

// Lease is one DHCP lease as dnsmasq/odhcpd writes it: the name a device gave
// for itself, which is what a person recognises in a dashboard.
type Lease struct {
	MAC  string
	IP   net.IP
	Name string
}

// ParseLeases reads /tmp/dhcp.leases: expiry, MAC, address, name, client-id.
func ParseLeases(r io.Reader) []Lease {
	var out []Lease
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		ip := net.ParseIP(f[2])
		if ip == nil {
			continue
		}
		name := f[3]
		if name == "*" {
			name = ""
		}
		out = append(out, Lease{MAC: strings.ToLower(f[1]), IP: ip, Name: name})
	}
	return out
}

// Table maps an address to what a person would call it.
type Table struct {
	byIP  map[string]Lease
	byMAC map[string]Lease
}

func NewTable(leases []Lease) *Table {
	t := &Table{byIP: map[string]Lease{}, byMAC: map[string]Lease{}}
	for _, l := range leases {
		t.byIP[l.IP.String()] = l
		t.byMAC[strings.ToLower(l.MAC)] = l
	}
	return t
}

// NameByMAC returns the name a device gave itself in its lease, or "" when the
// lease has none. Unlike Name it does not fall back to the address: the caller
// merges several sources and needs to know whether the DHCP name really exists.
func (t *Table) NameByMAC(mac string) string {
	if t == nil {
		return ""
	}
	return t.byMAC[strings.ToLower(mac)].Name
}

// Name returns the device name for an address, falling back to its MAC and
// then to the address itself, so a row is never blank.
func (t *Table) Name(ip net.IP) string {
	if t == nil || ip == nil {
		return ""
	}
	l, ok := t.byIP[ip.String()]
	if !ok {
		return ip.String()
	}
	if l.Name != "" {
		return l.Name
	}
	if l.MAC != "" {
		return l.MAC
	}
	return ip.String()
}

// MACs lists every device that holds a lease, in lowercase colon form and
// sorted, so a device that is idle still has a row. A nil table knows none.
func (t *Table) MACs() []string {
	if t == nil {
		return nil
	}
	out := make([]string, 0, len(t.byMAC))
	for m := range t.byMAC {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// IPByMAC returns the address the lease gave a device, or "" when there is no
// such lease. It is what places a device that has never been seen on the wire.
func (t *Table) IPByMAC(mac string) string {
	if t == nil {
		return ""
	}
	l, ok := t.byMAC[strings.ToLower(mac)]
	if !ok || l.IP == nil {
		return ""
	}
	return l.IP.String()
}

// MACByIP returns the hardware address that holds a lease on this address, or
// "" when no lease names it.
func (t *Table) MACByIP(ip string) string {
	if t == nil {
		return ""
	}
	return t.byIP[ip].MAC
}
