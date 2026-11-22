package rules

import (
	"os"
	"strings"
	"testing"

	"netryx/internal/inventory"
)

func wireless(t *testing.T) WirelessConfig {
	t.Helper()
	f, err := os.Open("testdata/wireless.uci")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w, err := ParseWireless(f)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func noSwitch(string) bool { return false }

// spec 6.7: frames between clients of one SSID never cross the bridge, so a
// Wi-Fi device is isolated from its neighbours only when its SSID isolates
// clients, and only when every radio carrying that SSID does.
func TestAWiFiDeviceSaysWhetherItsSSIDIsolatesClients(t *testing.T) {
	w := wireless(t)
	things := IsolationOf(inventory.Link{Kind: "wifi", Iface: "phy0-ap1", SSID: "Things"}, w, noSwitch, 1)
	if !things.Routed || !things.Bridged || !things.SSIDIsolated || things.SSID != "Things" {
		t.Fatalf("Things = %+v; want isolated from its SSID neighbours", things)
	}
	if !strings.Contains(things.Text, "isolated from its Wi-Fi neighbours on Things") {
		t.Fatalf("Text = %q", things.Text)
	}
	home := IsolationOf(inventory.Link{Kind: "wifi", Iface: "phy0-ap0", SSID: "Home"}, w, noSwitch, 1)
	if home.SSIDIsolated || !strings.Contains(home.Text, "not isolated from its Wi-Fi neighbours on Home") {
		t.Fatalf("Home = %+v", home)
	}
	mixed := IsolationOf(inventory.Link{Kind: "wifi", SSID: "Mixed"}, w, noSwitch, 1)
	if mixed.SSIDIsolated {
		t.Fatalf("Mixed isolates on one radio only: %+v; want not isolated", mixed)
	}
	unknown := IsolationOf(inventory.Link{Kind: "wifi", SSID: "Neighbour"}, w, noSwitch, 1)
	if unknown.SSIDIsolated {
		t.Fatalf("an SSID not in the config is not isolated: %+v", unknown)
	}
}

// docs/stage2c-measured.md section 1: wired ports on a hardware switch talk
// to each other without the CPU, so neither the bridge table nor a port flag
// keeps an isolated device from them.
func TestAWiredDeviceOnAHardwareSwitchSaysWhatIsNotCovered(t *testing.T) {
	sw := func(port string) bool { return port == "lan2" }
	got := IsolationOf(inventory.Link{Kind: "wired", Iface: "lan2"}, WirelessConfig{}, sw, 1)
	if !got.HardwareSwitch || !strings.Contains(got.Text, "not isolated from wired neighbours") {
		t.Fatalf("lan2 = %+v", got)
	}
}

func TestASoftwareBridgePortIsFullyIsolated(t *testing.T) {
	got := IsolationOf(inventory.Link{Kind: "wired", Iface: "v-camera"}, WirelessConfig{}, noSwitch, 1)
	if got.HardwareSwitch || got.Text != "isolated from the rest of the house" {
		t.Fatalf("v-camera = %+v", got)
	}
	none := IsolationOf(inventory.Link{}, WirelessConfig{}, noSwitch, 0)
	if none.Text != "isolated from the rest of the house" {
		t.Fatalf("a device with no link = %+v", none)
	}
}

// Final review I5: frames between devices behind one unmanaged switch or a
// second access point never reach the router; spec 6.7 offers isolation as
// complete only when one MAC is seen on that port.
func TestSeveralDevicesBehindOnePortAreSaidNotToBeIsolatedFromEachOther(t *testing.T) {
	got := IsolationOf(inventory.Link{Kind: "wired", Iface: "lan3"}, WirelessConfig{}, noSwitch, 3)
	if got.SharedPort != 3 || !strings.Contains(got.Text, "not isolated from devices behind the same switch or access point") {
		t.Fatalf("lan3 with three devices = %+v", got)
	}
}
