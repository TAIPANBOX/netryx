// Package enrich names the far end of a connection: which country and which
// network a remote address belongs to.
//
// The source is the public-domain iptoasn.com table. It is one line per
// address range, IPv4 and IPv6 mixed, and most ranges share a handful of
// organisations, so the loaded table keeps each distinct answer once and the
// ranges carry only an index into it. That is what lets the whole internet's
// table fit comfortably in a router's memory.
package enrich

import (
	"bufio"
	"cmp"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// MaxRanges bounds the table. Each range costs at most a few dozen bytes once
// loaded, so this keeps a hostile or corrupt file from taking the router's
// memory; the real file has about 580 000.
const MaxRanges = 2_000_000

// maxRanges is MaxRanges as a variable so a test can lower it.
var maxRanges = MaxRanges

// unknownCC is what a caller gets when there is no answer. It is not a valid
// ISO country code, so it cannot be mistaken for one.
const unknownCC = "--"

// ASInfo is who an address belongs to.
type ASInfo struct {
	CC  string
	ASN uint32
	Org string
}

type v4Range struct {
	lo, hi uint32
	info   uint32
}

type v6Range struct {
	lo, hi [16]byte
	info   uint32
}

// GeoDB answers "whose address is this" by binary search over sorted ranges.
// It is built once and only read afterwards, so lookups need no locking.
type GeoDB struct {
	v4      []v4Range
	v6      []v6Range
	infos   []ASInfo
	skipped int
	// pk is set when the table was read from a pack (pack.go); the slices
	// above are then empty.
	pk *geoPack
}

// LoadIP2ASN reads an iptoasn TSV file, gzipped or not. A line that cannot be
// understood is skipped rather than failing the load: the public file is large
// and changes shape now and then, and one odd line should not blind the whole
// monitor. Only a failure to read the input is an error.
func LoadIP2ASN(r io.Reader) (*GeoDB, error) {
	br := bufio.NewReader(r)
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		br = bufio.NewReader(zr)
	}

	g := &GeoDB{}
	intern := make(map[ASInfo]uint32)
	stored := 0
	for {
		line, tooLong, err := readLine(br)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if tooLong {
			g.skipped++
		} else if line != "" {
			start, end, info, ok := parseLine(line)
			if !ok {
				g.skipped++
			} else {
				if stored >= maxRanges {
					return nil, fmt.Errorf("enrich: the table has more than %d ranges, the limit", maxRanges)
				}
				stored++
				idx, seen := intern[info]
				if !seen {
					idx = uint32(len(g.infos))
					g.infos = append(g.infos, info)
					intern[info] = idx
				}
				if start.Is4() {
					g.v4 = append(g.v4, v4Range{lo: addr4(start), hi: addr4(end), info: idx})
				} else {
					g.v6 = append(g.v6, v6Range{lo: start.As16(), hi: end.As16(), info: idx})
				}
			}
		}
		if err == io.EOF {
			break
		}
	}

	// Stable, so two ranges starting at the same address keep file order and
	// the same file always loads the same table.
	slices.SortStableFunc(g.v4, func(a, b v4Range) int { return cmp.Compare(a.lo, b.lo) })
	slices.SortStableFunc(g.v6, func(a, b v6Range) int { return compare16(a.lo, b.lo) })
	var n int
	g.v4, n = dropOverlaps4(g.v4)
	g.skipped += n
	g.v6, n = dropOverlaps6(g.v6)
	g.skipped += n
	return g, nil
}

// readLine returns the next line without its newline. A line too long to hold
// in one buffer is consumed and reported as tooLong, so one bad line cannot
// stop the load and cannot grow memory without bound. err is io.EOF on the
// last line, which may still carry text.
func readLine(br *bufio.Reader) (line string, tooLong bool, err error) {
	b, err := br.ReadSlice('\n')
	for err == bufio.ErrBufferFull {
		tooLong = true
		_, err = br.ReadSlice('\n')
	}
	if tooLong {
		return "", true, err
	}
	return strings.TrimRight(string(b), "\r\n"), false, err
}

// dropOverlaps4 keeps the first of each run of overlapping ranges. The table
// is searched for the one range that starts at or before an address, so two
// ranges that overlap would hide part of the later one; dropping it and
// counting it is the honest result.
func dropOverlaps4(rs []v4Range) ([]v4Range, int) {
	out := rs[:0]
	dropped := 0
	for _, r := range rs {
		if len(out) > 0 && r.lo <= out[len(out)-1].hi {
			dropped++
			continue
		}
		out = append(out, r)
	}
	return out, dropped
}

func dropOverlaps6(rs []v6Range) ([]v6Range, int) {
	out := rs[:0]
	dropped := 0
	for _, r := range rs {
		if len(out) > 0 && compare16(r.lo, out[len(out)-1].hi) <= 0 {
			dropped++
			continue
		}
		out = append(out, r)
	}
	return out, dropped
}

