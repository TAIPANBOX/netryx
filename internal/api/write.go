package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"netryx/internal/alerts"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"netryx/internal/flows"
	"netryx/internal/rules"
	"netryx/internal/uciconf"
)

// maxBody bounds a JSON body: a rule is a few hundred bytes, and 16 KiB is
// room for 256 members.
const maxBody = 16 << 10

// maxTargets bounds the targets of one rule.
const maxTargets = 64

// maxName bounds a device or group name in bytes (uci refuses a value over
// 128 bytes, and a Ukrainian letter takes two).
const maxName = 120

// ErrNotApplied is what Rules.Write returns when the batch was committed and
// the reload after it failed: the rule is in the config and shows itself as
// not applied, so the API answers 200 with the state rather than 500.
// ErrPendingChanges is what Rules.Write returns, before it writes anything,
// when uci holds uncommitted changes of netryx that are not ours.
var ErrPendingChanges = errors.New("uncommitted uci changes")

var ErrNotApplied = errors.New("written, not applied")

// ErrAgentPending is what Rules.Write returns, before it writes anything,
// while the router agent's apply of netryx waits for its confirmation: the
// rollback would restore the file as it was before it and erase this write
// (R39).
var ErrAgentPending = errors.New("an agent apply of netryx is pending")

// Rules is the router side of the write routes: the rule engine, the config
// it reads and the way a change reaches it. Write runs the batch the agent
// would run and then reloads the engine, so there is one path from a request
// to an active rule.
type Rules interface {
	State() []rules.RuleState
	// LabelName is the name labels.json still holds for a MAC ("" if none):
	// the first PUT for a device moves it into UCI.
	LabelName(mac string) string
	// Sections reads /etc/config/netryx as it is now.
	Sections() ([]uciconf.Section, error)
	// Known is the catalog the engine validates against.
	Known() rules.Known
	// Write commits the changes and reloads; ErrNotApplied (possibly wrapped)
	// means committed but not applied.
	Write([]uciconf.Change) error
	// Cut ends one connection of mac: reply says the device's side is the
	// reply tuple (a flow opened from outside). The note is "" when the cut
	// is held, else why the connection may come back.
	Cut(mac string, local, remote netip.AddrPort, proto uint8, reply bool) (string, error)
	// UsageOf is a device's use of its limited apps today.
	UsageOf(mac string) []rules.LimitUse
	// SafeDNS says whether the safe-search resolver answers, and if not why.
	SafeDNS() (bool, string)
	// Adblock reads adblock-fast's sources; SetAdblock switches one, after
	// checking its url is still the one the page listed, and restarts
	// adblock-fast in the background.
	Adblock() (AdblockState, error)
	SetAdblock(id, url string, enabled bool) error
	// Security is how each isolated or held device stands (spec 6.7, 6.8),
	// by MAC; Pending the devices in quarantine; AlertsState the alerts and
	// how they reach the owner (spec 6.9).
	Security() map[string]DeviceSecurity
	Pending() []rules.Pending
	AlertsState() AlertsState
}

// DeviceSecurity is a device's isolation and quarantine as the console shows
// them. LikelySame is a hint from the name the device gives itself.
type DeviceSecurity struct {
	Isolate, Quarantined bool
	LikelySame           string
	Isolation            *rules.Isolation
}

// AlertsState is what GET /api/alerts tells beside the settings.
type AlertsState struct {
	Alerts           []alerts.Alert
	TelegramReady    bool
	TelegramProblem  string
	CountryTriesSeen bool
	// QuarantineActive says the kernel holds by the quarantine now,
	// GraceEnds when it starts to (zero: not in a grace), QuarantineProblem
	// why it does not hold when it is on.
	QuarantineActive  bool
	GraceEnds         time.Time
	QuarantineProblem string
}

// AdblockSource is one of adblock-fast's lists; ID is "file_url_<n>", the
// position of its anonymous section.
type AdblockSource struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

// AdblockState is adblock-fast as the router has it.
type AdblockState struct {
	Installed bool            `json:"installed"`
	Sources   []AdblockSource `json:"sources"`
}

// The answers SetAdblock can give besides success.
var (
	ErrNoAdblock   = errors.New("adblock-fast is not installed")
	ErrNotSource   = errors.New("no such adblock-fast source")
	ErrSourceMoved = errors.New("the adblock-fast source list has changed: reload the page")
)

// adblockNote is what the page says beside the switches (measured:
// docs/stage2b-measured.md, section 4).
const adblockNote = "adblock-fast will restart the router's DNS, about 20 s"

// WithRules serves the rule routes and the writes behind them.
func WithRules(r Rules) Option { return func(s *server) { s.wr = r } }

func (s *server) writeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/rules", s.routeM(methods{http.MethodGet: s.rulesList, http.MethodPost: s.ruleCreate}))
	mux.HandleFunc("/api/rules/{id}", s.routeM(methods{http.MethodPut: s.ruleReplace, http.MethodDelete: s.ruleDelete}))
	mux.HandleFunc("/api/allow", s.routeM(methods{http.MethodGet: s.allowList, http.MethodPost: s.allowCreate}))
	mux.HandleFunc("/api/allow/{id}", s.routeM(methods{http.MethodDelete: s.allowDelete}))
	mux.HandleFunc("/api/groups", s.routeM(methods{http.MethodGet: s.groupsList}))
	mux.HandleFunc("/api/groups/{id}", s.routeM(methods{http.MethodPut: s.groupPut, http.MethodDelete: s.groupDelete}))
	mux.HandleFunc("/api/people", s.routeM(methods{http.MethodGet: s.peopleList}))
	mux.HandleFunc("/api/people/{id}", s.routeM(methods{http.MethodPut: s.personPut, http.MethodDelete: s.personDelete}))
	mux.HandleFunc("/api/devices/{mac}", s.routeM(methods{http.MethodGet: s.device, http.MethodPut: s.devicePut}))
	mux.HandleFunc("/api/devices/{mac}/conns/cut", s.routeM(methods{http.MethodPost: s.connCut}))
	mux.HandleFunc("/api/filters", s.routeM(methods{http.MethodGet: s.filtersGet, http.MethodPut: s.filtersPut}))
	mux.HandleFunc("/api/usage", s.routeM(methods{http.MethodGet: s.usage}))
	mux.HandleFunc("/api/adblock", s.routeM(methods{http.MethodGet: s.adblockGet}))
	mux.HandleFunc("/api/adblock/{id}", s.routeM(methods{http.MethodPut: s.adblockPut}))
	mux.HandleFunc("/api/alerts", s.routeM(methods{http.MethodGet: s.alertsGet, http.MethodPut: s.alertsPut}))
	mux.HandleFunc("/api/quarantine", s.routeM(methods{http.MethodPut: s.quarantinePut}))
}

