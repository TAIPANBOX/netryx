package netview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The router's networks as netifd reports them: each with its protocol,
// device, addresses and uptime; the WAN is the one holding a default route,
// and loopback is not a network of the house.
func TestInterfacesAreReadFromNetifd(t *testing.T) {
	got := ParseInterfaces(read(t, "vm-interface-dump.json"), nil)
	if len(got) != 3 {
		t.Fatalf("%d interfaces, want lan, wan and wan6: %+v", len(got), got)
	}
	by := map[string]Iface{}
	for _, i := range got {
		by[i.Name] = i
	}
	lan, wan, wan6 := by["lan"], by["wan"], by["wan6"]
	if lan.Device != "br-lan" || lan.Proto != "static" || !lan.Up || lan.WAN || len(lan.IPv4) != 1 || lan.IPv4[0] != "192.168.50.1/24" {
		t.Errorf("lan %+v", lan)
	}
	if len(lan.IPv6) != 1 || lan.IPv6[0] != "fd00:1:2::1/60" {
		t.Errorf("lan's own address from its prefix assignment: %v", lan.IPv6)
	}
	if !wan.WAN || wan.Proto != "dhcp" || wan.Device != "eth0" || wan.Gateway != "192.0.2.1" || wan.UptimeSec != 85826 {
		t.Errorf("wan %+v", wan)
	}
	if !wan6.WAN || len(wan6.IPv6) != 1 {
		t.Errorf("wan6 %+v", wan6)
	}
	if ParseInterfaces([]byte("not json"), nil) != nil {
		t.Errorf("a reply that does not parse is no interfaces")
	}
}

// Wi-Fi as the config sets it up: radios with band, channel and width, and
// the networks each one offers; a password is never read.
func TestWirelessConfigNamesRadiosAndNetworks(t *testing.T) {
	radios, aps, err := ParseWireless(read(t, "wireless.uci"))
	if err != nil {
		t.Fatal(err)
	}
	if len(radios) != 3 || radios[0].Band != "2.4 GHz" || radios[1].Channel != "36" || radios[1].Width != "HE80" || !radios[2].Disabled {
		t.Errorf("radios %+v", radios)
	}
	if len(aps) != 3 || aps[0].SSID != "Home" || aps[0].Radio != "radio0" || !aps[2].Disabled || aps[2].Network != "guest" {
		t.Errorf("networks %+v", aps)
	}
	b, _ := json.Marshal(aps)
	if strings.Contains(string(b), "not-a-real-password") {
		t.Errorf("a Wi-Fi password reached the view: %s", b)
	}
}

// WireGuard: the interfaces and peers the config names, with what the kernel
// says of each peer now. Neither a private key nor a preshared key is ever
// read into the view.
func TestWireGuardPeersCarryNamesHandshakesAndTrafficButNoSecret(t *testing.T) {
	ifs, err := ParseWireGuardConfig(read(t, "network.uci"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ifs) != 1 || ifs[0].Name != "wg0" || ifs[0].ListenPort != 51820 || len(ifs[0].Peers) != 2 {
		t.Fatalf("wireguard %+v", ifs)
	}
	ApplyWireGuardDump(ifs, read(t, "wg-dump.txt"))
	w := ifs[0]
	if w.PublicKey != "cm91dGVyLXB1YmxpYy1rZXktMDAwMDAwMDAwMDAwMDA9" || !w.Up {
		t.Errorf("interface %+v", w)
	}
	phone, laptop := w.Peers[0], w.Peers[1]
	if phone.Name != "Sam's phone" || phone.Endpoint != "203.0.113.7:41641" || phone.Rx != 123456 || phone.Tx != 654321 ||
		!phone.Handshake.Equal(time.Unix(1763830000, 0)) {
		t.Errorf("phone %+v", phone)
	}
	if laptop.Name != "" || laptop.Endpoint != "laptop.example:51820" || !laptop.Handshake.IsZero() || len(laptop.AllowedIPs) != 2 {
		t.Errorf("laptop %+v: the configured endpoint stands until the kernel knows one", laptop)
	}
	b, _ := json.Marshal(ifs)
	for _, secret := range []string{"cHJpdmF0ZS1rZXktb2YtdGhlLXJvdXRlci0wMDAwMDA9", "cHJlc2hhcmVkLWtleS1vZi10aGUtcGhvbmUtMDAwMDA9"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("a secret key reached the view: %s", b)
		}
	}
}

