package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"netryx/internal/enrich"
)

// writeLists lays out a v2fly style data directory: one file per list, named
// after the list, one rule per line.
func writeLists(t *testing.T, lists map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range lists {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// setBrands gives a test the Simple Icons colours it needs, and restores the
// previous ones when the test ends.
func setBrands(t *testing.T, brands map[string]string) {
	t.Helper()
	old := brandColours
	brandColours = brands
	t.Cleanup(func() { brandColours = old })
}

// fixtureLists covers each rule form the generator must handle, and a cycle.
// Keyword and regexp rules must not reach the output; the attribute tag on
// a.com must be stripped.
var fixtureLists = map[string]string{
	"a": "include:b\n" +
		"keyword:x\n" +
		"regexp:.*\n" +
		"full:f.a.com\n" +
		"a.com @ads\n" +
		"domain:c.a.com # trailing comment\n",
	"b": "b.com\n",
	"c": "include:c\nc.example\n",
}

func TestGeneratorFollowsIncludesAndSkipsRegex(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	catalog := "a\tapp\tApp\tother\n" +
		"c\tcyc\tCycle\tother\n" +
		"@org\tNETFLIX\tnetflix\tNetflix\tvideo\n"
	var out bytes.Buffer
	if _, _, err := generate(dir, strings.NewReader(catalog), &out); err != nil {
		t.Fatalf("generate: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"d\ta.com\tapp\tApp\tother\n",
		"f\tf.a.com\tapp\tApp\tother\n",
		"d\tb.com\tapp\tApp\tother\n",
		"d\tc.a.com\tapp\tApp\tother\n",
		"d\tc.example\tcyc\tCycle\tother\n",
		"o\tnetflix\tnetflix\tNetflix\tvideo\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output lacks %q; got:\n%s", want, got)
		}
	}
	for _, bad := range []string{"keyword", "regexp", ".*", "@ads"} {
		if strings.Contains(got, bad) {
			t.Fatalf("output contains %q, which must not reach the table; got:\n%s", bad, got)
		}
	}
}

func TestGeneratorOutputLoadsAsAnAppMap(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	catalog := "a\tapp\tApp\tother\n" +
		"@org\tNETFLIX\tnetflix\tNetflix\tvideo\n"
	var out bytes.Buffer
	if _, _, err := generate(dir, strings.NewReader(catalog), &out); err != nil {
		t.Fatalf("generate: %v", err)
	}
	m, err := enrich.LoadAppMap(&out)
	if err != nil {
		t.Fatalf("the generated file does not load: %v", err)
	}
	if app := m.Resolve("www.b.com", ""); app.ID != "app" {
		t.Fatalf("Resolve(www.b.com) = %+v; want the app from the included list", app)
	}
	if app := m.Resolve("unknown.example", "AS2906 NETFLIX-ASN"); app.ID != "netflix" {
		t.Fatalf("Resolve by organisation = %+v; want netflix", app)
	}
}

func TestGeneratorRefusesAMissingList(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	_, _, err := generate(dir, strings.NewReader("nope\tx\tX\tother\n"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("generate accepted a catalog entry naming a list that does not exist")
	}
}

func TestGeneratorRefusesAnUnknownCategory(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	_, _, err := generate(dir, strings.NewReader("a\tapp\tApp\tvidoe\n"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("generate accepted the category \"vidoe\"")
	}
}

func TestTheNewCategoriesAreAccepted(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	for _, cat := range []string{"travel", "payments", "smarthome", "dev"} {
		if _, _, err := generate(dir, strings.NewReader("a\tapp\tApp\t"+cat+"\n"), &bytes.Buffer{}); err != nil {
			t.Fatalf("generate refused the category %q: %v", cat, err)
		}
	}
}

func TestGeneratorRefusesAMalformedCatalogLine(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	_, _, err := generate(dir, strings.NewReader("a\tapp\tApp\n"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("generate accepted a catalog line with three fields")
	}
}

func TestAFailedGenerationWritesNothing(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	// The first entry is valid and would have written rules before the
	// second entry fails, so a streaming writer would leave a partial table.
	catalogText := "a\tapp\tApp\tother\nnope\tx\tX\tother\n"
	var out bytes.Buffer
	if _, _, err := generate(dir, strings.NewReader(catalogText), &out); err == nil {
		t.Fatal("generate accepted a catalog naming a list that does not exist")
	}
	if out.Len() != 0 {
		t.Fatalf("a failed generate wrote %d bytes; want none", out.Len())
	}

	catalog := filepath.Join(t.TempDir(), "catalog.tsv")
	if err := os.WriteFile(catalog, []byte(catalogText), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "appmap.tsv")
	if _, err := writeTable(dir, catalog, target, ""); err == nil {
		t.Fatal("writeTable accepted a missing list")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("a failed run created %s", target)
	}

	// A table from an earlier good run must survive a failed run unchanged.
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeTable(dir, catalog, target, ""); err == nil {
		t.Fatal("writeTable accepted a missing list on the second run")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old\n" {
		t.Fatalf("a failed run changed the existing table to %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("a failed run left %d files in the output directory; want only the table", len(entries))
	}
}

func TestHandWrittenDomainsWinOverLists(t *testing.T) {
	dir := writeLists(t, map[string]string{
		"microsoft": "teams.microsoft.com\noutlook.com\n",
	})
	// The hand written line comes after the list on purpose: it must win
	// because hand written rules are emitted first, not because of order.
	catalog := "microsoft\tmicrosoft\tMicrosoft\tcloud\n" +
		"@domain\tteams.microsoft.com\tteams\tMicrosoft Teams\tcalls\n"
	var out bytes.Buffer
	if _, _, err := generate(dir, strings.NewReader(catalog), &out); err != nil {
		t.Fatalf("generate: %v", err)
	}
	m, err := enrich.LoadAppMap(&out)
	if err != nil {
		t.Fatalf("the generated file does not load: %v", err)
	}
	if app := m.Resolve("teams.microsoft.com", ""); app.ID != "teams" || app.Category != "calls" {
		t.Fatalf("Resolve(teams.microsoft.com) = %+v; want the hand written teams rule", app)
	}
	if app := m.Resolve("outlook.com", ""); app.ID != "microsoft" {
		t.Fatalf("Resolve(outlook.com) = %+v; want the list rule microsoft", app)
	}
}

func TestAnEmptyOrgRuleIsRejected(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	_, _, err := generate(dir, strings.NewReader("@org\t\tall\tAll\tother\n"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("generate accepted an @org line with an empty substring, which matches every organisation")
	}
}

func TestAnAppIdOutsideThePatternIsRefused(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	for _, id := range []string{"Bad", "x/y", "-lead"} {
		if _, _, err := generate(dir, strings.NewReader("a\t"+id+"\tApp\tother\n"), &bytes.Buffer{}); err == nil {
			t.Fatalf("generate accepted the app id %q", id)
		}
	}
}

func TestAnAppThatLosesAllItsRulesIsAnError(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	// b.com is taken by app, which comes first, so other2 would have no rule
	// and would silently vanish from the meta.
	catalog := "a\tapp\tApp\tother\n" +
		"b\tother2\tOther\tother\n"
	_, _, err := generate(dir, strings.NewReader(catalog), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "other2") {
		t.Fatalf("generate returned %v; want an error naming other2", err)
	}
}

func TestAFavIconTakesNoHandWrittenColour(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	for _, line := range []string{
		"a\tapp\tApp\tother\tfav:example.com\t112233\n",
		"a\tapp\tApp\tother\t\t112233\n",
	} {
		if _, _, err := generate(dir, strings.NewReader(line), &bytes.Buffer{}); err == nil {
			t.Fatalf("generate accepted a hand written colour on %q", line)
		}
	}
}

func TestASimpleIconColourComesFromTheBrandFile(t *testing.T) {
	setBrands(t, map[string]string{"github": "181717"})
	dir := writeLists(t, fixtureLists)
	var out bytes.Buffer
	_, meta, err := generate(dir, strings.NewReader("a\tapp\tApp\tother\tsi:github\t\n"), &out)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := meta["app"].Color; got != "181717" {
		t.Fatalf("colour = %q; want the Simple Icons colour 181717", got)
	}
}

func TestASimpleIconColourThatDisagreesIsRefused(t *testing.T) {
	setBrands(t, map[string]string{"github": "181717"})
	dir := writeLists(t, fixtureLists)
	if _, _, err := generate(dir, strings.NewReader("a\tapp\tApp\tother\tsi:github\t000000\n"), &bytes.Buffer{}); err == nil {
		t.Fatal("generate accepted a colour that is not the Simple Icons colour")
	}
}

func TestASimpleIconWithNoBrandColourIsAnError(t *testing.T) {
	setBrands(t, map[string]string{})
	dir := writeLists(t, fixtureLists)
	if _, _, err := generate(dir, strings.NewReader("a\tapp\tApp\tother\tsi:github\t\n"), &bytes.Buffer{}); err == nil {
		t.Fatal("generate accepted a Simple Icons slug with no brand colour")
	}
}

func TestBrandSlugsFollowTheTitleRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "simple-icons.json")
	raw := `[{"title":"Booking.com","hex":"003580"},{"title":"Last.fm","hex":"d51007"},{"title":"Booking.com","hex":"FFFFFF"}]`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadBrands(path)
	if err != nil {
		t.Fatalf("loadBrands: %v", err)
	}
	want := map[string]string{"bookingdotcom": "003580", "lastdotfm": "D51007"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadBrands = %v; want %v", got, want)
	}
}

func TestCatalogIconAndColourColumnsReachTheMeta(t *testing.T) {
	setBrands(t, map[string]string{"github": "181717"})
	dir := writeLists(t, fixtureLists)
	// The second line emits nothing of its own, because the list is already
	// covered by the first. Its icon still belongs to app, which is in the table.
	catalog := "a\tapp\tApp\tother\t\t\n" +
		"b\tapp\tApp\tother\tsi:github\t\n" +
		"@org\tNETFLIX\tnetflix\tNetflix\tvideo\tfav:netflix.com\t\n"
	var out bytes.Buffer
	_, meta, err := generate(dir, strings.NewReader(catalog), &out)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	want := map[string]appMeta{
		"app":     {Name: "App", Category: "other", Icon: "si", Slug: "github", Color: "181717"},
		"netflix": {Name: "Netflix", Category: "video", Icon: "fav", Domain: "netflix.com", Color: ""},
	}
	if !reflect.DeepEqual(meta, want) {
		t.Fatalf("meta = %+v; want %+v", meta, want)
	}
	if strings.Contains(out.String(), "github") || strings.Contains(out.String(), "181717") {
		t.Fatalf("an icon or colour reached the table; got:\n%s", out.String())
	}
}

func TestAnIconOrIdOutsideTheFormatIsRefused(t *testing.T) {
	setBrands(t, map[string]string{"github": "181717"})
	dir := writeLists(t, fixtureLists)
	for _, line := range []string{
		"a\tapp\tApp\tother\tsi:Git Hub\t\n",
		"a\tapp\tApp\tother\tgithub\t\n",
		"a\tapp\tApp\tother\tfav:https://x.com/\t\n",
		"a\tapp\tApp\tother\tfav:localhost\t\n",
	} {
		if _, _, err := generate(dir, strings.NewReader(line), &bytes.Buffer{}); err == nil {
			t.Fatalf("generate accepted the catalog line %q", line)
		}
	}
}

func TestWriteTableWritesTheMetaBesideTheTable(t *testing.T) {
	setBrands(t, map[string]string{"github": "181717"})
	dir := writeLists(t, fixtureLists)
	catalog := filepath.Join(t.TempDir(), "catalog.tsv")
	if err := os.WriteFile(catalog, []byte("a\tapp\tApp\tother\tsi:github\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "appmap.tsv")
	metaPath := filepath.Join(t.TempDir(), "meta.json")
	if _, err := writeTable(dir, catalog, target, metaPath); err != nil {
		t.Fatalf("writeTable: %v", err)
	}
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the meta file is not JSON: %v", err)
	}
	app := got["app"]
	if app["name"] != "App" || app["category"] != "other" || app["icon"] != "si" || app["slug"] != "github" || app["color"] != "181717" {
		t.Fatalf("meta[app] = %v", app)
	}
}

func TestAddrAndPortLinesLoad(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	catalog := "@domain\tdns.google\tdoh\tDNS over HTTPS\tdoh\n" +
		"@addr\t8.8.8.8/32\tdoh\tDNS over HTTPS\tdoh\n" +
		"@addr\t2001:4860:4860::8888/128\tdoh\tDNS over HTTPS\tdoh\n" +
		"@port\tudp\t51820\tvpn\tVPN\tvpn\n" +
		"@port\ttcp\t1194\tvpn\tVPN\tvpn\n"
	var out bytes.Buffer
	if _, _, err := generate(dir, strings.NewReader(catalog), &out); err != nil {
		t.Fatalf("generate: %v", err)
	}
	m, err := enrich.LoadAppMap(&out)
	if err != nil {
		t.Fatalf("the generated file does not load: %v", err)
	}
	if got := m.Addrs("doh"); len(got) != 2 || got[0].String() != "8.8.8.8/32" {
		t.Fatalf("Addrs(doh) = %v; want 8.8.8.8/32 and the IPv6 one", got)
	}
	if got := m.Ports("vpn"); len(got) != 2 || got[0] != (enrich.Port{Proto: 17, Num: 51820}) {
		t.Fatalf("Ports(vpn) = %v; want udp 51820 then tcp 1194", got)
	}
	for _, bad := range []string{
		"@addr\t8.8.8.8/33\tdoh\tDNS over HTTPS\tdoh\n",
		"@addr\tdns.google\tdoh\tDNS over HTTPS\tdoh\n",
		"@port\tudp\t70000\tvpn\tVPN\tvpn\n",
		"@port\ticmp\t1\tvpn\tVPN\tvpn\n",
		"@port\tudp\t51820\tvpn\tVPN\n",
	} {
		_, _, err := generate(dir, strings.NewReader("@domain\tx.example\tvpn\tVPN\tvpn\n"+bad), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Fatalf("generate(%q) = %v; want an error naming line 2", bad, err)
		}
	}
}

func TestCategoriesIncludeDohAndVpn(t *testing.T) {
	dir := writeLists(t, fixtureLists)
	for _, cat := range []string{"doh", "vpn"} {
		if _, _, err := generate(dir, strings.NewReader("a\tapp\tApp\t"+cat+"\n"), &bytes.Buffer{}); err != nil {
			t.Fatalf("generate refused the category %q: %v", cat, err)
		}
	}
}
