//go:build heavyprofile

package daily

// The measurement behind the daily file's figures in CLAUDE.md: 31 days of
// files for 64 busy devices, what one day costs the flash and what the 30
// days kept cost the heap once loaded. scripts/flash-budget.sh runs it:
//
//	scripts/forge.sh sh scripts/flash-budget.sh

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"netryx/internal/rollup"
)

func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// profileDay is a busy day: every hour counted, each device moving a
// different amount every hour, 24 rule counters and 8 interfaces.
func profileDay(d time.Time, devs int) Day {
	h := rollup.DayHours{Day: d, Mask: 1<<24 - 1, By: map[string]*[2][24]uint64{}}
	for i := 0; i < devs; i++ {
		v := &[2][24]uint64{}
		for x := 0; x < 24; x++ {
			v[0][x] = uint64((i*7919+x*104729+d.YearDay()*13)%(3<<20)) << 10
			v[1][x] = v[0][x] / 9
		}
		h.By[fmt.Sprintf("4e:c2:94:9f:%02x:%02x", i, i)] = v
	}
	h.By["router"] = &[2][24]uint64{}
	day := FromHours(h)
	day.CountersAt = d.Add(24*time.Hour + 2*time.Minute)
	for i := 0; i < 24; i++ {
		day.Hits = append(day.Hits, Counter{fmt.Sprintf("rule_%02d", i), uint64(i) * 123456789})
	}
	for _, n := range []string{"guest", "iot", "lan", "loopback", "wan", "wan6", "wg0", "wwan"} {
		day.Ifaces = append(day.Ifaces, Iface{n, 1 << 40, 1 << 38})
	}
	return day
}

func budgetKB(t *testing.T, env string, def float64) float64 {
	v := os.Getenv(env)
	if v == "" {
		return def
	}
	b, err := strconv.ParseFloat(v, 64)
	if err != nil || b <= 0 {
		t.Fatalf("%s=%q is not a number of KB above zero", env, v)
	}
	return b
}

func TestDailyProfile(t *testing.T) {
	const devs = 64
	dir := t.TempDir()
	first := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := first.Add(31 * 24 * time.Hour)
	now := end.Add(2 * time.Minute)
	s, err := Open(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	for d := first; d.Before(end); d = d.Add(24 * time.Hour) {
		if ok, err := s.Write(profileDay(d, devs)); !ok || err != nil {
			t.Fatalf("writing %v: %v %v", d, ok, err)
		}
	}
	if err := s.Prune(now); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"+Ext))
	if len(files) != Keep {
		t.Fatalf("%d files after 31 days, want %d", len(files), Keep)
	}
	var biggest int64
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && fi.Size() > biggest {
			biggest = fi.Size()
		}
	}
	thirty, err := Encode(profileDay(first, 30))
	if err != nil {
		t.Fatal(err)
	}
	s = nil

	before := liveHeap()
	loaded, err := Open(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	heap := int64(liveHeap()) - int64(before)
	if loaded.Len() != Keep {
		t.Fatalf("%d days loaded, want %d", loaded.Len(), Keep)
	}
	t.Logf("a day's file for 30 devices: %d bytes", len(thirty))
	t.Logf("a day's file for %d devices: %d bytes", devs, biggest)
	t.Logf("heap of %d loaded days: %.1f KB", Keep, float64(heap)/1024)

	fileBudget := budgetKB(t, "NETRYX_DAILY_BUDGET_KB", 16)
	heapBudget := budgetKB(t, "NETRYX_DAILY_HEAP_BUDGET_KB", 1024)
	if kb := float64(biggest) / 1024; kb > fileBudget {
		t.Errorf("a day's file is over its budget: %.1f KB, budget %.0f KB", kb, fileBudget)
	} else {
		t.Logf("file budget: %.0f KB, slack %.1f KB", fileBudget, fileBudget-kb)
	}
	if kb := float64(heap) / 1024; kb > heapBudget {
		t.Errorf("the loaded days are over their budget: %.1f KB, budget %.0f KB", kb, heapBudget)
	} else {
		t.Logf("heap budget: %.0f KB, slack %.1f KB", heapBudget, heapBudget-kb)
	}
	runtime.KeepAlive(loaded)
}
