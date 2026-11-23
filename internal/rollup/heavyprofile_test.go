//go:build heavyprofile

package rollup

// The heavy-profile measurement behind the flash figure in CLAUDE.md. It
// builds 31 days of a busy house, so it runs on forge, not in the suite:
//
//	scripts/forge.sh go test -tags heavyprofile -run TestHeavyProfile -v ./internal/rollup/

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"testing"
	"time"

	"netryx/internal/flows"
)

func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// TestHeavyProfile: 30 devices, each hour 10 apps x 2 countries x 5 flows,
// 50 domains and 15 organisations a device, every string a fresh copy as
// separate flows would carry it; 31 days, so every ring is full, saved a
// few minutes into each hour as the service does.
func TestHeavyProfile(t *testing.T) {
	end := base.Add(31 * 24 * time.Hour)
	s := NewStore(Options{})
	before := liveHeap()
	dir, n := fill(t, s, end)
	heap := liveHeap() - before
	if s.Late() != 0 || s.Dropped() != 0 {
		t.Fatalf("the profile lost deltas: late %d dropped %d", s.Late(), s.Dropped())
	}
	size := func(k string) (int64, int) {
		es, _ := os.ReadDir(filepath.Join(dir, k))
		var b int64
		for _, e := range es {
			fi, _ := e.Info()
			b += fi.Size()
		}
		return b, len(es)
	}
	hb, hn := size("hours")
	db, dn := size("days")
	perDay := float64(hb)/float64(hn)*24 + float64(db)/float64(dn)
	t.Logf("%d deltas, %d interned strings", n, len(s.strs))
	t.Logf("heap with every ring full (1440 minutes, 168 hours, 30 days): %.1f MB", float64(heap)/(1<<20))
	t.Logf("on disk: %d hour files, %.0f KB each; %d day files, %.0f KB each", hn, float64(hb)/1024/float64(hn), dn, float64(db)/1024/float64(dn))
	t.Logf("written a day: %.0f KB (24 hours and 1 day)", perDay/1024)
	// The heap budget for the full store, in MB: NETRYX_HEAP_BUDGET_MB, 24 by
	// default. scripts/memory-budget.sh reads the line above and this verdict.
	budget := 24.0
	if v := os.Getenv("NETRYX_HEAP_BUDGET_MB"); v != "" {
		b, err := strconv.ParseFloat(v, 64)
		if err != nil || b <= 0 {
			t.Fatalf("NETRYX_HEAP_BUDGET_MB=%q is not a number of MB above zero", v)
		}
		budget = b
	}
	if mb := float64(heap) / (1 << 20); mb > budget {
		t.Errorf("the heap is over its budget: %.1f MB, budget %.0f MB", mb, budget)
	} else {
		t.Logf("heap budget: %.0f MB, slack %.1f MB", budget, budget-mb)
	}
	// NETRYX_HEAPPROFILE=<file> writes what the full store holds, by where it
	// was allocated: go tool pprof -top -sample_index=inuse_space <file>.
	if p := os.Getenv("NETRYX_HEAPPROFILE"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		pprof.WriteHeapProfile(f)
		f.Close()
	}
	runtime.KeepAlive(s)
}

// fill feeds s the heavy profile up to end, saving as the service does, and
// returns the state directory and the number of deltas.
func fill(t *testing.T, s *Store, end time.Time) (string, int) {
	const devs, apps, ccs, asns, doms = 30, 10, 2, 15, 50
	cc := []string{"GB", "US"}
	dir := t.TempDir()
	n := 0
	for h := base; h.Before(end); h = h.Add(time.Hour) {
		clock := h.Add(50 * time.Minute)
		s.now = func() time.Time { return clock }
		for d := 0; d < devs; d++ {
			for a := 0; a < apps; a++ {
				for c := 0; c < ccs; c++ {
					for x := 0; x < 5; x++ {
						s.Add(flows.Delta{
							At:     h.Add(time.Duration(a*5+x) * time.Minute),
							MAC:    fmt.Sprintf("4e:c2:94:9f:%02x:%02x", d, d),
							App:    fmt.Sprintf("app%02d", a),
							CC:     fresh(cc[c]),
							Domain: fmt.Sprintf("cdn%02d.example-service.com", (a*5+x+c+h.Hour())%doms),
							ASN:    uint32((a + x) % asns),
							Org:    fmt.Sprintf("ORG-%d", (a+x)%asns),
							Up:     10, Down: 100, First: true,
						})
						n++
					}
				}
			}
		}
		s.now = func() time.Time { return h.Add(5 * time.Minute) }
		if _, err := SaveClosed(s, dir); err != nil {
			t.Fatal(err)
		}
	}
	return dir, n
}
