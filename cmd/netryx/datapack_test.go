package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netryx/internal/enrich"
	"netryx/internal/inventory"
	"netryx/internal/packfile"
)

func writePacks(t *testing.T, dir string) (geo, oui string) {
	t.Helper()
	g, err := enrich.LoadIP2ASN(strings.NewReader("1.1.1.0\t1.1.1.255\t13335\tUS\tCLOUDFLARENET\n"))
	if err != nil {
		t.Fatal(err)
	}
	gb, err := g.MarshalPack()
	if err != nil {
		t.Fatal(err)
	}
	ob, err := inventory.MarshalOUIPack(inventory.OUIMap{"8C79F5": "Samsung Electronics Co.,Ltd"})
	if err != nil {
		t.Fatal(err)
	}
	geo, oui = filepath.Join(dir, "ip2asn.pack"), filepath.Join(dir, "oui.pack")
	if err := packfile.WriteFile(geo, gb); err != nil {
		t.Fatal(err)
	}
	if err := packfile.WriteFile(oui, ob); err != nil {
		t.Fatal(err)
	}
	return geo, oui
}

// The packs the package ships name the flows and the makers as the text
// files did.
func TestDataPacksNameTheFlows(t *testing.T) {
	o := testOptions(t)
	o.Geo, o.OUI = writePacks(t, t.TempDir())
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	f := openAndClose(c, 3, "192.168.50.10", "1.1.1.1", time.Now())
	if f.Geo.CC != "US" || f.Geo.Org != "CLOUDFLARENET" {
		t.Errorf("the address table pack places it: %+v", f.Geo)
	}
	if c.inv.oui == nil || c.inv.oui.Vendor("8C79F5") != "Samsung Electronics Co.,Ltd" {
		t.Errorf("the vendor list pack names the maker: %v", c.inv.oui)
	}
}

// A pack that fails its check is refused with the reason, and the console
// runs as it does without the file: no country, no maker.
func TestADamagedDataPackDegradesLikeAMissingOne(t *testing.T) {
	o := testOptions(t)
	o.Geo, o.OUI = writePacks(t, t.TempDir())
	for _, p := range []string{o.Geo, o.OUI} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		b[len(b)-40] ^= 1
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	c, err := buildConsole(o)
	if err != nil {
		t.Fatalf("a damaged data pack must not stop the console: %v", err)
	}
	f := openAndClose(c, 4, "192.168.50.10", "1.1.1.1", time.Now())
	if f.Geo.CC != "--" {
		t.Errorf("a damaged address table is no address table, got %q", f.Geo.CC)
	}
	if c.inv.oui != nil {
		t.Errorf("a damaged vendor list is no vendor list, got %v", c.inv.oui)
	}
	for _, want := range []string{"address table " + o.Geo + " cannot be read", "vendor list " + o.OUI + " cannot be read", "checksum"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log does not say %q:\n%s", want, logs.String())
		}
	}
}

// A config written before the packs names the text file; after an upgrade
// that no longer ships it, the pack beside it is read. A text file that is
// still there is read as before, and any other path is left alone.
func TestAnOldConfigsTextPathFindsThePack(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "oui.txt")
	current := filepath.Join(dir, "oui.pack")
	if got := dataPath(legacy, legacy, current); got != legacy {
		t.Errorf("with neither file the configured path stays: %s", got)
	}
	write(t, current, "pack")
	if got := dataPath(legacy, legacy, current); got != current {
		t.Errorf("the old path gone and the pack there: read %s, want the pack", got)
	}
	write(t, legacy, "text")
	if got := dataPath(legacy, legacy, current); got != legacy {
		t.Errorf("the old file still there: read %s, want it", got)
	}
	other := filepath.Join(dir, "mine.txt")
	if got := dataPath(other, legacy, current); got != other {
		t.Errorf("a path of the owner's own: read %s, want it", got)
	}
}
