package enrich

import (
	"strings"
	"testing"
)

// appSample is the appmap format: one rule per line, tab separated. d is a
// domain suffix, f an exact name, o a substring of an ASN organisation name.
const appSample = "# comments and blank lines are skipped\n" +
	"\n" +
	"d\tgooglevideo.com\tyoutube\tYouTube\tvideo\n" +
	"d\tyoutube.com\tyoutube\tYouTube\tvideo\n" +
	"f\tmusic.youtube.com\tytmusic\tYouTube Music\tmusic\n" +
	"d\tapple.com\tapple\tApple\tcloud\n" +
	"d\ticloud.apple.com\ticloud\tiCloud\tcloud\n" +
	"o\tnetflix\tnetflix\tNetflix\tvideo\n"

func loadApps(t *testing.T, s string) *AppMap {
	t.Helper()
	m, err := LoadAppMap(strings.NewReader(s))
	if err != nil {
		t.Fatalf("LoadAppMap: %v", err)
	}
	return m
}

func TestSuffixMatchesOnLabelBoundary(t *testing.T) {
	m := loadApps(t, appSample)
	app, ok := m.Classify("rr4---sn-abc.googlevideo.com")
	if !ok || app.ID != "youtube" {
		t.Fatalf("Classify(googlevideo host) = %+v, %v; want youtube", app, ok)
	}
	if _, ok := m.Classify("evilgooglevideo.com"); ok {
		t.Fatal("Classify(evilgooglevideo.com) matched; a suffix must start on a label boundary")
	}
}

func TestExactBeatsSuffix(t *testing.T) {
	m := loadApps(t, appSample)
	app, ok := m.Classify("music.youtube.com")
	if !ok || app.ID != "ytmusic" {
		t.Fatalf("Classify(music.youtube.com) = %+v, %v; want the exact ytmusic rule", app, ok)
	}
	app, ok = m.Classify("www.youtube.com")
	if !ok || app.ID != "youtube" {
		t.Fatalf("Classify(www.youtube.com) = %+v, %v; want the suffix youtube rule", app, ok)
	}
}

func TestLongestSuffixWins(t *testing.T) {
	m := loadApps(t, appSample)
	app, ok := m.Classify("x.icloud.apple.com")
	if !ok || app.ID != "icloud" {
		t.Fatalf("Classify(x.icloud.apple.com) = %+v, %v; want icloud over apple", app, ok)
	}
	app, ok = m.Classify("developer.apple.com")
	if !ok || app.ID != "apple" {
		t.Fatalf("Classify(developer.apple.com) = %+v, %v; want apple", app, ok)
	}
}

func TestCaseAndTrailingDotIgnored(t *testing.T) {
	m := loadApps(t, appSample)
	app, ok := m.Classify("WWW.YouTube.COM.")
	if !ok || app.ID != "youtube" {
		t.Fatalf("Classify(WWW.YouTube.COM.) = %+v, %v; want youtube", app, ok)
	}
	app, ok = m.Classify("Music.Youtube.com.")
	if !ok || app.ID != "ytmusic" {
		t.Fatalf("Classify(Music.Youtube.com.) = %+v, %v; want ytmusic", app, ok)
	}
}

func TestEmptyNameMatchesNothing(t *testing.T) {
	m := loadApps(t, appSample)
	if _, ok := m.Classify(""); ok {
		t.Fatal("Classify(\"\") matched a rule")
	}
}

func TestResolveFallsBackToOrganisation(t *testing.T) {
	m := loadApps(t, appSample)
	app := m.Resolve("unknown.example", "NETFLIX-ASN")
	if app.ID != "netflix" || app.Category != "video" {
		t.Fatalf("Resolve by org NETFLIX-ASN = %+v; want netflix in video", app)
	}
	app = m.Resolve("unknown.example", "SOMEONE-ELSE")
	want := App{ID: "unknown", Name: "Unknown · SOMEONE-ELSE", Category: "unknown"}
	if app != want {
		t.Fatalf("Resolve with an org no rule covers = %+v; want %+v", app, want)
	}
	if app := m.Resolve("unknown.example", ""); app != Unknown {
		t.Fatalf("Resolve with no org = %+v; want Unknown", app)
	}
}

func TestResolvePrefersTheNameOverTheOrganisation(t *testing.T) {
	m := loadApps(t, appSample)
	app := m.Resolve("www.youtube.com", "NETFLIX-ASN")
	if app.ID != "youtube" {
		t.Fatalf("Resolve(youtube name, netflix org) = %+v; want the name to win", app)
	}
}

func TestNilMapBehavesAsEmpty(t *testing.T) {
	var m *AppMap
	if _, ok := m.Classify("www.youtube.com"); ok {
		t.Fatal("nil AppMap classified a name")
	}
	if app := m.Resolve("www.youtube.com", ""); app != Unknown {
		t.Fatalf("nil AppMap Resolve = %+v; want Unknown", app)
	}
}

