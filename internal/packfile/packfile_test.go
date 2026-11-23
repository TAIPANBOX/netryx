package packfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const magic = "NXTESTPK"

func TestASealedPackChecksAndItsBodyComesBack(t *testing.T) {
	body := []byte("the table")
	b := Seal(magic, 3, body)
	got, err := Check(b, magic, 3)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("Check = %q, %v", got, err)
	}
	path := filepath.Join(t.TempDir(), "x.pack")
	if err := WriteFile(path, b); err != nil {
		t.Fatal(err)
	}
	if !HasMagic(path, magic) || HasMagic(path, "NXOTHER!") {
		t.Error("HasMagic does not tell the kinds apart")
	}
	m, mapped, err := Open(path)
	if err != nil || !mapped || !bytes.Equal(m, b) {
		t.Fatalf("Open = %d bytes, mapped %v, %v", len(m), mapped, err)
	}
	Release(m, mapped)
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Errorf("a pack is written 0644, got %v", st.Mode().Perm())
	}
}

func TestOpenRefusesWhatCannotBeAPack(t *testing.T) {
	dir := t.TempDir()
	short := filepath.Join(dir, "short.pack")
	os.WriteFile(short, []byte("NXTESTPK"), 0o644)
	for name, c := range map[string]struct{ path, want string }{
		"a directory": {dir, "not a regular file"},
		"too short":   {short, "too short"},
	} {
		if _, _, err := Open(c.path); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if _, _, err := Open(filepath.Join(dir, "gone.pack")); !os.IsNotExist(err) {
		t.Errorf("a missing file: %v", err)
	}
	if HasMagic(filepath.Join(dir, "gone.pack"), magic) {
		t.Error("a missing file has no magic")
	}
}

// A write that cannot finish leaves nothing behind, not half a pack.
func TestAFailedWriteLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFile(filepath.Join(dir, "no", "such", "dir.pack"), []byte("x")); err == nil {
		t.Fatal("a write into a missing directory succeeded")
	}
	target := filepath.Join(dir, "sub")
	os.Mkdir(target, 0o755)
	if err := WriteFile(target, []byte("x")); err == nil {
		t.Fatal("a pack renamed over a directory")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("a failed write left %d entries", len(ents))
	}
}
