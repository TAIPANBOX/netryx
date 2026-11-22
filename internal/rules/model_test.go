package rules

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"netryx/internal/uciconf"
)

type fake struct{}

func (fake) Country(cc string) bool { return cc == "RU" || cc == "CN" || cc == "UA" }
func (fake) App(id string) bool     { return id == "tiktok" || id == "youtube" }
func (fake) Category(c string) bool { return c == "adult" || c == "gambling" }

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func sec(typ, name string, kv ...any) uciconf.Section {
	s := uciconf.Section{Type: typ, Name: name, Options: map[string][]string{}}
	for i := 0; i < len(kv); i += 2 {
		switch v := kv[i+1].(type) {
		case string:
			s.Options[kv[i].(string)] = []string{v}
		case []string:
			s.Options[kv[i].(string)] = v
		}
	}
	return s
}

func rule(name string, kv ...any) uciconf.Section {
	base := []any{"enabled", "1", "action", "block", "what", "app", "target", []string{"tiktok"},
		"who", "device", "member", []string{"02:4e:58:00:00:03"}}
	m := map[string]any{}
	for i := 0; i < len(base); i += 2 {
		m[base[i].(string)] = base[i+1]
	}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	var all []any
	for k, v := range m {
		all = append(all, k, v)
	}
	return sec("rule", name, all...)
}

func byID(c Config, id string) Rule {
	for _, r := range c.Rules {
		if r.ID == id {
			return r
		}
	}
	return Rule{ID: "MISSING"}
}

func TestARuleIsWhatWhoAndWhen(t *testing.T) {
	c := FromSections([]uciconf.Section{
		rule("r1", "target", []string{"tiktok", "youtube"}, "days", []string{"mon", "tue"},
			"from", "21:00", "to", "07:00", "member", []string{"02:4E:58:00:00:03"}),
	}, fake{}, now)
	r := byID(c, "r1")
	if r.Err != "" || !r.Enabled || r.Action != "block" || r.What != "app" || r.Who != "device" {
		t.Fatalf("rule: %+v", r)
	}
	if !reflect.DeepEqual(r.Targets, []string{"tiktok", "youtube"}) {
		t.Fatalf("targets %v", r.Targets)
	}
	if !reflect.DeepEqual(r.Members, []string{"02:4e:58:00:00:03"}) {
		t.Fatalf("members %v", r.Members)
	}
	if r.From != 1260 || r.To != 420 {
		t.Fatalf("from %d to %d", r.From, r.To)
	}
	if !reflect.DeepEqual(r.Days, []time.Weekday{time.Monday, time.Tuesday}) {
		t.Fatalf("days %v", r.Days)
	}
	// No from/to: always.
	r2 := byID(FromSections([]uciconf.Section{rule("r2")}, fake{}, now), "r2")
	if r2.From != -1 || r2.To != -1 || len(r2.Days) != 0 || r2.Err != "" {
		t.Fatalf("always: %+v", r2)
	}
}

func TestABadRuleIsInactiveWithItsReasonAndTheRestApply(t *testing.T) {
	c := FromSections([]uciconf.Section{
		rule("bad_country", "what", "country", "target", []string{"XX"}),
		rule("bad_time", "from", "25:00", "to", "07:00"),
		rule("bad_member", "member", []string{"iphone"}),
		rule("bad_app", "target", []string{"nosuchapp"}),
		rule("good"),
	}, fake{}, now)
	want := map[string]string{
		"bad_country": "unknown country XX",
		"bad_time":    "wrong time 25:00",
		"bad_member":  "not a MAC: iphone",
		"bad_app":     "unknown app nosuchapp",
		"good":        "",
	}
	n := 0
	for id, e := range want {
		if got := byID(c, id).Err; got != e {
			t.Errorf("%s: Err %q, want %q", id, got, e)
		}
		if e != "" {
			n++
		}
	}
	bad := 0
	for _, r := range c.Rules {
		if r.Err != "" {
			bad++
		}
	}
	if bad != n || len(c.Rules) != 5 {
		t.Fatalf("bad %d rules %d", bad, len(c.Rules))
	}
}

