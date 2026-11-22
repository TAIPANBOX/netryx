package rollup

import (
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
	"time"

	"netryx/internal/flows"
)

// Pairs answers in one pass what Top would answer once per key: for every
// app (or country), the devices that moved its bytes, ranked as Top ranks
// them. The house-wide Apps and Destinations lists read it, so they never
// scan the history once per row.
func TestPairsAgreeWithTopForEveryKey(t *testing.T) {
	macs := []string{phone, laptop, "tv", "nas", "other"}
	apps := []string{"youtube", "netflix", "google", "unknown"}
	ccs := []string{"GB", "US", "--", "LAN"}
	for seed := uint64(1); seed <= 100; seed++ {
		r := rand.New(rand.NewPCG(seed, seed*5+3))
		s := newTestStore(Options{MaxKeysPerHour: 40}, later)
		var ds []flows.Delta
		for i := 0; i < 200; i++ {
			ds = append(ds, delta(base.Add(time.Duration(r.Int64N(int64(3*time.Hour)))),
				macs[r.IntN(len(macs))], apps[r.IntN(len(apps))], ccs[r.IntN(len(ccs))],
				r.Uint64N(1<<16), r.Uint64N(1<<20)))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i].At.Before(ds[j].At) })
		for _, d := range ds {
			feed(s, d)
		}
		end := base.Add(3*time.Hour - time.Second)
		for _, from := range []time.Time{ds[0].At, base, longAgo} {
			for _, dim := range []string{"app", "cc"} {
				got := s.Pairs(Filter{}, dim, "mac", from, end)
				keys := s.Top(Filter{}, dim, from, end, 0)
				if len(got) != len(keys) {
					t.Fatalf("seed %d, %s from %v: %d keys, Top has %d", seed, dim, from, len(got), len(keys))
				}
				for _, k := range keys {
					f := Filter{}
					if dim == "app" {
						f.App = k.Key
					} else {
						f.CC = k.Key
					}
					want := s.Top(f, "mac", from, end, 0)
					if !reflect.DeepEqual(got[k.Key], want) {
						t.Fatalf("seed %d, %s %q from %v:\n got %+v\nwant %+v", seed, dim, k.Key, from, got[k.Key], want)
					}
				}
			}
		}
	}
}

// The filter narrows the pass as it narrows Top: the house's own traffic
// left out of the countries, one device's apps only.
func TestPairsHonourTheFilter(t *testing.T) {
	s := newTestStore(Options{}, later)
	feed(s, delta(base, phone, "youtube", "US", 1, 10))
	feed(s, delta(base, laptop, "youtube", "LAN", 1, 20))
	feed(s, delta(base, laptop, "netflix", "GB", 1, 30))
	got := s.Pairs(Filter{NotCC: "LAN"}, "cc", "mac", base, base.Add(time.Hour))
	if _, ok := got["LAN"]; ok || len(got) != 2 {
		t.Fatalf("NotCC LAN: %+v", got)
	}
	got = s.Pairs(Filter{MAC: laptop}, "app", "mac", base, base.Add(time.Hour))
	if len(got) != 2 || len(got["youtube"]) != 1 || got["youtube"][0].Key != laptop {
		t.Fatalf("MAC laptop: %+v", got)
	}
}

// Only the dimensions every key carries can be paired; anything else is no
// answer, never a wrong one.
func TestPairsRefuseDimensionsKeysDoNotCarry(t *testing.T) {
	s := newTestStore(Options{}, later)
	feed(s, delta(base, phone, "youtube", "US", 1, 10))
	for _, c := range [][2]string{{"asn", "mac"}, {"app", "domain"}, {"app", "app"}, {"", "mac"}} {
		if got := s.Pairs(Filter{}, c[0], c[1], base, base.Add(time.Hour)); got != nil {
			t.Errorf("Pairs(%q, %q) = %+v, want nil", c[0], c[1], got)
		}
	}
}
