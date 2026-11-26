package inventory

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"unsafe"

	"netryx/internal/packfile"
)

// The vendor list as a pack (see internal/packfile), built by tools/datapack
// from oui.txt when the package is built. Its body, all integers
// little-endian:
//
//	n, blobLen                       two uint32
//	n registrations, by prefix       prefix uint32 (24 bits), name offset uint32, name length uint32
//	blobLen bytes of vendor names
const (
	// OUIMagic begins every vendor list pack.
	OUIMagic = "NXOUIVND"
	// OUIVersion is the layout this netryx reads and writes.
	OUIVersion = 1

	ouiHead = 2 * 4
	recOUI  = 12
)

// OUIPack is the vendor list read from a pack: a slice of the mapped file.
type OUIPack struct {
	recs, blob []byte
	n          int
	mapped     bool
}

// MarshalOUIPack writes the vendor list LoadOUI read as a pack. Every key must
// be six hex digits, as every registration in the IEEE file is; one that is
// not cannot be written and fails the build rather than vanish.
func MarshalOUIPack(m OUIMap) ([]byte, error) {
	if len(m) > maxOUI {
		return nil, fmt.Errorf("oui: more than %d registrations", maxOUI)
	}
	type reg struct {
		prefix uint32
		name   string
	}
	regs := make([]reg, 0, len(m))
	for k, v := range m {
		p, ok := parseOUI(k)
		if !ok {
			return nil, fmt.Errorf("oui: registration %q is not six upper-case hex digits", k)
		}
		regs = append(regs, reg{p, v})
	}
	slices.SortFunc(regs, func(a, b reg) int { return int(a.prefix) - int(b.prefix) })
	var blob []byte
	body := binary.LittleEndian.AppendUint32(nil, uint32(len(regs)))
	total := 0
	for _, r := range regs {
		total += len(r.name)
	}
	if uint64(total) > math.MaxUint32 { // uint64: 32-bit targets cannot hold the constant in an int
		return nil, errors.New("oui: the vendor names are too large for a pack")
	}
	body = binary.LittleEndian.AppendUint32(body, uint32(total))
	for _, r := range regs {
		body = binary.LittleEndian.AppendUint32(body, r.prefix)
		body = binary.LittleEndian.AppendUint32(body, uint32(len(blob)))
		body = binary.LittleEndian.AppendUint32(body, uint32(len(r.name)))
		blob = append(blob, r.name...)
	}
	body = append(body, blob...)
	return packfile.Seal(OUIMagic, OUIVersion, body), nil
}

// OpenOUI opens the vendor list at path. A pack (tools/datapack) is read
// through a read-only mapping and costs no heap; oui.txt, which an install
// configured before the packs still names, is loaded the old way. A path
// ending in .pack must be a pack: a damaged one is refused, never read as
// text.
func OpenOUI(path string) (Vendors, error) {
	if strings.HasSuffix(path, ".pack") || packfile.HasMagic(path, OUIMagic) {
		p, err := OpenOUIPack(path)
		if err != nil {
			return nil, err
		}
		return p, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m, err := LoadOUI(f)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// OpenOUIPack maps the pack at path and checks it whole before it answers.
func OpenOUIPack(path string) (*OUIPack, error) {
	b, mapped, err := packfile.Open(path)
	if err != nil {
		return nil, err
	}
	p, err := ReadOUIPack(b)
	if err != nil {
		packfile.Release(b, mapped)
		return nil, fmt.Errorf("vendor list pack %s: %w", path, err)
	}
	p.mapped = mapped
	return p, nil
}

// ReadOUIPack reads a pack held in b, which must not change afterwards. Every
// count and offset and the order of the prefixes is checked first.
func ReadOUIPack(b []byte) (*OUIPack, error) {
	body, err := packfile.Check(b, OUIMagic, OUIVersion)
	if err != nil {
		return nil, err
	}
	if len(body) < ouiHead {
		return nil, errors.New("the header is cut short")
	}
	n := uint64(binary.LittleEndian.Uint32(body))
	blobLen := uint64(binary.LittleEndian.Uint32(body[4:]))
	if n > maxOUI {
		return nil, fmt.Errorf("%d registrations, over the limit of %d", n, maxOUI)
	}
	if want := ouiHead + n*recOUI + blobLen; want != uint64(len(body)) {
		return nil, fmt.Errorf("the counts describe %d bytes but the body is %d", want, len(body))
	}
	p := &OUIPack{n: int(n), recs: body[ouiHead : ouiHead+n*recOUI], blob: body[ouiHead+n*recOUI:]}
	for i := 0; i < p.n; i++ {
		prefix, off, ln := p.rec(i)
		if prefix >= 1<<24 {
			return nil, fmt.Errorf("registration %d has a prefix wider than 24 bits", i)
		}
		if i > 0 {
			if prev, _, _ := p.rec(i - 1); prefix <= prev {
				return nil, fmt.Errorf("registration %d is out of order or repeated", i)
			}
		}
		if uint64(off)+uint64(ln) > blobLen {
			return nil, fmt.Errorf("registration %d names bytes %d to %d of %d", i, off, uint64(off)+uint64(ln), blobLen)
		}
	}
	return p, nil
}

// Len is the number of registrations.
func (p *OUIPack) Len() int {
	if p == nil {
		return 0
	}
	return p.n
}

// Mapped reports whether the list is read through a mapping of its file.
func (p *OUIPack) Mapped() bool { return p != nil && p.mapped }

func (p *OUIPack) rec(i int) (prefix, off, ln uint32) {
	r := p.recs[i*recOUI : i*recOUI+recOUI]
	return binary.LittleEndian.Uint32(r), binary.LittleEndian.Uint32(r[4:]), binary.LittleEndian.Uint32(r[8:])
}

// Vendor answers as the map LoadOUI builds would: only six upper-case hex
// digits can name a registration. The name points into the pack, which is
// never unmapped, so the answer allocates nothing.
func (p *OUIPack) Vendor(oui string) string {
	if p == nil {
		return ""
	}
	x, ok := parseOUI(oui)
	if !ok {
		return ""
	}
	lo, hi := 0, p.n
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if prefix, _, _ := p.rec(mid); prefix < x {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < p.n {
		if prefix, off, ln := p.rec(lo); prefix == x {
			if ln == 0 {
				return ""
			}
			return unsafe.String(&p.blob[off], int(ln))
		}
	}
	return ""
}

// Prefixes lists every registration's six hex digits, in order. It is for
// tests and the converter's checks, not for the service.
func (p *OUIPack) Prefixes() []string {
	out := make([]string, 0, p.Len())
	for i := 0; i < p.Len(); i++ {
		prefix, _, _ := p.rec(i)
		out = append(out, fmt.Sprintf("%06X", prefix))
	}
	return out
}

// parseOUI reads six upper-case hex digits, the form LoadOUI keys its map by.
func parseOUI(s string) (uint32, bool) {
	if len(s) != 6 {
		return 0, false
	}
	for i := 0; i < 6; i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'A' <= c && c <= 'F') {
			return 0, false
		}
	}
	x, err := strconv.ParseUint(s, 16, 32)
	return uint32(x), err == nil
}
