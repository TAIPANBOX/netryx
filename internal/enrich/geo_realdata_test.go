//go:build realdata

package enrich

import (
	"os"
	"runtime"
	"testing"
	"time"
)

// TestRealFileFootprint loads the full iptoasn table and reports what it costs
// in memory. It only measures; it asserts nothing about the numbers. It runs
// on forge, where the file lives, and skips where the file is absent.
func TestRealFileFootprint(t *testing.T) {
	const path = "../../data/ip2asn-combined.tsv.gz"
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		t.Skipf("%s not present", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	g, err := LoadIP2ASN(f)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("LoadIP2ASN: %v", err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(g)

	t.Logf("ranges=%d v4=%d v6=%d distinct_answers=%d", g.Len(), len(g.v4), len(g.v6), len(g.infos))
	t.Logf("skipped=%d", g.Skipped())
	t.Logf("load_time=%s", elapsed)
	t.Logf("heap_retained_bytes=%d (%.1f MB)", after.HeapAlloc-before.HeapAlloc,
		float64(after.HeapAlloc-before.HeapAlloc)/(1<<20))
	t.Logf("total_allocated_during_load_bytes=%d", after.TotalAlloc-before.TotalAlloc)
}