func bad(msg string) error { return httpError{http.StatusBadRequest, msg} }

// body reads one JSON value of at most 16 KiB into v and refuses unknown
// fields and anything after the value.
func body(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return bad("the request body could not be read")
	}
	if len(b) > maxBody {
		return httpError{http.StatusRequestEntityTooLarge, "the request body is larger than 16 KiB"}
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return bad("the request body is not JSON of the expected shape: " + err.Error())
	}
	if _, err := dec.Token(); err != io.EOF {
		return bad("the request body has extra data after the JSON")
	}
	return nil
}

// randHex is eight random hex digits; tests replace it.
var randHex = func() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("api: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// newID is prefix and eight random hex digits.
func newID(prefix string) string { return prefix + randHex() }

// ---- the would-be config --------------------------------------------------

// applyChanges is what uci would hold after the batch: the same semantics as
// uciconf.Batch, done on the parsed sections so the result can be validated
// before anything is written.
func applyChanges(secs []uciconf.Section, changes []uciconf.Change) []uciconf.Section {
	out := make([]uciconf.Section, len(secs))
	copy(out, secs)
	find := func(name string) int {
		for i, s := range out {
			if s.Name == name {
				return i
			}
		}
		return -1
	}
	remove := func(i int) { out = append(out[:i:i], out[i+1:]...) }
	for _, c := range changes {
		i := find(c.Section)
		if c.Delete {
			if i >= 0 {
				remove(i)
			}
			continue
		}
		if c.Replace && i >= 0 {
			remove(i)
			i = -1
		}
		var s uciconf.Section
		if i >= 0 {
			s = uciconf.Section{Type: c.Type, Name: c.Section, Options: map[string][]string{}}
			for k, v := range out[i].Options {
				s.Options[k] = v
			}
		} else {
			s = uciconf.Section{Type: c.Type, Name: c.Section, Options: map[string][]string{}}
		}
		for k, v := range c.Set {
			if len(v) == 0 {
				delete(s.Options, k)
				continue
			}
			s.Options[k] = append([]string{}, v...)
		}
		if i >= 0 {
			out[i] = s
		} else {
			out = append(out, s)
		}
	}
	return out
}

// plan is one write in progress: the config now, the config after, and the
// model's reading of both.
type plan struct {
	secs, would []uciconf.Section
	before      rules.Config
	after       rules.Config
	// notApplied is the reload's message when the write was committed and
	// the reload then failed.
	notApplied string
}

// prepare reads the config and applies the changes to it in memory. The
// caller holds s.wmu.
func (s *server) prepare(changes func(secs []uciconf.Section, cfg rules.Config) ([]uciconf.Change, error)) (plan, []uciconf.Change, error) {
	secs, err := s.wr.Sections()
	if err != nil {
		return plan{}, nil, err
	}
	k, now := s.wr.Known(), s.now()
	p := plan{secs: secs, before: rules.FromSections(secs, k, now)}
	ch, err := changes(secs, p.before)
	if err != nil {
		return plan{}, nil, err
	}
	ch = append(ch, endedSections(secs, ch, now)...)
	if _, err := uciconf.Batch(ch); err != nil {
		return plan{}, nil, bad("value not allowed: " + err.Error())
	}
	p.would = applyChanges(secs, ch)
	p.after = rules.FromSections(p.would, k, now)
	return p, ch, nil
}

// endedSections deletes the allowances and the rules whose until has come,
// so they do not pile up in the file (an ended rule would otherwise keep one
// of the 200 places). A section the changes already delete or rewrite is
// left to them: a PUT that gives an ended pause a new until keeps it.
func endedSections(secs []uciconf.Section, ch []uciconf.Change, now time.Time) []uciconf.Change {
	gone := map[string]bool{}
	for _, c := range ch {
		gone[c.Section] = true
	}
	var out []uciconf.Change
	for _, sc := range secs {
		if (sc.Type != "allow" && sc.Type != "rule") || gone[sc.Name] || !validName(sc.Name) {
			continue
		}
		if rules.Ended(sc, now) {
			out = append(out, uciconf.Change{Section: sc.Name, Delete: true})
		}
	}
	return out
}

// commit writes the changes. A reload failure after the commit is not a
// failure of the write: its message is returned beside a nil error.
func (s *server) commit(ch []uciconf.Change) (string, error) {
	err := s.wr.Write(ch)
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, ErrPendingChanges):
		return "", httpError{http.StatusConflict, "uci has unsaved changes: commit or revert them first"}
	case errors.Is(err, ErrAgentPending):
		return "", httpError{http.StatusConflict, "the agent is changing the settings, try again in a minute"}
	case errors.Is(err, ErrNotApplied):
		return strings.TrimPrefix(err.Error(), ErrNotApplied.Error()+": "), nil
	}
	return "", httpError{http.StatusInternalServerError, "the settings could not be written"}
}

// write is the whole of one change: under the lock, build the changes from
// the config as it is, check the would-be config with the model, write, and
// answer with what the engine now says.
func (s *server) write(build func([]uciconf.Section, rules.Config) ([]uciconf.Change, error),
	check func(plan) error, answer func(plan) any) (any, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	p, ch, err := s.prepare(build)
	if err != nil {
		return nil, err
	}
	if check != nil {
		if err := check(p); err != nil {
			return nil, err
		}
	}
	msg, err := s.commit(ch)
	if err != nil {
		return nil, err
	}
	p.notApplied = msg
	return answer(p), nil
}

