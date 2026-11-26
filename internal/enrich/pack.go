package enrich

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"os"
	"strings"
	"unsafe"

	"netryx/internal/packfile"
)

// The address table as a pack (see internal/packfile), built by tools/datapack
// from the iptoasn file when the package is built. Its body, all integers
// little-endian:
//
//	n4, n6, nAS, blobLen, skipped, 0     six uint32
//	n4 IPv4 ranges                        lo, hi, info: three uint32
//	n6 IPv6 ranges                        lo, hi: 16 bytes each, as on the wire; info: uint32
//	nAS answers                           asn uint32, org offset uint32, org length uint16, country 2 bytes
//	blobLen bytes of organisation names
//
// The ranges are sorted and do not overlap, exactly as LoadIP2ASN leaves them,
// so a lookup is the same binary search over the mapped bytes.
const (
	// GeoMagic begins every address table pack.
	GeoMagic = "NXIP2ASN"
	// GeoVersion is the layout this netryx reads and writes.
	GeoVersion = 1

	geoHead = 6 * 4
	rec4    = 12
	rec6    = 36
	recAS   = 12
)

// geoPack is the address table read from a pack: slices of the mapped file.
type geoPack struct {
	v4, v6, as, blob []byte
	n4, n6           int
	mapped           bool
}

// MarshalPack writes the table loaded from the iptoasn file as a pack. The
// answers keep their indices, and each distinct organisation name is stored
// once.
func (g *GeoDB) MarshalPack() ([]byte, error) {
	if g == nil || g.pk != nil {
		return nil, errors.New("enrich: only a table loaded from the iptoasn file is written as a pack")
	}
	// Compared as uint64: on a 32-bit router an int cannot hold MaxUint32, and
	// the constant alone would not compile there.
	if uint64(g.skipped) > math.MaxUint32 || uint64(len(g.infos)) > math.MaxUint32 {
		return nil, errors.New("enrich: the table is too large for a pack")
	}
	var blob []byte
	orgAt := map[string]uint32{}
	as := make([]byte, 0, len(g.infos)*recAS)
	for _, in := range g.infos {
		if len(in.CC) != 2 {
			return nil, fmt.Errorf("enrich: country %q is not two bytes", in.CC)
		}
		if len(in.Org) > math.MaxUint16 {
			return nil, fmt.Errorf("enrich: an organisation name of %d bytes is too long for a pack", len(in.Org))
		}
		off, seen := orgAt[in.Org]
		if !seen {
			if uint64(len(blob))+uint64(len(in.Org)) > math.MaxUint32 {
				return nil, errors.New("enrich: the organisation names are too large for a pack")
			}
			off = uint32(len(blob))
			blob = append(blob, in.Org...)
			orgAt[in.Org] = off
		}
		as = binary.LittleEndian.AppendUint32(as, in.ASN)
		as = binary.LittleEndian.AppendUint32(as, off)
		as = binary.LittleEndian.AppendUint16(as, uint16(len(in.Org)))
		as = append(as, in.CC...)
	}
	body := make([]byte, 0, geoHead+len(g.v4)*rec4+len(g.v6)*rec6+len(as)+len(blob))
	for _, n := range []int{len(g.v4), len(g.v6), len(g.infos), len(blob), g.skipped, 0} {
		body = binary.LittleEndian.AppendUint32(body, uint32(n))
	}
	for _, r := range g.v4 {
		body = binary.LittleEndian.AppendUint32(body, r.lo)
		body = binary.LittleEndian.AppendUint32(body, r.hi)
		body = binary.LittleEndian.AppendUint32(body, r.info)
	}
	for _, r := range g.v6 {
		body = append(body, r.lo[:]...)
		body = append(body, r.hi[:]...)
		body = binary.LittleEndian.AppendUint32(body, r.info)
	}
	body = append(body, as...)
	body = append(body, blob...)
	return packfile.Seal(GeoMagic, GeoVersion, body), nil
}