// Port forwards: the DNAT redirects the firewall config holds, with their
// outside port, protocol and where they lead; other redirects are not
// forwards.
func TestPortForwardsAreTheFirewallsDNATRedirects(t *testing.T) {
	fw, err := ParseForwards(read(t, "firewall.uci"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fw) != 2 {
		t.Fatalf("forwards %+v, want the two DNAT redirects", fw)
	}
	game, nas := fw[0], fw[1]
	if game.Name != "Game console" || game.Proto != "udp" || game.Port != "3074" || game.DestIP != "192.168.50.105" || game.DestPort != "3074" || !game.Enabled || game.From != "wan" {
		t.Errorf("game %+v", game)
	}
	if nas.DestPort != "443" || nas.Enabled || nas.Proto != "tcp udp" {
		t.Errorf("nas %+v", nas)
	}
}

type fakeRun map[string][]byte

func (f fakeRun) run(_ context.Context, name string, args ...string) ([]byte, error) {
	k := name + " " + strings.Join(args, " ")
	if b, ok := f[k]; ok {
		return b, nil
	}
	return nil, errors.New("exec: not found: " + k)
}

func routerFS(t *testing.T, withWireless bool) fstest.MapFS {
	fsys := fstest.MapFS{
		"etc/config/network":                         {Data: read(t, "network.uci")},
		"etc/config/firewall":                        {Data: read(t, "firewall.uci")},
		"sys/class/net/br-lan/brif/lan1/port_no":     {Data: []byte("0x1\n")},
		"sys/class/net/br-lan/brif/lan2/port_no":     {Data: []byte("0x2\n")},
		"sys/class/net/br-lan/brif/phy1-ap0/port_no": {Data: []byte("0x3\n")},
		"sys/class/net/lan1/carrier":                 {Data: []byte("1\n")},
		"sys/class/net/lan1/speed":                   {Data: []byte("1000\n")},
		"sys/class/net/lan1/phys_switch_id":          {Data: []byte("00000000\n")},
		"sys/class/net/lan2/carrier":                 {Data: []byte("0\n")},
		"sys/class/net/lan2/speed":                   {Data: []byte("-1\n")},
		"sys/class/net/phy1-ap0/phy80211/name":       {Data: []byte("phy1\n")},
		"sys/class/net/eth0/statistics/rx_bytes":     {Data: []byte("1000\n")},
		"sys/class/net/eth0/statistics/tx_bytes":     {Data: []byte("200\n")},
	}
	if withWireless {
		fsys["etc/config/wireless"] = &fstest.MapFile{Data: read(t, "wireless.uci")}
	}
	return fsys
}

// The whole view from one router: networks with their counters, the bridge's
// ports without its radios, Wi-Fi from the config and hostapd, WireGuard from
// the config and wg, forwards from the firewall.
func TestTheViewIsReadFromTheRouter(t *testing.T) {
	run := fakeRun{
		"ubus call network.interface dump":      read(t, "vm-interface-dump.json"),
		"ubus list hostapd.*":                   []byte("hostapd.phy1-ap0\n"),
		"ubus call hostapd.phy1-ap0 get_status": read(t, "hostapd-status.json"),
		"wg show all dump":                      read(t, "wg-dump.txt"),
	}
	s := &Source{FS: routerFS(t, true), Run: run.run, Bridges: []string{"br-lan"}, Now: func() time.Time { return time.Unix(1763830100, 0) }}
	v := s.View(context.Background())
	if len(v.Interfaces) != 3 {
		t.Errorf("interfaces %+v", v.Interfaces)
	}
	for _, i := range v.Interfaces {
		if i.Name == "wan" && (i.RxBytes != 1000 || i.TxBytes != 200) {
			t.Errorf("wan counters %+v", i)
		}
	}
	if len(v.Ports) != 2 || v.Ports[0].Name != "lan1" || !v.Ports[0].Up || v.Ports[0].SpeedMbps != 1000 || !v.Ports[0].Switch ||
		v.Ports[1].Up || v.Ports[1].SpeedMbps != 0 {
		t.Errorf("ports %+v, want lan1 up at 1000 on a switch, lan2 down, no radio", v.Ports)
	}
	if v.WiFi.Why != "" || len(v.WiFi.Radios) != 3 || len(v.WiFi.Live) != 1 || v.WiFi.Live[0].Iface != "phy1-ap0" || v.WiFi.Live[0].SSID != "Home" ||
		v.WiFi.Live[0].Band != "5 GHz" || v.WiFi.Live[0].Channel != 36 {
		t.Errorf("wifi %+v", v.WiFi)
	}
	if v.WireGuard.Why != "" || len(v.WireGuard.Interfaces) != 1 || v.WireGuard.Interfaces[0].Peers[0].Rx != 123456 {
		t.Errorf("wireguard %+v", v.WireGuard)
	}
	if v.Forwards.Why != "" || len(v.Forwards.Rules) != 2 {
		t.Errorf("forwards %+v", v.Forwards)
	}
}

// What a router does not have is said, never shown as an empty box.
func TestWhatTheRouterLacksIsSaid(t *testing.T) {
	run := fakeRun{"ubus call network.interface dump": read(t, "vm-interface-dump.json")}
	fsys := routerFS(t, false)
	delete(fsys, "etc/config/firewall")
	fsys["etc/config/network"] = &fstest.MapFile{Data: []byte("config interface 'lan'\n\toption proto 'static'\n")}
	s := &Source{FS: fsys, Run: run.run, Bridges: []string{"br-lan"}, Now: time.Now}
	v := s.View(context.Background())
	if v.WiFi.Why != NoWiFi {
		t.Errorf("wifi why %q, want %q", v.WiFi.Why, NoWiFi)
	}
	if v.WireGuard.Why != NoWireGuard {
		t.Errorf("wireguard why %q", v.WireGuard.Why)
	}
	if v.Forwards.Why != NoForwards {
		t.Errorf("forwards why %q", v.Forwards.Why)
	}
	// WireGuard configured but no wg tool: the peers from the config, and why
	// their handshakes are missing.
	fsys["etc/config/network"] = &fstest.MapFile{Data: read(t, "network.uci")}
	s = &Source{FS: fsys, Run: run.run, Bridges: []string{"br-lan"}, Now: time.Now}
	v = s.View(context.Background())
	if v.WireGuard.Why != "" || v.WireGuard.Note != NoWgTool || len(v.WireGuard.Interfaces[0].Peers) != 2 {
		t.Errorf("wireguard without wg: %+v", v.WireGuard)
	}
	// Wi-Fi configured but nothing running.
	fsys["etc/config/wireless"] = &fstest.MapFile{Data: read(t, "wireless.uci")}
	v = (&Source{FS: fsys, Run: run.run, Now: time.Now}).View(context.Background())
	if v.WiFi.Why != "" || v.WiFi.Note != NoAccessPoint {
		t.Errorf("wifi configured but not running: %+v", v.WiFi)
	}
}

// A view is read at most every few seconds however often it is asked for.
func TestTheViewIsCachedForAFewSeconds(t *testing.T) {
	calls := 0
	now := time.Unix(1763830000, 0)
	s := &Source{FS: fstest.MapFS{}, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		return nil, errors.New("no")
	}, Now: func() time.Time { return now }}
	s.View(context.Background())
	first := calls
	s.View(context.Background())
	if calls != first {
		t.Errorf("read again within the cache window: %d calls, want %d", calls, first)
	}
	now = now.Add(cacheFor + time.Second)
	s.View(context.Background())
	if calls == first {
		t.Errorf("not read again after the cache window")
	}
}

