package enrich

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"netryx/internal/packfile"
)

// packSample has what the loader must handle: IPv4 and IPv6 mixed, ranges
// out of order, neighbours, an overlap it drops, AS 0 it skips, a bad country
// it turns into "--", a malformed line, organisations shared by several
// answers and an empty organisation.
const packSample = "8.8.8.0\t8.8.8.255\t15169\tUS\tGOOGLE\n" +
	"1.0.0.0\t1.0.0.255\t13335\tUS\tCLOUDFLARENET\n" +
	"1.0.1.0\t1.0.3.255\t13335\tUS\tCLOUDFLARENET\n" +
	"1.0.2.0\t1.0.2.10\t64512\tAU\tOVERLAP\n" +
	"0.0.0.0\t0.255.255.255\t0\tNone\tNot routed\n" +
	"2.0.0.0\t2.0.0.255\t3215\tXYZ\tORANGE\n" +
	"5.0.0.0\t5.0.0.255\t64513\tDE\t\n" +
	"not a line\n" +
	"255.255.255.0\t255.255.255.255\t64514\tGB\tGOOGLE\n" +
	"2001:db8::\t2001:db8::ffff\t64500\tGB\tEXAMPLE\n" +
	"2a00::\t2a00::ff\t15169\tUS\tGOOGLE\n" +
	"::\t::ff\t64501\tNL\tLOW\n" +
	"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ff00\tffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff\t64502\tFR\tHIGH\n"

func packOf(t *testing.T, g *GeoDB) []byte {
	t.Helper()
	b, err := g.MarshalPack()
	if err != nil {
		t.Fatalf("MarshalPack: %v", err)
	}
	return b
}

func readPack(t *testing.T, b []byte) *GeoDB {
	t.Helper()
	p, err := ReadGeoPack(b)
	if err != nil {
		t.Fatalf("ReadGeoPack: %v", err)
	}
	return p
}

// probes are the addresses where an off-by-one would show: every range's
// ends and the addresses just outside them, and the ends of each family.
func probes(g *GeoDB) []netip.Addr {
	out := []netip.Addr{netip.IPv4Unspecified(), netip.AddrFrom4([4]byte{255, 255, 255, 255}),
		netip.IPv6Unspecified(), netip.AddrFrom16([16]byte{0: 0xff, 1: 0xff, 2: 0xff, 3: 0xff, 4: 0xff, 5: 0xff, 6: 0xff, 7: 0xff, 8: 0xff, 9: 0xff, 10: 0xff, 11: 0xff, 12: 0xff, 13: 0xff, 14: 0xff, 15: 0xff})}
	edge := func(lo, hi netip.Addr) {
		out = append(out, lo, hi, lo.Prev(), hi.Next())
	}
	for _, r := range g.v4 {
		edge(netip.AddrFrom4(u32bytes(r.lo)), netip.AddrFrom4(u32bytes(r.hi)))
	}
	for _, r := range g.v6 {
		edge(netip.AddrFrom16(r.lo), netip.AddrFrom16(r.hi))
	}
	var valid []netip.Addr
	for _, a := range out {
		if a.IsValid() {
			valid = append(valid, a)
		}
	}
	return valid
}

// sameAnswers compares the two tables on every address given, and on the
// IPv4-mapped form of every IPv4 one.
func sameAnswers(t *testing.T, old, pack *GeoDB, addrs []netip.Addr) {
	t.Helper()
	bad := 0
	for _, a := range addrs {
		forms := []netip.Addr{a}
		if a.Is4() {
			forms = append(forms, netip.AddrFrom16(a.As16()))
		}
		for _, f := range forms {
			wi, wok := old.Lookup(f)
			gi, gok := pack.Lookup(f)
			if wi != gi || wok != gok {
				if bad++; bad <= 10 {
					t.Errorf("Lookup(%s): loader %+v %v, pack %+v %v", f, wi, wok, gi, gok)
				}
			}
		}
	}
	if bad > 10 {
		t.Errorf("... %d addresses answered differently in all", bad)
	}
}

