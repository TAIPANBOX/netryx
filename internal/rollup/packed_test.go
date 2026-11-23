package rollup

import (
	"fmt"
	"maps"
	"reflect"
	"testing"
	"time"
)

func cloneMargins[K comparable](ms map[string]*margin[K]) map[string]*margin[K] {
	out := map[string]*margin[K]{}
	for mac, m := range ms {
		out[mac] = &margin[K]{Rows: maps.Clone(m.Rows), Other: m.Other}
	}
	return out
}

// A closed hour is packed, and the packed hour holds every key, row, pushed
// out total, organisation and naming source its maps held: read back in maps
// it is the same bucket, and the domains still say how they were named.
func TestAPackedBucketHoldsEverythingItsMapsHeld(t *testing.T) {
	s := newTestStore(Options{TopPerDevice: 3}, later)
	srcs := []string{"sni", "dns", "quic", ""}
	for i := 0; i < 40; i++ {
		d := delta(base.Add(time.Duration(i)*time.Second), []string{phone, laptop}[i%2], fmt.Sprintf("app%d", i%5), []string{"GB", "US", "DE"}[i%3], uint64(i), uint64(1000+i*i))
		d.First = i%4 == 0
		d.Domain = fmt.Sprintf("d%d.example", i%7)
		d.NameSrc = srcs[i%4]
		d.ASN, d.Org = uint32(64500+i%6), fmt.Sprintf("ORG-%d", i%6)
		feed(s, d)
	}
	h := s.hourAt(base)
	if h.pk != nil {
		t.Fatal("the open hour is packed")
	}
	want := &bucket{Keys: maps.Clone(h.Keys), ASN: cloneMargins(h.ASN), Dom: cloneMargins(h.Dom)}
	wantTop := s.Top(Filter{}, "domain", base, base, 0)
	if len(wantTop) < 4 {
		t.Fatalf("too few domain rows to tell anything: %+v", wantTop)
	}

	// The hour closes; the next delta packs it.
	feed(s, delta(base.Add(2*time.Hour), phone, "x", "GB", 1, 1))
	if h.pk == nil || h.Keys != nil || h.ASN != nil || h.Dom != nil {
		t.Fatal("the closed hour is not packed")
	}
	got := h.expanded()
	if !reflect.DeepEqual(got.Keys, want.Keys) {
		t.Fatalf("keys after packing:\n%v\nwant\n%v", got.Keys, want.Keys)
	}
	for mac, m := range want.ASN {
		if g := got.ASN[mac]; g == nil || !reflect.DeepEqual(*g, *m) {
			t.Fatalf("organisations of %s after packing:\n%+v\nwant\n%+v", mac, g, m)
		}
	}
	for mac, m := range want.Dom {
		if g := got.Dom[mac]; g == nil || !reflect.DeepEqual(*g, *m) {
			t.Fatalf("domains of %s after packing:\n%+v\nwant\n%+v", mac, g, m)
		}
	}
	if len(got.ASN) != len(want.ASN) || len(got.Dom) != len(want.Dom) {
		t.Fatalf("devices after packing: %d and %d, want %d and %d", len(got.ASN), len(got.Dom), len(want.ASN), len(want.Dom))
	}
	if top := s.Top(Filter{}, "domain", base, base, 0); !sameRows(top, wantTop) {
		t.Fatalf("the domains of the packed hour:\n%+v\nwant\n%+v", top, wantTop)
	}

	// A packed hour that takes more (a clock stepped back) is unpacked and
	// counts on from what it held.
	addAt(s, base.Add(30*time.Minute), delta(base.Add(30*time.Minute), phone, "app0", "GB", 5, 5))
	if h.pk != nil {
		t.Fatal("the hour that took a delta is still packed")
	}
	k := Key{phone, "app0", "GB"}
	if h.Keys[k] != add(want.Keys[k], Bytes{Up: 5, Down: 5}) {
		t.Fatalf("the unpacked hour counts on: %+v, held %+v", h.Keys[k], want.Keys[k])
	}
}