// wg is asked once, for every interface, so no name from the config is ever
// passed to it; its single-interface dump would carry no interface name.
func TestWgIsAskedOnceForEveryInterface(t *testing.T) {
	var asked []string
	fsys := fstest.MapFS{"etc/config/network": {Data: []byte("config interface 'wg0'\n\toption proto 'wireguard'\nconfig interface 'wg1'\n\toption proto 'wireguard'\n")}}
	s := &Source{FS: fsys, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "wg" {
			asked = append(asked, strings.Join(args, " "))
		}
		return nil, errors.New("no")
	}, Now: time.Now}
	s.View(context.Background())
	if len(asked) != 1 || asked[0] != "show all dump" {
		t.Errorf("wg asked %q, want once: show all dump", asked)
	}
}

// A viewer who leaves mid-read does not cut the reading short: the router is
// read under its own deadline, and what is cached is a whole reading.
func TestAReadIsNotCutShortByTheViewerLeaving(t *testing.T) {
	dump := read(t, "vm-interface-dump.json")
	s := &Source{FS: fstest.MapFS{}, Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if name == "ubus" && len(args) > 1 && args[1] == "network.interface" {
			return dump, nil
		}
		return nil, errors.New("no")
	}, Now: time.Now}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if v := s.View(gone); len(v.Interfaces) != 3 || v.IfacesWhy != "" {
		t.Fatalf("a cancelled viewer got %+v", v)
	}
}

// Viewers asking at once share one reading; nobody queues up a second.
func TestViewersShareOneRead(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	reads := 0
	s := &Source{FS: fstest.MapFS{}, Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "ubus" && len(args) > 1 && args[1] == "network.interface" {
			mu.Lock()
			reads++
			mu.Unlock()
			<-release
		}
		return nil, errors.New("no")
	}, Now: time.Now}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.View(context.Background()) }()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if reads != 1 {
		t.Errorf("%d readings for four viewers at once, want 1", reads)
	}
}

