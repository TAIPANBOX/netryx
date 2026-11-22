package enrich

import (
	"bytes"
	"compress/gzip"
	"net/netip"
	"strings"
	"testing"
)

// sample is the iptoasn format as the file ships it: tab separated, one range
// per line, IPv4 and IPv6 mixed freely. Each case below names what it proves.
const sample = "1.0.0.0\t1.0.0.255\t13335\tUS\tCLOUDFLARENET\n" +
	"2001:db8::\t2001:db8::ffff\t64500\tGB\tEXAMPLE\n" +
	"0.0.0.0\t0.255.255.255\t0\tNone\tNot routed\n"

func load(t *testing.T, tsv string) *GeoDB {
	t.Helper()
	g, err := LoadIP2ASN(strings.NewReader(tsv))
	if err != nil {
		t.Fatalf("LoadIP2ASN: %v", err)
	}
	return g
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestLookupFindsTheRangeAtBothEnds(t *testing.T) {
	g := load(t, sample)
	for _, hit := range []string{"1.0.0.0", "1.0.0.255"} {
		info, ok := g.Lookup(addr(hit))
		if !ok {
			t.Fatalf("Lookup(%s) missed an address inside 1.0.0.0-1.0.0.255", hit)
		}
		if info.CC != "US" || info.ASN != 13335 || info.Org != "CLOUDFLARENET" {
			t.Fatalf("Lookup(%s) = %+v, want US AS13335 CLOUDFLARENET", hit, info)
		}
	}
	if _, ok := g.Lookup(addr("1.0.1.0")); ok {
		t.Fatal("Lookup(1.0.1.0) hit an address just past the range")
	}
}

func TestLookupIPv6(t *testing.T) {
	g := load(t, sample)
	info, ok := g.Lookup(addr("2001:db8::42"))
	if !ok {
		t.Fatal("Lookup(2001:db8::42) missed an address inside the IPv6 range")
	}
	if info.CC != "GB" || info.ASN != 64500 || info.Org != "EXAMPLE" {
		t.Fatalf("Lookup(2001:db8::42) = %+v, want GB AS64500 EXAMPLE", info)
	}
}

func TestNotRoutedIsNoAnswer(t *testing.T) {
	g := load(t, sample)
	if _, ok := g.Lookup(addr("0.1.2.3")); ok {
		t.Fatal("a range with AS 0 (not routed) was returned as an answer")
	}
}

func TestHostileLinesAreSkippedNotFatal(t *testing.T) {
	tsv := "1.0.0.0\t1.0.0.255\t13335\tUS\tCLOUDFLARENET\n" +
		"1.0.1.0\t1.0.1.255\t13335\tUS\n" + // too few fields
		"9.9.9.9\t9.9.9.1\t1\tUS\tBACKWARDS\n" + // end before start
		"3.0.0.0\t3.0.0.255\t3\n" + // three fields, the other shape a bad line takes
		"hello\tworld\t1\tUS\tNOT AN ADDRESS\n" + // not addresses at all
		"1.2.3.4\t2001:db8::1\t1\tUS\tMIXED FAMILIES\n" + // one address of each family
		"2.0.0.0\t2.0.0.255\t2\tDE\tOK\n"
	g, err := LoadIP2ASN(strings.NewReader(tsv))
	if err != nil {
		t.Fatalf("a bad line made the whole load fail: %v", err)
	}
	if g.Len() != 2 {
		t.Fatalf("Len() = %d, want 2: only the two valid lines", g.Len())
	}
	for _, hit := range []string{"1.0.0.5", "2.0.0.5"} {
		if _, ok := g.Lookup(addr(hit)); !ok {
			t.Fatalf("valid line around the bad ones was lost: Lookup(%s) missed", hit)
		}
	}
	if _, ok := g.Lookup(addr("9.9.9.5")); ok {
		t.Fatal("the backwards range was loaded")
	}
}

func TestGzipIsDetected(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(sample)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	g, err := LoadIP2ASN(&buf)
	if err != nil {
		t.Fatalf("LoadIP2ASN on gzip data: %v", err)
	}
	info, ok := g.Lookup(addr("1.0.0.7"))
	if !ok || info.ASN != 13335 {
		t.Fatalf("gzipped file loaded differently: %+v, %v", info, ok)
	}
}

func TestNilDBIsSafe(t *testing.T) {
	var g *GeoDB
	info, ok := g.Lookup(addr("1.0.0.1"))
	if ok || info.CC != "--" {
		t.Fatalf("nil GeoDB Lookup = %+v, %v; want CC \"--\" and false", info, ok)
	}
	if g.Len() != 0 {
		t.Fatalf("nil GeoDB Len() = %d, want 0", g.Len())
	}
}

func TestLookupAcceptsMappedIPv4(t *testing.T) {
	// A connection seen on a dual-stack socket arrives as ::ffff:1.0.0.7.
	g := load(t, sample)
	info, ok := g.Lookup(addr("::ffff:1.0.0.7"))
	if !ok || info.ASN != 13335 {
		t.Fatalf("mapped IPv4 address missed: %+v, %v", info, ok)
	}
}

func TestLookupDoesNotAllocate(t *testing.T) {
	g := load(t, sample)
	v4 := addr("1.0.0.7")
	v6 := addr("2001:db8::42")
	n := testing.AllocsPerRun(100, func() {
		g.Lookup(v4)
		g.Lookup(v6)
	})
	if n != 0 {
		t.Fatalf("Lookup allocates %.1f times per call pair; the hot path must not allocate", n)
	}
}

func TestOverlappingRangesAreSkippedNotSilentlyWrong(t *testing.T) {
	// B sits inside A. Keeping both would make the search miss addresses in A
	// after B's end, so the later range is dropped and counted instead.
	tsv := "1.0.0.0\t1.0.0.100\t100\tUS\tA\n" +
		"1.0.0.5\t1.0.0.10\t200\tDE\tB\n"
	g := load(t, tsv)
	info, ok := g.Lookup(addr("1.0.0.50"))
	if !ok || info.ASN != 100 {
		t.Fatalf("Lookup(1.0.0.50) = %+v, %v; want the range A (AS100)", info, ok)
	}
	if g.Skipped() != 1 {
		t.Fatalf("Skipped() = %d, want 1: the overlapping range", g.Skipped())
	}
}

func TestAnOverlongLineIsSkipped(t *testing.T) {
	// A line past bufio's default buffer must not abort the whole load: it is
	// skipped like any other bad line, and the lines either side still load.
	tsv := "1.0.0.0\t1.0.0.255\t13335\tUS\tCLOUDFLARENET\n" +
		strings.Repeat("x", 100*1024) + "\n" +
		"2.0.0.0\t2.0.0.255\t2\tDE\tOK\n"
	g, err := LoadIP2ASN(strings.NewReader(tsv))
	if err != nil {
		t.Fatalf("an overlong line failed the whole load: %v", err)
	}
	for _, hit := range []string{"1.0.0.5", "2.0.0.5"} {
		if _, ok := g.Lookup(addr(hit)); !ok {
			t.Fatalf("a valid line around the overlong one was lost: Lookup(%s) missed", hit)
		}
	}
	if g.Skipped() != 1 {
		t.Fatalf("Skipped() = %d, want 1: the overlong line", g.Skipped())
	}
}

func TestTooManyRangesIsAnError(t *testing.T) {
	old := maxRanges
	maxRanges = 3
	t.Cleanup(func() { maxRanges = old })
	tsv := "1.0.0.0\t1.0.0.1\t1\tUS\tA\n" +
		"2.0.0.0\t2.0.0.1\t2\tUS\tB\n" +
		"3.0.0.0\t3.0.0.1\t3\tUS\tC\n" +
		"4.0.0.0\t4.0.0.1\t4\tUS\tD\n"
	_, err := LoadIP2ASN(strings.NewReader(tsv))
	if err == nil {
		t.Fatal("a table past the range limit loaded without error")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Fatalf("the error does not name the limit: %v", err)
	}
}

func TestGeoRangesMergeAdjacent(t *testing.T) {
	g := load(t, "5.8.0.0\t5.8.0.255\t1\tRU\tA\n"+
		"5.8.1.0\t5.8.1.255\t2\tRU\tB\n"+ // touches the one before: merged
		"5.8.2.0\t5.8.2.255\t9\tUS\tOTHER\n"+ // not asked for: a gap
		"5.8.3.0\t5.8.3.255\t1\tRU\tA\n"+ // after the gap: its own range
		"2a00::\t2a00::ffff\t1\tRU\tA\n"+
		"2a00::1:0\t2a00::1:ffff\t2\tRU\tB\n")
	v4, v6 := g.Ranges(func(i ASInfo) bool { return i.CC == "RU" })
	want4 := []Range{{addr("5.8.0.0"), addr("5.8.1.255")}, {addr("5.8.3.0"), addr("5.8.3.255")}}
	want6 := []Range{{addr("2a00::"), addr("2a00::1:ffff")}}
	if len(v4) != 2 || v4[0] != want4[0] || v4[1] != want4[1] {
		t.Fatalf("v4 = %v, want %v", v4, want4)
	}
	if len(v6) != 1 || v6[0] != want6[0] {
		t.Fatalf("v6 = %v, want %v", v6, want6)
	}
}

func TestGeoRangesOfNothing(t *testing.T) {
	var g *GeoDB
	if v4, v6 := g.Ranges(func(ASInfo) bool { return true }); v4 != nil || v6 != nil {
		t.Fatal("a nil table has no ranges")
	}
	g = load(t, sample)
	if v4, v6 := g.Ranges(func(i ASInfo) bool { return i.CC == "ZZ" }); len(v4)+len(v6) != 0 {
		t.Fatal("no owner matches, no range")
	}
}
