package rollup

import (
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"netryx/internal/flows"
)

// Holders answers in one pass who reached one network or asked for one
// domain, by device, by app and (a network) by country: what Top would give
// for each device and each app alone. The network and domain pages read it,
// so they never scan the history once per client or app.
func TestHoldersAgreeWithTopPerDeviceAndApp(t *testing.T) {
	macs := []string{phone, laptop, "aa:bb:cc:dd:ee:03"}
	apps := []string{"youtube", "netflix", "google"}
	ccs := []string{"US", "DE"}
	doms := []string{"a.example", "b.example", "c.example"}
	for seed := uint64(1); seed <= 60; seed++ {
		r := rand.New(rand.NewPCG(seed, seed*3+7))
		s := newTestStore(Options{}, later)
		var ds []flows.Delta
		for i := 0; i < 150; i++ {
			ds = append(ds, flows.Delta{
				At: base.Add(time.Duration(r.Int64N(int64(3 * time.Hour)))), MAC: macs[r.IntN(3)], App: apps[r.IntN(3)],
				CC: ccs[r.IntN(2)], Domain: doms[r.IntN(3)], ASN: uint32(100 + r.IntN(4)), Org: "ORG",
				Up: r.Uint64N(1 << 12), Down: r.Uint64N(1 << 16),
			})
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i].At.Before(ds[j].At) })
		for _, d := range ds {
			feed(s, d)
		}
		end := base.Add(3*time.Hour - time.Second)
		for _, c := range []struct{ dim, key string }{{"asn", "101"}, {"domain", "b.example"}} {
			h := s.Holders(c.dim, c.key, base, end)
			sum := func(rows []Row) Bytes {
				var b Bytes
				for _, x := range rows {
					if strings.EqualFold(x.Key, c.key) {
						b = add(b, x.Bytes)
					}
				}
				return b
			}
			for _, mac := range macs {
				want := sum(s.Top(Filter{MAC: mac}, c.dim, base, end, 0))
				if got := rowsByKey(h.MACs)[mac].Bytes; got.Down != want.Down || got.Up != want.Up {
					t.Fatalf("seed %d %s %s: device %s has %+v, Top says %+v", seed, c.dim, c.key, mac, got, want)
				}
			}
			for _, app := range apps {
				want := sum(s.Top(Filter{App: app}, c.dim, base, end, 0))
				if got := rowsByKey(h.Apps)[app].Bytes; got.Down != want.Down || got.Up != want.Up {
					t.Fatalf("seed %d %s %s: app %s has %+v, Top says %+v", seed, c.dim, c.key, app, got, want)
				}
			}
			if c.dim == "asn" {
				for _, cc := range ccs {
					want := Bytes{}
					for _, p := range s.Top(Filter{CC: cc}, "place", base, end, 0) {
						if p.Extra["asn"] == c.key {
							want = add(want, p.Bytes)
						}
					}
					if got := rowsByKey(h.CCs)[cc].Bytes; got.Down != want.Down {
						t.Fatalf("seed %d: country %s has %+v, Top says %+v", seed, cc, got, want)
					}
				}
				if h.Org != "ORG" {
					t.Fatalf("org %q", h.Org)
				}
			}
		}
	}
}

func TestHoldersRefuseOtherDimensions(t *testing.T) {
	s := newTestStore(Options{}, later)
	feed(s, delta(base, phone, "youtube", "US", 1, 1))
	if h := s.Holders("mac", "x", base, base.Add(time.Hour)); h.MACs != nil || h.Apps != nil {
		t.Errorf("Holders(mac) = %+v", h)
	}
	_ = strconv.Itoa
}
