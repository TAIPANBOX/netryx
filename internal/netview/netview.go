// Package netview reads how the router is wired: its networks, the bridge's
// ports, Wi-Fi, WireGuard and port forwards. It only reads: ubus, sysfs, the
// UCI files and `wg show`, and never a password or a private key.
package netview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"netryx/internal/uciconf"
)

// What a router may not have, said in place of an empty box.
const (
	NoWiFi        = "this router has no Wi-Fi radio (no /etc/config/wireless and no access point running)"
	NoAccessPoint = "Wi-Fi is configured, but no access point is running now"
	NoWireGuard   = "no WireGuard interface is configured"
	NoWgTool      = "wireguard-tools is not installed, so handshakes and traffic are not shown"
	NoForwards    = "no port forwards are configured"
	NoNetifd      = "netifd did not answer, so the networks are not shown"
)

const (
	// cacheFor is how long one reading answers: the page polls, the router
	// is asked at most this often.
	cacheFor = 5 * time.Second
	// The lists are bounded: a home router has a handful of each.
	maxIfaces   = 64
	maxPorts    = 64
	maxRadios   = 16
	maxAPs      = 64
	maxWG       = 16
	maxPeers    = 256
	maxForwards = 256
)

// Iface is one network netifd runs: lan, wan, wan6, guest.
type Iface struct {
	Name      string   `json:"name"`
	Proto     string   `json:"proto"`
	Device    string   `json:"device"`
	Up        bool     `json:"up"`
	WAN       bool     `json:"wan"`
	UptimeSec int64    `json:"uptimeSec"`
	IPv4      []string `json:"ipv4"`
	IPv6      []string `json:"ipv6"`
	Gateway   string   `json:"gateway"`
	RxBytes   uint64   `json:"rxBytes"`
	TxBytes   uint64   `json:"txBytes"`
}

// Port is one wired port of a house bridge.
type Port struct {
	Name      string `json:"name"`
	Bridge    string `json:"bridge"`
	Up        bool   `json:"up"`
	SpeedMbps int    `json:"speedMbps"`
	// Switch: the port sits on a hardware switch (DSA), so its traffic with
	// the other ports of that switch never reaches the CPU.
	Switch bool `json:"switch"`
}

// Radio is a wifi-device of the config.
type Radio struct {
	Name     string `json:"name"`
	Band     string `json:"band"`
	Channel  string `json:"channel"`
	Width    string `json:"width"`
	Disabled bool   `json:"disabled"`
}

// AP is a wifi-iface of the config: a network a radio offers.
type AP struct {
	Name     string `json:"name"`
	Radio    string `json:"radio"`
	SSID     string `json:"ssid"`
	Mode     string `json:"mode"`
	Network  string `json:"network"`
	Disabled bool   `json:"disabled"`
}

// LiveAP is an access point hostapd runs now.
type LiveAP struct {
	Iface   string `json:"iface"`
	SSID    string `json:"ssid"`
	Band    string `json:"band"`
	Channel int    `json:"channel"`
}

// WiFi is the radios and networks of the config and what runs now. Why is
// set when the router has none; Note when something stops it showing more.
type WiFi struct {
	Why    string   `json:"why"`
	Note   string   `json:"note"`
	Radios []Radio  `json:"radios"`
	APs    []AP     `json:"aps"`
	Live   []LiveAP `json:"live"`
}

// Peer is a WireGuard peer: its name from the config, and from the kernel
// where it was last seen, its last handshake and its traffic.
type Peer struct {
	Name       string    `json:"name"`
	PublicKey  string    `json:"publicKey"`
	Endpoint   string    `json:"endpoint"`
	AllowedIPs []string  `json:"allowedIps"`
	Handshake  time.Time `json:"-"`
	Rx         uint64    `json:"rx"`
	Tx         uint64    `json:"tx"`
}

// WGIface is a WireGuard interface of the config.
type WGIface struct {
	Name       string   `json:"name"`
	ListenPort int      `json:"listenPort"`
	Addresses  []string `json:"addresses"`
	PublicKey  string   `json:"publicKey"`
	Up         bool     `json:"up"`
	Peers      []Peer   `json:"peers"`
}

type WireGuard struct {
	Why        string    `json:"why"`
	Note       string    `json:"note"`
	Interfaces []WGIface `json:"interfaces"`
}

// Forward is a DNAT redirect of the firewall config.
type Forward struct {
	Name     string `json:"name"`
	From     string `json:"from"`
	Proto    string `json:"proto"`
	Port     string `json:"port"`
	DestIP   string `json:"destIp"`
	DestPort string `json:"destPort"`
	Enabled  bool   `json:"enabled"`
}

