// Package rules turns the rule sections of /etc/config/netryx into validated
// rules. A rule that fails validation is kept with its reason in Err, so the
// console can show it; the compiler skips it and the rest apply.
package rules

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"netryx/internal/uciconf"
)

// Limits on what one router keeps.
const (
	MaxRules   = 200
	MaxMembers = 256
	MaxGroups  = 32
	// MaxPeople bounds the house's people, and MaxPersonMACs one person's devices.
	MaxPeople     = 32
	MaxPersonMACs = 32
	MaxAllows     = 512
)

// Rule is one validated (or rejected) rule.
type Rule struct {
	ID       string
	Enabled  bool
	Action   string // "block" or "limit"
	What     string // country|app|category|all
	Targets  []string
	Who      string   // all|group|device
	Members  []string // lower-case MACs after group expansion
	Group    string
	Days     []time.Weekday // empty = every day
	From, To int            // minutes after local midnight, -1 = always
	Until    time.Time      // zero = none
	Minutes  int            // action limit: minutes a local day, 1 to MaxLimitMinutes
	Err      string         // "" when valid
}

// Allow is a temporary allowance. Rule empty means every rule.
type Allow struct {
	ID, Rule, Member string
	Until            time.Time
}

// Group is a named set of devices. FilteredDNS sends its members to the
// safe-search resolver (spec 6.5).
type Group struct {
	ID, Name    string
	FilteredDNS bool
}

// Person is someone in the house and the devices that are theirs. A device
// is at most one person's: a config that names it twice gives it to the
// first. A person changes no rule; it only says whose a device is.
type Person struct {
	ID, Name string
	MACs     []string
}

// Filters is the house's DNS settings (spec 5, section filters 'filters').
// Without the section everything is off, so an upgrade changes nothing until
// the owner turns it on.
type Filters struct {
	ForceDNS, BlockDoT, BlockDoH, SafeSearch bool
	YouTube                                  string // off | moderate | strict
}

// MaxLimitMinutes is a whole day.
const MaxLimitMinutes = 1440

// Device is a known device.
type Device struct {
	MAC, Name, Group string
	// Isolate keeps the device from the rest of the house (spec 6.7);
	// Approved takes it out of quarantine (spec 6.8).
	Isolate, Approved bool
}

// Alerts is what the owner hears about (spec 5, section alerts 'alerts').
// Without the section every kind is on and goes to Telegram, which falls back
// to the console when Hermes's bot is not there.
type Alerts struct {
	NewDevice, BlockedCountry, Upload bool
	Channel                           string // telegram | console
}

// DefaultAlerts is the alert settings without an alerts section.
var DefaultAlerts = Alerts{NewDevice: true, BlockedCountry: true, Upload: true, Channel: "telegram"}

// Quarantine holds devices never seen before (spec 6.8, section quarantine
// 'quarantine'); on unless the section turns it off.
type Quarantine struct{ Enabled bool }

// Config is everything FromSections read.
type Config struct {
	Rules      []Rule
	Allows     []Allow
	Groups     []Group
	People     []Person
	Devices    []Device
	Filters    Filters
	Alerts     Alerts
	Quarantine Quarantine
}

// Known answers whether a country, app or category exists.
type Known interface {
	Country(cc string) bool
	App(id string) bool
	Category(c string) bool
}