func TestAPackAnswersAsTheTableItWasWrittenFrom(t *testing.T) {
	old := load(t, packSample)
	p := readPack(t, packOf(t, old))
	if p.Len() != old.Len() || p.Skipped() != old.Skipped() {
		t.Fatalf("pack Len %d Skipped %d, loader Len %d Skipped %d", p.Len(), p.Skipped(), old.Len(), old.Skipped())
	}
	if err := SamePack(old, p); err != nil {
		t.Fatal(err)
	}
	sameAnswers(t, old, p, probes(old))
	if info, ok := p.Lookup(addr("5.0.0.7")); !ok || info.Org != "" || info.CC != "DE" {
		t.Errorf("an empty organisation reads back as %+v %v", info, ok)
	}
	if info, _ := p.Lookup(addr("2.0.0.1")); info.CC != "--" {
		t.Errorf("a country that was not two letters reads back as %q", info.CC)
	}
	for name, m := range map[string]func(ASInfo) bool{
		"all":     func(ASInfo) bool { return true },
		"US":      func(a ASInfo) bool { return a.CC == "US" },
		"GOOGLE":  func(a ASInfo) bool { return a.Org == "GOOGLE" },
		"AS13335": func(a ASInfo) bool { return a.ASN == 13335 },
		"none":    func(ASInfo) bool { return false },
	} {
		w4, w6 := old.Ranges(m)
		g4, g6 := p.Ranges(m)
		if !reflect.DeepEqual(w4, g4) || !reflect.DeepEqual(w6, g6) {
			t.Errorf("Ranges(%s): loader %v %v, pack %v %v", name, w4, w6, g4, g6)
		}
	}
}

func TestAnEmptyTableIsAPackToo(t *testing.T) {
	old := load(t, "not a line\n")
	p := readPack(t, packOf(t, old))
	if p.Len() != 0 || p.Skipped() != 1 {
		t.Fatalf("Len %d Skipped %d", p.Len(), p.Skipped())
	}
	if info, ok := p.Lookup(addr("1.2.3.4")); ok || info.CC != "--" {
		t.Errorf("an empty pack answered %+v %v", info, ok)
	}
}

// reseal rewrites a pack's body and seals it again with a good checksum, so
// only the structural checks stand between it and a wrong answer.
func reseal(b []byte, edit func(body []byte)) []byte {
	body := append([]byte(nil), b[packfile.MagicLen+4:len(b)-32]...)
	edit(body)
	return packfile.Seal(GeoMagic, GeoVersion, body)
}

func TestADamagedPackIsRefusedWithItsReason(t *testing.T) {
	old := load(t, packSample)
	good := packOf(t, old)
	n4 := len(old.v4)
	n6 := len(old.v6)
	v4At := geoHead
	v6At := v4At + n4*rec4
	asAt := v6At + n6*rec6
	put := func(body []byte, at int, v uint32) { binary.LittleEndian.PutUint32(body[at:], v) }
	cases := []struct {
		name, want string
		b          []byte
	}{
		{"one byte changed", "checksum", func() []byte {
			b := append([]byte(nil), good...)
			b[len(b)/2] ^= 1
			return b
		}()},
		{"cut short", "checksum", good[:len(good)-1]},
		{"cut to nothing", "too short", good[:20]},
		{"another kind of file", "magic", packfile.Seal("NXOUIVND", GeoVersion, good[12:len(good)-32])},
		{"another version", "version", packfile.Seal(GeoMagic, GeoVersion+1, good[12:len(good)-32])},
		{"a count that lies", "counts describe", reseal(good, func(b []byte) { put(b, 0, uint32(n4+1)) })},
		{"a count past the limit", "over the limit", reseal(good, func(b []byte) { put(b, 4, MaxRanges+1) })},
		{"a reserved field set", "reserved", reseal(good, func(b []byte) { put(b, 20, 1) })},
		{"ranges out of order", "out of order", reseal(good, func(b []byte) {
			r0 := append([]byte(nil), b[v4At:v4At+rec4]...)
			copy(b[v4At:], b[v4At+rec4:v4At+2*rec4])
			copy(b[v4At+rec4:], r0)
		})},
		{"an IPv4 range ending before it starts", "ends before", reseal(good, func(b []byte) { put(b, v4At, 0xffffffff) })},
		{"an IPv4 answer that does not exist", "names answer", reseal(good, func(b []byte) { put(b, v4At+8, 1000) })},
		{"an IPv6 answer that does not exist", "names answer", reseal(good, func(b []byte) { put(b, v6At+32, 1000) })},
		{"IPv6 ranges overlapping", "out of order", reseal(good, func(b []byte) {
			copy(b[v6At+rec6:v6At+rec6+16], b[v6At+16:v6At+32])
		})},
		{"an organisation past the names", "names bytes", reseal(good, func(b []byte) { put(b, asAt+4, 1<<20) })},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, err := ReadGeoPack(c.b)
			if err == nil {
				t.Fatalf("a damaged pack was accepted: %d ranges", g.Len())
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("refused with %q, want a reason naming %q", err, c.want)
			}
		})
	}
}

