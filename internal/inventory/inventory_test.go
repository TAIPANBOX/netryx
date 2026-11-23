package inventory

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netryx/internal/collect"
	"netryx/internal/device"
	"netryx/internal/neigh"
)

// A locally administered address is the one a phone invents for itself. The
// second hex digit of the first octet carries the bit, so these four differ in
// their first octet only.
func TestRandomMACIsRecognised(t *testing.T) {
	for _, mac := range []string{"02:00:00:00:00:01", "a6:11:22:33:44:55", "fa:11:22:33:44:55", "4e:11:22:33:44:55"} {
		if !IsRandom(mac) {
			t.Errorf("IsRandom(%s) = false, want true: the locally administered bit is set", mac)
		}
	}
	if IsRandom("8c:79:f5:00:00:01") {
		t.Error("IsRandom(8c:79:f5:...) = true, want false: a vendor-assigned address")
	}
}

// oui.txt carries two lines per vendor, one tagged (hex) and one (base 16).
// Only the (hex) line is the registration; the other repeats it in a form
// nobody needs, so counting it would double every vendor.
func TestOUIParsesTheIEEEFormat(t *testing.T) {
	src := "OUI/MA-L\n\n" +
		"8C-79-F5   (hex)\t\tSamsung Electronics Co.,Ltd\n" +
		"8C79F5     (base 16)\t\tSamsung Electronics Co.,Ltd\n"
	m, err := LoadOUI(strings.NewReader(src))
	if err != nil {
		t.Fatalf("LoadOUI: %v", err)
	}
	if got := m["8C79F5"]; got != "Samsung Electronics Co.,Ltd" {
		t.Errorf("vendor for 8C79F5 = %q, want the Samsung registration", got)
	}
	if len(m) != 1 {
		t.Errorf("got %d entries, want 1: the base-16 line must not be counted", len(m))
	}
}

// The OUI map is held for the life of the process, so a file that grows past
// the cap is refused instead of read until the router runs out of memory.
func TestOUIRefusesMoreThanTheCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i <= maxOUI; i++ {
		fmt.Fprintf(&b, "%06X   (hex)\t\tVendor %d\n", i, i)
	}
	if _, err := LoadOUI(strings.NewReader(b.String())); err == nil {
		t.Fatalf("LoadOUI accepted %d entries, want an error past the %d cap", maxOUI+1, maxOUI)
	}
}

// ubus reports the frequency once at the top level, and each client only
// carries its own signal. The band has to come from that shared frequency.
func TestHostapdClientsGiveSignal(t *testing.T) {
	js := []byte(`{"freq":5180,"clients":{"AA:BB:CC:DD:EE:FF":{"signal":-58,"rx":{}}}}`)
	got := ParseHostapdClients("wlan0", js)
	want := Link{Kind: "wifi", Iface: "wlan0", Band: "5 GHz", Signal: -58}
	link, ok := got["aa:bb:cc:dd:ee:ff"]
	if !ok {
		t.Fatalf("no entry for aa:bb:cc:dd:ee:ff in %v: keys are lowercase colon form", got)
	}
	if link != want {
		t.Errorf("link = %+v, want %+v", link, want)
	}
}

// The band boundaries are where a 6 GHz radio would be misfiled as 5 GHz if
// the comparison were off by one, so each band is checked at a real channel.
func TestHostapdBandFollowsFrequency(t *testing.T) {
	cases := map[int]string{2437: "2.4 GHz", 5180: "5 GHz", 5955: "6 GHz", 0: ""}
	for freq, want := range cases {
		js := []byte(fmt.Sprintf(`{"freq":%d,"clients":{"aa:bb:cc:dd:ee:ff":{"signal":-40}}}`, freq))
		link := ParseHostapdClients("wlan1", js)["aa:bb:cc:dd:ee:ff"]
		if link.Band != want {
			t.Errorf("freq %d: band = %q, want %q", freq, link.Band, want)
		}
	}
}

