// Command iconpack writes the icons the console serves for the apps in the
// meta file that appmap produced: one sanitised SVG per app that has a Simple
// Icons brand, and a manifest that says, for every app, whether it has an SVG,
// a favicon or only a letter tile, and which colour it is.
//
// It is a build step, run on forge after appmap:
//
//	go run ./tools/iconpack -si data/simple-icons/icons -meta data/appmeta.json -out internal/web/dist/icons
//
// The SVG files are not copied from Simple Icons. Each one is rebuilt from the
// path data alone, so nothing else in the source file (a script, an event
// handler, a link) can reach the console. The path data is checked against a
// strict character set first, because it is written into the output as is.
package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// appMeta mirrors the struct in tools/appmap. The two commands are separate
// main packages, so the shape is kept in step by hand: the -meta file is their
// only contract.
type appMeta struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Icon     string `json:"icon"`
	Slug     string `json:"slug"`
	Domain   string `json:"domain"`
	Color    string `json:"color"`
}

// manifestEntry is what the console needs to draw one app's tile without
// loading anything from the internet.
type manifestEntry struct {
	Kind  string `json:"kind"`
	Color string `json:"color"`
	// Name lets the console name an app it has only seen as an id, such as a
	// layer of the live chart whose app has left the apps list.
	Name string `json:"name,omitempty"`
}

var (
	// pathData is the only content copied from a Simple Icons file. The set is
	// the SVG path grammar, so no quote, angle bracket or name can get through.
	pathData = regexp.MustCompile(`^[MmLlHhVvCcSsQqTtAaZz0-9.,\s+-]+$`)
	// An app id becomes a file name, so it is restricted to the same shape the
	// catalog uses for ids.
	appID    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	slugRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9+._-]{0,63}$`)
	colourRe = regexp.MustCompile(`^[0-9A-Fa-f]{6}$`)
)

// maxSVGBytes bounds one Simple Icons file read. The largest brand icons are
// a few kilobytes, so a file this size is not one of them.
const maxSVGBytes = 256 << 10

func main() {
	si := flag.String("si", "", "the Simple Icons icons directory (one <slug>.svg per brand)")
	meta := flag.String("meta", "", "the -meta JSON from tools/appmap")
	out := flag.String("out", "", "the directory to write <id>.svg files and manifest.json into")
	flag.Parse()
	if *si == "" || *meta == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: iconpack -si <icons dir> -meta <meta.json> -out <dir>")
		os.Exit(2)
	}
	if err := pack(*si, *meta, *out); err != nil {
		fmt.Fprintln(os.Stderr, "iconpack:", err)
		os.Exit(1)
	}
}

// pack builds every icon and the manifest in memory first, and only then
// writes them, so a refused app leaves the output directory as it was. Files
// for apps that are no longer in the meta are removed, so the directory always
// matches the catalog.
func pack(siDir, metaPath, outDir string) error {
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return err
	}
	var apps map[string]appMeta
	if err := json.Unmarshal(raw, &apps); err != nil {
		return fmt.Errorf("meta: %w", err)
	}

	svgs := make(map[string][]byte)
	manifest := make(map[string]manifestEntry, len(apps))
	for id, a := range apps {
		if !appID.MatchString(id) {
			return fmt.Errorf("app id %q is not a plain name", id)
		}
		// A colour comes from Simple Icons through appmap. An app without one
		// is written with an empty colour, and the console draws its tile in
		// a neutral shade.
		entry := manifestEntry{Name: a.Name}
		if a.Color != "" {
			if !colourRe.MatchString(a.Color) {
				return fmt.Errorf("app %s: colour %q is not six hex digits", id, a.Color)
			}
			entry.Color = "#" + strings.ToUpper(a.Color)
		}
		switch a.Icon {
		case "si":
			if !slugRe.MatchString(a.Slug) {
				return fmt.Errorf("app %s: slug %q is not a Simple Icons slug", id, a.Slug)
			}
			svg, err := regenerate(filepath.Join(siDir, a.Slug+".svg"))
			if err != nil {
				return fmt.Errorf("app %s: slug %s: %w", id, a.Slug, err)
			}
			svgs[id] = svg
			entry.Kind = "svg"
		case "fav":
			entry.Kind = "fav"
		case "":
			entry.Kind = "letter"
		default:
			return fmt.Errorf("app %s: icon kind %q is not si, fav or empty", id, a.Icon)
		}
		manifest[id] = entry
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for id, svg := range svgs {
		if err := replaceFile(filepath.Join(outDir, id+".svg"), svg); err != nil {
			return err
		}
	}
	// Remove the svg files of apps that left the catalog, so the directory
	// cannot keep an icon the manifest no longer names.
	stale, err := filepath.Glob(filepath.Join(outDir, "*.svg"))
	if err != nil {
		return err
	}
	for _, f := range stale {
		id := strings.TrimSuffix(filepath.Base(f), ".svg")
		if _, keep := svgs[id]; !keep {
			if err := os.Remove(f); err != nil {
				return err
			}
		}
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return replaceFile(filepath.Join(outDir, "manifest.json"), append(manifestJSON, '\n'))
}

// regenerate reads one Simple Icons file and returns a fresh SVG made of its
// path data only. The file must be an svg root with at least one path, and
// every path's data must match pathData.
func regenerate(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("not in Simple Icons: %w", err)
	}
	defer f.Close()
	src, err := io.ReadAll(io.LimitReader(f, maxSVGBytes+1))
	if err != nil {
		return nil, err
	}
	if len(src) > maxSVGBytes {
		return nil, errors.New("file is larger than an icon can be")
	}
	d, err := pathOf(src)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="`)
	b.WriteString(d)
	b.WriteString(`"/></svg>`)
	return b.Bytes(), nil
}

// pathOf returns the path data of an SVG document, joining several paths with
// a space. Only attributes named d are read; everything else is dropped.
func pathOf(src []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(src))
	var parts []string
	root := ""
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("not XML: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if root == "" {
			root = start.Name.Local
		}
		if start.Name.Local != "path" {
			continue
		}
		for _, a := range start.Attr {
			if a.Name.Local != "d" {
				continue
			}
			if !pathData.MatchString(a.Value) {
				return "", errors.New("path data has characters outside the SVG path grammar")
			}
			parts = append(parts, a.Value)
		}
	}
	if root != "svg" {
		return "", errors.New("the file is not an svg document")
	}
	if len(parts) == 0 {
		return "", errors.New("the svg has no path data")
	}
	return strings.Join(parts, " "), nil
}

// replaceFile writes data beside path and renames it over path, so a reader
// never sees a half written icon.
func replaceFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
