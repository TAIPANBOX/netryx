package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"netryx/internal/collect"
	"netryx/internal/nflog"
	"netryx/internal/preflight"
)

// One device speaking to two places is one row with both flows folded in, and
// its name comes from the lease rather than from the address. This is the whole
// promise of the tool, and before this test the function that keeps it had no
// coverage at all.
func TestSnapshotFoldsFlowsIntoOneRowPerDevice(t *testing.T) {
	dir := t.TempDir()
	ct := filepath.Join(dir, "nf_conntrack")
	write(t, ct, `ipv4     2 tcp      6 431997 ESTABLISHED src=192.168.50.10 dst=140.82.121.4 sport=51000 dport=443 packets=42 bytes=5120 src=140.82.121.4 dst=192.168.1.23 sport=443 dport=51000 packets=38 bytes=40960 [ASSURED] mark=0 use=1
ipv4     2 tcp      6 431997 ESTABLISHED src=192.168.50.10 dst=1.1.1.1 sport=51001 dport=443 packets=8 bytes=1000 src=1.1.1.1 dst=192.168.1.23 sport=443 dport=51001 packets=6 bytes=2000 [ASSURED] mark=0 use=1
ipv4     2 tcp      6 86399 ESTABLISHED src=192.168.50.12 dst=142.250.180.14 sport=51004 dport=443 packets=900 bytes=90000 src=142.250.180.14 dst=192.168.1.23 sport=443 dport=51004 packets=1200 bytes=1400000 [OFFLOAD] mark=0 use=1
`)
	leases := filepath.Join(dir, "dhcp.leases")
	write(t, leases, "1789549116 94:83:c4:48:39:89 192.168.50.10 phone-lounge *\n")

	s, err := snapshot(ct, leases)
	if err != nil {
		t.Fatal(err)
	}
	if s.Flows != 3 {
		t.Fatalf("three flows were read, got %d", s.Flows)
	}
	if len(s.Devices) != 2 {
		t.Fatalf("two devices sent them, got %d rows: %+v", len(s.Devices), s.Devices)
	}
	byAddr := map[string]int{}
	for i, d := range s.Devices {
		byAddr[d.Addr] = i
	}
	a, ok := byAddr["192.168.50.10"]
	if !ok {
		t.Fatalf("the device with two flows is missing: %+v", s.Devices)
	}
	d := s.Devices[a]
	if d.Name != "phone-lounge" {
		t.Errorf("the lease names it, so the row is not an address: %q", d.Name)
	}
	if d.Flows != 2 {
		t.Errorf("its two flows fold into one row: flows=%d", d.Flows)
	}
	// The direction asymmetry is the exfiltration signal, so the two must never
	// be summed together.
	if d.UpBytes != 6120 || d.DownBytes != 42960 {
		t.Errorf("up and down are kept apart: up=%d down=%d", d.UpBytes, d.DownBytes)
	}
	if d.UpPackets != 50 || d.DownPackets != 44 {
		t.Errorf("packets likewise: up=%d down=%d", d.UpPackets, d.DownPackets)
	}
	if !d.NATed {
		t.Error("the reply came back to a different address, so this flow is NATed")
	}

	// A device with no lease is still a row, named by its address rather than
	// dropped or left blank.
	u := s.Devices[byAddr["192.168.50.12"]]
	if u.Name != "192.168.50.12" {
		t.Errorf("an unnamed device falls back to its address: %q", u.Name)
	}
	if !u.Offloaded {
		t.Error("the kernel marked that flow OFFLOAD, and the row must say so")
	}
}