func TestOtherBadRulesSayWhy(t *testing.T) {
	cases := []struct {
		name, err string
		s         uciconf.Section
	}{
		{"cat", "unknown category nope", rule("cat", "what", "category", "target", []string{"nope"})},
		{"same", "the start and the end are the same", rule("same", "from", "08:00", "to", "08:00")},
		{"nogroup", "no group kids", rule("nogroup", "who", "group", "group", "kids")},
		{"limit", "a time limit needs 1 to 1440 minutes", rule("limit", "action", "limit")},
		{"onlyfrom", "both times are needed, a start and an end", rule("onlyfrom", "from", "08:00")},
		{"day", "wrong day funday", rule("day", "days", []string{"funday"})},
		{"gone", "ended", rule("gone", "until", strconv.FormatInt(now.Add(-time.Minute).Unix(), 10))},
	}
	for _, c := range cases {
		if got := byID(FromSections([]uciconf.Section{c.s}, fake{}, now), c.name).Err; got != c.err {
			t.Errorf("%s: %q, want %q", c.name, got, c.err)
		}
	}
	// A pause (what all) needs no targets; a future until is kept.
	fut := now.Add(time.Hour)
	p := byID(FromSections([]uciconf.Section{rule("p", "what", "all", "target", []string{},
		"until", fut.Format(time.RFC3339))}, fake{}, now), "p")
	if p.Err != "" || !p.Until.Equal(fut) {
		t.Fatalf("pause: %+v", p)
	}
	u := byID(FromSections([]uciconf.Section{rule("u", "until", strconv.FormatInt(fut.Unix(), 10))}, fake{}, now), "u")
	if u.Err != "" || u.Until.Unix() != fut.Unix() {
		t.Fatalf("unix until: %+v", u)
	}
}

func TestGroupsExpandToTheirDevices(t *testing.T) {
	c := FromSections([]uciconf.Section{
		sec("group", "kids", "name", "Діти"),
		sec("device", "d1", "mac", "02:4E:58:00:00:03", "name", "iPhone", "group", "kids"),
		sec("device", "d2", "mac", "02-4e-58-00-00-04", "name", "iPad", "group", "kids"),
		sec("device", "d3", "mac", "02:4e:58:00:00:05", "name", "TV"),
		rule("g", "who", "group", "group", "kids", "member", []string{}),
		rule("a", "who", "all", "member", []string{}),
	}, fake{}, now)
	g := byID(c, "g")
	if g.Err != "" || g.Group != "kids" || !reflect.DeepEqual(g.Members, []string{"02:4e:58:00:00:03", "02:4e:58:00:00:04"}) {
		t.Fatalf("group rule: %+v", g)
	}
	if a := byID(c, "a"); a.Err != "" || len(a.Members) != 0 {
		t.Fatalf("all rule: %+v", a)
	}
	if len(c.Groups) != 1 || c.Groups[0] != (Group{ID: "kids", Name: "Діти"}) || len(c.Devices) != 3 ||
		c.Devices[0] != (Device{MAC: "02:4e:58:00:00:03", Name: "iPhone", Group: "kids"}) {
		t.Fatalf("groups %+v devices %+v", c.Groups, c.Devices)
	}
}

func TestAMemberListPastTheLimitIsMarked(t *testing.T) {
	var secs []uciconf.Section
	for i := 0; i < MaxMembers+1; i++ {
		mac := "02:00:00:00:" + hex2(i/256) + ":" + hex2(i%256)
		secs = append(secs, sec("device", "d"+strconv.Itoa(i), "mac", mac, "group", "big"))
	}
	secs = append(secs, sec("group", "big", "name", "Big"), rule("g", "who", "group", "group", "big", "member", []string{}))
	if got := byID(FromSections(secs, fake{}, now), "g").Err; got != "more than 256 devices in a rule" {
		t.Fatalf("Err %q", got)
	}
}

func hex2(n int) string {
	return strings.ToLower(string("0123456789abcdef"[n/16]) + string("0123456789abcdef"[n%16]))
}

