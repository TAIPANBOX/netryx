package device

import (
	"strings"
	"testing"
)

// A device is looked up by hardware address when the address it holds today is
// new to the table, so the MAC lookup must find the name the lease recorded.
func TestLeaseNameIsFoundByMAC(t *testing.T) {
	tab := NewTable(ParseLeases(strings.NewReader(
		"1700000000 8C:79:F5:DD:EE:FF 192.168.1.20 laptop *\n" +
			"1700000000 02:11:22:33:44:55 192.168.1.21 * *\n")))
	if got := tab.NameByMAC("8c:79:f5:dd:ee:ff"); got != "laptop" {
		t.Errorf("NameByMAC = %q, want laptop", got)
	}
	if got := tab.NameByMAC("8C:79:F5:DD:EE:FF"); got != "laptop" {
		t.Errorf("NameByMAC with capitals = %q, want laptop: the lookup normalises case", got)
	}
}

// A lease without a name, an unknown MAC and a nil table all give the empty
// string, so the caller falls through to the next source instead of showing a
// hostname that was never given.
func TestLeaseNameByMACIsEmptyWhenUnknown(t *testing.T) {
	tab := NewTable(ParseLeases(strings.NewReader("1700000000 02:11:22:33:44:55 192.168.1.21 * *\n")))
	if got := tab.NameByMAC("02:11:22:33:44:55"); got != "" {
		t.Errorf("unnamed lease gave %q, want empty", got)
	}
	if got := tab.NameByMAC("aa:bb:cc:dd:ee:ff"); got != "" {
		t.Errorf("unknown MAC gave %q, want empty", got)
	}
	var nilTable *Table
	if got := nilTable.NameByMAC("02:11:22:33:44:55"); got != "" {
		t.Errorf("nil table gave %q, want empty", got)
	}
}

// Every lease is a device the house has known, so the list of known MACs must
// include a lease that never carried traffic, in lowercase colon form, once.
func TestLeaseMACsListEveryKnownDevice(t *testing.T) {
	tab := NewTable(ParseLeases(strings.NewReader(
		"1700000000 8C:79:F5:DD:EE:FF 192.168.1.20 laptop *\n" +
			"1700000000 02:11:22:33:44:55 192.168.1.21 * *\n" +
			"1700000000 02:11:22:33:44:55 192.168.1.22 * *\n")))
	got := tab.MACs()
	want := map[string]bool{"8c:79:f5:dd:ee:ff": true, "02:11:22:33:44:55": true}
	if len(got) != len(want) {
		t.Fatalf("MACs = %v, want the two distinct devices once each", got)
	}
	for _, m := range got {
		if !want[m] {
			t.Errorf("MACs contains %q, want only lowercase colon forms of the leased devices", m)
		}
	}
}

// A missing table knows no devices; it must not panic.
func TestNilLeaseTableHasNoMACs(t *testing.T) {
	var tab *Table
	if got := tab.MACs(); len(got) != 0 {
		t.Errorf("nil table MACs = %v, want none", got)
	}
}

// A device known only from its lease has no address in the neighbour table, so
// the address the lease gave it must be findable by its MAC.
func TestLeaseIPIsFoundByMAC(t *testing.T) {
	tab := NewTable(ParseLeases(strings.NewReader("1700000000 8C:79:F5:DD:EE:FF 192.168.1.20 laptop *\n")))
	if got := tab.IPByMAC("8c:79:f5:dd:ee:ff"); got != "192.168.1.20" {
		t.Errorf("IPByMAC = %q, want 192.168.1.20", got)
	}
	if got := tab.IPByMAC("aa:bb:cc:dd:ee:ff"); got != "" {
		t.Errorf("IPByMAC for an unknown MAC = %q, want empty", got)
	}
	var nilTable *Table
	if got := nilTable.IPByMAC("8c:79:f5:dd:ee:ff"); got != "" {
		t.Errorf("nil table IPByMAC = %q, want empty", got)
	}
}