// Accounting off is the silent one: the kernel prints no counters and a parser
// that shrugs turns it into "0 bytes", which reads as a quiet device.
func TestSnapshotCarriesTheAccountingWarning(t *testing.T) {
	dir := t.TempDir()
	ct := filepath.Join(dir, "nf_conntrack")
	write(t, ct, "ipv4     2 tcp      6 431997 ESTABLISHED src=192.168.50.10 dst=140.82.121.4 sport=51000 dport=443 src=140.82.121.4 dst=192.168.1.23 sport=443 dport=51000 [ASSURED] mark=0 use=1\n")
	s, err := snapshot(ct, filepath.Join(dir, "no-such-leases"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.AcctOff {
		t.Fatal("no flow carried a counter, and the snapshot must say so rather than report zeros")
	}
	if len(s.Devices) != 1 || s.Devices[0].Name != "192.168.50.10" {
		t.Fatalf("a missing lease file is not a failure, the device is named by address: %+v", s.Devices)
	}
}

// A conntrack file that is not there is an error, not an empty report. An empty
// report looks like a quiet network.
func TestSnapshotRefusesAMissingConntrack(t *testing.T) {
	if _, err := snapshot(filepath.Join(t.TempDir(), "nope"), ""); err == nil {
		t.Fatal("a missing conntrack file must be an error")
	}
}

// An unrecognised word must not become a working configuration.
func TestRuleOptionsRefusesAModeItDoesNotKnow(t *testing.T) {
	for _, m := range []string{"ingres", "INGRESS", "", "bridge", "forward "} {
		if _, err := ruleOptions(m, []string{"br-lan"}, false); err == nil {
			t.Errorf("mode %q is not a mode, and taking it would watch the wrong place", m)
		}
	}
	in, err := ruleOptions("ingress", []string{"br-lan"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if in.Mode != nflog.Ingress || !in.DNS {
		t.Errorf("ingress with passive DNS asked for: mode=%v dns=%v", in.Mode, in.DNS)
	}
	fwd, err := ruleOptions("forward", []string{"br-lan"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if fwd.Mode != nflog.Forward || fwd.DNS {
		t.Errorf("forward without DNS: mode=%v dns=%v", fwd.Mode, fwd.DNS)
	}
	if len(fwd.Interfaces) != 1 || fwd.Interfaces[0] != "br-lan" {
		t.Errorf("the interfaces are carried through: %v", fwd.Interfaces)
	}
}

// fakeReader stands in for the netlink socket, which cannot exist off a router.
type fakeReader struct {
	batches [][]nflog.Packet
	errs    []error
	n       int
}

func (f *fakeReader) Receive([]byte) ([]nflog.Packet, error) {
	if f.n >= len(f.errs) {
		return nil, errors.New("end of the fixture")
	}
	i := f.n
	f.n++
	return f.batches[i], f.errs[i]
}

// The loop must keep reading when the kernel says it dropped copies, and stop
// when the socket is genuinely gone. Getting this backwards means a monitor
// that quietly stops at the first busy moment.
func TestReadIntoSurvivesLostCopiesAndStopsOnARealError(t *testing.T) {
	p1 := nflog.Packet{HWAddr: "02:00:00:00:00:01", Seq: 1, HasSeq: true}
	p2 := nflog.Packet{HWAddr: "02:00:00:00:00:01", Seq: 5, HasSeq: true}
	f := &fakeReader{
		batches: [][]nflog.Packet{{p1}, nil, {p2}, nil},
		errs:    []error{nil, syscall.ENOBUFS, nil, errors.New("socket closed")},
	}
	col := collect.New()
	done := make(chan struct{})
	go func() { readInto(f, col, make(chan struct{})); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not return when the socket failed")
	}
	if f.n != 4 {
		t.Fatalf("it must read past the recoverable error: read %d of 4", f.n)
	}
	// Sequence 1 then 5 is three copies the kernel numbered and did not deliver.
	if lost := col.LostCopies(); lost != 3 {
		t.Errorf("the gap between seq 1 and seq 5 is 3 lost copies, got %d", lost)
	}
}

// Asked to stop, it stops, rather than blocking on a socket that may never
// speak again.
func TestReadIntoStopsWhenAsked(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	f := &fakeReader{batches: [][]nflog.Packet{{}}, errs: []error{nil}}
	done := make(chan struct{})
	go func() { readInto(f, collect.New(), stop); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a closed stop channel must end the loop")
	}
	if f.n != 0 {
		t.Errorf("and it must not have read anything first: %d", f.n)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// D2, measured on the Flint 2: fw4's offload setting was read once, before
// the console was built. What the engine asks reads it again each time, so
// flow offloading turned on (or off) under the running service is seen; a
// file that cannot be read counts as offloading off, as at the start.
func TestTheOffloadSettingIsReadAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "firewall")
	write(t, path, "config defaults\n\toption input 'REJECT'\n")
	var told []string
	logf := func(f string, a ...any) { told = append(told, fmt.Sprintf(f, a...)) }
	blind := blindNow(path, true, true, "forward", logf)
	if blind() != "" {
		t.Fatal("offloading off: names are seen")
	}
	write(t, path, "config defaults\n\toption input 'REJECT'\n\toption flow_offloading '1'\n")
	if blind() == "" {
		t.Fatal("offloading turned on after the start: forward mode cannot see names")
	}
	write(t, path, "config defaults\n\toption flow_offloading '1'\n\toption flow_offloading_hw '1'\n")
	if blind() == "" {
		t.Fatal("hardware offloading: forward mode cannot see names")
	}
	os.Remove(path)
	if blind() != "" || blind() != "" {
		t.Fatal("an unreadable setting counts as offloading off")
	}
	if len(told) != 1 || !strings.HasPrefix(told[0], "offload setting could not be read") {
		t.Fatalf("an unreadable setting is said once, not at every look: %q", told)
	}
	if blindNow(path, true, true, "ingress", logf)() != "" {
		t.Fatal("ingress mode sees names whatever the offload")
	}
}

// R41: the rules engine is told whether it can see names (blindReason, what
// serve hands it, answers "" when it can). It sees them only with the capture
// and passive DNS both on, and not in forward mode when flow offloading takes
// established connections past the forward chain.
func TestNamesAreSeenOnlyWithCaptureAndPassiveDNS(t *testing.T) {
	on := preflight.Offload{Software: true, Found: true}
	off := preflight.Offload{Found: true}
	for _, tc := range []struct {
		capture, dns bool
		mode         string
		o            preflight.Offload
		want         bool
	}{
		{true, true, "ingress", off, true},
		{true, true, "ingress", on, true},
		{true, true, "forward", off, true},
		{true, true, "forward", on, false},
		{true, true, "forward", preflight.Offload{Hardware: true, Found: true}, false},
		{false, true, "ingress", off, false},
		{true, false, "ingress", off, false},
	} {
		if got := blindReason(tc.capture, tc.dns, tc.mode, tc.o); (got == "") != tc.want {
			t.Errorf("capture %v dns %v %s offload %v: reason %q", tc.capture, tc.dns, tc.mode, tc.o, got)
		}
	}
}

// D3, measured on the Flint 2: in forward mode under flow offloading, with
// capture and passive_dns both on, the rule said "turn on capture and
// passive_dns". The reason names each cause there is, with what to do about
// it, in the words of the config's own options.
func TestTheBlindReasonNamesItsCause(t *testing.T) {
	on := preflight.Offload{Software: true, Found: true}
	off := preflight.Offload{Found: true}
	const (
		noCapture = "capture is off (set capture to 1)"
		noDNS     = "passive_dns is off (set passive_dns to 1)"
		offloaded = "forward mode under flow offloading (set mode to ingress, or turn flow offloading off)"
	)
	for _, tc := range []struct {
		capture, dns bool
		mode         string
		o            preflight.Offload
		want         string
	}{
		{true, true, "forward", on, "names cannot be seen: " + offloaded},
		{true, true, "forward", preflight.Offload{Hardware: true, Found: true}, "names cannot be seen: " + offloaded},
		{false, true, "ingress", off, "names cannot be seen: " + noCapture},
		{true, false, "ingress", on, "names cannot be seen: " + noDNS},
		{false, false, "forward", on, "names cannot be seen: " + noCapture + "; " + noDNS + "; " + offloaded},
		{true, true, "forward", off, ""},
	} {
		if got := blindReason(tc.capture, tc.dns, tc.mode, tc.o); got != tc.want {
			t.Errorf("capture %v dns %v %s offload %+v:\n got %q\nwant %q", tc.capture, tc.dns, tc.mode, tc.o, got, tc.want)
		}
	}
}
