// Command appmap turns a checkout of the v2fly domain-list-community data
// directory into the appmap.tsv that internal/enrich loads at startup, and,
// with -meta, the JSON that tools/iconpack reads for each app's icon.
//
// It is a build step, not part of the monitor. It runs on forge against a
// pinned checkout, so the generated table is reproducible from one commit:
//
//	go run ./tools/appmap -v2fly data/v2fly/data -catalog tools/appmap/catalog.tsv -o data/appmap.tsv -meta data/appmeta.json
//
// The catalog names the apps the monitor knows and which v2fly list covers
// each one. Lists are looked up by file name, and a list that is not in the
// checkout is an error, so a bump of the pinned commit cannot silently drop
// an app. A failed run writes nothing, so a half table never replaces a good
// one.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"netryx/internal/enrich"
)

// categories are the only category ids the UI knows. An unknown one is a
// typo in the catalog, and it is refused rather than shown as its own group.
var categories = map[string]bool{
	"video": true, "social": true, "messaging": true, "calls": true,
	"cloud": true, "gaming": true, "music": true, "ai": true,
	"ads": true, "shopping": true, "news": true, "other": true,
	"travel": true, "payments": true, "smarthome": true, "dev": true,
	"doh": true, "vpn": true,
}

// The icon and colour columns are checked here, before they reach a file
// that tools/iconpack later turns into a path on disk.
var (
	slugPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9+._-]{0,63}$`)
	domainPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)+$`)
	colourPattern = regexp.MustCompile(`^[0-9A-Fa-f]{6}$`)
	// An app id is written into the table and into a file name by
	// tools/iconpack, so it has the same shape there and here.
	idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	// brandSlugChars spells out the symbols Simple Icons writes into a slug,
	// so "Booking.com" becomes bookingdotcom.
	brandSlugChars = strings.NewReplacer("+", "plus", ".", "dot", "&", "and", "#", "sharp")
)

// appMeta is what the console knows about one app beyond its table rules: the
// name and category, where its icon comes from and its brand colour. The
// -meta file holds one of these per app. tools/iconpack keeps its own copy of
// this struct because each command is a main package and cannot import the
// other.
type appMeta struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Icon     string `json:"icon"`
	Slug     string `json:"slug"`
	Domain   string `json:"domain"`
	Color    string `json:"color"`
}

// brandColours maps a Simple Icons slug to its brand colour. It is filled from
// simple-icons.json by -brands, and it is the only source of a colour.
var brandColours map[string]string