func TestLoadRejectsAMalformedLine(t *testing.T) {
	if _, err := LoadAppMap(strings.NewReader("d\tyoutube.com\tyoutube\n")); err == nil {
		t.Fatal("LoadAppMap accepted a line with four fields instead of five")
	}
	if _, err := LoadAppMap(strings.NewReader("x\tyoutube.com\tyoutube\tYouTube\tvideo\n")); err == nil {
		t.Fatal("LoadAppMap accepted an unknown rule kind")
	}
}

func TestLoadRefusesMoreRulesThanTheLimit(t *testing.T) {
	saved := maxAppRules
	maxAppRules = 2
	t.Cleanup(func() { maxAppRules = saved })
	three := "d\ta.com\ta\tA\tother\nd\tb.com\tb\tB\tother\nd\tc.com\tc\tC\tother\n"
	if _, err := LoadAppMap(strings.NewReader(three)); err == nil {
		t.Fatal("LoadAppMap loaded three rules past a limit of two")
	}
}

func TestAnEmptyOrgRuleIsRejected(t *testing.T) {
	// An empty substring is contained in every organisation, so one such
	// line would label all traffic with its app.
	if _, err := LoadAppMap(strings.NewReader("o\t\tall\tAll\tother\n")); err == nil {
		t.Fatal("LoadAppMap accepted an org rule with an empty substring")
	}
}

func TestAppsCanBeLookedUpByID(t *testing.T) {
	// A second rule for youtube with another name must not replace the first.
	m := loadApps(t, appSample+"d\tyoutube.org\tyoutube\tOther name\tother\n")
	app, ok := m.ByID("youtube")
	if !ok || app.Name != "YouTube" || app.Category != "video" {
		t.Fatalf("ByID(youtube) = %+v, %v; want YouTube in video from the first rule", app, ok)
	}
	if app, ok := m.ByID("ytmusic"); !ok || app.Name != "YouTube Music" {
		t.Fatalf("ByID(ytmusic) = %+v, %v; want YouTube Music", app, ok)
	}
	if _, ok := m.ByID("nope"); ok {
		t.Fatal("ByID(nope) found an app; an id no rule names must not be found")
	}
	var none *AppMap
	if _, ok := none.ByID("youtube"); ok {
		t.Fatal("a nil AppMap found an app; it must know none")
	}
}

// The rules engine validates app ids and expands categories from this list,
// so each id is in it once, under the category ByID gives it.
func TestAppsListsEveryIDOnceInOrder(t *testing.T) {
	m, err := LoadAppMap(strings.NewReader("d\tyoutube.com\tyoutube\tYouTube\tvideo\n" +
		"d\tgooglevideo.com\tyoutube\tYouTube\tvideo\n" +
		"o\ttelegram\ttelegram\tTelegram\tmessaging\n" +
		"f\twww.netflix.com\tnetflix\tNetflix\tvideo\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := m.Apps()
	want := []App{{"netflix", "Netflix", "video"}, {"telegram", "Telegram", "messaging"}, {"youtube", "YouTube", "video"}}
	if len(got) != len(want) {
		t.Fatalf("Apps() = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Apps()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	var nilMap *AppMap
	if nilMap.Apps() != nil {
		t.Fatal("a nil map knows no app")
	}
}

func TestAppMapAnswersAddrsAndPorts(t *testing.T) {
	m := loadApps(t, appSample+
		"a\t8.8.8.8/32\tdoh\tDNS over HTTPS\tdoh\n"+
		"a\t1.1.1.1/32\tdoh\tDNS over HTTPS\tdoh\n"+
		"p\tudp/51820\tvpn\tVPN\tvpn\n"+
		"p\tudp/1194\tvpn\tVPN\tvpn\n"+
		"p\ttcp/1194\tvpn\tVPN\tvpn\n")
	if got := m.Addrs("doh"); len(got) != 2 || got[0].String() != "8.8.8.8/32" || got[1].String() != "1.1.1.1/32" {
		t.Fatalf("Addrs(doh) = %v", got)
	}
	want := []Port{{Proto: 17, Num: 51820}, {Proto: 17, Num: 1194}, {Proto: 6, Num: 1194}}
	if got := m.Ports("vpn"); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("Ports(vpn) = %v; want %v", got, want)
	}
	if m.Addrs("youtube") != nil || m.Ports("youtube") != nil {
		t.Fatal("an app without addr or port rules has some")
	}
	if a, ok := m.ByID("vpn"); !ok || a.Category != "vpn" {
		t.Fatalf("ByID(vpn) = %+v, %v", a, ok)
	}
	var nilMap *AppMap
	if nilMap.Addrs("doh") != nil || nilMap.Ports("vpn") != nil {
		t.Fatal("a nil map answered")
	}
	for _, bad := range []string{"a\tnope\tdoh\tD\tdoh\n", "p\tudp/0\tvpn\tV\tvpn\n", "p\tsctp/1\tvpn\tV\tvpn\n", "p\t51820\tvpn\tV\tvpn\n"} {
		if _, err := LoadAppMap(strings.NewReader(bad)); err == nil {
			t.Fatalf("LoadAppMap(%q) accepted a malformed rule", bad)
		}
	}
}