func TestAPassedAllowanceIsDropped(t *testing.T) {
	c := FromSections([]uciconf.Section{
		sec("allow", "a_old", "rule", "r1", "member", "02:4e:58:00:00:03", "until", strconv.FormatInt(now.Add(-time.Second).Unix(), 10)),
		sec("allow", "a_live", "rule", "r1", "member", "02:4E:58:00:00:03", "until", strconv.FormatInt(now.Add(time.Minute).Unix(), 10)),
		sec("allow", "a_all", "rule", "", "member", "02:4e:58:00:00:04", "until", strconv.FormatInt(now.Add(time.Minute).Unix(), 10)),
		sec("allow", "a_nomac", "rule", "r1", "member", "iphone", "until", strconv.FormatInt(now.Add(time.Minute).Unix(), 10)),
	}, fake{}, now)
	if len(c.Allows) != 2 || c.Allows[0].ID != "a_live" || c.Allows[0].Member != "02:4e:58:00:00:03" ||
		c.Allows[1].ID != "a_all" || c.Allows[1].Rule != "" {
		t.Fatalf("allows %+v", c.Allows)
	}
}

func TestRulesPastTheLimitAreMarked(t *testing.T) {
	var secs []uciconf.Section
	for i := 0; i < MaxRules+3; i++ {
		secs = append(secs, rule("r"+strconv.Itoa(i)))
	}
	c := FromSections(secs, fake{}, now)
	if len(c.Rules) != MaxRules+3 {
		t.Fatalf("rules kept %d", len(c.Rules))
	}
	for i, r := range c.Rules {
		want := ""
		if i >= MaxRules {
			want = "more than 200 rules"
		}
		if r.Err != want {
			t.Fatalf("rule %d: Err %q, want %q", i, r.Err, want)
		}
	}
}

func TestGroupsAndAllowancesPastTheLimitAreCut(t *testing.T) {
	var secs []uciconf.Section
	for i := 0; i < MaxGroups+2; i++ {
		secs = append(secs, sec("group", "g"+strconv.Itoa(i), "name", "G"))
	}
	for i := 0; i < MaxAllows+2; i++ {
		secs = append(secs, sec("allow", "a"+strconv.Itoa(i), "rule", "", "member", "02:4e:58:00:00:03",
			"until", strconv.FormatInt(now.Add(time.Hour).Unix(), 10)))
	}
	c := FromSections(secs, fake{}, now)
	if len(c.Groups) != MaxGroups || len(c.Allows) != MaxAllows {
		t.Fatalf("groups %d allows %d", len(c.Groups), len(c.Allows))
	}
}

func TestOtherSectionTypesAreIgnored(t *testing.T) {
	c := FromSections([]uciconf.Section{sec("netryx", "main", "x", "1"), sec("filters", "filters"), sec("alerts", "alerts")}, fake{}, now)
	if len(c.Rules)+len(c.Allows)+len(c.Groups)+len(c.Devices) != 0 {
		t.Fatalf("%+v", c)
	}
}

func TestAGroupWithNoDevicesOpensNothing(t *testing.T) {
	c := FromSections([]uciconf.Section{
		sec("group", "kids", "name", "Діти"),
		rule("g", "who", "group", "group", "kids", "member", []string{}),
	}, fake{}, now)
	if got := byID(c, "g").Err; got != "the group has no devices" {
		t.Fatalf("Err %q", got)
	}
}

func TestADeviceWithABadMACIsLeftOutOfItsGroupAndTheRuleStillApplies(t *testing.T) {
	c := FromSections([]uciconf.Section{
		sec("group", "kids", "name", "Діти"),
		sec("device", "d1", "mac", "02:4e:58:00:00:03", "group", "kids"),
		sec("device", "d2", "mac", "not-a-mac", "group", "kids"),
		rule("g", "who", "group", "group", "kids", "member", []string{}),
	}, fake{}, now)
	g := byID(c, "g")
	if g.Err != "" || !reflect.DeepEqual(g.Members, []string{"02:4e:58:00:00:03"}) {
		t.Fatalf("rule %+v", g)
	}
	if len(c.Devices) != 1 {
		t.Fatalf("devices %+v", c.Devices)
	}
}

