package inventory

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"netryx/internal/packfile"
)

const ouiSample = "OUI/MA-L\n\n" +
	"8C-79-F5   (hex)\t\tSamsung Electronics Co.,Ltd\n" +
	"8C79F5     (base 16)\t\tSamsung Electronics Co.,Ltd\n" +
	"00-00-00   (hex)\t\tXEROX CORPORATION\n" +
	"ff-ff-fe   (hex)\t\tLast One\n" +
	"00-1A-2B   (hex)\t\t\n" +
	"00-00-01   (hex)\t\tXEROX CORPORATION\n" +
	"8C-79-F5   (hex)\t\tSamsung, again\n" +
	"12-34   (hex)\t\tToo short\n"

func ouiMapOf(t *testing.T, src string) OUIMap {
	t.Helper()
	m, err := LoadOUI(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func ouiPackOf(t *testing.T, m OUIMap) []byte {
	t.Helper()
	b, err := MarshalOUIPack(m)
	if err != nil {
		t.Fatalf("MarshalOUIPack: %v", err)
	}
	return b
}

// ouiProbes are every registration, its neighbours, the ends of the space,
// and forms the map never holds (lower case, too short, not hex).
func ouiProbes(m OUIMap) []string {
	out := []string{"000000", "FFFFFF", "fffffe", "8c79f5", "8C79F", "8C79F50", "", "GGGGGG", "8C79F%"}
	for k := range m {
		var x uint32
		fmt.Sscanf(k, "%06X", &x)
		out = append(out, k, fmt.Sprintf("%06X", (x-1)&0xffffff), fmt.Sprintf("%06X", (x+1)&0xffffff))
	}
	return out
}

func sameVendors(t *testing.T, old OUIMap, p *OUIPack, probes []string) {
	t.Helper()
	bad := 0
	for _, k := range probes {
		if w, g := old.Vendor(k), p.Vendor(k); w != g {
			if bad++; bad <= 10 {
				t.Errorf("Vendor(%q): map %q, pack %q", k, w, g)
			}
		}
		mac := strings.ToLower(k) + ":00:00:00"
		if w, g := vendorOf(old, mac), vendorOf(p, mac); w != g {
			if bad++; bad <= 10 {
				t.Errorf("vendorOf(%q): map %q, pack %q", mac, w, g)
			}
		}
	}
	if bad > 10 {
		t.Errorf("... %d answers differed in all", bad)
	}
}

func TestAVendorPackAnswersAsTheList(t *testing.T) {
	m := ouiMapOf(t, ouiSample)
	p, err := ReadOUIPack(ouiPackOf(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if p.Len() != len(m) {
		t.Fatalf("pack holds %d, map %d", p.Len(), len(m))
	}
	sameVendors(t, m, p, ouiProbes(m))
	if got := p.Vendor("8C79F5"); got != "Samsung, again" {
		t.Errorf("a repeated registration keeps the last name, as the map does: %q", got)
	}
}

func TestAVendorPackRefusesAKeyItCannotHold(t *testing.T) {
	if _, err := MarshalOUIPack(OUIMap{"ZZZZZZ": "not hex"}); err == nil {
		t.Fatal("a registration that is not hex was written")
	}
}

func resealOUI(b []byte, edit func(body []byte)) []byte {
	body := append([]byte(nil), b[packfile.MagicLen+4:len(b)-32]...)
	edit(body)
	return packfile.Seal(OUIMagic, OUIVersion, body)
}

func TestADamagedVendorPackIsRefusedWithItsReason(t *testing.T) {
	good := ouiPackOf(t, ouiMapOf(t, ouiSample))
	put := func(b []byte, at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
	cases := []struct {
		name, want string
		b          []byte
	}{
		{"one byte changed", "checksum", func() []byte {
			b := append([]byte(nil), good...)
			b[len(b)-40] ^= 1
			return b
		}()},
		{"cut short", "checksum", good[:len(good)-3]},
		{"another kind of file", "magic", packfile.Seal("NXIP2ASN", OUIVersion, good[12:len(good)-32])},
		{"another version", "version", packfile.Seal(OUIMagic, OUIVersion+1, good[12:len(good)-32])},
		{"a count that lies", "counts describe", resealOUI(good, func(b []byte) { put(b, 0, 99) })},
		{"a count past the limit", "over the limit", resealOUI(good, func(b []byte) { put(b, 0, maxOUI+1) })},
		{"prefixes out of order", "out of order", resealOUI(good, func(b []byte) { put(b, ouiHead, 0xfffff0) })},
		{"a prefix wider than 24 bits", "wider", resealOUI(good, func(b []byte) { put(b, ouiHead, 1<<24) })},
		{"a name past the names", "names bytes", resealOUI(good, func(b []byte) { put(b, ouiHead+4, 1<<20) })},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := ReadOUIPack(c.b)
			if err == nil {
				t.Fatalf("a damaged pack was accepted: %d registrations", p.Len())
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("refused with %q, want a reason naming %q", err, c.want)
			}
		})
	}
}

func TestOpenOUIReadsAPackAndTheOldFile(t *testing.T) {
	dir := t.TempDir()
	m := ouiMapOf(t, ouiSample)
	packPath := filepath.Join(dir, "oui.pack")
	if err := packfile.WriteFile(packPath, ouiPackOf(t, m)); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(dir, "oui.txt")
	os.WriteFile(txt, []byte(ouiSample), 0o644)

	v, err := OpenOUI(packPath)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := v.(*OUIPack)
	if !ok || !p.Mapped() {
		t.Fatalf("a pack was not read through a mapping: %T", v)
	}
	v, err = OpenOUI(txt)
	if err != nil {
		t.Fatal(err)
	}
	old, ok := v.(OUIMap)
	if !ok {
		t.Fatalf("oui.txt was not loaded the old way: %T", v)
	}
	sameVendors(t, old, p, ouiProbes(m))

	bad := filepath.Join(dir, "damaged.pack")
	os.WriteFile(bad, []byte(ouiSample), 0o644)
	if v, err := OpenOUI(bad); err == nil || v != nil {
		t.Fatalf("a .pack holding text was read: %v %v", v, err)
	}
}

func TestAVendorLookupDoesNotAllocate(t *testing.T) {
	p, err := ReadOUIPack(ouiPackOf(t, ouiMapOf(t, ouiSample)))
	if err != nil {
		t.Fatal(err)
	}
	if n := testing.AllocsPerRun(100, func() { p.Vendor("8C79F5"); p.Vendor("123456") }); n != 0 {
		t.Fatalf("a vendor lookup allocates %v times", n)
	}
}

// TestTheVendorPackAnswersAsTheListForEveryPrefix compares the real oui.txt
// loaded by LoadOUI with its pack: every registration, the prefixes either
// side of each, and 200 000 random prefixes. NETRYX_DATA names the directory
// holding oui.txt; without it the test is skipped.
func TestTheVendorPackAnswersAsTheListForEveryPrefix(t *testing.T) {
	dir := os.Getenv("NETRYX_DATA")
	if dir == "" {
		t.Skip("NETRYX_DATA is not set")
	}
	src, err := os.ReadFile(filepath.Join(dir, "oui.txt"))
	if err != nil {
		t.Fatal(err)
	}
	m := ouiMapOf(t, string(src))
	path := filepath.Join(t.TempDir(), "oui.pack")
	if err := packfile.WriteFile(path, ouiPackOf(t, m)); err != nil {
		t.Fatal(err)
	}
	p, err := OpenOUIPack(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Len() != len(m) {
		t.Fatalf("pack holds %d, map %d", p.Len(), len(m))
	}
	probes := ouiProbes(m)
	for _, k := range p.Prefixes() {
		if _, ok := m[k]; !ok {
			t.Errorf("the pack holds %s, the list does not", k)
		}
	}
	rng := rand.New(rand.NewPCG(20261123, 2))
	for i := 0; i < 200_000; i++ {
		probes = append(probes, fmt.Sprintf("%06X", rng.Uint32()&0xffffff))
	}
	sameVendors(t, m, p, probes)
	t.Logf("compared %d registrations and %d prefixes in all", len(m), len(probes))
}