// FromSections reads the rule model. k must be non-nil. It never fails as a whole: a bad rule
// is kept with its reason in Err, an excess group or allowance is cut.
func FromSections(secs []uciconf.Section, k Known, now time.Time) Config {
	c := Config{
		Filters:    Filters{YouTube: "off"},
		Alerts:     DefaultAlerts,
		Quarantine: Quarantine{Enabled: true},
	}
	for _, s := range secs {
		// Only the sections named filters, alerts and quarantine count: the
		// agent writes agent_* sections and cannot turn the house's filters
		// off, silence its alerts or open its quarantine (R33, R34).
		switch {
		case s.Type == "filters" && s.Name == "filters":
			c.Filters = readFilters(s)
		case s.Type == "alerts" && s.Name == "alerts":
			c.Alerts = readAlerts(s)
		case s.Type == "quarantine" && s.Name == "quarantine":
			c.Quarantine = Quarantine{Enabled: enabled(first(s, "enabled"))}
		}
	}
	groups := map[string]bool{}
	for _, s := range secs {
		if s.Type != "group" || len(c.Groups) >= MaxGroups || AgentOwned(s.Name) {
			continue
		}
		c.Groups = append(c.Groups, Group{ID: s.Name, Name: first(s, "name"), FilteredDNS: enabledOff(first(s, "filtered_dns"))})
		groups[s.Name] = true
	}
	owned := map[string]bool{}
	for _, s := range secs {
		if s.Type != "person" || len(c.People) >= MaxPeople || AgentOwned(s.Name) {
			continue
		}
		p := Person{ID: s.Name, Name: first(s, "name"), MACs: []string{}}
		for _, v := range words(s, "mac") {
			if mac, ok := normMAC(v); ok && !owned[mac] && len(p.MACs) < MaxPersonMACs {
				owned[mac] = true
				p.MACs = append(p.MACs, mac)
			}
		}
		c.People = append(c.People, p)
	}
	for _, s := range secs {
		if s.Type != "device" || AgentOwned(s.Name) {
			continue
		}
		mac, ok := normMAC(first(s, "mac"))
		if !ok {
			continue
		}
		c.Devices = append(c.Devices, Device{MAC: mac, Name: first(s, "name"), Group: first(s, "group"),
			Isolate: enabledOff(first(s, "isolate")), Approved: enabledOff(first(s, "approved"))})
	}
	names := map[string]bool{}
	counted := 0
	for _, s := range secs {
		switch s.Type {
		case "rule":
			r := parseRule(s, k, now, groups, c.Devices)
			if names[s.Name] && r.Err == "" {
				r.Err = "a rule with this name already exists"
			}
			names[s.Name] = true
			// A rule whose until has passed waits only for the next write to
			// delete it; it takes none of the 200 places meanwhile.
			if !Ended(s, now) {
				if counted >= MaxRules {
					r.Err = "more than 200 rules"
				}
				counted++
			}
			c.Rules = append(c.Rules, r)
		case "allow":
			if len(c.Allows) >= MaxAllows {
				continue
			}
			// The rule option must be present; present and empty means every rule.
			if _, has := s.Options["rule"]; !has {
				continue
			}
			mac, ok := normMAC(first(s, "member"))
			until, uok := parseUntil(first(s, "until"))
			if !ok || !uok || !until.After(now) || until.After(now.Add(MaxAllowFor)) {
				continue
			}
			c.Allows = append(c.Allows, Allow{ID: s.Name, Rule: first(s, "rule"), Member: mac, Until: until})
		}
	}
	return c
}