// The name a person reads is the first of: what they typed in, the name the
// device gave itself over DHCP, the vendor with the last bytes of its address,
// and finally the address itself. A device that is only a random address has
// no vendor to show, so it falls to the address.
func TestNamePrecedence(t *testing.T) {
	oui := OUIMap{"8C79F5": "Samsung Electronics Co.,Ltd"}
	leases := device.NewTable(device.ParseLeases(strings.NewReader(
		"1700000000 8c:79:f5:dd:ee:ff 192.168.1.20 laptop *\n")))
	samsung := "8c:79:f5:dd:ee:ff"
	random := "02:11:22:33:44:55"

	cases := []struct {
		name   string
		mac    string
		labels map[string]Label
		leases *device.Table
		want   string
	}{
		{"label wins over everything", samsung, map[string]Label{samsung: {Name: "Kitchen speaker"}}, leases, "Kitchen speaker"},
		{"DHCP hostname when no label", samsung, nil, leases, "laptop"},
		{"vendor and last three bytes without a hostname", samsung, nil, nil, "Samsung Electronics Co.,Ltd dd:ee:ff"},
		{"random address with no hostname falls to the address", random, nil, nil, random},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Sources{
				Collected: []collect.Device{{MAC: c.mac}},
				Leases:    c.leases,
				OUI:       oui,
				Labels:    c.labels,
			}
			devs := Build(s)
			if len(devs) != 1 {
				t.Fatalf("Build returned %d devices, want 1", len(devs))
			}
			if devs[0].Name != c.want {
				t.Errorf("Name = %q, want %q", devs[0].Name, c.want)
			}
		})
	}
}

// A device that the capture saw and the neighbour table knows under two
// addresses (one IPv4, one IPv6 temporary) is one row with both.
func TestBuildMergesAddressesFromNeighbours(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	mac := "8c:79:f5:dd:ee:ff"
	reg := neigh.NewRegistry()
	reg.Observe(neigh.Entry{IP: net.ParseIP("192.168.1.20"), MAC: mac, State: 0x02}, now)
	reg.Observe(neigh.Entry{IP: net.ParseIP("fd00::1234"), MAC: mac, State: 0x02}, now)

	first := now.Add(-time.Hour)
	devs := Build(Sources{
		Collected: []collect.Device{{MAC: mac, Addrs: []string{"192.168.1.20"}, First: first, Last: now}},
		Neigh:     reg,
	})
	if len(devs) != 1 {
		t.Fatalf("Build returned %d devices, want 1 row for one MAC", len(devs))
	}
	d := devs[0]
	if len(d.IPv4) != 1 || d.IPv4[0] != "192.168.1.20" {
		t.Errorf("IPv4 = %v, want [192.168.1.20]", d.IPv4)
	}
	if len(d.IPv6) != 1 || d.IPv6[0] != "fd00::1234" {
		t.Errorf("IPv6 = %v, want [fd00::1234]", d.IPv6)
	}
	if !d.First.Equal(first) || !d.Last.Equal(now) {
		t.Errorf("First/Last = %v/%v, want %v/%v", d.First, d.Last, first, now)
	}
}

// Wi-Fi link and the user's group come from their own sources and reach the
// device under the same MAC key.
func TestBuildTakesLinkAndGroupFromSources(t *testing.T) {
	mac := "8c:79:f5:dd:ee:ff"
	devs := Build(Sources{
		Collected: []collect.Device{{MAC: mac}},
		WiFi:      map[string]Link{mac: {Kind: "wifi", Iface: "wlan1", Band: "5 GHz", Signal: -61}},
		Labels:    map[string]Label{mac: {Name: "Laptop", Group: "adults"}},
	})
	if len(devs) != 1 {
		t.Fatalf("Build returned %d devices, want 1", len(devs))
	}
	d := devs[0]
	if d.Link.Kind != "wifi" || d.Link.Signal != -61 || d.Link.Band != "5 GHz" {
		t.Errorf("Link = %+v, want the hostapd link", d.Link)
	}
	if d.Label != "Laptop" || d.Group != "adults" {
		t.Errorf("Label/Group = %q/%q, want Laptop/adults", d.Label, d.Group)
	}
}

// A router with no labels file yet must still show its devices, not fail.
func TestMissingLabelsFileIsEmpty(t *testing.T) {
	m, err := LoadLabels(filepath.Join(t.TempDir(), "labels.json"))
	if err != nil {
		t.Fatalf("LoadLabels on a missing file: %v, want no error", err)
	}
	if len(m) != 0 {
		t.Errorf("got %d labels from a missing file, want 0", len(m))
	}
}