func TestAnAllowNeedsItsRuleOptionButMayLeaveItEmpty(t *testing.T) {
	until := strconv.FormatInt(now.Add(time.Minute).Unix(), 10)
	c := FromSections([]uciconf.Section{
		sec("allow", "a_keyless", "member", "02:4e:58:00:00:03", "until", until),
		sec("allow", "a_empty", "rule", "", "member", "02:4e:58:00:00:03", "until", until),
	}, fake{}, now)
	if len(c.Allows) != 1 || c.Allows[0].ID != "a_empty" || c.Allows[0].Rule != "" {
		t.Fatalf("allows %+v", c.Allows)
	}
}

func TestMissingOrUnknownFieldsSayWhat(t *testing.T) {
	cases := []struct {
		name, err string
		s         uciconf.Section
	}{
		{"noaction", "no action given", rule("noaction", "action", "")},
		{"badaction", "unknown action zap", rule("badaction", "action", "zap")},
		{"nowhat", "nothing to block given", rule("nowhat", "what", "")},
		{"badwhat", "unknown rule type dns", rule("badwhat", "what", "dns")},
		{"nowho", "no one to apply to given", rule("nowho", "who", "")},
		{"badwho", "unknown who: room", rule("badwho", "who", "room")},
		{"nomember", "no device given", rule("nomember", "member", []string{})},
		{"notarget", "no targets", rule("notarget", "what", "country", "target", []string{})},
	}
	for _, c := range cases {
		if got := byID(FromSections([]uciconf.Section{c.s}, fake{}, now), c.name).Err; got != c.err {
			t.Errorf("%s: %q, want %q", c.name, got, c.err)
		}
	}
}

func TestAnAllowWithAnEmptyRuleSurvivesTheConfigFile(t *testing.T) {
	until := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Add(time.Hour).Unix()
	text := fmt.Sprintf("config allow 'a1'\n\toption rule ''\n\toption member '02:4e:58:00:00:01'\n\toption until '%d'\n", until)
	read := func(s string) Config {
		secs, err := uciconf.Parse(strings.NewReader(s))
		if err != nil {
			t.Fatal(err)
		}
		return FromSections(secs, fake{}, now)
	}
	c := read(text)
	if len(c.Allows) != 1 || c.Allows[0].Rule != "" || c.Allows[0].Member != "02:4e:58:00:00:01" {
		t.Fatalf("empty rule: %+v", c.Allows)
	}
	if c := read(strings.Replace(text, "\toption rule ''\n", "", 1)); len(c.Allows) != 0 {
		t.Fatalf("no rule option must open nothing: %+v", c.Allows)
	}
}

func TestARuleNeedsALowerCaseName(t *testing.T) {
	const want = "a rule needs a name of lowercase Latin letters, digits and _"
	long := strings.Repeat("a", 33)
	for _, name := range []string{"Kids_YT", "kids-yt", "@rule[0]", "1kids", "", "kids yt", long} {
		c := FromSections([]uciconf.Section{rule(name)}, fake{}, now)
		if len(c.Rules) != 1 || c.Rules[0].Err != want {
			t.Errorf("name %q: %+v, want Err %q", name, c.Rules, want)
		}
	}
	for _, name := range []string{"kids_yt", "a", strings.Repeat("a", 32), "r2d2"} {
		c := FromSections([]uciconf.Section{rule(name)}, fake{}, now)
		if c.Rules[0].Err != "" {
			t.Errorf("name %q must be fine, got %q", name, c.Rules[0].Err)
		}
	}
}

func TestTwoRulesCannotShareAName(t *testing.T) {
	c := FromSections([]uciconf.Section{rule("kids"), rule("kids")}, fake{}, now)
	if c.Rules[0].Err != "" || c.Rules[1].Err != "a rule with this name already exists" {
		t.Fatalf("%+v", c.Rules)
	}
}