func hasSection(secs []uciconf.Section, typ, name string) (uciconf.Section, bool) {
	for _, s := range secs {
		if s.Type == typ && s.Name == name {
			return s, true
		}
	}
	return uciconf.Section{}, false
}

// taken says a section of another type already has the name: uci would
// retype it, so a rule could leave enforcement or `main` become a group.
func taken(secs []uciconf.Section, name, typ string) bool {
	for _, s := range secs {
		if s.Name == name && s.Type != typ {
			return true
		}
	}
	return false
}

var errTaken = httpError{http.StatusConflict, "this name is already taken by another section"}

// freeID draws ids until one names no section of any type.
func freeID(secs []uciconf.Section, prefix string) (string, error) {
	for i := 0; i < 9; i++ {
		id := newID(prefix)
		if !taken(secs, id, "") {
			return id, nil
		}
	}
	return "", errors.New("api: no free id")
}

func countType(secs []uciconf.Section, typ string) int {
	n := 0
	for _, s := range secs {
		// The model ignores agent_* groups and devices, so they take no place.
		if s.Type == typ && !((typ == "group" || typ == "device") && rules.AgentOwned(s.Name)) {
			n++
		}
	}
	return n
}

// liveRules counts the rule sections that take a place: an ended one is
// deleted with this very write.
func liveRules(secs []uciconf.Section, now time.Time) int {
	n := 0
	for _, s := range secs {
		if s.Type == "rule" && !rules.Ended(s, now) {
			n++
		}
	}
	return n
}

// ---- rules ---------------------------------------------------------------

type ruleJSON struct {
	ID        string         `json:"id"`
	Enabled   bool           `json:"enabled"`
	What      string         `json:"what"`
	Targets   []string       `json:"targets"`
	Who       string         `json:"who"`
	Members   []string       `json:"members"`
	Group     string         `json:"group"`
	Days      []string       `json:"days"`
	From      string         `json:"from"`
	To        string         `json:"to"`
	Until     *int64         `json:"until"`
	ActiveNow bool           `json:"activeNow"`
	Hits      uint64         `json:"hits"`
	Applied   bool           `json:"applied"`
	Problem   string         `json:"problem"`
	Error     string         `json:"error"`
	Allows    []allowJSON    `json:"allows"`
	Action    string         `json:"action"`
	Minutes   int            `json:"minutes"`
	Used      map[string]int `json:"used"`
}

// allowJSON is a live allowance of a rule: whose, until when (Unix seconds)
// and by whom, "agent" or "owner".
type allowJSON struct {
	ID     string `json:"id"`
	Member string `json:"member"`
	Until  int64  `json:"until"`
	By     string `json:"by"`
}