func TestOpenGeoReadsAPackAndTheOldFile(t *testing.T) {
	dir := t.TempDir()
	old := load(t, packSample)
	packPath := filepath.Join(dir, "ip2asn.pack")
	if err := packfile.WriteFile(packPath, packOf(t, old)); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(packSample))
	zw.Close()
	gzPath := filepath.Join(dir, "ip2asn-combined.tsv.gz")
	if err := os.WriteFile(gzPath, gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := OpenGeo(packPath)
	if err != nil {
		t.Fatalf("OpenGeo(pack): %v", err)
	}
	if p.pk == nil {
		t.Fatal("a pack was not read as a pack")
	}
	if !p.Mapped() {
		t.Error("a pack on a local file system was not mapped")
	}
	text, err := OpenGeo(gzPath)
	if err != nil {
		t.Fatalf("OpenGeo(tsv.gz): %v", err)
	}
	if text.pk != nil {
		t.Fatal("the old file was not loaded the old way")
	}
	sameAnswers(t, text, p, probes(old))

	// A file named as a pack is never read as text, however it is damaged.
	bad := filepath.Join(dir, "damaged.pack")
	os.WriteFile(bad, []byte(packSample), 0o644)
	if g, err := OpenGeo(bad); err == nil {
		t.Fatalf("a .pack holding text was read: %d ranges", g.Len())
	}
	if _, err := OpenGeo(filepath.Join(dir, "gone.pack")); !os.IsNotExist(err) {
		t.Fatalf("a missing pack: %v, want not exist", err)
	}
}

// A connection is looked up once per flow, so the pack's lookup must not
// allocate any more than the loader's does.
func TestAPackLookupDoesNotAllocate(t *testing.T) {
	p := readPack(t, packOf(t, load(t, packSample)))
	a4, a6 := addr("1.0.2.5"), addr("2a00::7")
	if n := testing.AllocsPerRun(100, func() {
		p.Lookup(a4)
		p.Lookup(a6)
	}); n != 0 {
		t.Fatalf("a lookup allocates %v times", n)
	}
}

// TestThePackAnswersAsTheLoaderForEveryAddress compares the real table loaded
// from iptoasn's file with its pack: every range's ends and the addresses just
// outside them, a million random IPv4 and a million random IPv6 addresses
// (half of them inside 2000::/3, where the ranges are), and the ranges the
// country rules read. NETRYX_DATA names the directory holding
// ip2asn-combined.tsv.gz; without it the test is skipped.
func TestThePackAnswersAsTheLoaderForEveryAddress(t *testing.T) {
	dir := os.Getenv("NETRYX_DATA")
	if dir == "" {
		t.Skip("NETRYX_DATA is not set")
	}
	f, err := os.Open(filepath.Join(dir, "ip2asn-combined.tsv.gz"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := LoadIP2ASN(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ip2asn.pack")
	if err := packfile.WriteFile(path, packOf(t, old)); err != nil {
		t.Fatal(err)
	}
	p, err := OpenGeoPack(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Len() != old.Len() || p.Skipped() != old.Skipped() {
		t.Fatalf("pack Len %d Skipped %d, loader Len %d Skipped %d", p.Len(), p.Skipped(), old.Len(), old.Skipped())
	}
	edges := probes(old)
	rng := rand.New(rand.NewPCG(20261123, 1))
	var random []netip.Addr
	for i := 0; i < 1_000_000; i++ {
		random = append(random, netip.AddrFrom4(u32bytes(rng.Uint32())))
		var b [16]byte
		binary.BigEndian.PutUint64(b[:8], rng.Uint64())
		binary.BigEndian.PutUint64(b[8:], rng.Uint64())
		if i%2 == 0 {
			b[0] = 0x20 | b[0]&0x1f
		}
		random = append(random, netip.AddrFrom16(b))
	}
	sameAnswers(t, old, p, edges)
	sameAnswers(t, old, p, random)
	hits := 0
	for _, a := range random {
		if _, ok := old.Lookup(a); ok {
			hits++
		}
	}
	t.Logf("compared %d range edges and %d random addresses (%d inside a range); %d ranges, %d IPv4, %d IPv6",
		len(edges), len(random), hits, old.Len(), len(old.v4), len(old.v6))

	ccs := map[string]bool{}
	old.Ranges(func(a ASInfo) bool { ccs[a.CC] = true; return false })
	matchers := map[string]func(ASInfo) bool{
		"all":        func(ASInfo) bool { return true },
		"AS15169":    func(a ASInfo) bool { return a.ASN == 15169 },
		"org GOOGLE": func(a ASInfo) bool { return strings.HasPrefix(a.Org, "GOOGLE") },
	}
	for cc := range ccs {
		matchers["country "+cc] = func(a ASInfo) bool { return a.CC == cc }
	}
	for name, m := range matchers {
		w4, w6 := old.Ranges(m)
		g4, g6 := p.Ranges(m)
		if !reflect.DeepEqual(w4, g4) || !reflect.DeepEqual(w6, g6) {
			t.Errorf("Ranges(%s) differ: loader %d+%d, pack %d+%d", name, len(w4), len(w6), len(g4), len(g6))
		}
	}
	t.Logf("compared Ranges for %d matchers, %d of them countries", len(matchers), len(ccs))
}