// The file is keyed by a MAC typed by a person, possibly in capitals; a lookup
// by the normalised address must still find it.
func TestLabelsAreKeyedByLowercaseColonMAC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labels.json")
	if err := os.WriteFile(path, []byte(`{"8C-79-F5-DD-EE-FF": {"name": "Kitchen speaker", "group": "kids"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadLabels(path)
	if err != nil {
		t.Fatalf("LoadLabels: %v", err)
	}
	got, ok := m["8c:79:f5:dd:ee:ff"]
	if !ok || got.Name != "Kitchen speaker" || got.Group != "kids" {
		t.Errorf("label = %+v, ok=%v; want Kitchen speaker/kids under the lowercase colon key", got, ok)
	}
}

// A lease is a device the house has known, even when it is idle: a client list
// shows it with the name its lease gave and the address it holds.
func TestALeaseOnlyDeviceIsListed(t *testing.T) {
	leases := device.NewTable(device.ParseLeases(strings.NewReader(
		"1700000000 8c:79:f5:dd:ee:ff 192.168.1.20 laptop *\n")))
	devs := Build(Sources{Leases: leases})
	if len(devs) != 1 {
		t.Fatalf("Build returned %d devices for a lease-only device, want 1", len(devs))
	}
	d := devs[0]
	if d.Name != "laptop" {
		t.Errorf("Name = %q, want laptop from the lease", d.Name)
	}
	if len(d.IPv4) != 1 || d.IPv4[0] != "192.168.1.20" {
		t.Errorf("IPv4 = %v, want [192.168.1.20] from the lease", d.IPv4)
	}
}

// A label with nothing else known about its device stays unlisted: the decision
// is that only devices the house has seen or leased are shown.
func TestALabelAloneIsNotListed(t *testing.T) {
	devs := Build(Sources{Labels: map[string]Label{"8c:79:f5:dd:ee:ff": {Name: "Ghost"}}})
	if len(devs) != 0 {
		t.Errorf("Build listed %d devices from a label alone, want none", len(devs))
	}
}

// A label is human input. Whitespace and control characters are removed, and a
// name left empty is treated as absent, so the next name in precedence wins.
func TestABlankLabelDoesNotHideTheDevicesName(t *testing.T) {
	mac := "8c:79:f5:dd:ee:ff"
	leases := device.NewTable(device.ParseLeases(strings.NewReader(
		"1700000000 8c:79:f5:dd:ee:ff 192.168.1.20 laptop *\n")))
	devs := Build(Sources{
		Collected: []collect.Device{{MAC: mac}},
		Leases:    leases,
		Labels:    map[string]Label{mac: {Name: "  \t\n ", Group: " adults\r\n"}},
	})
	if len(devs) != 1 {
		t.Fatalf("Build returned %d devices, want 1", len(devs))
	}
	if devs[0].Name != "laptop" {
		t.Errorf("Name = %q, want laptop: a blank label must not hide it", devs[0].Name)
	}
	if devs[0].Label != "" {
		t.Errorf("Label = %q, want empty after cleaning", devs[0].Label)
	}
	if devs[0].Group != "adults" {
		t.Errorf("Group = %q, want adults with the whitespace and newline removed", devs[0].Group)
	}
}

// A label file is read from disk on every start, so a huge or hostile file must
// be refused by size and by entry count rather than read into memory.
func TestLabelsAreBounded(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, maxLabelsBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLabels(big); err == nil {
		t.Error("LoadLabels accepted a file over 1 MiB, want an error naming the limit")
	}

	var b strings.Builder
	b.WriteString("{")
	for i := 0; i <= maxLabels; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"02:00:00:00:%02x:%02x":{"name":"n%d"}`, i>>8, i&0xff, i)
	}
	b.WriteString("}")
	many := filepath.Join(dir, "many.json")
	if err := os.WriteFile(many, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLabels(many); err == nil {
		t.Errorf("LoadLabels accepted %d entries, want an error past %d", maxLabels+1, maxLabels)
	}
}

// A hostile hostapd reply can list thousands of clients; the router keeps a
// bounded number and drops the rest rather than growing without limit.
func TestHostapdClientsAreBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"freq":5180,"clients":{`)
	for i := 0; i < maxClients+10; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"02:00:00:00:%02x:%02x":{"signal":-50}`, i>>8, i&0xff)
	}
	b.WriteString("}}")
	got := ParseHostapdClients("wlan0", []byte(b.String()))
	if len(got) > maxClients {
		t.Errorf("kept %d clients, want at most %d", len(got), maxClients)
	}
}

