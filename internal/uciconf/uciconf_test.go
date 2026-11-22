package uciconf

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParseReadsSectionsOptionsAndLists(t *testing.T) {
	f, err := os.Open("../../package/netryx/files/netryx.config")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	secs, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(secs) != 1 || secs[0].Type != "netryx" || secs[0].Name != "main" {
		t.Fatalf("shipped config: %+v", secs)
	}
	if got := secs[0].Options["interface"]; !reflect.DeepEqual(got, []string{"br-lan"}) {
		t.Fatalf("interface = %v", got)
	}
	if got := secs[0].Options["ping_target"]; !reflect.DeepEqual(got, []string{""}) {
		t.Fatalf("ping_target = %v", got)
	}

	fixture := `
# a comment
config rule 'd_1'
	option name "Кухонний планшет"   # trailing comment
	option note 'has # inside'
	list target 'ru'
	list target cn

config rule
	option x 1
`
	secs, err = Parse(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(secs) != 2 {
		t.Fatalf("sections: %+v", secs)
	}
	a := secs[0]
	if a.Type != "rule" || a.Name != "d_1" {
		t.Fatalf("a = %+v", a)
	}
	if a.Options["name"][0] != "Кухонний планшет" || a.Options["note"][0] != "has # inside" {
		t.Fatalf("quoted values: %+v", a.Options)
	}
	if !reflect.DeepEqual(a.Options["target"], []string{"ru", "cn"}) {
		t.Fatalf("list: %v", a.Options["target"])
	}
	if secs[1].Name != "@rule[1]" {
		t.Fatalf("anonymous name = %q", secs[1].Name)
	}
}

func TestParseNamesTheBadLine(t *testing.T) {
	_, err := Parse(strings.NewReader("config rule 'x\n"))
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("unterminated quote: %v", err)
	}
	_, err = Parse(strings.NewReader("\n\noption a 'b'\n"))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("option before config: %v", err)
	}
	_, err = Parse(strings.NewReader("config rule 'x'\nfrobnicate a b\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("unknown keyword: %v", err)
	}
}

func TestBatchQuotesEveryValueAndRefusesUnsafeOnes(t *testing.T) {
	out, err := Batch([]Change{
		{Section: "d_1", Type: "rule", Set: map[string][]string{
			"name":   {"Кухонний планшет"},
			"target": {"ru", "cn"},
		}},
		{Section: "old", Delete: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "set netryx.d_1=rule\n" +
		"set netryx.d_1.name='Кухонний планшет'\n" +
		"delete netryx.d_1.target\n" +
		"add_list netryx.d_1.target='ru'\n" +
		"add_list netryx.d_1.target='cn'\n" +
		"delete netryx.old\n" +
		"commit netryx\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	if !strings.HasSuffix(out, "commit netryx\n") {
		t.Fatal("no commit")
	}

	bad := func(name string, c Change) {
		t.Helper()
		if _, err := Batch([]Change{c}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	set := func(k, v string) map[string][]string { return map[string][]string{k: {v}} }
	bad("quote", Change{Section: "a", Type: "rule", Set: set("n", "it's")})
	bad("newline", Change{Section: "a", Type: "rule", Set: set("n", "a\nb")})
	bad("nul", Change{Section: "a", Type: "rule", Set: set("n", "a\x00b")})
	bad("tab", Change{Section: "a", Type: "rule", Set: set("n", "a\tb")})
	bad("del", Change{Section: "a", Type: "rule", Set: set("n", "a\x7fb")})
	bad("long", Change{Section: "a", Type: "rule", Set: set("n", strings.Repeat("x", 129))})
	bad("section", Change{Section: "X-1", Type: "rule"})
	bad("empty section", Change{Section: "", Type: "rule"})
	bad("long section", Change{Section: "a" + strings.Repeat("b", 32), Type: "rule"})
	bad("option", Change{Section: "a", Type: "rule", Set: set("Bad-Opt", "v")})
	bad("type", Change{Section: "a", Type: "Ru le", Set: set("n", "v")})
	bad("delete bad", Change{Section: "X-1", Delete: true})

	if _, err := Batch([]Change{{Section: "a", Type: "rule", Set: set("n", strings.Repeat("x", 128))}}); err != nil {
		t.Errorf("128 bytes refused: %v", err)
	}
}

func TestReplaceRewritesTheWholeSection(t *testing.T) {
	out, err := Batch([]Change{{Section: "r1", Type: "rule", Replace: true,
		Set: map[string][]string{"action": {"block"}, "days": {}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "delete netryx.r1\nset netryx.r1=rule\nset netryx.r1.action='block'\ncommit netryx\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestAnEmptySliceDeletesTheOption(t *testing.T) {
	out, err := Batch([]Change{{Section: "r1", Type: "rule", Set: map[string][]string{"days": {}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "set netryx.r1=rule\ndelete netryx.r1.days\ncommit netryx\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestAListRewrittenAsOneValueIsASet(t *testing.T) {
	out, err := Batch([]Change{{Section: "r1", Type: "rule", Set: map[string][]string{"target": {"ru"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "add_list") || !strings.Contains(out, "set netryx.r1.target='ru'\n") {
		t.Fatalf("got:\n%s", out)
	}
}

func TestParseJoinsAdjacentQuotes(t *testing.T) {
	secs, err := Parse(strings.NewReader("config t 'n'\n\toption a a'b'\"c\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := secs[0].Options["a"][0]; got != "abc" {
		t.Fatalf("got %q", got)
	}
}

func TestBatchRefusesBadUTF8AndUnicodeControls(t *testing.T) {
	for _, v := range []string{"a\xffb", "a\u0085b", "a\u009fb"} {
		if _, err := Batch([]Change{{Section: "a", Type: "rule", Set: map[string][]string{"n": {v}}}}); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

// uci writes an apostrophe as '\” (close the quote, an escaped quote, open
// again), and a double-quoted value may hold \"; libuci reads a backslash
// outside single quotes as "the next character as it is". One such value
// written by the agent must not stop the whole file from being read.
func TestParseReadsBackslashesAsLibuciDoes(t *testing.T) {
	conf := "config device 'agent_x'\n" +
		"\toption name 'it'\\''s'\n" +
		"\toption quoted \"a\\\"b\"\n" +
		"\toption bare a\\ b\n" +
		"\toption single 'a\\b'\n" +
		"\toption joined 'x'\\\n" +
		"'y'\n" +
		"config rule 'r1'\n" +
		"\toption what 'all'\n"
	secs, err := Parse(strings.NewReader(conf))
	if err != nil {
		t.Fatal(err)
	}
	if len(secs) != 2 || secs[1].Name != "r1" || secs[1].Options["what"][0] != "all" {
		t.Fatalf("sections: %+v", secs)
	}
	for k, want := range map[string]string{"name": "it's", "quoted": `a"b`, "bare": "a b", "single": `a\b`, "joined": "xy"} {
		if got := secs[0].Options[k]; len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	_, err = Parse(strings.NewReader("config rule 'a'\n\toption b 'c'\\"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("a backslash ending the file: %v", err)
	}
}

// libuci accepts a package line at the top of a file (uci export writes it);
// it names the file and is no section.
func TestPackageLinesAreRead(t *testing.T) {
	secs, err := Parse(strings.NewReader("package firewall\n\nconfig redirect\n\toption name 'x'\n"))
	if err != nil || len(secs) != 1 || secs[0].Type != "redirect" {
		t.Fatalf("Parse = %+v, %v", secs, err)
	}
}