func parseRule(s uciconf.Section, k Known, now time.Time, groups map[string]bool, devices []Device) Rule {
	r := Rule{
		ID:      s.Name,
		Enabled: enabled(first(s, "enabled")),
		Action:  first(s, "action"),
		What:    first(s, "what"),
		Targets: words(s, "target"),
		Who:     first(s, "who"),
		Group:   first(s, "group"),
		From:    -1,
		To:      -1,
	}
	fail := func(reason string) Rule {
		if r.Err == "" {
			r.Err = reason
		}
		return r
	}
	if strings.HasPrefix(s.Name, "f_") {
		return fail("names starting with f_ are kept for the filters")
	}
	if !ValidID(s.Name) {
		return fail("a rule needs a name of lowercase Latin letters, digits and _")
	}
	switch r.Action {
	case "block":
	case "limit":
		n, err := strconv.Atoi(first(s, "minutes"))
		if err != nil || n < 1 || n > MaxLimitMinutes {
			return fail("a time limit needs 1 to 1440 minutes")
		}
		r.Minutes = n
		if r.What != "app" && r.What != "category" {
			return fail("a time limit is only for an app or a category")
		}
		if first(s, "from") != "" || first(s, "to") != "" {
			return fail("a time limit counts the whole day, no hours are needed")
		}
	default:
		if r.Action == "" {
			return fail("no action given")
		}
		return fail("unknown action " + r.Action)
	}

	switch r.What {
	case "all":
	case "country", "app", "category":
		if len(r.Targets) == 0 {
			return fail("no targets")
		}
		for _, t := range r.Targets {
			switch r.What {
			case "country":
				if !isCC(t) || !k.Country(t) {
					return fail("unknown country " + t)
				}
			case "app":
				if !k.App(t) {
					return fail("unknown app " + t)
				}
			case "category":
				if !k.Category(t) {
					return fail("unknown category " + t)
				}
			}
		}
	default:
		if r.What == "" {
			return fail("nothing to block given")
		}
		return fail("unknown rule type " + r.What)
	}

	switch r.Who {
	case "all":
	case "device":
		members := words(s, "member")
		if len(members) == 0 {
			return fail("no device given")
		}
		for _, m := range members {
			mac, ok := normMAC(m)
			if !ok {
				return fail("not a MAC: " + m)
			}
			r.Members = append(r.Members, mac)
		}
	case "group":
		if !groups[r.Group] {
			return fail("no group " + r.Group)
		}
		for _, d := range devices {
			if d.Group == r.Group {
				r.Members = append(r.Members, d.MAC)
			}
		}
		if len(r.Members) == 0 {
			return fail("the group has no devices")
		}
	default:
		if r.Who == "" {
			return fail("no one to apply to given")
		}
		return fail("unknown who: " + r.Who)
	}
	if len(r.Members) > MaxMembers {
		return fail("more than 256 devices in a rule")
	}

	for _, d := range words(s, "days") {
		wd, ok := weekdays[strings.ToLower(d)]
		if !ok {
			return fail("wrong day " + d)
		}
		r.Days = append(r.Days, wd)
	}

	from, to := first(s, "from"), first(s, "to")
	if (from == "") != (to == "") {
		return fail("both times are needed, a start and an end")
	}
	if from != "" {
		f, ok := parseClock(from)
		if !ok {
			return fail("wrong time " + from)
		}
		t, ok := parseClock(to)
		if !ok {
			return fail("wrong time " + to)
		}
		if f == t {
			return fail("the start and the end are the same")
		}
		r.From, r.To = f, t
	}

	if u := first(s, "until"); u != "" {
		until, ok := parseUntil(u)
		if !ok {
			return fail("wrong end " + u)
		}
		r.Until = until
		if !until.After(now) {
			return fail("ended")
		}
	}
	return r
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// ValidID reports whether a rule name can be used as it is inside nft names
// (set, counter and chain names) and as the key of an allowance. An anonymous
// section ("@rule[3]") has no stable name, so it is not valid, and "all" is
// taken by the every-rule allowance set allow_all (R19).
func ValidID(id string) bool { return id != "all" && idRe.MatchString(id) }

// MaxAllowFor is the longest an allowance may run from now; the API caps its
// own at the same 240 minutes, and an agent that writes the section itself is
// held to it here.
const MaxAllowFor = 240 * time.Minute

// AgentOwned reports a section named agent_*: what the router agent writes.
// The agent's grant matches section names, so a device or group section it
// wrote must not rename a device or regroup one; the model ignores them.
func AgentOwned(name string) bool { return strings.HasPrefix(name, "agent_") }

// words is every value of a list option, each split on whitespace: the router
// agent sets an option to one string (uci set), never a list, so several
// targets, members or days arrive as one value. A real list reads the same.
func words(s uciconf.Section, key string) []string {
	out := []string{}
	for _, v := range s.Options[key] {
		out = append(out, strings.Fields(v)...)
	}
	return out
}

func first(s uciconf.Section, key string) string {
	if v := s.Options[key]; len(v) > 0 {
		return strings.TrimSpace(v[0])
	}
	return ""
}

func readFilters(s uciconf.Section) Filters {
	f := Filters{
		ForceDNS:   enabledOff(first(s, "force_dns")),
		BlockDoT:   enabledOff(first(s, "block_dot")),
		BlockDoH:   enabledOff(first(s, "block_doh")),
		SafeSearch: enabledOff(first(s, "safesearch")),
		YouTube:    "off",
	}
	switch y := first(s, "youtube"); y {
	case "moderate", "strict":
		f.YouTube = y
	}
	return f
}

func readAlerts(s uciconf.Section) Alerts {
	a := Alerts{
		NewDevice:      enabled(first(s, "new_device")),
		BlockedCountry: enabled(first(s, "blocked_country")),
		Upload:         enabled(first(s, "upload")),
		Channel:        "telegram",
	}
	if first(s, "channel") == "console" {
		a.Channel = "console"
	}
	return a
}

// enabledOff reads a boolean option that is off unless it says on.
func enabledOff(v string) bool {
	switch strings.ToLower(v) {
	case "1", "on", "yes", "true", "enabled":
		return true
	}
	return false
}

func enabled(v string) bool {
	switch strings.ToLower(v) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

func isCC(s string) bool {
	return len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

// parseClock reads HH:MM into minutes after midnight.
func parseClock(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h, e1 := strconv.Atoi(s[:2])
	m, e2 := strconv.Atoi(s[3:])
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 || !digits(s[:2]) || !digits(s[3:]) {
		return 0, false
	}
	return h*60 + m, true
}

func digits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Ended says a section's until option is a time that has come: an ended
// rule or allowance, which the next write deletes.
func Ended(s uciconf.Section, now time.Time) bool {
	u, ok := parseUntil(first(s, "until"))
	return ok && !u.After(now)
}

// parseUntil reads Unix seconds (what the config stores) or RFC 3339.
func parseUntil(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// normMAC returns a lower-case, colon-separated hardware address.
// NormMAC reads a MAC as the model does: lower case, colons, six octets.
func NormMAC(s string) (string, bool) { return normMAC(s) }

func normMAC(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(s, "-", ":")))
	if len(s) != 17 {
		return "", false
	}
	for i := 0; i < 17; i++ {
		c := s[i]
		if i%3 == 2 {
			if c != ':' {
				return "", false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", false
		}
	}
	return s, true
}