// R19: the name "all" is the every-rule allowance set's (allow_all); a rule
// called that would share it and open what it should not.
func TestARuleCannotBeCalledAll(t *testing.T) {
	c := FromSections([]uciconf.Section{rule("all")}, fake{}, time.Now())
	if r := byID(c, "all"); r.Err == "" {
		t.Fatalf("a rule named all must be refused: %+v", r)
	}
	if ValidID("all") {
		t.Fatal("ValidID(all) must be false")
	}
}

// An agent that can write one value per option (uci_apply sets a string, never
// a list) gives several targets, members or days in that one value, split on
// whitespace. A real list keeps working.
func TestOneOptionValueMayHoldAWhitespaceSeparatedList(t *testing.T) {
	c := FromSections([]uciconf.Section{
		rule("t", "target", "tiktok  youtube"),
		rule("m", "target", []string{"tiktok"}, "member", "02:4e:58:00:00:03\t02-4E-58-00-00-04 "),
		rule("d", "days", "sat sun"),
		rule("l", "target", []string{"tiktok", "youtube"}, "days", []string{"mon", "tue"}),
	}, fake{}, now)
	if r := byID(c, "t"); r.Err != "" || !reflect.DeepEqual(r.Targets, []string{"tiktok", "youtube"}) {
		t.Errorf("targets: %+v", r)
	}
	if r := byID(c, "m"); r.Err != "" || !reflect.DeepEqual(r.Members, []string{"02:4e:58:00:00:03", "02:4e:58:00:00:04"}) {
		t.Errorf("members: %+v", r)
	}
	if r := byID(c, "d"); r.Err != "" || !reflect.DeepEqual(r.Days, []time.Weekday{time.Saturday, time.Sunday}) {
		t.Errorf("days: %+v", r)
	}
	if r := byID(c, "l"); r.Err != "" || len(r.Targets) != 2 || len(r.Days) != 2 {
		t.Errorf("a list option: %+v", r)
	}
	// An allowance's member stays one MAC.
	a := FromSections([]uciconf.Section{
		sec("allow", "x", "rule", "", "member", "02:4e:58:00:00:03 02:4e:58:00:00:04", "until", strconv.FormatInt(now.Add(time.Hour).Unix(), 10)),
	}, fake{}, now)
	if len(a.Allows) != 0 {
		t.Errorf("an allowance with two MACs was taken: %+v", a.Allows)
	}
}

// The agent's grant matches section names, so netryx ignores what an agent
// section would rename or regroup: device and group sections named agent_*.
func TestAgentDeviceAndGroupSectionsAreIgnored(t *testing.T) {
	c := FromSections([]uciconf.Section{
		sec("group", "kids", "name", "Діти"),
		sec("device", "d_024e58000003", "mac", "02:4e:58:00:00:03", "name", "iPhone", "group", "kids"),
		sec("device", "agent_x", "mac", "02:4e:58:00:00:03", "name", "Renamed", "group", "agent_g"),
		sec("group", "agent_g", "name", "Mine"),
		rule("g", "who", "group", "group", "agent_g", "member", []string{}),
	}, fake{}, now)
	if len(c.Devices) != 1 || c.Devices[0] != (Device{MAC: "02:4e:58:00:00:03", Name: "iPhone", Group: "kids"}) {
		t.Errorf("devices: %+v", c.Devices)
	}
	if len(c.Groups) != 1 || c.Groups[0].ID != "kids" {
		t.Errorf("groups: %+v", c.Groups)
	}
	if r := byID(c, "g"); r.Err == "" {
		t.Errorf("an agent_g group was a group: %+v", r)
	}
}

// An allowance longer than the API's own cap of 240 minutes is dropped, so an
// agent cannot write an allowance that lasts for ever.
func TestAnAllowanceLongerThanFourHoursIsDropped(t *testing.T) {
	at := func(m int) uciconf.Section {
		return sec("allow", "a"+strconv.Itoa(m), "rule", "", "member", "02:4e:58:00:00:03",
			"until", strconv.FormatInt(now.Add(time.Duration(m)*time.Minute).Unix(), 10))
	}
	c := FromSections([]uciconf.Section{at(240), at(241)}, fake{}, now)
	if len(c.Allows) != 1 || c.Allows[0].ID != "a240" {
		t.Errorf("allows: %+v", c.Allows)
	}
}

