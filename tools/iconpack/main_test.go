package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// sampleSVG is shaped like a Simple Icons file, with a script and attributes
// that must not reach the output.
const sampleSVG = `<svg role="img" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" onload="alert(1)">` +
	`<title>GitHub</title><script>alert(1)</script>` +
	`<path d="M12 0C5.37 0 0 5.37 0 12z" fill="red"/></svg>`

// writeSimpleIcons lays out an icons directory: one file per slug.
func writeSimpleIcons(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// writeMeta writes the meta file appmap produces.
func writeMeta(t *testing.T, meta map[string]appMeta) string {
	t.Helper()
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "meta.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAMissingSlugIsAnError(t *testing.T) {
	si := writeSimpleIcons(t, map[string]string{"github.svg": sampleSVG})
	meta := writeMeta(t, map[string]appMeta{
		"ghost": {Name: "Ghost", Category: "other", Icon: "si", Slug: "nosuch", Color: "000000"},
	})
	out := t.TempDir()
	if err := pack(si, meta, out); err == nil {
		t.Fatal("pack accepted a slug that Simple Icons does not have")
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatalf("a failed pack wrote %d files; want none", len(entries))
	}
}

func TestIconsAreRegeneratedNotCopied(t *testing.T) {
	si := writeSimpleIcons(t, map[string]string{"github.svg": sampleSVG})
	meta := writeMeta(t, map[string]appMeta{
		"github": {Name: "GitHub", Category: "cloud", Icon: "si", Slug: "github", Color: "181717"},
	})
	out := t.TempDir()
	if err := pack(si, meta, out); err != nil {
		t.Fatalf("pack: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "github.svg"))
	if err != nil {
		t.Fatal(err)
	}
	want := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M12 0C5.37 0 0 5.37 0 12z"/></svg>`
	if string(got) != want {
		t.Fatalf("icon = %q\nwant   %q", got, want)
	}
}

func TestPathDataWithMarkupIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"markup in the path data":  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0&lt;script&gt;alert(1)&lt;/script&gt;"/></svg>`,
		"a letter outside the set": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0 X1 1"/></svg>`,
		"quotes in the path data":  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0 L1 1&quot; onload=&quot;x"/></svg>`,
	} {
		t.Run(name, func(t *testing.T) {
			si := writeSimpleIcons(t, map[string]string{"bad.svg": body})
			meta := writeMeta(t, map[string]appMeta{
				"bad": {Name: "Bad", Category: "other", Icon: "si", Slug: "bad", Color: "000000"},
			})
			out := t.TempDir()
			if err := pack(si, meta, out); err == nil {
				t.Fatal("pack accepted path data outside the allowed set")
			}
			if _, err := os.Stat(filepath.Join(out, "bad.svg")); !os.IsNotExist(err) {
				t.Fatal("a refused icon was written")
			}
		})
	}
}

func TestTheManifestNamesEveryAppWithItsKindAndColour(t *testing.T) {
	si := writeSimpleIcons(t, map[string]string{"github.svg": sampleSVG})
	meta := writeMeta(t, map[string]appMeta{
		"github":  {Name: "GitHub", Category: "cloud", Icon: "si", Slug: "github", Color: "181717"},
		"netflix": {Name: "Netflix", Category: "video", Icon: "fav", Domain: "netflix.com", Color: ""},
		"plain":   {Name: "Plain", Category: "other", Color: ""},
	})
	out := t.TempDir()
	if err := pack(si, meta, out); err != nil {
		t.Fatalf("pack: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]manifestEntry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	want := map[string]manifestEntry{
		"github":  {Kind: "svg", Color: "#181717", Name: "GitHub"},
		"netflix": {Kind: "fav", Color: "", Name: "Netflix"},
		"plain":   {Kind: "letter", Color: "", Name: "Plain"},
	}
	if len(got) != len(want) {
		t.Fatalf("manifest has %d apps; want %d: %v", len(got), len(want), got)
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("manifest[%s] = %+v; want %+v", id, got[id], w)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "netflix.svg")); !os.IsNotExist(err) {
		t.Fatal("a favicon app got an svg file")
	}
}

func TestAnAppIdThatIsNotAFileNameIsRefused(t *testing.T) {
	si := writeSimpleIcons(t, map[string]string{"github.svg": sampleSVG})
	meta := writeMeta(t, map[string]appMeta{
		"../escape": {Name: "X", Category: "other", Icon: "si", Slug: "github", Color: "000000"},
	})
	if err := pack(si, meta, t.TempDir()); err == nil {
		t.Fatal("pack accepted an app id that would leave the output directory")
	}
}

func TestAnAppWithoutAColourIsWrittenEmpty(t *testing.T) {
	si := writeSimpleIcons(t, map[string]string{"github.svg": sampleSVG})
	meta := writeMeta(t, map[string]appMeta{
		"github": {Name: "GitHub", Category: "cloud", Icon: "si", Slug: "github"},
	})
	out := t.TempDir()
	if err := pack(si, meta, out); err != nil {
		t.Fatalf("pack refused an app with no colour: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]manifestEntry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["github"].Color != "" {
		t.Fatalf("manifest colour = %q; want an empty string, not a bare #", got["github"].Color)
	}
}

func TestAnAppIdMayContainADot(t *testing.T) {
	si := writeSimpleIcons(t, map[string]string{"github.svg": sampleSVG})
	meta := writeMeta(t, map[string]appMeta{
		"x.y": {Name: "X", Category: "other", Icon: "si", Slug: "github", Color: "181717"},
	})
	if err := pack(si, meta, t.TempDir()); err != nil {
		t.Fatalf("pack refused the app id x.y, which appmap accepts: %v", err)
	}
}