type Forwards struct {
	Why   string    `json:"why"`
	Rules []Forward `json:"rules"`
}

// View is the whole reading.
type View struct {
	At         time.Time `json:"-"`
	IfacesWhy  string    `json:"interfacesWhy"`
	Interfaces []Iface   `json:"interfaces"`
	Ports      []Port    `json:"ports"`
	WiFi       WiFi      `json:"wifi"`
	WireGuard  WireGuard `json:"wireguard"`
	Forwards   Forwards  `json:"forwards"`
}

// Runner runs a program and returns what it printed.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Source reads the view from the router: FS is its root, Run its programs,
// Bridges the house bridges whose ports are listed.
type Source struct {
	FS      fs.FS
	Run     Runner
	Bridges []string
	Now     func() time.Time

	mu     sync.Mutex
	cached View
	has    bool
}

// View returns the router's view, read at most every cacheFor.
func (s *Source) View(ctx context.Context) View {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	if s.has && now.Sub(s.cached.At) < cacheFor && !now.Before(s.cached.At) {
		return s.cached
	}
	v := s.read(ctx)
	v.At = now
	s.cached, s.has = v, true
	return v
}

func (s *Source) read(ctx context.Context) View {
	v := View{Interfaces: []Iface{}, Ports: []Port{}}
	if out, err := s.Run(ctx, "ubus", "call", "network.interface", "dump"); err == nil {
		if ifs := ParseInterfaces(out); ifs != nil {
			v.Interfaces = ifs
		}
	}
	if len(v.Interfaces) == 0 {
		v.IfacesWhy = NoNetifd
	}
	for i := range v.Interfaces {
		dev := v.Interfaces[i].Device
		if validIface(dev) {
			v.Interfaces[i].RxBytes = s.counter(dev, "rx_bytes")
			v.Interfaces[i].TxBytes = s.counter(dev, "tx_bytes")
		}
	}
	v.Ports = s.ports()
	v.WiFi = s.wifi(ctx)
	v.WireGuard = s.wireguard(ctx)
	v.Forwards = Forwards{Rules: []Forward{}}
	if b, err := fs.ReadFile(s.FS, "etc/config/firewall"); err == nil {
		v.Forwards.Rules = ParseForwards(b)
	}
	if len(v.Forwards.Rules) == 0 {
		v.Forwards.Why = NoForwards
	}
	return v
}