// A rule whose until has passed is still in the file until the next write
// cleans it, and it must not take one of the 200 places meanwhile: otherwise
// pauses stop working after about 200 uses.
func TestRulesPastTheirEndDoNotCountTowardTheLimit(t *testing.T) {
	past := strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)
	var secs []uciconf.Section
	for i := 0; i < 5; i++ {
		secs = append(secs, rule("old"+strconv.Itoa(i), "until", past))
	}
	for i := 0; i < MaxRules; i++ {
		secs = append(secs, rule("r"+strconv.Itoa(i)))
	}
	c := FromSections(secs, fake{}, now)
	for _, r := range c.Rules {
		want := ""
		if strings.HasPrefix(r.ID, "old") {
			want = "ended"
		}
		if r.Err != want {
			t.Fatalf("rule %s: Err %q, want %q", r.ID, r.Err, want)
		}
	}
}

func TestFiltersAreReadAndDefaultOff(t *testing.T) {
	c := FromSections(nil, fake{}, now)
	if c.Filters != (Filters{YouTube: "off"}) {
		t.Fatalf("no filters section: %+v; want everything off", c.Filters)
	}
	c = FromSections([]uciconf.Section{sec("filters", "filters",
		"force_dns", "1", "block_dot", "1", "block_doh", "0", "safesearch", "1", "youtube", "strict")}, fake{}, now)
	want := Filters{ForceDNS: true, BlockDoT: true, SafeSearch: true, YouTube: "strict"}
	if c.Filters != want {
		t.Fatalf("Filters = %+v; want %+v", c.Filters, want)
	}
	c = FromSections([]uciconf.Section{sec("filters", "filters", "youtube", "loud")}, fake{}, now)
	if c.Filters.YouTube != "off" {
		t.Fatalf("an unknown youtube level read as %q; want off", c.Filters.YouTube)
	}
}

func TestALimitRuleNeedsAppsAndMinutes(t *testing.T) {
	c := FromSections([]uciconf.Section{
		rule("ok", "action", "limit", "minutes", "60"),
		rule("cat", "action", "limit", "what", "category", "target", []string{"gambling"}, "minutes", "1440"),
		rule("country", "action", "limit", "what", "country", "target", []string{"RU"}, "minutes", "60"),
		rule("pause", "action", "limit", "what", "all", "minutes", "60"),
		rule("zero", "action", "limit", "minutes", "0"),
		rule("big", "action", "limit", "minutes", "1441"),
		rule("none", "action", "limit"),
		rule("hours", "action", "limit", "minutes", "30", "from", "21:00", "to", "07:00"),
	}, fake{}, now)
	if r := byID(c, "ok"); r.Err != "" || r.Action != "limit" || r.Minutes != 60 {
		t.Fatalf("ok = %+v", r)
	}
	if r := byID(c, "cat"); r.Err != "" || r.Minutes != 1440 {
		t.Fatalf("cat = %+v", r)
	}
	for id, want := range map[string]string{
		"country": "a time limit is only for an app or a category",
		"pause":   "a time limit is only for an app or a category",
		"zero":    "a time limit needs 1 to 1440 minutes",
		"big":     "a time limit needs 1 to 1440 minutes",
		"none":    "a time limit needs 1 to 1440 minutes",
		"hours":   "a time limit counts the whole day, no hours are needed",
	} {
		if r := byID(c, id); r.Err != want {
			t.Fatalf("%s: Err = %q; want %q", id, r.Err, want)
		}
	}
}

func TestAGroupCanAskForFilteredDNS(t *testing.T) {
	c := FromSections([]uciconf.Section{
		sec("group", "kids", "name", "Діти", "filtered_dns", "1"),
		sec("group", "adults", "name", "Дорослі"),
	}, fake{}, now)
	if len(c.Groups) != 2 || !c.Groups[0].FilteredDNS || c.Groups[1].FilteredDNS {
		t.Fatalf("Groups = %+v; want kids filtered, adults not", c.Groups)
	}
}