// A viewer that waits for someone else's reading stops waiting when it
// leaves, and is answered with what is known.
func TestAWaitingViewerCanLeave(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	s := &Source{FS: fstest.MapFS{}, Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "ubus" && len(args) > 1 && args[1] == "network.interface" {
			<-release
		}
		return nil, errors.New("no")
	}, Now: time.Now}
	go s.View(context.Background())
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	var got View
	go func() { got = s.View(ctx); close(done) }()
	select {
	case <-done:
		if got.IfacesWhy != NotYet {
			t.Errorf("a viewer who left before the first reading got %q, want %q", got.IfacesWhy, NotYet)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a viewer that left was still waiting")
	}
}

// A config that is there but cannot be read is said so, never shown as a
// router without that feature.
func TestAConfigThatCannotBeReadIsSaidSo(t *testing.T) {
	bad := []byte("config redirect\n\toption name 'x\n")
	fsys := fstest.MapFS{"etc/config/firewall": {Data: bad}, "etc/config/network": {Data: bad}, "etc/config/wireless": {Data: bad}}
	v := (&Source{FS: fsys, Run: fakeRun{}.run, Now: time.Now}).View(context.Background())
	for what, why := range map[string]string{"forwards": v.Forwards.Why, "wireguard": v.WireGuard.Why, "wifi": v.WiFi.Why} {
		if !strings.HasPrefix(why, "could not read /etc/config/") {
			t.Errorf("%s says %q, want that its config could not be read", what, why)
		}
	}
}

// The WAN is the network the service was told is the WAN even while it is
// down, and a tunnel that carries a default route is not the internet line.
func TestTheWANIsTheConfiguredLineAndNotATunnel(t *testing.T) {
	dump := []byte(`{"interface":[
	 {"interface":"wan","up":false,"proto":"dhcp","device":"eth1","route":[]},
	 {"interface":"vpn","up":true,"proto":"wireguard","l3_device":"vpn","route":[{"target":"0.0.0.0","mask":0,"nexthop":"0.0.0.0"}]},
	 {"interface":"wwan","up":true,"proto":"dhcp","l3_device":"wlan0","route":[{"target":"0.0.0.0","mask":0,"nexthop":"192.0.2.1"}]}]}`)
	by := map[string]Iface{}
	for _, i := range ParseInterfaces(dump, []string{"wan"}) {
		by[i.Name] = i
	}
	if !by["wan"].WAN {
		t.Errorf("the configured WAN, down, lost its mark: %+v", by["wan"])
	}
	if by["vpn"].WAN || by["vpn"].Gateway != "" {
		t.Errorf("a WireGuard tunnel's default route made it the internet: %+v", by["vpn"])
	}
	if !by["wwan"].WAN || by["wwan"].Gateway != "192.0.2.1" {
		t.Errorf("a second uplink with a default route: %+v", by["wwan"])
	}
}

// UPnP mappings a device opened for itself, as miniupnpd keeps them
// (proto:outside port:device address:device port:expiry:description).
func TestUPnPMappingsAreReadFromTheLeaseFile(t *testing.T) {
	leases := []byte("TCP:51413:192.168.50.105:51413:1763900000:Transmission at 192.168.50.105:51413 TCP\n" +
		"UDP:3478:192.168.50.20:3478:0:Xbox\n" +
		"garbage line\n" +
		"TCP:notaport:192.168.50.20:1:0:x\n")
	got := ParseUPnP(leases)
	if len(got) != 2 {
		t.Fatalf("mappings %+v, want the two well-formed lines", got)
	}
	tr, xb := got[0], got[1]
	if tr.Proto != "tcp" || tr.Port != "51413" || tr.DestIP != "192.168.50.105" || tr.DestPort != "51413" || tr.Desc != "Transmission at 192.168.50.105:51413 TCP" || tr.Expires.Unix() != 1763900000 {
		t.Errorf("transmission %+v", tr)
	}
	if xb.Proto != "udp" || !xb.Expires.IsZero() || xb.Desc != "Xbox" {
		t.Errorf("xbox %+v", xb)
	}
}

func TestTheViewCarriesUPnPMappingsOrSaysThereAreNone(t *testing.T) {
	fsys := fstest.MapFS{"var/run/miniupnpd.leases": {Data: []byte("UDP:3478:192.168.50.20:3478:0:Xbox\n")}}
	s := &Source{FS: fsys, Run: fakeRun{}.run}
	if v := s.read(context.Background()); len(v.Forwards.UPnP) != 1 || v.Forwards.UPnP[0].DestIP != "192.168.50.20" {
		t.Fatalf("upnp %+v", v.Forwards.UPnP)
	}
	s = &Source{FS: fstest.MapFS{}, Run: fakeRun{}.run}
	if v := s.read(context.Background()); v.Forwards.UPnP == nil || len(v.Forwards.UPnP) != 0 {
		t.Fatalf("no lease file: %+v, want an empty list", v.Forwards.UPnP)
	}
}
