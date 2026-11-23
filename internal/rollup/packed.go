package rollup

// A closed hour or day takes no delta, and there are 198 of them in memory,
// so once it closes it is kept packed: its keys and marginal rows as varints
// in three byte slices, each string as its number in the bucket's own table
// of the (interned) strings it uses. A map entry costs a hundred bytes and
// more; a packed row costs about a dozen. The open bucket stays in maps,
// and a packed one that has to take more (a merge from disk, a clock
// stepped back) is unpacked first. Readers go through keys, asnMargins and
// domMargins, which read either form; files are written from the maps, so
// what is on flash does not change.

import (
	"encoding/binary"
	"iter"
	"maps"
)

// packed is a closed bucket's content. keys holds per key its MAC, app and
// country, then up, down and flows. asn and dom hold per device its MAC,
// what it pushed out (up, down, flows) and the byte length of its rows, then
// the rows: an organisation row is app, country, AS number and organisation,
// a domain row app, domain and source, each followed by up, down and flows.
type packed struct {
	strs []string
	keys []byte
	asn  []byte
	dom  []byte
}

// strTable numbers the strings of one bucket as it is packed.
type strTable struct {
	idx  map[string]uint64
	strs []string
}

func (t *strTable) ref(b []byte, x string) []byte {
	i, ok := t.idx[x]
	if !ok {
		i = uint64(len(t.strs))
		t.idx[x] = i
		t.strs = append(t.strs, x)
	}
	return binary.AppendUvarint(b, i)
}