func TestTheAgentCannotWriteFilters(t *testing.T) {
	c := FromSections([]uciconf.Section{
		sec("filters", "filters", "force_dns", "1", "safesearch", "1"),
		sec("filters", "agent_off", "force_dns", "0", "safesearch", "0"),
		sec("filters", "second", "force_dns", "0"),
	}, fake{}, now)
	if !c.Filters.ForceDNS || !c.Filters.SafeSearch {
		t.Fatalf("Filters = %+v; an agent or second filters section changed them", c.Filters)
	}
}

func TestNamesStartingWithFAreKeptForFilters(t *testing.T) {
	c := FromSections([]uciconf.Section{rule("f_doh"), rule("fine")}, fake{}, now)
	if r := byID(c, "f_doh"); r.Err != "names starting with f_ are kept for the filters" {
		t.Fatalf("a rule named f_doh: Err = %q", r.Err)
	}
	if r := byID(c, "fine"); r.Err != "" {
		t.Fatalf("a rule named fine: Err = %q", r.Err)
	}
}

func TestDevicesCarryIsolateAndApproved(t *testing.T) {
	c := FromSections([]uciconf.Section{
		sec("device", "d_cam", "mac", "02:4E:58:00:00:04", "isolate", "1", "approved", "1"),
		sec("device", "d_tv", "mac", "02:4e:58:00:00:01"),
	}, fake{}, now)
	if len(c.Devices) != 2 {
		t.Fatalf("Devices = %+v", c.Devices)
	}
	if d := c.Devices[0]; !d.Isolate || !d.Approved || d.MAC != "02:4e:58:00:00:04" {
		t.Fatalf("camera = %+v; want isolated and approved", d)
	}
	if d := c.Devices[1]; d.Isolate || d.Approved {
		t.Fatalf("tv = %+v; want neither", d)
	}
}

func TestAlertsAndQuarantineAreReadAndDefaultOn(t *testing.T) {
	c := FromSections(nil, fake{}, now)
	if c.Alerts != (Alerts{NewDevice: true, BlockedCountry: true, Upload: true, Channel: "telegram"}) {
		t.Fatalf("no alerts section: %+v; want all on through telegram", c.Alerts)
	}
	if !c.Quarantine.Enabled {
		t.Fatal("no quarantine section: want quarantine on")
	}
	c = FromSections([]uciconf.Section{
		sec("alerts", "alerts", "new_device", "0", "blocked_country", "1", "upload", "0", "channel", "console"),
		sec("quarantine", "quarantine", "enabled", "0"),
	}, fake{}, now)
	if c.Alerts != (Alerts{BlockedCountry: true, Channel: "console"}) || c.Quarantine.Enabled {
		t.Fatalf("Alerts = %+v, Quarantine = %+v", c.Alerts, c.Quarantine)
	}
	c = FromSections([]uciconf.Section{sec("alerts", "alerts", "channel", "pigeon")}, fake{}, now)
	if c.Alerts.Channel != "telegram" {
		t.Fatalf("an unknown channel read as %q; want telegram", c.Alerts.Channel)
	}
}

// The agent writes agent_* sections; it cannot silence the owner's alerts,
// open the quarantine, or isolate and approve devices (its device sections
// are ignored, as in 2a).
func TestTheAgentCannotSilenceAlertsOrOpenQuarantine(t *testing.T) {
	c := FromSections([]uciconf.Section{
		sec("alerts", "agent_quiet", "new_device", "0", "blocked_country", "0", "upload", "0"),
		sec("quarantine", "agent_open", "enabled", "0"),
		sec("device", "agent_cam", "mac", "02:4e:58:00:00:09", "approved", "1"),
	}, fake{}, now)
	if !c.Alerts.NewDevice || !c.Alerts.BlockedCountry || !c.Alerts.Upload || !c.Quarantine.Enabled {
		t.Fatalf("an agent section changed the owner's settings: %+v %+v", c.Alerts, c.Quarantine)
	}
	if len(c.Devices) != 0 {
		t.Fatalf("an agent device section was read: %+v", c.Devices)
	}
}