// parseLine reads one "start end asn cc description" line. Anything that does
// not fit is reported as not ok, never half-parsed.
func parseLine(line string) (start, end netip.Addr, info ASInfo, ok bool) {
	f := strings.SplitN(strings.TrimRight(line, "\r\n"), "\t", 5)
	if len(f) != 5 {
		return
	}
	start, err1 := netip.ParseAddr(f[0])
	end, err2 := netip.ParseAddr(f[1])
	if err1 != nil || err2 != nil || start.Is4() != end.Is4() || start.Compare(end) > 0 {
		return
	}
	asn, err := strconv.ParseUint(f[2], 10, 32)
	// AS 0 is how the file marks space that is not routed. Nothing is
	// announced there, so it is no answer at all.
	if err != nil || asn == 0 {
		return
	}
	cc := f[3]
	if len(cc) != 2 {
		cc = unknownCC
	}
	return start, end, ASInfo{CC: cc, ASN: uint32(asn), Org: f[4]}, true
}

// Lookup returns who owns the address. A miss, or a nil database, returns
// ok false, and the info then carries the unknown country code.
//
// It does not allocate: a connection is looked up once per flow, and this is
// on that path.
func (g *GeoDB) Lookup(a netip.Addr) (ASInfo, bool) {
	if g == nil {
		return ASInfo{CC: unknownCC}, false
	}
	// A dual-stack socket reports IPv4 peers in their mapped IPv6 form, and
	// the table holds them as plain IPv4.
	a = a.Unmap()
	if g.pk != nil {
		if a.Is4() {
			return g.pk.lookup4(addr4(a))
		}
		b := a.As16()
		return g.pk.lookup6(&b)
	}
	if a.Is4() {
		x := addr4(a)
		// Find the last range that starts at or before x; it is the only one
		// that can contain it, because ranges do not overlap.
		i := lastAtOrBefore4(g.v4, x)
		if i >= 0 && x <= g.v4[i].hi {
			return g.infos[g.v4[i].info], true
		}
		return ASInfo{CC: unknownCC}, false
	}
	b := a.As16()
	i := lastAtOrBefore6(g.v6, b)
	if i >= 0 && compare16(b, g.v6[i].hi) <= 0 {
		return g.infos[g.v6[i].info], true
	}
	return ASInfo{CC: unknownCC}, false
}

// Skipped is the number of input lines the loader did not use: malformed,
// not routed, overlapping an earlier range, or too long to read.
func (g *GeoDB) Skipped() int {
	if g == nil {
		return 0
	}
	return g.skipped
}

// Len is the number of ranges loaded, IPv4 and IPv6 together.
func (g *GeoDB) Len() int {
	if g == nil {
		return 0
	}
	if g.pk != nil {
		return g.pk.n4 + g.pk.n6
	}
	return len(g.v4) + len(g.v6)
}

func addr4(a netip.Addr) uint32 {
	b := a.As4()
	return binary.BigEndian.Uint32(b[:])
}

func lastAtOrBefore4(rs []v4Range, x uint32) int {
	lo, hi := 0, len(rs)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if rs[mid].lo <= x {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo - 1
}

func lastAtOrBefore6(rs []v6Range, x [16]byte) int {
	lo, hi := 0, len(rs)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if compare16(rs[mid].lo, x) <= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo - 1
}

// compare16 orders 128-bit addresses. Go does not order arrays with <, and
// slicing them for bytes.Compare would be a cost on every lookup.
func compare16(a, b [16]byte) int {
	for i := range a {
		if c := cmp.Compare(a[i], b[i]); c != 0 {
			return c
		}
	}
	return 0
}

// Range is an inclusive address range.
type Range struct{ Lo, Hi netip.Addr }

// Ranges returns the address ranges whose owner satisfies match, in address
// order, IPv4 and IPv6 apart. Ranges that touch or overlap are merged, so a
// country made of many neighbouring networks becomes few intervals.
func (g *GeoDB) Ranges(match func(ASInfo) bool) (v4, v6 []Range) {
	if g == nil || match == nil {
		return nil, nil
	}
	if g.pk != nil {
		return g.pk.ranges(match)
	}
	for _, r := range g.v4 {
		if match(g.infos[r.info]) {
			v4 = addRange(v4, Range{netip.AddrFrom4(u32bytes(r.lo)), netip.AddrFrom4(u32bytes(r.hi))})
		}
	}
	for _, r := range g.v6 {
		if match(g.infos[r.info]) {
			v6 = addRange(v6, Range{netip.AddrFrom16(r.lo), netip.AddrFrom16(r.hi)})
		}
	}
	return v4, v6
}

// addRange appends r, or widens the last range when r touches or overlaps it.
// The input is in address order, so only the last range can be affected.
func addRange(out []Range, r Range) []Range {
	if n := len(out); n > 0 {
		last := &out[n-1]
		if r.Lo.Compare(last.Hi) <= 0 || last.Hi.Next() == r.Lo {
			if r.Hi.Compare(last.Hi) > 0 {
				last.Hi = r.Hi
			}
			return out
		}
	}
	return append(out, r)
}

func u32bytes(x uint32) [4]byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], x)
	return b
}
