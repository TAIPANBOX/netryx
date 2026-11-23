//go:build heavyprofile

package enrich_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"netryx/internal/enrich"
	"netryx/internal/inventory"
	"netryx/internal/packfile"
)

// TestTheFixedTablesFitTheirBudget measures the live heap the address table
// and the vendor list hold once the service has opened them, the way
// buildConsole opens them, and fails above NETRYX_FIXED_BUDGET_MB (2 by
// default). The packs are built from the sources in NETRYX_DATA first, as
// package/netryx/build.sh builds them; NETRYX_GEO and NETRYX_OUI name other
// files to open instead (the text sources, to measure the old loaders).
//
//	NETRYX_DATA=$HOME/netryx-geo/data go test -tags heavyprofile -run TestTheFixedTablesFitTheirBudget -v ./internal/enrich/
func TestTheFixedTablesFitTheirBudget(t *testing.T) {
	dir := os.Getenv("NETRYX_DATA")
	if dir == "" {
		t.Skip("NETRYX_DATA is not set")
	}
	budget := 2.0
	if s := os.Getenv("NETRYX_FIXED_BUDGET_MB"); s != "" {
		b, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatalf("NETRYX_FIXED_BUDGET_MB=%q: %v", s, err)
		}
		budget = b
	}
	geoPath, ouiPath := os.Getenv("NETRYX_GEO"), os.Getenv("NETRYX_OUI")
	if geoPath == "" || ouiPath == "" {
		out := t.TempDir()
		f, err := os.Open(filepath.Join(dir, "ip2asn-combined.tsv.gz"))
		if err != nil {
			t.Fatal(err)
		}
		g, err := enrich.LoadIP2ASN(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		gb, err := g.MarshalPack()
		if err != nil {
			t.Fatal(err)
		}
		of, err := os.Open(filepath.Join(dir, "oui.txt"))
		if err != nil {
			t.Fatal(err)
		}
		m, err := inventory.LoadOUI(of)
		of.Close()
		if err != nil {
			t.Fatal(err)
		}
		ob, err := inventory.MarshalOUIPack(m)
		if err != nil {
			t.Fatal(err)
		}
		if geoPath == "" {
			geoPath = filepath.Join(out, "ip2asn.pack")
			if err := packfile.WriteFile(geoPath, gb); err != nil {
				t.Fatal(err)
			}
		}
		if ouiPath == "" {
			ouiPath = filepath.Join(out, "oui.pack")
			if err := packfile.WriteFile(ouiPath, ob); err != nil {
				t.Fatal(err)
			}
		}
	}

	heap := func() int64 {
		var ms runtime.MemStats
		runtime.GC()
		runtime.GC()
		runtime.ReadMemStats(&ms)
		return int64(ms.HeapAlloc)
	}
	mb := func(b int64) float64 { return float64(b) / (1 << 20) }

	before := heap()
	geo, err := enrich.OpenGeo(geoPath)
	if err != nil {
		t.Fatalf("OpenGeo(%s): %v", geoPath, err)
	}
	afterGeo := heap()
	oui, err := inventory.OpenOUI(ouiPath)
	if err != nil {
		t.Fatalf("OpenOUI(%s): %v", ouiPath, err)
	}
	after := heap()
	runtime.KeepAlive(geo)
	runtime.KeepAlive(oui)

	geoMB, ouiMB, total := mb(afterGeo-before), mb(after-afterGeo), mb(after-before)
	t.Logf("address table %s: %d ranges, %.2f MB of live heap (mapped %v)", geoPath, geo.Len(), geoMB, geo.Mapped())
	t.Logf("vendor list %s: %.2f MB of live heap", ouiPath, ouiMB)
	t.Logf("together %.2f MB (%d bytes), budget %.2f MB, slack %.2f MB", total, after-before, budget, budget-total)
	if total > budget {
		t.Fatalf("the address table and the vendor list hold %.2f MB of live heap, over the %.2f MB budget", total, budget)
	}
}