// OpenGeo opens the address table at path. A pack (tools/datapack) is read
// through a read-only mapping and costs no heap; an iptoasn TSV, gzipped or
// not, which an install configured before the packs still names, is loaded the
// old way. A path ending in .pack must be a pack: a damaged one is refused,
// never read as text.
func OpenGeo(path string) (*GeoDB, error) {
	if strings.HasSuffix(path, ".pack") || packfile.HasMagic(path, GeoMagic) {
		return OpenGeoPack(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return LoadIP2ASN(f)
}

// OpenGeoPack maps the pack at path and checks it whole before it answers.
func OpenGeoPack(path string) (*GeoDB, error) {
	b, mapped, err := packfile.Open(path)
	if err != nil {
		return nil, err
	}
	g, err := ReadGeoPack(b)
	if err != nil {
		packfile.Release(b, mapped)
		return nil, fmt.Errorf("address table pack %s: %w", path, err)
	}
	g.pk.mapped = mapped
	return g, nil
}

// ReadGeoPack reads a pack held in b, which must not change afterwards: the
// table and the strings it hands out are slices of it. Every count, offset,
// index and the order of the ranges is checked first, so a lookup never reads
// outside b and never answers from a table LoadIP2ASN would not have built.
func ReadGeoPack(b []byte) (*GeoDB, error) {
	body, err := packfile.Check(b, GeoMagic, GeoVersion)
	if err != nil {
		return nil, err
	}
	if len(body) < geoHead {
		return nil, errors.New("the header is cut short")
	}
	u := func(i int) uint64 { return uint64(binary.LittleEndian.Uint32(body[i*4:])) }
	n4, n6, nAS, blobLen, skipped, zero := u(0), u(1), u(2), u(3), u(4), u(5)
	switch {
	case zero != 0:
		return nil, errors.New("a reserved header field is not zero")
	case n4+n6 > MaxRanges:
		return nil, fmt.Errorf("%d ranges, over the limit of %d", n4+n6, MaxRanges)
	case nAS > MaxRanges:
		return nil, fmt.Errorf("%d answers, over the limit of %d", nAS, MaxRanges)
	case (n4 > 0 || n6 > 0) && nAS == 0:
		return nil, errors.New("ranges but no answers")
	}
	want := geoHead + n4*rec4 + n6*rec6 + nAS*recAS + blobLen
	if want != uint64(len(body)) {
		return nil, fmt.Errorf("the counts describe %d bytes but the body is %d", want, len(body))
	}
	p := &geoPack{n4: int(n4), n6: int(n6)}
	at := geoHead
	cut := func(n uint64) []byte { s := body[at : at+int(n)]; at += int(n); return s }
	p.v4, p.v6, p.as, p.blob = cut(n4*rec4), cut(n6*rec6), cut(nAS*recAS), cut(blobLen)

	for i := 0; i < p.n4; i++ {
		lo, hi, idx := p.r4(i)
		if lo > hi {
			return nil, fmt.Errorf("IPv4 range %d ends before it starts", i)
		}
		if uint64(idx) >= nAS {
			return nil, fmt.Errorf("IPv4 range %d names answer %d of %d", i, idx, nAS)
		}
		if i > 0 {
			if _, prevHi, _ := p.r4(i - 1); lo <= prevHi {
				return nil, fmt.Errorf("IPv4 range %d is out of order or overlaps the one before", i)
			}
		}
	}
	for i := 0; i < p.n6; i++ {
		r := p.v6[i*rec6:]
		if bytes.Compare(r[:16], r[16:32]) > 0 {
			return nil, fmt.Errorf("IPv6 range %d ends before it starts", i)
		}
		if idx := uint64(binary.LittleEndian.Uint32(r[32:])); idx >= nAS {
			return nil, fmt.Errorf("IPv6 range %d names answer %d of %d", i, idx, nAS)
		}
		if i > 0 && bytes.Compare(r[:16], p.v6[(i-1)*rec6+16:(i-1)*rec6+32]) <= 0 {
			return nil, fmt.Errorf("IPv6 range %d is out of order or overlaps the one before", i)
		}
	}
	for i := uint64(0); i < nAS; i++ {
		r := p.as[i*recAS:]
		off := uint64(binary.LittleEndian.Uint32(r[4:]))
		n := uint64(binary.LittleEndian.Uint16(r[8:]))
		if off+n > blobLen {
			return nil, fmt.Errorf("answer %d names bytes %d to %d of %d names", i, off, off+n, blobLen)
		}
	}
	return &GeoDB{pk: p, skipped: int(skipped)}, nil
}

// Mapped reports whether the table is read through a mapping of its file.
func (g *GeoDB) Mapped() bool { return g != nil && g.pk != nil && g.pk.mapped }

func (p *geoPack) r4(i int) (lo, hi, idx uint32) {
	r := p.v4[i*rec4 : i*rec4+rec4]
	return binary.LittleEndian.Uint32(r), binary.LittleEndian.Uint32(r[4:]), binary.LittleEndian.Uint32(r[8:])
}

// info is answer i. Its strings point into the pack, which is never unmapped,
// so building them allocates nothing.
func (p *geoPack) info(i uint32) ASInfo {
	r := p.as[int(i)*recAS : int(i)*recAS+recAS]
	n := int(binary.LittleEndian.Uint16(r[8:]))
	org := ""
	if n > 0 {
		org = unsafe.String(&p.blob[binary.LittleEndian.Uint32(r[4:])], n)
	}
	return ASInfo{CC: unsafe.String(&r[10], 2), ASN: binary.LittleEndian.Uint32(r), Org: org}
}

func (p *geoPack) lookup4(x uint32) (ASInfo, bool) {
	lo, hi := 0, p.n4
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if start, _, _ := p.r4(mid); start <= x {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if i := lo - 1; i >= 0 {
		if _, end, idx := p.r4(i); x <= end {
			return p.info(idx), true
		}
	}
	return ASInfo{CC: unknownCC}, false
}

func (p *geoPack) lookup6(x *[16]byte) (ASInfo, bool) {
	lo, hi := 0, p.n6
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if bytes.Compare(p.v6[mid*rec6:mid*rec6+16], x[:]) <= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if i := lo - 1; i >= 0 {
		r := p.v6[i*rec6 : i*rec6+rec6]
		if bytes.Compare(x[:], r[16:32]) <= 0 {
			return p.info(binary.LittleEndian.Uint32(r[32:])), true
		}
	}
	return ASInfo{CC: unknownCC}, false
}

func (p *geoPack) ranges(match func(ASInfo) bool) (v4, v6 []Range) {
	for i := 0; i < p.n4; i++ {
		lo, hi, idx := p.r4(i)
		if match(p.info(idx)) {
			v4 = addRange(v4, Range{netip.AddrFrom4(u32bytes(lo)), netip.AddrFrom4(u32bytes(hi))})
		}
	}
	for i := 0; i < p.n6; i++ {
		r := p.v6[i*rec6 : i*rec6+rec6]
		if match(p.info(binary.LittleEndian.Uint32(r[32:]))) {
			var lo, hi [16]byte
			copy(lo[:], r[:16])
			copy(hi[:], r[16:32])
			v6 = addRange(v6, Range{netip.AddrFrom16(lo), netip.AddrFrom16(hi)})
		}
	}
	return v4, v6
}

// SamePack reports how a pack differs from the table it was written from:
// every range, its bounds and its answer, and the count of skipped lines. The
// converter runs it before a pack replaces the one beside it.
func SamePack(src, pack *GeoDB) error {
	if src == nil || src.pk != nil || pack == nil || pack.pk == nil {
		return errors.New("enrich: SamePack compares a loaded table with a pack")
	}
	p := pack.pk
	if len(src.v4) != p.n4 || len(src.v6) != p.n6 {
		return fmt.Errorf("%d+%d ranges in the table, %d+%d in the pack", len(src.v4), len(src.v6), p.n4, p.n6)
	}
	if src.skipped != pack.skipped {
		return fmt.Errorf("%d lines skipped by the table, %d by the pack", src.skipped, pack.skipped)
	}
	for i, r := range src.v4 {
		lo, hi, idx := p.r4(i)
		if lo != r.lo || hi != r.hi || p.info(idx) != src.infos[r.info] {
			return fmt.Errorf("IPv4 range %d differs", i)
		}
	}
	for i, r := range src.v6 {
		b := p.v6[i*rec6 : i*rec6+rec6]
		if !bytes.Equal(b[:16], r.lo[:]) || !bytes.Equal(b[16:32], r.hi[:]) ||
			p.info(binary.LittleEndian.Uint32(b[32:])) != src.infos[r.info] {
			return fmt.Errorf("IPv6 range %d differs", i)
		}
	}
	return nil
}