// loadBrands reads simple-icons.json, a list of {title, hex}, and returns the
// colour of each brand by its slug. The first entry for a slug wins.
func loadBrands(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []struct {
		Title string `json:"title"`
		Hex   string `json:"hex"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	brands := make(map[string]string, len(entries))
	for _, e := range entries {
		slug := brandSlug(e.Title)
		if slug == "" {
			continue
		}
		if !colourPattern.MatchString(e.Hex) {
			return nil, fmt.Errorf("brand %q: hex %q is not six hex digits", e.Title, e.Hex)
		}
		if _, dup := brands[slug]; !dup {
			brands[slug] = strings.ToUpper(e.Hex)
		}
	}
	return brands, nil
}

// brandSlug derives the Simple Icons file name from a brand title: lower case,
// the spelled out symbols, and nothing but letters and digits. The forge run
// checks these names against the icon files, so a mismatch shows up there.
func brandSlug(title string) string {
	var b strings.Builder
	for _, r := range brandSlugChars.Replace(strings.ToLower(title)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// rule is one domain line taken from a v2fly list.
type rule struct {
	kind string // "d" suffix or "f" exact
	name string
}

func main() {
	v2fly := flag.String("v2fly", "", "the v2fly data directory")
	catalog := flag.String("catalog", "", "the catalog file")
	out := flag.String("o", "", "write the table to this file, replacing it atomically; stdout when empty")
	meta := flag.String("meta", "", "also write the per-app icon and colour JSON to this file")
	brands := flag.String("brands", "", "simple-icons.json: the only source of a brand colour")
	flag.Parse()
	if *v2fly == "" || *catalog == "" || *brands == "" {
		fmt.Fprintln(os.Stderr, "usage: appmap -v2fly <data dir> -catalog <catalog.tsv> -brands <simple-icons.json> [-o appmap.tsv] [-meta meta.json]")
		os.Exit(2)
	}
	colours, err := loadBrands(*brands)
	if err != nil {
		fmt.Fprintln(os.Stderr, "appmap:", err)
		os.Exit(1)
	}
	brandColours = colours
	counts, err := writeTable(*v2fly, *catalog, *out, *meta)
	if err != nil {
		fmt.Fprintln(os.Stderr, "appmap:", err)
		os.Exit(1)
	}
	printCounts(counts)
}

// writeTable generates the table to outPath, or to stdout when outPath is
// empty, and the meta JSON to metaPath when that is set. With a path, each
// file is replaced in one rename, so readers see the old file or the new one,
// never a partial one.
func writeTable(dataDir, catalogPath, outPath, metaPath string) (map[string]int, error) {
	cf, err := os.Open(catalogPath)
	if err != nil {
		return nil, err
	}
	defer cf.Close()
	var buf bytes.Buffer
	counts, meta, err := generate(dataDir, cf, &buf)
	if err != nil {
		return nil, err
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, err
	}
	metaJSON = append(metaJSON, '\n')
	if outPath == "" {
		if _, err := os.Stdout.Write(buf.Bytes()); err != nil {
			return nil, err
		}
	} else if err := replaceFile(outPath, buf.Bytes()); err != nil {
		return nil, err
	}
	if metaPath != "" {
		if err := replaceFile(metaPath, metaJSON); err != nil {
			return nil, err
		}
	}
	return counts, nil
}

// replaceFile writes data to a temporary file beside path and renames it over
// path. The temporary file is in the same directory, so the rename stays on
// one filesystem and is atomic.
func replaceFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	// After a successful rename the temporary name is gone and this is a no-op.
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

// generate builds the whole table in memory and writes it to out only once
// every catalog entry has succeeded. A failure anywhere leaves out untouched.
// It returns the number of rules per category, and the meta of every app that
// has at least one rule in the table. The first rule to name a domain keeps
// it, so hand written domains, which go first, beat any list.
func generate(dataDir string, catalog io.Reader, out io.Writer) (map[string]int, map[string]appMeta, error) {
	lines, err := readLines(catalog)
	if err != nil {
		return nil, nil, err
	}

	var buf bytes.Buffer
	if _, err := fmt.Fprintln(&buf, "# Generated by tools/appmap from the v2fly domain list at the pinned commit. Do not edit by hand."); err != nil {
		return nil, nil, err
	}
	counts := make(map[string]int)
	seen := make(map[string]bool)
	// inTable holds the ids with at least one rule written; only those get meta.
	inTable := make(map[string]bool)
	// apps holds what every catalog line says about its id, whether or not the
	// line wrote a rule: a line that only adds an icon still counts.
	apps := make(map[string]*appMeta)
	total := 0
	emit := func(kind, key, id, name, category string) error {
		if seen[kind+"\t"+key] {
			return nil
		}
		if total >= enrich.MaxAppRules {
			return fmt.Errorf("appmap: more than %d rules, the limit", enrich.MaxAppRules)
		}
		seen[kind+"\t"+key] = true
		total++
		counts[category]++
		inTable[id] = true
		_, err := fmt.Fprintf(&buf, "%s\t%s\t%s\t%s\t%s\n", kind, key, id, name, category)
		return err
	}
	note := func(id string, a appMeta) {
		m, ok := apps[id]
		if !ok {
			m = &appMeta{Name: a.Name, Category: a.Category}
			apps[id] = m
		}
		// The first line that sets an icon or a colour wins, field by field.
		if m.Icon == "" && a.Icon != "" {
			m.Icon, m.Slug, m.Domain = a.Icon, a.Slug, a.Domain
		}
		if m.Color == "" {
			m.Color = a.Color
		}
	}

	// Pass one: hand written domains. They are emitted before any list so
	// that they win the first-rule-wins rule, wherever they sit in the catalog.
	for i, line := range lines {
		f := catalogFields(line)
		if len(f) == 0 || f[0] != "@domain" {
			continue
		}
		lineNo := i + 1
		if len(f) != 5 && len(f) != 7 {
			return nil, nil, fmt.Errorf("appmap: catalog line %d: @domain wants 5 or 7 fields, got %d", lineNo, len(f))
		}
		if f[1] == "" {
			return nil, nil, fmt.Errorf("appmap: catalog line %d: empty domain", lineNo)
		}
		if !categories[f[4]] {
			return nil, nil, fmt.Errorf("appmap: catalog line %d: unknown category %q", lineNo, f[4])
		}
		a, err := catalogMeta(f[2], f[3], f[4], f, 5)
		if err != nil {
			return nil, nil, fmt.Errorf("appmap: catalog line %d: %w", lineNo, err)
		}
		note(f[2], a)
		if err := emit("d", strings.ToLower(f[1]), f[2], f[3], f[4]); err != nil {
			return nil, nil, err
		}
	}

	// Pass two: org rules and v2fly lists, in catalog order.
	for i, line := range lines {
		f := catalogFields(line)
		if len(f) == 0 || f[0] == "@domain" {
			continue
		}
		lineNo := i + 1
		if f[0] == "@addr" || f[0] == "@port" {
			if err := staticRule(f, lineNo, note, emit); err != nil {
				return nil, nil, err
			}
			continue
		}
		if f[0] == "@org" {
			if len(f) != 5 && len(f) != 7 {
				return nil, nil, fmt.Errorf("appmap: catalog line %d: @org wants 5 or 7 fields, got %d", lineNo, len(f))
			}
			// An empty substring is contained in every organisation.
			if f[1] == "" {
				return nil, nil, fmt.Errorf("appmap: catalog line %d: empty organisation substring", lineNo)
			}
			if !categories[f[4]] {
				return nil, nil, fmt.Errorf("appmap: catalog line %d: unknown category %q", lineNo, f[4])
			}
			a, err := catalogMeta(f[2], f[3], f[4], f, 5)
			if err != nil {
				return nil, nil, fmt.Errorf("appmap: catalog line %d: %w", lineNo, err)
			}
			note(f[2], a)
			if err := emit("o", strings.ToLower(f[1]), f[2], f[3], f[4]); err != nil {
				return nil, nil, err
			}
			continue
		}
		if len(f) != 4 && len(f) != 6 {
			return nil, nil, fmt.Errorf("appmap: catalog line %d: want 4 or 6 tab separated fields, got %d", lineNo, len(f))
		}
		list, id, name, category := f[0], f[1], f[2], f[3]
		if !categories[category] {
			return nil, nil, fmt.Errorf("appmap: catalog line %d: unknown category %q", lineNo, category)
		}
		a, err := catalogMeta(id, name, category, f, 4)
		if err != nil {
			return nil, nil, fmt.Errorf("appmap: catalog line %d: %w", lineNo, err)
		}
		note(id, a)
		var rules []rule
		if err := collectList(dataDir, list, map[string]bool{}, &rules); err != nil {
			return nil, nil, fmt.Errorf("appmap: catalog line %d: %w", lineNo, err)
		}
		for _, r := range rules {
			if err := emit(r.kind, r.name, id, name, category); err != nil {
				return nil, nil, err
			}
		}
	}

	// An app with no rule left was named by a catalog line whose domains all
	// belong to earlier apps. It would vanish from the meta without a word, so
	// it is an error that names the id.
	var lost []string
	for id := range apps {
		if !inTable[id] {
			lost = append(lost, id)
		}
	}
	if len(lost) > 0 {
		sort.Strings(lost)
		return nil, nil, fmt.Errorf("appmap: app %s has no rule left in the table: its domains all belong to earlier apps", strings.Join(lost, ", "))
	}

	meta := make(map[string]appMeta)
	for id, m := range apps {
		if inTable[id] {
			meta[id] = *m
		}
	}

	if _, err := out.Write(buf.Bytes()); err != nil {
		return nil, nil, err
	}
	return counts, meta, nil
}

// staticRule reads an @addr or @port catalog line:
//
//	@addr <cidr> <id> <name> <category> [<icon> <colour>]
//	@port <tcp|udp> <port> <id> <name> <category> [<icon> <colour>]
//
// and emits an a or p rule. A malformed range or port is an error naming the
// line, like any other malformed catalog line.
func staticRule(f []string, lineNo int, note func(string, appMeta), emit func(kind, key, id, name, category string) error) error {
	kind, key, rest := "a", "", f[2:]
	if f[0] == "@addr" {
		if len(f) != 5 && len(f) != 7 {
			return fmt.Errorf("appmap: catalog line %d: @addr wants 5 or 7 fields, got %d", lineNo, len(f))
		}
		pfx, err := netip.ParsePrefix(f[1])
		if err != nil {
			return fmt.Errorf("appmap: catalog line %d: %v", lineNo, err)
		}
		key = pfx.Masked().String()
	} else {
		if len(f) != 6 && len(f) != 8 {
			return fmt.Errorf("appmap: catalog line %d: @port wants 6 or 8 fields, got %d", lineNo, len(f))
		}
		port := f[1] + "/" + f[2]
		if _, err := enrich.ParsePort(port); err != nil {
			return fmt.Errorf("appmap: catalog line %d: %v", lineNo, err)
		}
		kind, key, rest = "p", port, f[3:]
	}
	id, name, category := rest[0], rest[1], rest[2]
	if !categories[category] {
		return fmt.Errorf("appmap: catalog line %d: unknown category %q", lineNo, category)
	}
	a, err := catalogMeta(id, name, category, rest, 3)
	if err != nil {
		return fmt.Errorf("appmap: catalog line %d: %w", lineNo, err)
	}
	note(id, a)
	return emit(kind, key, id, name, category)
}

// catalogMeta reads the id and the optional icon and colour columns, which
// start at field at. A line without them is still valid: the app gets a letter
// tile. A colour is never typed by hand: a Simple Icons brand takes its colour
// from simple-icons.json, and any other app takes none.
func catalogMeta(id, name, category string, f []string, at int) (appMeta, error) {
	a := appMeta{Name: name, Category: category}
	if !idPattern.MatchString(id) {
		return a, fmt.Errorf("app id %q: want lower case letters, digits, dots, dashes or underscores", id)
	}
	var icon, colour string
	if len(f) > at {
		icon = f[at]
	}
	if len(f) > at+1 {
		colour = f[at+1]
	}
	switch {
	case icon == "":
		if colour != "" {
			return a, fmt.Errorf("app %s: a letter tile takes no colour, got %q", id, colour)
		}
	case strings.HasPrefix(icon, "si:"):
		a.Icon, a.Slug = "si", strings.TrimPrefix(icon, "si:")
		if !slugPattern.MatchString(a.Slug) {
			return a, fmt.Errorf("icon %q: not a Simple Icons slug", icon)
		}
		hex, ok := brandColours[a.Slug]
		if !ok {
			return a, fmt.Errorf("icon %q: simple-icons.json has no colour for this slug", icon)
		}
		if colour != "" && !(colourPattern.MatchString(colour) && strings.ToUpper(colour) == hex) {
			return a, fmt.Errorf("app %s: colour %q is not the Simple Icons colour %s", id, colour, hex)
		}
		a.Color = hex
	case strings.HasPrefix(icon, "fav:"):
		a.Icon, a.Domain = "fav", strings.TrimPrefix(icon, "fav:")
		if !domainPattern.MatchString(a.Domain) {
			return a, fmt.Errorf("icon %q: not a main domain", icon)
		}
		if colour != "" {
			return a, fmt.Errorf("app %s: a favicon takes no hand written colour, got %q", id, colour)
		}
	default:
		return a, fmt.Errorf("icon %q: want si:<slug> or fav:<domain>", icon)
	}
	return a, nil
}

// readLines reads the whole catalog. It is small and the passes over it need
// every line, so it is held in memory, bounded by the catalog file itself.
func readLines(r io.Reader) ([]string, error) {
	var lines []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		lines = append(lines, strings.TrimRight(sc.Text(), "\r"))
	}
	return lines, sc.Err()
}

// catalogFields splits one catalog line into tab separated fields. Blank lines
// and comments give nil.
func catalogFields(line string) []string {
	if line == "" || strings.HasPrefix(line, "#") {
		return nil
	}
	return strings.Split(line, "\t")
}

// collectList appends the domain rules of one v2fly list, and of every list it
// includes. visited makes an include cycle end instead of recursing forever.
// Keyword and regexp rules are skipped: the monitor matches names by suffix
// and exact text, and a regular expression has no place in that table.
func collectList(dataDir, name string, visited map[string]bool, rules *[]rule) error {
	if visited[name] {
		return nil
	}
	visited[name] = true
	// A list name is a file name in the checkout. Anything with a path in it
	// would read outside the data directory.
	if name == "" || filepath.Base(name) != name {
		return fmt.Errorf("list name %q is not a file name", name)
	}
	file, err := os.Open(filepath.Join(dataDir, name))
	if err != nil {
		return fmt.Errorf("list %q: %w", name, err)
	}
	defer file.Close()

	sc := bufio.NewScanner(file)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		// The first field is the rule; anything after it is an attribute such
		// as @ads or @cn, which the monitor does not use.
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		prefix, value, found := strings.Cut(fields[0], ":")
		if !found {
			value, prefix = prefix, "domain"
		}
		value = strings.ToLower(value)
		switch prefix {
		case "domain":
			if err := addRule(rules, "d", value); err != nil {
				return err
			}
		case "full":
			if err := addRule(rules, "f", value); err != nil {
				return err
			}
		case "include":
			if err := collectList(dataDir, value, visited, rules); err != nil {
				return err
			}
		default:
			// keyword:, regexp: and any tag we do not model are skipped.
		}
	}
	return sc.Err()
}

func addRule(rules *[]rule, kind, name string) error {
	if name == "" {
		return errors.New("empty domain rule")
	}
	if len(*rules) >= enrich.MaxAppRules {
		return fmt.Errorf("more than %d rules in one catalog entry", enrich.MaxAppRules)
	}
	*rules = append(*rules, rule{kind: kind, name: name})
	return nil
}

func printCounts(counts map[string]int) {
	cats := make([]string, 0, len(counts))
	total := 0
	for c, n := range counts {
		cats = append(cats, c)
		total += n
	}
	sort.Strings(cats)
	var b bytes.Buffer
	for _, c := range cats {
		fmt.Fprintf(&b, "%-10s %d\n", c, counts[c])
	}
	fmt.Fprintf(&b, "%-10s %d\n", "total", total)
	os.Stderr.Write(b.Bytes())
}