func (s *Source) sysfs(path string) string {
	b, err := fs.ReadFile(s.FS, "sys/class/net/"+path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (s *Source) counter(dev, name string) uint64 {
	n, _ := strconv.ParseUint(s.sysfs(dev+"/statistics/"+name), 10, 64)
	return n
}

// ports lists each house bridge's wired ports: radios are access points,
// not ports.
func (s *Source) ports() []Port {
	out := []Port{}
	for _, br := range s.Bridges {
		if !validIface(br) {
			continue
		}
		ents, err := fs.ReadDir(s.FS, "sys/class/net/"+br+"/brif")
		if err != nil {
			continue
		}
		for _, e := range ents {
			name := e.Name()
			if !validIface(name) || s.isRadio(name) || len(out) >= maxPorts {
				continue
			}
			speed, _ := strconv.Atoi(s.sysfs(name + "/speed"))
			if speed < 0 {
				speed = 0
			}
			out = append(out, Port{
				Name: name, Bridge: br, Up: s.sysfs(name+"/carrier") == "1", SpeedMbps: speed,
				Switch: s.sysfs(name+"/phys_switch_id") != "",
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Source) isRadio(port string) bool {
	for _, p := range []string{"phy80211", "wireless"} {
		if _, err := fs.Stat(s.FS, "sys/class/net/"+port+"/"+p); err == nil {
			return true
		}
	}
	return false
}

func (s *Source) wifi(ctx context.Context) WiFi {
	w := WiFi{Radios: []Radio{}, APs: []AP{}, Live: []LiveAP{}}
	if b, err := fs.ReadFile(s.FS, "etc/config/wireless"); err == nil {
		w.Radios, w.APs = ParseWireless(b)
	}
	if out, err := s.Run(ctx, "ubus", "list", "hostapd.*"); err == nil {
		for _, obj := range strings.Fields(string(out)) {
			iface, ok := strings.CutPrefix(obj, "hostapd.")
			if !ok || !validIface(iface) || len(w.Live) >= maxAPs {
				continue
			}
			ap := LiveAP{Iface: iface}
			if st, err := s.Run(ctx, "ubus", "call", obj, "get_status"); err == nil {
				var h struct {
					SSID    string `json:"ssid"`
					Freq    int    `json:"freq"`
					Channel int    `json:"channel"`
				}
				if json.Unmarshal(st, &h) == nil {
					ap.SSID, ap.Band, ap.Channel = h.SSID, BandOf(h.Freq), h.Channel
				}
			}
			w.Live = append(w.Live, ap)
		}
	}
	switch {
	case len(w.Radios) == 0 && len(w.Live) == 0:
		w.Why = NoWiFi
	case len(w.Live) == 0:
		w.Note = NoAccessPoint
	}
	return w
}

func (s *Source) wireguard(ctx context.Context) WireGuard {
	w := WireGuard{Interfaces: []WGIface{}}
	if b, err := fs.ReadFile(s.FS, "etc/config/network"); err == nil {
		w.Interfaces = ParseWireGuardConfig(b)
	}
	if len(w.Interfaces) == 0 {
		w.Why = NoWireGuard
		return w
	}
	for i := range w.Interfaces {
		name := w.Interfaces[i].Name
		if !validIface(name) {
			continue
		}
		out, err := s.Run(ctx, "wg", "show", name, "dump")
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				w.Note = NoWgTool
			}
			continue
		}
		ApplyWireGuardDump(w.Interfaces[i:i+1], out)
	}
	return w
}

// BandOf maps a centre frequency in MHz to its band.
func BandOf(freq int) string {
	switch {
	case freq <= 0:
		return ""
	case freq < 3000:
		return "2.4 GHz"
	case freq < 5925:
		return "5 GHz"
	default:
		return "6 GHz"
	}
}

var bands = map[string]string{"2g": "2.4 GHz", "5g": "5 GHz", "6g": "6 GHz", "60g": "60 GHz"}

// ParseInterfaces reads `ubus call network.interface dump`. Loopback is left
// out; a network holding a default route is the WAN. A reply that does not
// parse is nil.
func ParseInterfaces(js []byte) []Iface {
	type addr struct {
		Address string `json:"address"`
		Mask    int    `json:"mask"`
		Local   *struct {
			Address string `json:"address"`
			Mask    int    `json:"mask"`
		} `json:"local-address"`
	}
	var d struct {
		Interface []struct {
			Interface string `json:"interface"`
			Up        bool   `json:"up"`
			Uptime    int64  `json:"uptime"`
			L3        string `json:"l3_device"`
			Device    string `json:"device"`
			Proto     string `json:"proto"`
			V4        []addr `json:"ipv4-address"`
			V6        []addr `json:"ipv6-address"`
			Assign    []addr `json:"ipv6-prefix-assignment"`
			Route     []struct {
				Target  string `json:"target"`
				Mask    int    `json:"mask"`
				Nexthop string `json:"nexthop"`
			} `json:"route"`
		} `json:"interface"`
	}
	if json.Unmarshal(js, &d) != nil {
		return nil
	}
	out := []Iface{}
	for _, i := range d.Interface {
		if i.Interface == "loopback" || i.Device == "lo" || len(out) >= maxIfaces {
			continue
		}
		f := Iface{Name: i.Interface, Proto: i.Proto, Device: i.L3, Up: i.Up, UptimeSec: i.Uptime, IPv4: []string{}, IPv6: []string{}}
		if f.Device == "" {
			f.Device = i.Device
		}
		for _, a := range i.V4 {
			f.IPv4 = append(f.IPv4, fmt.Sprintf("%s/%d", a.Address, a.Mask))
		}
		for _, a := range i.V6 {
			f.IPv6 = append(f.IPv6, fmt.Sprintf("%s/%d", a.Address, a.Mask))
		}
		for _, a := range i.Assign {
			if a.Local != nil && a.Local.Address != "" {
				f.IPv6 = append(f.IPv6, fmt.Sprintf("%s/%d", a.Local.Address, a.Local.Mask))
			}
		}
		for _, r := range i.Route {
			if r.Mask == 0 && (r.Target == "0.0.0.0" || r.Target == "::") {
				f.WAN = true
				if r.Target == "0.0.0.0" && f.Gateway == "" {
					f.Gateway = r.Nexthop
				}
			}
		}
		out = append(out, f)
	}
	return out
}

func uci(b []byte) []uciconf.Section {
	secs, err := uciconf.Parse(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	return secs
}

func opt(s uciconf.Section, k string) string {
	if v := s.Options[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func off(s uciconf.Section) bool { return opt(s, "disabled") == "1" }

// ParseWireless reads /etc/config/wireless: the radios and the networks they
// offer. Keys and passwords are never read.
func ParseWireless(b []byte) ([]Radio, []AP) {
	radios, aps := []Radio{}, []AP{}
	for _, s := range uci(b) {
		switch s.Type {
		case "wifi-device":
			if len(radios) < maxRadios {
				band := bands[opt(s, "band")]
				if band == "" {
					band = opt(s, "band")
				}
				radios = append(radios, Radio{Name: s.Name, Band: band, Channel: opt(s, "channel"), Width: opt(s, "htmode"), Disabled: off(s)})
			}
		case "wifi-iface":
			if len(aps) < maxAPs {
				aps = append(aps, AP{Name: s.Name, Radio: opt(s, "device"), SSID: opt(s, "ssid"), Mode: opt(s, "mode"), Network: opt(s, "network"), Disabled: off(s)})
			}
		}
	}
	return radios, aps
}

// ParseWireGuardConfig reads the WireGuard interfaces of /etc/config/network
// and their peers (sections of type wireguard_<interface>). Private and
// preshared keys are never read.
func ParseWireGuardConfig(b []byte) []WGIface {
	secs := uci(b)
	out := []WGIface{}
	at := map[string]int{}
	for _, s := range secs {
		if s.Type == "interface" && opt(s, "proto") == "wireguard" && len(out) < maxWG {
			port, _ := strconv.Atoi(opt(s, "listen_port"))
			at[s.Name] = len(out)
			out = append(out, WGIface{Name: s.Name, ListenPort: port, Addresses: append([]string{}, s.Options["addresses"]...), Peers: []Peer{}})
		}
	}
	for _, s := range secs {
		name, ok := strings.CutPrefix(s.Type, "wireguard_")
		i, known := at[name]
		if !ok || !known || len(out[i].Peers) >= maxPeers {
			continue
		}
		p := Peer{Name: opt(s, "description"), PublicKey: opt(s, "public_key"), AllowedIPs: append([]string{}, s.Options["allowed_ips"]...)}
		if h := opt(s, "endpoint_host"); h != "" {
			p.Endpoint = h
			if port := opt(s, "endpoint_port"); port != "" {
				p.Endpoint += ":" + port
			}
		}
		out[i].Peers = append(out[i].Peers, p)
	}
	return out
}

// ApplyWireGuardDump adds what `wg show <if> dump` says to the interfaces of
// the config: the interface's public key, and each peer's endpoint, last
// handshake and traffic. The private and preshared keys it prints are read
// past, never kept.
func ApplyWireGuardDump(ifs []WGIface, dump []byte) {
	at := map[string]int{}
	for i := range ifs {
		at[ifs[i].Name] = i
	}
	for _, line := range strings.Split(string(dump), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		i, ok := at[f[0]]
		if !ok {
			continue
		}
		switch len(f) {
		case 5: // interface: name, private key, public key, port, fwmark
			ifs[i].PublicKey, ifs[i].Up = f[2], true
			if p, err := strconv.Atoi(f[3]); err == nil {
				ifs[i].ListenPort = p
			}
		case 9: // peer: name, public key, psk, endpoint, allowed, handshake, rx, tx, keepalive
			for j := range ifs[i].Peers {
				p := &ifs[i].Peers[j]
				if p.PublicKey != f[1] {
					continue
				}
				if f[3] != "(none)" {
					p.Endpoint = f[3]
				}
				if hs, err := strconv.ParseInt(f[5], 10, 64); err == nil && hs > 0 {
					p.Handshake = time.Unix(hs, 0)
				}
				p.Rx, _ = strconv.ParseUint(f[6], 10, 64)
				p.Tx, _ = strconv.ParseUint(f[7], 10, 64)
			}
		}
	}
}

// ParseForwards reads the DNAT redirects of /etc/config/firewall: a port on
// the outside led to a device inside.
func ParseForwards(b []byte) []Forward {
	out := []Forward{}
	for _, s := range uci(b) {
		if s.Type != "redirect" || len(out) >= maxForwards {
			continue
		}
		if t := opt(s, "target"); t != "" && t != "DNAT" {
			continue
		}
		f := Forward{
			Name: opt(s, "name"), From: opt(s, "src"), Proto: strings.Join(s.Options["proto"], " "),
			Port: opt(s, "src_dport"), DestIP: opt(s, "dest_ip"), DestPort: opt(s, "dest_port"),
			Enabled: opt(s, "enabled") != "0",
		}
		if f.Proto == "" {
			f.Proto = "tcp udp"
		}
		if f.DestPort == "" {
			f.DestPort = f.Port
		}
		out = append(out, f)
	}
	return out
}

var ifaceName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._@-]{0,14}$`)

// validIface is a Linux interface name, so it cannot walk out of sysfs or
// read as an option to a program.
func validIface(s string) bool { return ifaceName.MatchString(s) && s != "." && s != ".." }
