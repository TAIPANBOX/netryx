package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDatapackWritesPacksThatVerify(t *testing.T) {
	dir := t.TempDir()
	geo := filepath.Join(dir, "ip2asn.tsv")
	oui := filepath.Join(dir, "oui.txt")
	os.WriteFile(geo, []byte("1.1.1.0\t1.1.1.255\t13335\tUS\tCLOUDFLARENET\n2001:db8::\t2001:db8::ff\t64500\tGB\tEXAMPLE\n"), 0o644)
	os.WriteFile(oui, []byte("8C-79-F5   (hex)\t\tSamsung Electronics Co.,Ltd\n"), 0o644)
	var out, errs bytes.Buffer
	if code := run([]string{"-ip2asn", geo, "-oui", oui, "-out", dir}, &out, &errs); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	gp, op := filepath.Join(dir, "ip2asn.pack"), filepath.Join(dir, "oui.pack")
	if code := run([]string{"-verify", gp, op}, &out, &errs); code != 0 {
		t.Fatalf("good packs failed -verify (%d): %s", code, errs.String())
	}
	if !strings.Contains(out.String(), "address table, 2 ranges") || !strings.Contains(out.String(), "vendor list, 1 registrations") {
		t.Errorf("-verify does not say what it checked:\n%s", out.String())
	}
	if code := run([]string{"-verify", "-kind", "oui", gp}, &out, &errs); code == 0 {
		t.Error("an address table passed as a vendor list")
	}

	b, _ := os.ReadFile(gp)
	b[len(b)/2] ^= 1
	bad := filepath.Join(dir, "bad.pack")
	os.WriteFile(bad, b, 0o644)
	errs.Reset()
	if code := run([]string{"-verify", op, bad}, &out, &errs); code != 1 {
		t.Fatalf("a damaged pack: exit %d, want 1", code)
	}
	if !strings.Contains(errs.String(), "checksum") {
		t.Errorf("-verify does not say why: %s", errs.String())
	}
	if code := run([]string{"-verify", filepath.Join(dir, "gone.pack")}, &out, &errs); code != 1 {
		t.Errorf("a missing pack: exit %d, want 1", code)
	}
	if code := run([]string{"-verify", geo}, &out, &errs); code != 1 {
		t.Errorf("a text file: exit %d, want 1", code)
	}
}

// A failed run leaves what was there before.
func TestAFailedRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	var out, errs bytes.Buffer
	if code := run([]string{"-ip2asn", filepath.Join(dir, "gone.tsv"), "-out", dir}, &out, &errs); code == 0 {
		t.Fatal("a missing source was packed")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("a failed run left %d files", len(ents))
	}
}
