package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"netryx/internal/inventory"
	"netryx/internal/netview"
)

type fakeNet struct{ v netview.View }

func (f fakeNet) View(context.Context) netview.View { return f.v }

func netFixture() netview.View {
	return netview.View{
		Interfaces: []netview.Iface{{Name: "wan", Proto: "dhcp", Device: "eth0", Up: true, WAN: true, IPv4: []string{"192.0.2.230/24"}, IPv6: []string{}}},
		Ports: []netview.Port{
			{Name: "lan1", Bridge: "br-lan", Up: true, SpeedMbps: 1000, Switch: true},
			{Name: "lan2", Bridge: "br-lan"},
		},
		WiFi: netview.WiFi{
			Radios: []netview.Radio{{Name: "radio1", Band: "5 GHz", Channel: "36"}},
			APs:    []netview.AP{{Name: "default_radio1", Radio: "radio1", SSID: "home", Mode: "ap"}},
			Live:   []netview.LiveAP{{Iface: "wl0", SSID: "home", Band: "5 GHz", Channel: 36}},
		},
		WireGuard: netview.WireGuard{Interfaces: []netview.WGIface{{
			Name: "wg0", Up: true, Peers: []netview.Peer{
				{Name: "Phone", PublicKey: "cGhvbmU=", Handshake: now0.Add(-90 * time.Second), Rx: 5, Tx: 6},
				{Name: "Laptop", PublicKey: "bGFwdG9w"},
			},
		}}},
		Forwards: netview.Forwards{Rules: []netview.Forward{
			{Name: "Game", Proto: "udp", Port: "3074", DestIP: "192.168.1.10", DestPort: "3074", Enabled: true},
			{Name: "Old", Proto: "tcp", Port: "8080", DestIP: "192.168.1.99", DestPort: "80"},
		}},
	}
}

// The Network screen: the router's networks, and beside each port and access
// point the devices behind it, by name, with their signal on Wi-Fi.
func TestTheNetworkViewNamesTheDevicesBehindEachPortAndAccessPoint(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	inv.devs = append(inv.devs,
		inventory.Device{MAC: "aa:bb:cc:dd:ee:04", Name: "NAS", Link: inventory.Link{Kind: "wired", Iface: "lan1"}, Last: now0},
		// Learned on the radio's bridge port by the forwarding table, not by
		// hostapd: not a Wi-Fi client of it.
		inventory.Device{MAC: "aa:bb:cc:dd:ee:05", Name: "Repeater", Link: inventory.Link{Kind: "wired", Iface: "wl0"}, Last: now0})
	rec := get(t, New(fl, h, inv, w, ap, clock, nil, WithNetwork(fakeNet{netFixture()})), http.MethodGet, "/api/network")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if str(field(at(field(body, "interfaces"), 0), "name")) != "wan" {
		t.Errorf("interfaces %v", field(body, "interfaces"))
	}
	lan1 := at(field(body, "ports"), 0)
	if d := arr(field(lan1, "devices")); len(d) != 1 || str(field(d[0], "name")) != "NAS" || field(d[0], "online") != true {
		t.Errorf("lan1 devices %v", field(lan1, "devices"))
	}
	if d := arr(field(at(field(body, "ports"), 1), "devices")); len(d) != 0 {
		t.Errorf("lan2 devices %v, want none", d)
	}
	live := at(field(body, "wifi", "live"), 0)
	cl := arr(field(live, "clients"))
	if len(cl) != 1 || str(field(cl[0], "name")) != "Laptop" || num(field(cl[0], "signal")) != -50 {
		t.Errorf("wl0 clients %v, want the laptop at -50 dBm", cl)
	}
	peer := at(field(at(field(body, "wireguard", "interfaces"), 0), "peers"), 0)
	if str(field(peer, "handshake")) != ts(now0.Add(-90*time.Second)) || num(field(peer, "rx")) != 5 {
		t.Errorf("peer %v", peer)
	}
	if hs := field(at(field(at(field(body, "wireguard", "interfaces"), 0), "peers"), 1), "handshake"); hs != nil {
		t.Errorf("a peer never seen has handshake %v, want null", hs)
	}
	game := at(field(body, "forwards", "rules"), 0)
	if str(field(game, "device", "name")) != "Laptop" {
		t.Errorf("the forward to 192.168.1.10 should name the laptop: %v", game)
	}
	if field(at(field(body, "forwards", "rules"), 1), "device") != nil {
		t.Errorf("a forward to an address no device holds names no device")
	}
}

// What the router lacks reaches the page as the reason, not as an empty list.
func TestTheNetworkViewSaysWhatTheRouterLacks(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	v := netview.View{
		Interfaces: []netview.Iface{}, Ports: []netview.Port{},
		WiFi:      netview.WiFi{Why: netview.NoWiFi},
		WireGuard: netview.WireGuard{Why: netview.NoWireGuard},
		Forwards:  netview.Forwards{Why: netview.NoForwards},
	}
	body := decode(t, get(t, New(fl, h, inv, w, ap, clock, nil, WithNetwork(fakeNet{v})), http.MethodGet, "/api/network"))
	if str(field(body, "wifi", "why")) != netview.NoWiFi || str(field(body, "wireguard", "why")) != netview.NoWireGuard || str(field(body, "forwards", "why")) != netview.NoForwards {
		t.Errorf("whys %v", body)
	}
	for _, k := range []string{"interfaces", "ports"} {
		if field(body, k) == nil {
			t.Errorf("%s is null, want an empty list", k)
		}
	}
}

func TestWithoutANetworkSourceTheViewIsUnavailable(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	if rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/network"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
}

// End to end from a router's files: no private or preshared key and no Wi-Fi
// password ever reaches the page.
func TestTheNetworkViewNeverCarriesASecret(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	src := &netview.Source{FS: secretFS(), Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("wg0\tUFJJVkFURQ==\tUFVCTElD\t51820\toff\nwg0\tcGVlcg==\tUFJFU0hBUkVE\t(none)\t10.8.0.2/32\t0\t0\t0\toff\n"), nil
	}, Now: clock}
	body := get(t, New(fl, h, inv, w, ap, clock, nil, WithNetwork(src)), http.MethodGet, "/api/network").Body.String()
	for _, secret := range []string{"UFJJVkFURQ==", "UFJFU0hBUkVE", "c2VjcmV0LXdpZmk="} {
		if strings.Contains(body, secret) {
			t.Errorf("a secret reached the page: %s", body)
		}
	}
	if !strings.Contains(body, "UFVCTElD") {
		t.Errorf("the interface's public key is missing: %s", body)
	}
}
