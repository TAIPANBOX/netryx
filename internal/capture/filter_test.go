package capture

import (
	"bufio"
	"encoding/hex"
	"os"
	"testing"

	"golang.org/x/net/bpf"
)

// The fixtures are real frames taken off the tap on 2026-09-15: handshakes.hex
// is what the filter is supposed to catch, bulk.hex is the traffic it must
// leave in the kernel.
func load(t *testing.T, name string) [][]byte {
	t.Helper()
	fh, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	var out [][]byte
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		b, err := hex.DecodeString(sc.Text())
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		t.Fatalf("%s is empty", name)
	}
	return out
}

func vm(t *testing.T) *bpf.VM {
	t.Helper()
	v, err := bpf.NewVM(Filter())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFilterCatchesRealHandshakes(t *testing.T) {
	v := vm(t)
	for i, f := range load(t, "handshakes.hex") {
		n, err := v.Run(f)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("frame %d was captured on the wire by this very filter, yet the program rejects it", i)
		}
	}
}

func TestFilterLeavesBulkTrafficAlone(t *testing.T) {
	v := vm(t)
	for i, f := range load(t, "bulk.hex") {
		n, err := v.Run(f)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("bulk frame %d passed the filter: every such packet would wake the process", i)
		}
	}
}

// Synthetic cases for the branches the fixtures happen not to cover.
func TestFilterBranches(t *testing.T) {
	v := vm(t)
	tcp4 := func(payload ...byte) []byte {
		f := make([]byte, 54)
		f[12], f[13] = 0x08, 0x00
		f[14] = 0x45 // IPv4, header length 5 words
		f[23] = 6    // TCP
		f[46] = 0x50 // TCP data offset 5 words
		return append(f, payload...)
	}
	if n, _ := v.Run(tcp4(0x16, 0x03, 0x01)); n == 0 {
		t.Fatal("an IPv4 TLS handshake must pass")
	}
	if n, _ := v.Run(tcp4(0x17, 0x03, 0x03)); n != 0 {
		t.Fatal("IPv4 application data must not pass")
	}
	udp4quic := func(first byte, dport byte) []byte {
		f := make([]byte, 42)
		f[12], f[13] = 0x08, 0x00
		f[14] = 0x45
		f[23] = 17
		f[36], f[37] = 0x01, dport // destination port 443 = 0x01bb
		return append(f, first)
	}
	if n, _ := v.Run(udp4quic(0xc0, 0xbb)); n == 0 {
		t.Fatal("a QUIC Initial on 443 must pass")
	}
	if n, _ := v.Run(udp4quic(0x40, 0xbb)); n != 0 {
		t.Fatal("a short-header QUIC packet is mid-session and must not pass")
	}
	if n, _ := v.Run(udp4quic(0xc0, 0xbc)); n != 0 {
		t.Fatal("UDP to another port must not pass")
	}
	// a fragment carries no usable transport header
	frag := tcp4(0x16, 0x03, 0x01)
	frag[20], frag[21] = 0x00, 0x08 // fragment offset 8, so no transport header here
	if n, _ := v.Run(frag); n != 0 {
		t.Fatal("a fragment must not pass")
	}
}