// Each device is one row. A collector row with no hardware address (a
// connection from a device whose handshake was never read) is not a device
// of its own: if the neighbour table or a lease knows its address, it is
// that device; if nothing does, it is not listed, because a row with no
// hardware address has nothing to call it and nowhere to link to.
func TestEachDeviceAppearsOnce(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	reg := neigh.NewRegistry()
	reg.Observe(neigh.Entry{IP: net.ParseIP("192.168.1.20"), MAC: "bb:bb:bb:00:00:02", State: 0x04}, now)
	leases := device.NewTable([]device.Lease{{MAC: "cc:cc:cc:00:00:03", IP: net.ParseIP("192.168.1.30"), Name: "tv"}})
	seen := now.Add(-time.Minute)
	devs := Build(Sources{
		Collected: []collect.Device{
			{MAC: "aa:aa:aa:00:00:01", Addrs: []string{"192.168.1.10"}, First: now.Add(-time.Hour), Last: now},
			{Addrs: []string{"192.168.1.20"}, First: now.Add(-2 * time.Hour), Last: seen},
			{Addrs: []string{"192.168.1.30"}, First: now.Add(-3 * time.Hour), Last: seen},
			{Addrs: []string{"192.168.1.99"}, First: now, Last: now},
		},
		Neigh:  reg,
		Leases: leases,
	})
	by := map[string]Device{}
	for _, d := range devs {
		if d.MAC == "" {
			t.Fatalf("a row with no hardware address is listed: %+v", devs)
		}
		if _, dup := by[d.MAC]; dup {
			t.Fatalf("%s is listed twice: %+v", d.MAC, devs)
		}
		by[d.MAC] = d
	}
	if len(by) != 3 {
		t.Fatalf("three devices, each once: %+v", devs)
	}
	if d := by["bb:bb:bb:00:00:02"]; !d.Last.Equal(seen) || !d.First.Equal(now.Add(-2*time.Hour)) {
		t.Errorf("the neighbour's address brings its sightings to its device: %+v", d)
	}
	if d := by["cc:cc:cc:00:00:03"]; !d.Last.Equal(seen) || d.Name != "tv" {
		t.Errorf("the lease's address brings its sightings to its device: %+v", d)
	}
}

// A neighbour the kernel has just confirmed reachable is a device that is on
// the network now, however long ago it was last seen doing anything.
func TestAReachableNeighbourIsMarked(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	reg := neigh.NewRegistry()
	reg.Observe(neigh.Entry{IP: net.ParseIP("192.168.1.20"), MAC: "bb:bb:bb:00:00:02", State: neigh.StateReachable}, now)
	reg.Observe(neigh.Entry{IP: net.ParseIP("192.168.1.30"), MAC: "cc:cc:cc:00:00:03", State: 0x04}, now) // STALE
	leases := device.NewTable([]device.Lease{
		{MAC: "bb:bb:bb:00:00:02", IP: net.ParseIP("192.168.1.20")},
		{MAC: "cc:cc:cc:00:00:03", IP: net.ParseIP("192.168.1.30")},
	})
	got := map[string]bool{}
	for _, d := range Build(Sources{Neigh: reg, Leases: leases}) {
		got[d.MAC] = d.Reachable
	}
	if !got["bb:bb:bb:00:00:02"] || got["cc:cc:cc:00:00:03"] {
		t.Fatalf("only the REACHABLE neighbour is marked: %v", got)
	}
}

// Two sightings of one device (the capture's row and a row whose address the
// neighbour table resolves to it, or a row for its open flows) are one device
// with the earliest first sighting, the latest last one and every address.
func TestTwoSightingsOfOneDeviceAreJoined(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	const mac = "bb:bb:bb:00:00:02"
	reg := neigh.NewRegistry()
	reg.Observe(neigh.Entry{IP: net.ParseIP("192.168.1.21"), MAC: mac, State: 0x04}, now)
	devs := Build(Sources{
		Collected: []collect.Device{
			{MAC: mac, Addrs: []string{"192.168.1.20"}, First: now.Add(-time.Hour), Last: now.Add(-30 * time.Minute)},
			{Addrs: []string{"192.168.1.21"}, First: now.Add(-2 * time.Hour), Last: now.Add(-40 * time.Minute)},
			{MAC: mac, Last: now},
		},
		Neigh: reg,
	})
	if len(devs) != 1 {
		t.Fatalf("one device: %+v", devs)
	}
	d := devs[0]
	if !d.First.Equal(now.Add(-2*time.Hour)) || !d.Last.Equal(now) {
		t.Errorf("the earliest first sighting and the latest last one: %v %v", d.First, d.Last)
	}
	if len(d.IPv4) != 2 {
		t.Errorf("every address it was seen on: %v", d.IPv4)
	}
}