var dayNames = [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func strs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func clock5(min int) string {
	if min < 0 {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", min/60, min%60)
}

func ruleView(st rules.RuleState) ruleJSON {
	r := st.Rule
	j := ruleJSON{
		ID: r.ID, Enabled: r.Enabled, What: r.What, Targets: strs(r.Targets), Who: r.Who,
		Members: strs(r.Members), Group: r.Group, Days: []string{}, From: clock5(r.From), To: clock5(r.To),
		ActiveNow: st.ActiveNow, Hits: st.Hits, Applied: st.Applied, Problem: st.Problem, Error: r.Err,
		Allows: []allowJSON{}, Action: r.Action, Minutes: r.Minutes, Used: map[string]int{},
	}
	for mac, n := range st.Used {
		j.Used[mac] = n
	}
	for _, a := range st.Allows {
		j.Allows = append(j.Allows, allowJSON{ID: a.ID, Member: a.Member, Until: a.Until.Unix(), By: a.By})
	}
	for _, d := range r.Days {
		j.Days = append(j.Days, dayNames[d%7])
	}
	if !r.Until.IsZero() {
		u := r.Until.Unix()
		j.Until = &u
	}
	return j
}

func (s *server) rulesList(*http.Request) (any, error) {
	out := []ruleJSON{}
	for _, st := range s.wr.State() {
		out = append(out, ruleView(st))
	}
	return out, nil
}

// ruleAfter is the answer to a write: the engine's state for the rule once
// it has reloaded, or the model's reading of what was written when the
// engine does not list it.
func (s *server) ruleAfter(id string, p plan) any {
	for _, st := range s.wr.State() {
		if st.ID == id {
			return ruleView(st)
		}
	}
	for _, r := range p.after.Rules {
		if r.ID == id {
			st := rules.RuleState{Rule: r}
			if p.notApplied != "" {
				st.Problem = "not applied: " + p.notApplied
			}
			return ruleView(st)
		}
	}
	return map[string]string{"id": id}
}

type ruleBody struct {
	Enabled *bool    `json:"enabled"`
	Action  string   `json:"action"`
	Minutes int      `json:"minutes"`
	What    string   `json:"what"`
	Targets []string `json:"targets"`
	Who     string   `json:"who"`
	Members []string `json:"members"`
	Group   string   `json:"group"`
	Days    []string `json:"days"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Until   *int64   `json:"until"`
}

// tidy drops repeated targets and bounds what is left.
func (b *ruleBody) tidy() error {
	seen := map[string]bool{}
	var out []string
	for _, t := range b.Targets {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) > maxTargets {
		return bad("more than 64 targets in a rule")
	}
	b.Targets = out
	return nil
}

// change is the rule as the agent writes it: the whole section, replacing
// whatever was there, so an option left out of a PUT is gone.
func (b ruleBody) change(id string) uciconf.Change {
	on := "1"
	if b.Enabled != nil && !*b.Enabled {
		on = "0"
	}
	action := "block"
	if b.Action != "" {
		action = b.Action
	}
	set := map[string][]string{"enabled": {on}, "action": {action}}
	if action == "limit" {
		set["minutes"] = []string{strconv.Itoa(b.Minutes)}
	}
	put := func(k string, v []string) {
		if len(v) > 0 {
			set[k] = v
		}
	}
	put("what", one(b.What))
	put("target", b.Targets)
	put("who", one(b.Who))
	switch b.Who {
	case "device":
		var m []string
		for _, x := range b.Members {
			m = append(m, normMAC(x))
		}
		put("member", m)
	case "group":
		put("group", one(b.Group))
	}
	var days []string
	for _, d := range b.Days {
		days = append(days, strings.ToLower(d))
	}
	put("days", days)
	put("from", one(b.From))
	put("to", one(b.To))
	if b.Until != nil && *b.Until != 0 {
		put("until", []string{strconv.FormatInt(*b.Until, 10)})
	}
	return uciconf.Change{Section: id, Type: "rule", Replace: true, Set: set}
}

func one(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// checkRule is the model's verdict on the rule the write would produce.
func checkRule(id string) func(plan) error {
	return func(p plan) error {
		for _, r := range p.after.Rules {
			if r.ID == id {
				if r.Err != "" {
					return bad(r.Err)
				}
				return nil
			}
		}
		return bad("the rule could not be read")
	}
}

func (s *server) ruleCreate(r *http.Request) (any, error) {
	var b ruleBody
	if err := body(r, &b); err != nil {
		return nil, err
	}
	if err := b.tidy(); err != nil {
		return nil, err
	}
	var id string
	return s.write(func(secs []uciconf.Section, cfg rules.Config) ([]uciconf.Change, error) {
		if liveRules(secs, s.now()) >= rules.MaxRules {
			return nil, bad("more than 200 rules")
		}
		var err error
		if id, err = freeID(secs, "r_"); err != nil {
			return nil, err
		}
		return []uciconf.Change{b.change(id)}, nil
	}, func(p plan) error { return checkRule(id)(p) }, func(p plan) any { return s.ruleAfter(id, p) })
}

func (s *server) ruleReplace(r *http.Request) (any, error) {
	id := r.PathValue("id")
	if !rules.ValidID(id) {
		return nil, bad("rule name: lowercase Latin letters, digits and _, up to 32 characters")
	}
	var b ruleBody
	if err := body(r, &b); err != nil {
		return nil, err
	}
	if err := b.tidy(); err != nil {
		return nil, err
	}
	return s.write(func(secs []uciconf.Section, cfg rules.Config) ([]uciconf.Change, error) {
		if _, ok := hasSection(secs, "rule", id); !ok {
			return nil, httpError{http.StatusNotFound, "no such rule"}
		}
		return []uciconf.Change{b.change(id)}, nil
	}, checkRule(id), func(p plan) any { return s.ruleAfter(id, p) })
}

func (s *server) ruleDelete(r *http.Request) (any, error) {
	id := r.PathValue("id")
	if !rules.ValidID(id) {
		return nil, bad("rule name: lowercase Latin letters, digits and _, up to 32 characters")
	}
	return s.write(func(secs []uciconf.Section, cfg rules.Config) ([]uciconf.Change, error) {
		if _, ok := hasSection(secs, "rule", id); !ok {
			return nil, httpError{http.StatusNotFound, "no such rule"}
		}
		ch := []uciconf.Change{{Section: id, Delete: true}}
		// An allowance that names the rule has nothing left to allow.
		for _, s := range secs {
			if s.Type == "allow" && len(s.Options["rule"]) > 0 && s.Options["rule"][0] == id {
				ch = append(ch, uciconf.Change{Section: s.Name, Delete: true})
			}
		}
		return ch, nil
	}, nil, func(plan) any { return map[string]string{"deleted": id} })
}

// ---- allowances ----------------------------------------------------------

type allowBody struct {
	Rule    string `json:"rule"`
	MAC     string `json:"mac"`
	Minutes int    `json:"minutes"`
}

type badAllowJSON struct {
	ID     string `json:"id"`
	Member string `json:"member"`
	Why    string `json:"why"`
}

// allowList answers the allowance sections of the config that open nothing,
// with why, read as the model reads the file now: the owner sees an
// allowance that does not work instead of trusting it.
func (s *server) allowList(*http.Request) (any, error) {
	secs, err := s.wr.Sections()
	if err != nil {
		return nil, err
	}
	out := struct {
		Invalid []badAllowJSON `json:"invalid"`
	}{Invalid: []badAllowJSON{}}
	for _, b := range rules.FromSections(secs, s.wr.Known(), s.now()).BadAllows {
		out.Invalid = append(out.Invalid, badAllowJSON{ID: b.ID, Member: b.Member, Why: b.Why})
	}
	return out, nil
}

func (s *server) allowCreate(r *http.Request) (any, error) {
	var b allowBody
	if err := body(r, &b); err != nil {
		return nil, err
	}
	mac, err := macOf(b.MAC)
	if err != nil {
		return nil, err
	}
	if b.Minutes < 1 || b.Minutes > 240 {
		return nil, bad("minutes must be from 1 to 240")
	}
	// Every rule is asked for as '*'; a request without a rule is a client's
	// mistake, and must not become an allowance for everything.
	if b.Rule == "" {
		return nil, bad("rule is needed: '*' for every rule, or a rule's name")
	}
	if b.Rule != rules.EveryRule && !rules.ValidID(b.Rule) {
		return nil, bad("rule name: lowercase Latin letters, digits and _, up to 32 characters, or '*' for every rule")
	}
	until := s.now().Add(time.Duration(b.Minutes) * time.Minute).Unix()
	var id string
	return s.write(func(secs []uciconf.Section, cfg rules.Config) ([]uciconf.Change, error) {
		if b.Rule != rules.EveryRule {
			if _, ok := hasSection(secs, "rule", b.Rule); !ok {
				return nil, bad("no such rule")
			}
			for _, ru := range cfg.Rules {
				if ru.ID == b.Rule && ru.What == "country" {
					return nil, bad("a country cannot be allowed for a while")
				}
			}
		}
		if len(cfg.Allows) >= rules.MaxAllows {
			return nil, bad("more than 512 allowances")
		}
		var err error
		if id, err = freeID(secs, "a_"); err != nil {
			return nil, err
		}
		// The rule option is always written, every rule as '*': uci deletes
		// an option set to an empty value, and the model drops an allowance
		// without the key.
		return []uciconf.Change{{Section: id, Type: "allow", Replace: true, Set: map[string][]string{
			"rule": {b.Rule}, "member": {mac}, "until": {strconv.FormatInt(until, 10)},
		}}}, nil
	}, nil, func(plan) any {
		return map[string]any{"id": id, "rule": b.Rule, "mac": mac, "until": until}
	})
}

func (s *server) allowDelete(r *http.Request) (any, error) {
	id := r.PathValue("id")
	if !validName(id) {
		return nil, bad("the allowance name is not valid")
	}
	return s.write(func(secs []uciconf.Section, cfg rules.Config) ([]uciconf.Change, error) {
		if _, ok := hasSection(secs, "allow", id); !ok {
			return nil, httpError{http.StatusNotFound, "no such allowance"}
		}
		return []uciconf.Change{{Section: id, Delete: true}}, nil
	}, nil, func(plan) any { return map[string]string{"deleted": id} })
}

// validName is a section name uci and the model accept.
func validName(id string) bool {
	if id == "" || len(id) > 32 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// macOf reads a six-byte hardware address in any usual spelling.
func macOf(s string) (string, error) {
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 {
		return "", bad("not a MAC address")
	}
	return hw.String(), nil
}

// ---- groups --------------------------------------------------------------

type groupJSON struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Members     []string `json:"members"`
	FilteredDNS bool     `json:"filteredDns"`
	Builtin     bool     `json:"builtin,omitempty"`
}

// newGroup is the built-in group of the devices in quarantine (spec 6.8): the
// roster's, never written.
const newGroup = "new"

var errBuiltin = httpError{http.StatusMethodNotAllowed, "the New group belongs to quarantine: approve a device to take it out"}

func (s *server) groupsList(*http.Request) (any, error) {
	secs, err := s.wr.Sections()
	if err != nil {
		return nil, err
	}
	cfg := rules.FromSections(secs, s.wr.Known(), s.now())
	out := []groupJSON{}
	for _, g := range cfg.Groups {
		j := groupJSON{ID: g.ID, Name: g.Name, Members: []string{}, FilteredDNS: g.FilteredDNS}
		for _, d := range cfg.Devices {
			if d.Group == g.ID {
				j.Members = append(j.Members, d.MAC)
			}
		}
		sort.Strings(j.Members)
		out = append(out, j)
	}
	held := groupJSON{ID: newGroup, Name: "New", Members: []string{}, Builtin: true}
	for _, p := range s.wr.Pending() {
		held.Members = append(held.Members, p.MAC)
	}
	sort.Strings(held.Members)
	if len(held.Members) == 0 {
		// An empty New is not offered anywhere a group is chosen.
		return out, nil
	}
	return append(out, held), nil
}

// cleanName makes a person's text fit a uci value: no control characters,
// an apostrophe typed as the typographic one, trimmed, at most maxName bytes.
func cleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", bad("a name cannot contain control characters")
		}
	}
	if len(s) > maxName {
		return "", bad("the name is too long")
	}
	return strings.ReplaceAll(s, "'", "’"), nil
}

func (s *server) groupPut(r *http.Request) (any, error) {
	id := r.PathValue("id")
	if id == newGroup {
		return nil, errBuiltin
	}
	if !rules.ValidID(id) {
		return nil, bad("group name: lowercase Latin letters, digits and _, up to 32 characters")
	}
	var b struct {
		Name        string `json:"name"`
		FilteredDNS *bool  `json:"filteredDns"`
	}
	if err := body(r, &b); err != nil {
		return nil, err
	}
	name, err := cleanName(b.Name)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, bad("a group name is needed")
	}
	if rules.AgentOwned(id) {
		return nil, bad("names starting with agent_ are kept for the agent")
	}
	return s.write(func(secs []uciconf.Section, cfg rules.Config) ([]uciconf.Change, error) {
		if _, ok := hasSection(secs, "group", id); !ok && countType(secs, "group") >= rules.MaxGroups {
			return nil, bad("more than 32 groups")
		}
		if taken(secs, id, "group") {
			return nil, errTaken
		}
		// A merge: whatever else the group holds stays.
		set := map[string][]string{"name": {name}}
		if b.FilteredDNS != nil {
			set["filtered_dns"] = []string{boolOpt(*b.FilteredDNS)}
		}
		return []uciconf.Change{{Section: id, Type: "group", Set: set}}, nil
	}, nil, func(plan) any { return groupJSON{ID: id, Name: name, Members: []string{}} })
}

func (s *server) groupDelete(r *http.Request) (any, error) {
	id := r.PathValue("id")
	if id == newGroup {
		return nil, errBuiltin
	}
	if !validName(id) {
		return nil, bad("the group name is not valid")
	}
	return s.write(func(secs []uciconf.Section, cfg rules.Config) ([]uciconf.Change, error) {
		if _, ok := hasSection(secs, "group", id); !ok || rules.AgentOwned(id) {
			return nil, httpError{http.StatusNotFound, "no such group"}
		}
		for _, sc := range secs {
			if sc.Type == "rule" && len(sc.Options["group"]) > 0 && sc.Options["group"][0] == id {
				return nil, bad("the group is named by rule " + sc.Name)
			}
		}
		ch := []uciconf.Change{{Section: id, Delete: true}}
		for _, sc := range secs {
			if sc.Type == "device" && !rules.AgentOwned(sc.Name) && len(sc.Options["group"]) > 0 && sc.Options["group"][0] == id {
				ch = append(ch, uciconf.Change{Section: sc.Name, Type: "device", Set: map[string][]string{"group": {}}})
			}
		}
		return ch, nil
	}, nil, func(plan) any { return map[string]string{"deleted": id} })
}

// ---- people --------------------------------------------------------------

type personJSON struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	MACs []string `json:"macs"`
}

func (s *server) peopleList(*http.Request) (any, error) {
	secs, err := s.wr.Sections()
	if err != nil {
		return nil, err
	}
	out := []personJSON{}
	for _, p := range rules.FromSections(secs, s.wr.Known(), s.now()).People {
		out = append(out, personJSON{ID: p.ID, Name: p.Name, MACs: p.MACs})
	}
	return out, nil
}

// personPut writes the whole person: its name and exactly the devices given.
// A device another person has is refused and names them, so nothing changes
// owner without the owner seeing it.
func (s *server) personPut(r *http.Request) (any, error) {
	id := r.PathValue("id")
	if !rules.ValidID(id) {
		return nil, bad("person id: lowercase Latin letters, digits and _, up to 32 characters")
	}
	if rules.AgentOwned(id) {
		return nil, bad("names starting with agent_ are kept for the agent")
	}
	var b struct {
		Name string   `json:"name"`
		MACs []string `json:"macs"`
	}
	if err := body(r, &b); err != nil {
		return nil, err
	}
	name, err := cleanName(b.Name)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, bad("a person needs a name")
	}
	macs := []string{}
	for _, v := range b.MACs {
		m, ok := rules.NormMAC(v)
		if !ok {
			return nil, bad("not a MAC address: " + strconv.Quote(v))
		}
		if !slices.Contains(macs, m) {
			macs = append(macs, m)
		}
	}
	if len(macs) > rules.MaxPersonMACs {
		return nil, bad("a person can have at most 32 devices")
	}
	return s.write(func(secs []uciconf.Section, cfg rules.Config) ([]uciconf.Change, error) {
		if _, ok := hasSection(secs, "person", id); !ok && countType(secs, "person") >= rules.MaxPeople {
			return nil, bad("more than 32 people")
		}
		if taken(secs, id, "person") {
			return nil, errTaken
		}
		for _, p := range cfg.People {
			if p.ID == id {
				continue
			}
			for _, m := range macs {
				if slices.Contains(p.MACs, m) {
					return nil, bad(m + " is already " + p.Name + "'s")
				}
			}
		}
		return []uciconf.Change{{Section: id, Type: "person", Replace: true, Set: map[string][]string{"name": {name}, "mac": macs}}}, nil
	}, nil, func(plan) any { return personJSON{ID: id, Name: name, MACs: macs} })
}

// personDelete removes the person alone: devices, groups and rules stay.
func (s *server) personDelete(r *http.Request) (any, error) {
	id := r.PathValue("id")
	if !validName(id) {
		return nil, bad("the person id is not valid")
	}
	return s.write(func(secs []uciconf.Section, cfg rules.Config) ([]uciconf.Change, error) {
		if _, ok := hasSection(secs, "person", id); !ok || rules.AgentOwned(id) {
			return nil, httpError{http.StatusNotFound, "no such person"}
		}
		return []uciconf.Change{{Section: id, Delete: true}}, nil
	}, nil, func(plan) any { return map[string]string{"deleted": id} })
}

// ---- devices -------------------------------------------------------------

type deviceBody struct {
	Name     *string `json:"name"`
	Group    *string `json:"group"`
	Isolate  *bool   `json:"isolate"`
	Approved *bool   `json:"approved"`
}

func (s *server) devicePut(r *http.Request) (any, error) {
	d, err := s.deviceFor(r)
	if err != nil {
		return nil, err
	}
	mac, err := macOf(d.MAC)
	if err != nil {
		return nil, err
	}
	var b deviceBody
	if err := body(r, &b); err != nil {
		return nil, err
	}
	if b.Name == nil && b.Group == nil && b.Isolate == nil && b.Approved == nil {
		return nil, bad("nothing to change: a name, group, isolation or approval is needed")
	}
	if b.Isolate != nil && *b.Isolate && s.requesterIs(r, mac) {
		// Isolating the device in use would cut it off from this console,
		// LuCI and SSH, and the agent cannot undo it.
		return nil, httpError{http.StatusConflict, "this is the device you are using now: isolation would cut it off from the console, LuCI and SSH. Isolate it from another device"}
	}
	set := map[string][]string{"mac": {mac}}
	if b.Isolate != nil {
		set["isolate"] = []string{boolOpt(*b.Isolate)}
	}
	if b.Approved != nil {
		set["approved"] = []string{boolOpt(*b.Approved)}
	}
	if b.Name != nil {
		n, err := cleanName(*b.Name)
		if err != nil {
			return nil, err
		}
		set["name"] = one(n)
		if n == "" {
			set["name"] = []string{}
		}
	}
	return s.write(func(secs []uciconf.Section, cfg rules.Config) ([]uciconf.Change, error) {
		if b.Group != nil {
			if *b.Group == "" {
				set["group"] = []string{}
			} else {
				if _, ok := hasSection(secs, "group", *b.Group); !ok || rules.AgentOwned(*b.Group) {
					return nil, bad("no group " + *b.Group)
				}
				set["group"] = []string{*b.Group}
			}
		}
		// The device's own section when it has one, else one named from the
		// MAC: d_ and the twelve hex digits.
		name := "d_" + strings.ReplaceAll(mac, ":", "")
		named := false
		for _, sc := range secs {
			if m, ok := normSecMAC(sc); ok && m == mac && validName(sc.Name) {
				name = sc.Name
				named = len(sc.Options["name"]) > 0 && strings.TrimSpace(sc.Options["name"][0]) != ""
				break
			}
		}
		if taken(secs, name, "device") {
			return nil, errTaken
		}
		// The first PUT for a device takes its label's name into UCI, unless
		// the request names it or UCI already does. The label goes through
		// the cleaning a submitted name gets.
		if _, has := set["name"]; !has && !named {
			if n := fitLabel(s.wr.LabelName(mac)); n != "" {
				set["name"] = []string{n}
			}
		}
		return []uciconf.Change{{Section: name, Type: "device", Set: set}}, nil
	}, nil, func(p plan) any {
		out := map[string]string{"mac": mac}
		for _, dv := range p.after.Devices {
			if dv.MAC == mac {
				out["name"], out["group"] = dv.Name, dv.Group
			}
		}
		return out
	})
}

// fitLabel makes a stored label fit a uci value instead of refusing it: no
// control characters, the typographic apostrophe, trimmed, cut at maxName
// bytes on a letter boundary.
func fitLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.ReplaceAll(strings.TrimSpace(s), "'", "\u2019")
	for len(s) > maxName {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return strings.TrimSpace(s)
}

// normSecMAC is the hardware address a device section holds.
func normSecMAC(s uciconf.Section) (string, bool) {
	// An agent_* section names no device: the model ignores it, so an owner's
	// write must never land in it.
	if s.Type != "device" || rules.AgentOwned(s.Name) || len(s.Options["mac"]) == 0 {
		return "", false
	}
	hw, err := net.ParseMAC(s.Options["mac"][0])
	if err != nil || len(hw) != 6 {
		return "", false
	}
	return hw.String(), true
}

// ---- cutting a connection ------------------------------------------------

type cutBody struct {
	Local  string `json:"local"`
	Remote string `json:"remote"`
	Proto  uint8  `json:"proto"`
}

func (s *server) connCut(r *http.Request) (any, error) {
	mac, err := macOf(r.PathValue("mac"))
	if err != nil {
		return nil, err
	}
	var b cutBody
	if err := body(r, &b); err != nil {
		return nil, err
	}
	local, err1 := netip.ParseAddrPort(b.Local)
	remote, err2 := netip.ParseAddrPort(b.Remote)
	if err1 != nil || err2 != nil || b.Proto == 0 {
		return nil, bad("local, remote (address:port) and proto are needed")
	}
	if b.Proto != 6 && b.Proto != 17 {
		return nil, bad("only TCP (6) and UDP (17) can be cut")
	}
	// Only a live flow of this MAC: the tuple in the body is a name for one
	// of the device's own connections, never an address to delete by itself.
	for _, f := range s.f.LiveOf(mac) {
		if f.Proto == b.Proto && f.Local.Addr().Unmap() == local.Addr().Unmap() && f.Local.Port() == local.Port() &&
			f.Remote.Addr().Unmap() == remote.Addr().Unmap() && f.Remote.Port() == remote.Port() {
			note, err := s.wr.Cut(mac, f.Local, f.Remote, f.Proto, f.Dir == flows.In)
			if err != nil {
				return nil, httpError{http.StatusInternalServerError, "the connection was not cut"}
			}
			s.journalCut(mac, f.Proto, f.Remote.String(), f.App.ID, note)
			// held says whether the router keeps the flow from coming back;
			// a cut it could not hold carries the engine's note.
			return cutAnswer{Cut: true, Held: note == "", Note: note}, nil
		}
	}
	return nil, httpError{http.StatusNotFound, "the device has no such live connection"}
}

type cutAnswer struct {
	Cut  bool   `json:"cut"`
	Held bool   `json:"held"`
	Note string `json:"note,omitempty"`
}

func boolOpt(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

type filtersJSON struct {
	ForceDNS   bool   `json:"forceDns"`
	BlockDoT   bool   `json:"blockDot"`
	BlockDoH   bool   `json:"blockDoh"`
	SafeSearch bool   `json:"safeSearch"`
	YouTube    string `json:"youtube"`
}

func filtersView(f rules.Filters) filtersJSON {
	return filtersJSON{ForceDNS: f.ForceDNS, BlockDoT: f.BlockDoT, BlockDoH: f.BlockDoH, SafeSearch: f.SafeSearch, YouTube: f.YouTube}
}

func (s *server) filtersGet(*http.Request) (any, error) {
	secs, err := s.wr.Sections()
	if err != nil {
		return nil, err
	}
	f := rules.FromSections(secs, s.wr.Known(), s.now()).Filters
	ok, problem := s.wr.SafeDNS()
	return struct {
		filtersJSON
		SafeDNS map[string]any `json:"safeDns"`
	}{filtersView(f), map[string]any{"healthy": ok, "problem": problem}}, nil
}

// filtersPut writes the house's filters as their own section, the whole of it
// (the agent cannot: only the section named filters counts, R33).
func (s *server) filtersPut(r *http.Request) (any, error) {
	var b filtersJSON
	if err := body(r, &b); err != nil {
		return nil, err
	}
	switch b.YouTube {
	case "", "off", "moderate", "strict":
	default:
		return nil, bad("YouTube level: off, moderate or strict")
	}
	if b.YouTube == "" {
		b.YouTube = "off"
	}
	set := map[string][]string{
		"force_dns": {boolOpt(b.ForceDNS)}, "block_dot": {boolOpt(b.BlockDoT)}, "block_doh": {boolOpt(b.BlockDoH)},
		"safesearch": {boolOpt(b.SafeSearch)}, "youtube": {b.YouTube},
	}
	return s.write(func([]uciconf.Section, rules.Config) ([]uciconf.Change, error) {
		return []uciconf.Change{{Section: "filters", Type: "filters", Replace: true, Set: set}}, nil
	}, nil, func(p plan) any { return filtersView(p.after.Filters) })
}

type usageJSON struct {
	Rule       string           `json:"rule"`
	What       string           `json:"what"`
	Targets    []string         `json:"targets"`
	Used       int              `json:"used"`
	Limit      int              `json:"limit"`
	SpentUntil *int64           `json:"spentUntil"`
	Apps       []appMinutesJSON `json:"apps"`
}

type appMinutesJSON struct {
	App  string `json:"app"`
	Used int    `json:"used"`
}

func (s *server) usage(r *http.Request) (any, error) {
	hw, err := net.ParseMAC(r.URL.Query().Get("mac"))
	if err != nil || len(hw) != 6 {
		return nil, bad("mac must be a MAC address")
	}
	mac := hw.String()
	known := false
	for _, d := range s.inv.Devices() {
		if strings.EqualFold(d.MAC, mac) {
			known = true
			break
		}
	}
	if !known {
		return nil, httpError{http.StatusNotFound, "no device with that MAC"}
	}
	out := []usageJSON{}
	for _, u := range s.wr.UsageOf(mac) {
		j := usageJSON{Rule: u.Rule, What: u.What, Targets: append([]string{}, u.Targets...), Used: u.Used, Limit: u.Limit, Apps: []appMinutesJSON{}}
		for _, a := range u.Apps {
			j.Apps = append(j.Apps, appMinutesJSON{App: a.App, Used: a.Used})
		}
		if !u.SpentUntil.IsZero() {
			v := u.SpentUntil.Unix()
			j.SpentUntil = &v
		}
		out = append(out, j)
	}
	return out, nil
}

func (s *server) adblockGet(*http.Request) (any, error) {
	st, err := s.wr.Adblock()
	if err != nil {
		return nil, err
	}
	if st.Sources == nil {
		st.Sources = []AdblockSource{}
	}
	return struct {
		AdblockState
		RestartNote string `json:"restartNote"`
	}{st, adblockNote}, nil
}

// adblockPut switches one source of adblock-fast: only its enabled option,
// and only while the source at that place is still the one the page listed.
func (s *server) adblockPut(r *http.Request) (any, error) {
	var b struct {
		URL     string `json:"url"`
		Enabled bool   `json:"enabled"`
	}
	if err := body(r, &b); err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	err := s.wr.SetAdblock(id, b.URL, b.Enabled)
	switch {
	case err == nil:
	case errors.Is(err, ErrNoAdblock), errors.Is(err, ErrNotSource):
		return nil, httpError{http.StatusNotFound, err.Error()}
	case errors.Is(err, ErrPendingChanges):
		return nil, httpError{http.StatusConflict, "adblock-fast has unsaved changes: commit or revert them first"}
	case errors.Is(err, ErrSourceMoved):
		return nil, httpError{http.StatusConflict, err.Error()}
	default:
		return nil, err
	}
	return accepted{map[string]any{"id": id, "enabled": b.Enabled, "restartNote": adblockNote}}, nil
}

// ---- alerts and quarantine ------------------------------------------------

type alertSettingsJSON struct {
	NewDevice      bool   `json:"newDevice"`
	BlockedCountry bool   `json:"blockedCountry"`
	Upload         bool   `json:"upload"`
	Channel        string `json:"channel"`
	Quarantine     bool   `json:"quarantine"`
}

func (s *server) alertsGet(*http.Request) (any, error) {
	secs, err := s.wr.Sections()
	if err != nil {
		return nil, err
	}
	cfg := rules.FromSections(secs, s.wr.Known(), s.now())
	st := s.wr.AlertsState()
	list := st.Alerts
	if list == nil {
		list = []alerts.Alert{}
	}
	return map[string]any{
		"alerts":           list,
		"telegram":         map[string]any{"ready": st.TelegramReady, "problem": st.TelegramProblem},
		"countryTriesSeen": st.CountryTriesSeen,
		"quarantineState":  quarantineStateJSON(st),
		"settings": alertSettingsJSON{NewDevice: cfg.Alerts.NewDevice, BlockedCountry: cfg.Alerts.BlockedCountry,
			Upload: cfg.Alerts.Upload, Channel: cfg.Alerts.Channel, Quarantine: cfg.Quarantine.Enabled},
	}, nil
}

// alertsPut writes the alert settings as their own section, the whole of it
// (the agent cannot: only the section named alerts counts).
func (s *server) alertsPut(r *http.Request) (any, error) {
	var b alertSettingsJSON
	if err := body(r, &b); err != nil {
		return nil, err
	}
	switch b.Channel {
	case "":
		b.Channel = "telegram"
	case "telegram", "console":
	default:
		return nil, bad("alert channel: telegram or console")
	}
	set := map[string][]string{"new_device": {boolOpt(b.NewDevice)}, "blocked_country": {boolOpt(b.BlockedCountry)},
		"upload": {boolOpt(b.Upload)}, "channel": {b.Channel}}
	return s.write(func([]uciconf.Section, rules.Config) ([]uciconf.Change, error) {
		return []uciconf.Change{{Section: "alerts", Type: "alerts", Replace: true, Set: set}}, nil
	}, nil, func(p plan) any {
		a := p.after.Alerts
		return alertSettingsJSON{NewDevice: a.NewDevice, BlockedCountry: a.BlockedCountry, Upload: a.Upload, Channel: a.Channel, Quarantine: p.after.Quarantine.Enabled}
	})
}

func (s *server) quarantinePut(r *http.Request) (any, error) {
	var b struct {
		Enabled bool `json:"enabled"`
	}
	if err := body(r, &b); err != nil {
		return nil, err
	}
	return s.write(func([]uciconf.Section, rules.Config) ([]uciconf.Change, error) {
		return []uciconf.Change{{Section: "quarantine", Type: "quarantine", Replace: true, Set: map[string][]string{"enabled": {boolOpt(b.Enabled)}}}}, nil
	}, nil, func(p plan) any { return map[string]bool{"enabled": p.after.Quarantine.Enabled} })
}

func quarantineStateJSON(st AlertsState) map[string]any {
	var grace *int64
	if !st.GraceEnds.IsZero() {
		v := st.GraceEnds.Unix()
		grace = &v
	}
	return map[string]any{"active": st.QuarantineActive, "graceEnds": grace, "problem": st.QuarantineProblem}
}

// requesterIs says the request comes from the device mac, by the address the
// inventory knows for it.
func (s *server) requesterIs(r *http.Request, mac string) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, d := range s.inv.Devices() {
		if !strings.EqualFold(d.MAC, mac) {
			continue
		}
		for _, list := range [][]string{d.IPv4, d.IPv6} {
			for _, a := range list {
				if x, err := netip.ParseAddr(a); err == nil && x.Unmap() == ip {
					return true
				}
			}
		}
	}
	return false
}