func putNum(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

func putBytes(b []byte, v Bytes) []byte {
	return putNum(putNum(putNum(b, v.Up), v.Down), v.Flows)
}

// exactly returns b in memory of its own length, nil when empty.
func exactly[T any](b []T) []T {
	if len(b) == 0 {
		return nil
	}
	out := make([]T, len(b))
	copy(out, b)
	return out
}

func putASN(t *strTable, b []byte, k asnKey, c cell) []byte {
	b = t.ref(b, k.App)
	b = t.ref(b, k.CC)
	b = putNum(b, uint64(k.ASN))
	b = t.ref(b, c.Org)
	return putBytes(b, c.B)
}

func putDom(t *strTable, b []byte, k domKey, c cell) []byte {
	b = t.ref(b, k.App)
	b = t.ref(b, k.Domain)
	b = t.ref(b, c.Src)
	return putBytes(b, c.B)
}

func packMargins[K comparable](t *strTable, ms map[string]*margin[K], put func(*strTable, []byte, K, cell) []byte) []byte {
	var out, rows []byte
	for mac, m := range ms {
		if m == nil {
			continue
		}
		rows = rows[:0]
		for k, c := range m.Rows {
			rows = put(t, rows, k, c)
		}
		out = t.ref(out, mac)
		out = putBytes(out, m.Other)
		out = putNum(out, uint64(len(rows)))
		out = append(out, rows...)
	}
	return exactly(out)
}

// pack is the packed form of a bucket in maps.
func pack(bk *bucket) *packed {
	t := &strTable{idx: map[string]uint64{}}
	var keys []byte
	for k, b := range bk.Keys {
		keys = t.ref(keys, k.MAC)
		keys = t.ref(keys, k.App)
		keys = t.ref(keys, k.CC)
		keys = putBytes(keys, b)
	}
	pk := &packed{keys: exactly(keys)}
	pk.asn = packMargins(t, bk.ASN, putASN)
	pk.dom = packMargins(t, bk.Dom, putDom)
	pk.strs = exactly(t.strs)
	return pk
}

// unpacker reads what pack wrote; it never sees anything else.
type unpacker struct {
	b    []byte
	strs []string
}

func (u *unpacker) num() uint64 {
	v, n := binary.Uvarint(u.b)
	u.b = u.b[n:]
	return v
}

func (u *unpacker) str() string { return u.strs[u.num()] }

func (u *unpacker) bytes() Bytes {
	var b Bytes
	b.Up = u.num()
	b.Down = u.num()
	b.Flows = u.num()
	return b
}

func getASN(u *unpacker) (asnKey, cell) {
	var k asnKey
	var c cell
	k.App = u.str()
	k.CC = u.str()
	k.ASN = uint32(u.num())
	c.Org = u.str()
	c.B = u.bytes()
	return k, c
}

func getDom(u *unpacker) (domKey, cell) {
	var k domKey
	var c cell
	k.App = u.str()
	k.Domain = u.str()
	c.Src = u.str()
	c.B = u.bytes()
	return k, c
}

// marginOf is one device's rows of a marginal, in whichever form.
type marginOf[K comparable] struct {
	Other Bytes
	Rows  iter.Seq2[K, cell]
}

// keys is every key of the bucket and what it moved.
func (bk *bucket) keys() iter.Seq2[Key, Bytes] {
	if bk.pk == nil {
		return maps.All(bk.Keys)
	}
	pk := bk.pk
	return func(yield func(Key, Bytes) bool) {
		u := unpacker{pk.keys, pk.strs}
		for len(u.b) > 0 {
			var k Key
			k.MAC = u.str()
			k.App = u.str()
			k.CC = u.str()
			if !yield(k, u.bytes()) {
				return
			}
		}
	}
}

// asnMargins and domMargins are the devices of a marginal and their rows.
func (bk *bucket) asnMargins() iter.Seq2[string, marginOf[asnKey]] {
	if bk.pk == nil {
		return mapMargins(bk.ASN)
	}
	return packedMargins(bk.pk.asn, bk.pk.strs, getASN)
}

func (bk *bucket) domMargins() iter.Seq2[string, marginOf[domKey]] {
	if bk.pk == nil {
		return mapMargins(bk.Dom)
	}
	return packedMargins(bk.pk.dom, bk.pk.strs, getDom)
}

func mapMargins[K comparable](ms map[string]*margin[K]) iter.Seq2[string, marginOf[K]] {
	return func(yield func(string, marginOf[K]) bool) {
		for mac, m := range ms {
			if m == nil {
				continue
			}
			if !yield(mac, marginOf[K]{Other: m.Other, Rows: maps.All(m.Rows)}) {
				return
			}
		}
	}
}

func packedMargins[K comparable](data []byte, strs []string, get func(*unpacker) (K, cell)) iter.Seq2[string, marginOf[K]] {
	return func(yield func(string, marginOf[K]) bool) {
		u := unpacker{data, strs}
		for len(u.b) > 0 {
			mac := u.str()
			other := u.bytes()
			n := u.num()
			seg := u.b[:n]
			u.b = u.b[n:]
			rows := func(y func(K, cell) bool) {
				r := unpacker{seg, strs}
				for len(r.b) > 0 {
					if !y(get(&r)) {
						return
					}
				}
			}
			if !yield(mac, marginOf[K]{Other: other, Rows: rows}) {
				return
			}
		}
	}
}

func expandMargins[K comparable](it iter.Seq2[string, marginOf[K]]) map[string]*margin[K] {
	out := map[string]*margin[K]{}
	for mac, m := range it {
		rows := map[K]cell{}
		for k, c := range m.Rows {
			rows[k] = c
		}
		out[mac] = &margin[K]{Rows: rows, Other: m.Other}
	}
	return out
}

// expanded is the bucket in maps: itself when it is not packed, otherwise a
// copy, so a reader under the read lock never changes it.
func (bk *bucket) expanded() *bucket {
	if bk.pk == nil {
		return bk
	}
	e := &bucket{At: bk.At, Keys: map[Key]Bytes{}, asnRows: bk.asnRows, domRows: bk.domRows, compact: bk.compact}
	for k, b := range bk.keys() {
		e.Keys[k] = b
	}
	e.ASN = expandMargins(bk.asnMargins())
	e.Dom = expandMargins(bk.domMargins())
	return e
}

// unpack puts a packed bucket back in maps, so it can take more.
func (bk *bucket) unpack() {
	if bk.pk == nil {
		return
	}
	e := bk.expanded()
	bk.Keys, bk.ASN, bk.Dom, bk.pk = e.Keys, e.ASN, e.Dom, nil
}
