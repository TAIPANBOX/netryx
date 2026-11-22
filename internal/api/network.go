package api

// The Network screen: how the router is wired, read by netview, with the
// devices of the inventory placed behind each port, access point and forward.

import (
	"context"
	"net/http"
	"sort"
	"time"

	"netryx/internal/inventory"
	"netryx/internal/netview"
)

// Network reads the router's wiring. *netview.Source satisfies it.
type Network interface {
	View(context.Context) netview.View
}

// WithNetwork serves /api/network from n.
func WithNetwork(n Network) Option { return func(s *server) { s.net = n } }

const (
	// netReadTimeout bounds one reading of the router's wiring.
	netReadTimeout = 5 * time.Second
	// maxBehind bounds the devices listed behind one port or access point.
	maxBehind = 256
)

type netDeviceJSON struct {
	MAC    string `json:"mac"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
	Signal int    `json:"signal,omitempty"`
	Band   string `json:"band,omitempty"`
}

type portJSON struct {
	netview.Port
	Devices []netDeviceJSON `json:"devices"`
}

type liveAPJSON struct {
	netview.LiveAP
	Clients []netDeviceJSON `json:"clients"`
}

type wifiJSON struct {
	Why    string          `json:"why"`
	Note   string          `json:"note"`
	Radios []netview.Radio `json:"radios"`
	APs    []netview.AP    `json:"aps"`
	Live   []liveAPJSON    `json:"live"`
}

type peerJSON struct {
	netview.Peer
	// Handshake is the last handshake, null when the kernel never had one.
	Handshake *string `json:"handshake"`
}

type wgIfaceJSON struct {
	Name       string     `json:"name"`
	ListenPort int        `json:"listenPort"`
	Addresses  []string   `json:"addresses"`
	PublicKey  string     `json:"publicKey"`
	Up         bool       `json:"up"`
	Peers      []peerJSON `json:"peers"`
}

type wgJSON struct {
	Why        string        `json:"why"`
	Note       string        `json:"note"`
	Interfaces []wgIfaceJSON `json:"interfaces"`
}

type forwardJSON struct {
	netview.Forward
	// Device is the device that holds DestIP now, or null.
	Device *netDeviceJSON `json:"device"`
}

type forwardsJSON struct {
	Why   string        `json:"why"`
	Rules []forwardJSON `json:"rules"`
}

type netViewJSON struct {
	InterfacesWhy string          `json:"interfacesWhy"`
	Interfaces    []netview.Iface `json:"interfaces"`
	Ports         []portJSON      `json:"ports"`
	WiFi          wifiJSON        `json:"wifi"`
	WireGuard     wgJSON          `json:"wireguard"`
	Forwards      forwardsJSON    `json:"forwards"`
}

func (s *server) network(r *http.Request) (any, error) {
	if s.net == nil {
		return nil, httpError{http.StatusServiceUnavailable, "the router's network view is not available"}
	}
	// The reading has its own deadline; this one bounds the wait for it.
	ctx, cancel := context.WithTimeout(r.Context(), netReadTimeout)
	defer cancel()
	v := s.net.View(ctx)
	now := s.now()
	active := s.active(now)
	devs := s.inv.Devices()
	sort.SliceStable(devs, func(i, j int) bool { return devs[i].Name < devs[j].Name })
	view := func(d inventory.Device) netDeviceJSON {
		n := netDeviceJSON{MAC: d.MAC, Name: d.Name, Online: online(d, now, active)}
		if n.Name == "" {
			n.Name = d.MAC
		}
		return n
	}
	behind := func(kind, iface string) []netDeviceJSON {
		out := []netDeviceJSON{}
		for _, d := range devs {
			if d.Link.Kind == kind && d.Link.Iface == iface && len(out) < maxBehind {
				n := view(d)
				if kind == "wifi" {
					n.Signal, n.Band = d.Link.Signal, d.Link.Band
				}
				out = append(out, n)
			}
		}
		return out
	}

	out := netViewJSON{
		InterfacesWhy: v.IfacesWhy, Interfaces: v.Interfaces, Ports: []portJSON{},
		WiFi:      wifiJSON{Why: v.WiFi.Why, Note: v.WiFi.Note, Radios: v.WiFi.Radios, APs: v.WiFi.APs, Live: []liveAPJSON{}},
		WireGuard: wgJSON{Why: v.WireGuard.Why, Note: v.WireGuard.Note, Interfaces: []wgIfaceJSON{}},
		Forwards:  forwardsJSON{Why: v.Forwards.Why, Rules: []forwardJSON{}},
	}
	if out.Interfaces == nil {
		out.Interfaces = []netview.Iface{}
	}
	if out.WiFi.Radios == nil {
		out.WiFi.Radios = []netview.Radio{}
	}
	if out.WiFi.APs == nil {
		out.WiFi.APs = []netview.AP{}
	}
	for _, p := range v.Ports {
		out.Ports = append(out.Ports, portJSON{Port: p, Devices: behind("wired", p.Name)})
	}
	for _, ap := range v.WiFi.Live {
		out.WiFi.Live = append(out.WiFi.Live, liveAPJSON{LiveAP: ap, Clients: behind("wifi", ap.Iface)})
	}
	for _, wi := range v.WireGuard.Interfaces {
		j := wgIfaceJSON{Name: wi.Name, ListenPort: wi.ListenPort, Addresses: wi.Addresses, PublicKey: wi.PublicKey, Up: wi.Up, Peers: []peerJSON{}}
		if j.Addresses == nil {
			j.Addresses = []string{}
		}
		for _, p := range wi.Peers {
			pj := peerJSON{Peer: p}
			if !p.Handshake.IsZero() {
				t := ts(p.Handshake)
				pj.Handshake = &t
			}
			j.Peers = append(j.Peers, pj)
		}
		out.WireGuard.Interfaces = append(out.WireGuard.Interfaces, j)
	}
	for _, f := range v.Forwards.Rules {
		fj := forwardJSON{Forward: f}
		for _, d := range devs {
			for _, ip := range d.IPv4 {
				if ip == f.DestIP && fj.Device == nil {
					n := view(d)
					fj.Device = &n
				}
			}
		}
		out.Forwards.Rules = append(out.Forwards.Rules, fj)
	}
	return out, nil
}
