package neigh

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// msg builds a netlink neighbour message body the way the kernel lays it out,
// so the parser is tested against the wire format rather than against itself.
func msg(ifindex int32, state uint16, ip net.IP, mac net.HardwareAddr) []byte {
	b := make([]byte, 12)
	if ip.To4() != nil {
		b[0] = 2 // AF_INET
	} else {
		b[0] = 10 // AF_INET6
	}
	binary.LittleEndian.PutUint32(b[4:8], uint32(ifindex))
	binary.LittleEndian.PutUint16(b[8:10], state)
	attr := func(typ uint16, val []byte) []byte {
		l := 4 + len(val)
		a := make([]byte, (l+3)&^3)
		binary.LittleEndian.PutUint16(a[0:2], uint16(l))
		binary.LittleEndian.PutUint16(a[2:4], typ)
		copy(a[4:], val)
		return a
	}
	raw := ip.To4()
	if raw == nil {
		raw = ip.To16()
	}
	b = append(b, attr(ndaDst, raw)...)
	if mac != nil {
		b = append(b, attr(ndaLladdr, mac)...)
	}
	return b
}

func TestParseMessage(t *testing.T) {
	mac, _ := net.ParseMAC("02:57:c8:48:01:83")
	e, ok := ParseMessage(rtmNewNeigh, msg(7, nudReachable, net.ParseIP("2a01:4b00:ab09:c000:24ec:a181:a05f:5412"), mac))
	if !ok {
		t.Fatal("a reachable IPv6 neighbour must parse")
	}
	if e.MAC != "02:57:c8:48:01:83" || e.IfIndex != 7 || !e.Usable() || e.Deleted {
		t.Fatalf("decoded wrong: %+v", e)
	}
	if !e.IP.Equal(net.ParseIP("2a01:4b00:ab09:c000:24ec:a181:a05f:5412")) {
		t.Fatalf("address lost: %v", e.IP)
	}

	// an INCOMPLETE entry has no hardware address to trust
	inc, ok := ParseMessage(rtmNewNeigh, msg(7, nudIncomplete, net.ParseIP("192.168.1.9"), nil))
	if !ok || inc.Usable() {
		t.Fatalf("an incomplete entry binds nothing: %+v", inc)
	}
	failed, _ := ParseMessage(rtmNewNeigh, msg(7, nudFailed, net.ParseIP("192.168.1.9"), mac))
	if failed.Usable() {
		t.Fatal("a FAILED entry must not be treated as an answer")
	}
	del, ok := ParseMessage(rtmDelNeigh, msg(7, nudStale, net.ParseIP("192.168.1.9"), mac))
	if !ok || !del.Deleted {
		t.Fatalf("a removal must be recognised: %+v", del)
	}
	if _, ok := ParseMessage(16 /* RTM_NEWLINK */, msg(7, nudReachable, net.ParseIP("192.168.1.9"), mac)); ok {
		t.Fatal("another message type is not a neighbour")
	}
}

// The point of the package: a phone that rotates through privacy addresses is
// one device, not a dozen.
func TestRotatingIPv6AddressesCollapseToOneDevice(t *testing.T) {
	mac, _ := net.ParseMAC("02:57:c8:48:01:83")
	other, _ := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	r := NewRegistry()
	now := time.Date(2026, 9, 15, 14, 0, 0, 0, time.UTC)
	addrs := []string{
		"2a01:4b00:ab09:c000:24ec:a181:a05f:5412",
		"2a01:4b00:ab09:c000:adf7:412:6037:e167",
		"2a01:4b00:ab09:c000:e84e:700c:1d8:4892",
		"192.168.1.92",
	}
	for i, a := range addrs {
		e, _ := ParseMessage(rtmNewNeigh, msg(7, nudReachable, net.ParseIP(a), mac))
		r.Observe(e, now.Add(time.Duration(i)*time.Hour))
	}
	e, _ := ParseMessage(rtmNewNeigh, msg(7, nudReachable, net.ParseIP("192.168.1.99"), other))
	r.Observe(e, now)

	if r.Devices() != 2 {
		t.Fatalf("four addresses of one phone plus one other device is two devices, got %d", r.Devices())
	}
	if len(r.AddressesOf(mac.String())) != 4 {
		t.Fatalf("the phone's addresses: %v", r.AddressesOf(mac.String()))
	}
	for _, a := range addrs {
		if got := r.MACOf(net.ParseIP(a)); got != mac.String() {
			t.Fatalf("%s should map to the phone, got %q", a, got)
		}
	}
	if got := r.MACOf(net.ParseIP("2001:db8::1")); got != "" {
		t.Fatalf("an address never seen must map to nothing, got %q", got)
	}
}

// The kernel evicts a neighbour about a minute after its last packet. A device
// that went quiet is still the same device when it returns, so a removal must
// not erase the history.
func TestADeletedNeighbourIsStillRemembered(t *testing.T) {
	mac, _ := net.ParseMAC("02:57:c8:48:01:83")
	r := NewRegistry()
	now := time.Now()
	add, _ := ParseMessage(rtmNewNeigh, msg(7, nudReachable, net.ParseIP("192.168.1.92"), mac))
	r.Observe(add, now)
	del, _ := ParseMessage(rtmDelNeigh, msg(7, nudStale, net.ParseIP("192.168.1.92"), mac))
	r.Observe(del, now.Add(time.Minute))
	if got := r.MACOf(net.ParseIP("192.168.1.92")); got != mac.String() {
		t.Fatalf("the mapping must survive the kernel forgetting it, got %q", got)
	}
	// but a week-old address is allowed to go
	if dropped := r.Forget(now.Add(48 * time.Hour)); dropped != 1 {
		t.Fatalf("stale addresses should be dropped on request, dropped %d", dropped)
	}
	if r.Devices() != 0 || r.MACOf(net.ParseIP("192.168.1.92")) != "" {
		t.Fatal("after forgetting its last address the device goes too")
	}
}
