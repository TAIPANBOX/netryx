package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"netryx/internal/alerts"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"netryx/internal/enrich"
	"netryx/internal/flows"
	"netryx/internal/rules"
	"netryx/internal/uciconf"
)

type fakeKnown struct{}

func (fakeKnown) Country(cc string) bool { return cc == "UA" || cc == "US" || cc == "RU" }
func (fakeKnown) App(id string) bool     { return id == "telegram" || id == "tiktok" }
func (fakeKnown) Category(c string) bool { return c == "video" }

type cutCall struct {
	mac           string
	local, remote netip.AddrPort
	proto         uint8
	reply         bool
}

// fakeRules is the router's side: the sections /etc/config/netryx holds, the
// state the engine reports, and a record of what the API asked for.
type fakeRules struct {
	secs   []uciconf.Section
	state  []rules.RuleState
	writes [][]uciconf.Change
	cuts   []cutCall
	// notApplied makes Write report what the adapter reports when the batch
	// was committed and the reload then failed.
	notApplied bool
	writeErr   error
	// labels are the names labels.json still holds, by MAC.
	labels map[string]string
	usage  map[string][]rules.LimitUse
	safeOK bool
	safeP  string
	ab     AdblockState
	abErr  error
	abSet  []string
	sec    map[string]DeviceSecurity
	pend   []rules.Pending
	alerts AlertsState
	// cutNote is what Cut says when the cut is not held.
	cutNote string
}

func (f *fakeRules) Security() map[string]DeviceSecurity { return f.sec }
func (f *fakeRules) Pending() []rules.Pending            { return f.pend }
func (f *fakeRules) AlertsState() AlertsState            { return f.alerts }

func (f *fakeRules) UsageOf(mac string) []rules.LimitUse { return f.usage[mac] }
func (f *fakeRules) SafeDNS() (bool, string)             { return f.safeOK, f.safeP }
func (f *fakeRules) Adblock() (AdblockState, error)      { return f.ab, nil }
func (f *fakeRules) SetAdblock(id, url string, on bool) error {
	if f.abErr != nil {
		return f.abErr
	}
	f.abSet = append(f.abSet, fmt.Sprintf("%s %s %v", id, url, on))
	return nil
}

func (f *fakeRules) LabelName(mac string) string { return f.labels[mac] }

func (f *fakeRules) State() []rules.RuleState { return f.state }
func (f *fakeRules) Sections() ([]uciconf.Section, error) {
	return f.secs, nil
}
func (f *fakeRules) Known() rules.Known { return fakeKnown{} }
func (f *fakeRules) Write(c []uciconf.Change) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writes = append(f.writes, c)
	if f.notApplied {
		return fmt.Errorf("%w: nft: syntax error", ErrNotApplied)
	}
	return nil
}
func (f *fakeRules) Cut(mac string, l, r netip.AddrPort, proto uint8, reply bool) (string, error) {
	f.cuts = append(f.cuts, cutCall{mac, l, r, proto, reply})
	return f.cutNote, nil
}

func sec(typ, name string, kv ...string) uciconf.Section {
	s := uciconf.Section{Type: typ, Name: name, Options: map[string][]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		s.Options[kv[i]] = append(s.Options[kv[i]], kv[i+1])
	}
	return s
}

// house is a router that holds a group, a device in it and a rule.
func house() *fakeRules {
	return &fakeRules{secs: []uciconf.Section{
		sec("group", "kids", "name", "Діти"),
		sec("device", "d_aabbccddee02", "mac", phone, "name", "Phone", "group", "kids"),
		sec("rule", "r_night", "enabled", "1", "action", "block", "what", "app", "target", "tiktok",
			"who", "device", "member", phone, "days", "mon", "days", "tue", "from", "21:00", "to", "07:00"),
	}}
}

func writable(t *testing.T, fr *fakeRules) (http.Handler, *fakeFlows) {
	t.Helper()
	fl, h, inv, w, ap := fixture()
	return New(fl, h, inv, w, ap, clock, nil, WithRules(fr)), fl
}

func send(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

func batchOf(t *testing.T, c []uciconf.Change) string {
	t.Helper()
	s, err := uciconf.Batch(c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	s, _ := field(decode(t, rec), "error").(string)
	return s
}

func TestARuleWrittenThroughTheAPIIsTheBatchTheAgentWouldWrite(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodPost, "/api/rules", `{"what":"app","targets":["telegram"],"who":"device","members":["AA:BB:CC:DD:EE:02"],"days":["Sat","sun"],"from":"22:00","to":"06:30"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	id, _ := field(decode(t, rec), "id").(string)
	if len(id) != 10 || !strings.HasPrefix(id, "r_") || !rules.ValidID(id) {
		t.Fatalf("id %q is not r_ and eight hex digits", id)
	}
	if len(fr.writes) != 1 {
		t.Fatalf("%d writes, want one", len(fr.writes))
	}
	// What the agent writes for that rule: the Replace change, as Batch spells it.
	want := batchOf(t, []uciconf.Change{{Section: id, Type: "rule", Replace: true, Set: map[string][]string{
		"enabled": {"1"}, "action": {"block"}, "what": {"app"}, "target": {"telegram"},
		"who": {"device"}, "member": {phone}, "days": {"sat", "sun"}, "from": {"22:00"}, "to": {"06:30"},
	}}})
	if got := batchOf(t, fr.writes[0]); got != want {
		t.Errorf("batch\n%s\nwant\n%s", got, want)
	}
	for _, line := range []string{"delete netryx." + id + "\n", "set netryx." + id + "=rule\n", "add_list netryx." + id + ".days='sat'\n", "commit netryx\n"} {
		if !strings.Contains(want, line) {
			t.Errorf("the expected batch lacks %q", line)
		}
	}
}

func TestPUTWritesTheWholeSectionSoAnOmittedOptionIsGone(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	// r_night has days and a window; the new body has neither.
	rec := send(t, h, http.MethodPut, "/api/rules/r_night", `{"what":"app","targets":["telegram"],"who":"device","members":["`+phone+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if len(fr.writes) != 1 || len(fr.writes[0]) != 1 {
		t.Fatalf("writes: %+v", fr.writes)
	}
	c := fr.writes[0][0]
	if !c.Replace || c.Section != "r_night" || c.Type != "rule" {
		t.Errorf("change %+v: a PUT replaces the section", c)
	}
	for _, k := range []string{"days", "from", "to", "until"} {
		if _, has := c.Set[k]; has {
			t.Errorf("option %s is still set: %v", k, c.Set[k])
		}
	}
	b := batchOf(t, fr.writes[0])
	if !strings.HasPrefix(b, "delete netryx.r_night\nset netryx.r_night=rule\n") {
		t.Errorf("the batch must delete the section before it sets it:\n%s", b)
	}
	if strings.Contains(b, "days") || strings.Contains(b, "from") {
		t.Errorf("the old options leaked into the batch:\n%s", b)
	}
	// An id the model cannot use is refused, and so is one that does not exist.
	for _, id := range []string{"All", "all", "9x", "r-night"} {
		if rec := send(t, h, http.MethodPut, "/api/rules/"+id, `{"what":"all","who":"all"}`); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %s: %d", id, rec.Code)
		}
	}
	if rec := send(t, h, http.MethodPut, "/api/rules/r_nothing", `{"what":"all","who":"all"}`); rec.Code != http.StatusNotFound {
		t.Errorf("PUT of a rule that is not there: %d", rec.Code)
	}
	if len(fr.writes) != 1 {
		t.Errorf("a refused PUT wrote: %d writes", len(fr.writes))
	}
}

func TestAnInvalidRuleIsRefusedBeforeItIsWritten(t *testing.T) {
	cases := []struct{ name, body, err string }{
		{"country", `{"what":"country","targets":["XX"],"who":"all"}`, "unknown country XX"},
		{"app", `{"what":"app","targets":["nope"],"who":"all"}`, "unknown app nope"},
		{"category", `{"what":"category","targets":["x"],"who":"all"}`, "unknown category x"},
		{"no targets", `{"what":"app","who":"all"}`, "no targets"},
		{"who", `{"what":"all","who":"nobody"}`, "unknown who: nobody"},
		{"no device", `{"what":"all","who":"device"}`, "no device given"},
		{"not a MAC", `{"what":"all","who":"device","members":["zz"]}`, "not a MAC: zz"},
		{"no group", `{"what":"all","who":"group","group":"ghosts"}`, "no group ghosts"},
		{"half a window", `{"what":"all","who":"all","from":"21:00"}`, "both times are needed"},
		{"bad day", `{"what":"all","who":"all","days":["funday"]}`, "wrong day funday"},
		{"ended", `{"what":"all","who":"all","until":1000}`, "ended"},
	}
	fr := house()
	h, _ := writable(t, fr)
	for _, c := range cases {
		rec := send(t, h, http.MethodPost, "/api/rules", c.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
			continue
		}
		if got := errorOf(t, rec); !strings.Contains(got, c.err) {
			t.Errorf("%s: error %q, want it to say %q", c.name, got, c.err)
		}
	}
	if len(fr.writes) != 0 {
		t.Fatalf("an invalid rule reached the writer: %+v", fr.writes)
	}
	// A value Batch would refuse is a 400 too, never half a write.
	rec := send(t, h, http.MethodPost, "/api/rules", `{"what":"app","targets":["te'legram"],"who":"all"}`)
	if rec.Code != http.StatusBadRequest || len(fr.writes) != 0 {
		t.Errorf("a quote in a value: %d, %d writes", rec.Code, len(fr.writes))
	}
	// The bound on rules is a refusal, not a rule the model marks silently.
	big := house()
	for i := 0; i < rules.MaxRules; i++ {
		big.secs = append(big.secs, sec("rule", "r_f"+strconv.Itoa(i), "action", "block", "what", "all", "who", "all"))
	}
	h2, _ := writable(t, big)
	if rec := send(t, h2, http.MethodPost, "/api/rules", `{"what":"all","who":"all"}`); rec.Code != http.StatusBadRequest || len(big.writes) != 0 {
		t.Errorf("the 201st rule: %d, %d writes", rec.Code, len(big.writes))
	}
	// A valid rule that nft will not take is still written: the console shows
	// it as not applied instead of answering 500.
	fr2 := house()
	fr2.notApplied = true
	h3, _ := writable(t, fr2)
	if rec := send(t, h3, http.MethodPost, "/api/rules", `{"what":"all","who":"all"}`); rec.Code != http.StatusOK || len(fr2.writes) != 1 {
		t.Errorf("a reload failure after the write: %d, %d writes", rec.Code, len(fr2.writes))
	}
	fr3 := house()
	fr3.writeErr = errors.New("uci: Permission denied")
	h4, _ := writable(t, fr3)
	rec = send(t, h4, http.MethodPost, "/api/rules", `{"what":"all","who":"all"}`)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "Permission") {
		t.Errorf("a failed write: %d %s", rec.Code, rec.Body)
	}
}

func TestAnAllowanceEndsOnItsOwn(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodPost, "/api/allow", `{"rule":"r_night","mac":"`+phone+`","minutes":30}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/allow: %d %s", rec.Code, rec.Body)
	}
	id, _ := field(decode(t, rec), "id").(string)
	if len(id) != 10 || !strings.HasPrefix(id, "a_") {
		t.Fatalf("allowance id %q", id)
	}
	until := strconv.FormatInt(now0.Add(30*time.Minute).Unix(), 10)
	want := batchOf(t, []uciconf.Change{{Section: id, Type: "allow", Replace: true, Set: map[string][]string{
		"rule": {"r_night"}, "member": {phone}, "until": {until},
	}}})
	if len(fr.writes) != 1 || batchOf(t, fr.writes[0]) != want {
		t.Fatalf("batch %v\nwant\n%s", fr.writes, want)
	}
	if got := field(decode(t, rec), "until"); got != float64(now0.Add(30*time.Minute).Unix()) {
		t.Errorf("until %v", got)
	}

	// No rule means every rule, written as '*': uci deletes an option set to
	// an empty value, and the model drops an allowance without the key.
	rec = send(t, h, http.MethodPost, "/api/allow", `{"mac":"`+phone+`","minutes":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("an allowance for every rule: %d %s", rec.Code, rec.Body)
	}
	last := fr.writes[len(fr.writes)-1][0]
	if v, has := last.Set["rule"]; !has || len(v) != 1 || v[0] != "*" {
		t.Errorf("rule option %v (present %v): must be written as '*'", v, has)
	}
	if b := batchOf(t, fr.writes[len(fr.writes)-1]); !strings.Contains(b, ".rule='*'\n") {
		t.Errorf("the batch has no `rule='*'` line:\n%s", b)
	}

	for name, body := range map[string]string{
		"zero minutes":       `{"mac":"` + phone + `","minutes":0}`,
		"too long":           `{"mac":"` + phone + `","minutes":241}`,
		"no minutes":         `{"mac":"` + phone + `"}`,
		"not a MAC":          `{"mac":"zz","minutes":5}`,
		"a rule not there":   `{"rule":"r_ghost","mac":"` + phone + `","minutes":5}`,
		"a bad rule id":      `{"rule":"All","mac":"` + phone + `","minutes":5}`,
		"another field":      `{"mac":"` + phone + `","minutes":5,"x":1}`,
		"a fractional count": `{"mac":"` + phone + `","minutes":1.5}`,
	} {
		n := len(fr.writes)
		if rec := send(t, h, http.MethodPost, "/api/allow", body); rec.Code != http.StatusBadRequest || len(fr.writes) != n {
			t.Errorf("%s: %d, %d new writes", name, rec.Code, len(fr.writes)-n)
		}
	}

	// A country rule cannot be allowed for a while: the compiler would drop
	// the allowance, so the API refuses instead of writing a no-op.
	fr.secs = append(fr.secs, sec("rule", "r_ru", "action", "block", "what", "country", "target", "RU", "who", "all"))
	n := len(fr.writes)
	rec = send(t, h, http.MethodPost, "/api/allow", `{"rule":"r_ru","mac":"`+phone+`","minutes":5}`)
	if rec.Code != http.StatusBadRequest || len(fr.writes) != n || !strings.Contains(errorOf(t, rec), "a country cannot be allowed for a while") {
		t.Errorf("an allowance for a country rule: %d %s", rec.Code, rec.Body)
	}

	// DELETE ends one by hand; one that is not there is a 404.
	fr.secs = append(fr.secs, sec("allow", "a_1", "rule", "*", "member", phone, "until", until))
	rec = send(t, h, http.MethodDelete, "/api/allow/a_1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body)
	}
	if c := fr.writes[len(fr.writes)-1]; len(c) != 1 || !c[0].Delete || c[0].Section != "a_1" {
		t.Errorf("delete change %+v", c)
	}
	if rec := send(t, h, http.MethodDelete, "/api/allow/a_gone", ""); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE of nothing: %d", rec.Code)
	}
	// The bound: the 513th live allowance is refused.
	big := house()
	for i := 0; i < rules.MaxAllows; i++ {
		big.secs = append(big.secs, sec("allow", "a_"+strconv.Itoa(i), "rule", "*", "member", phone, "until", strconv.FormatInt(now0.Add(time.Hour).Unix(), 10)))
	}
	h2, _ := writable(t, big)
	if rec := send(t, h2, http.MethodPost, "/api/allow", `{"mac":"`+phone+`","minutes":5}`); rec.Code != http.StatusBadRequest || len(big.writes) != 0 {
		t.Errorf("the 513th allowance: %d", rec.Code)
	}
}

func TestDeletingARuleTakesItsAllowancesWithIt(t *testing.T) {
	fr := house()
	fr.secs = append(fr.secs,
		sec("allow", "a_mine", "rule", "r_night", "member", phone, "until", "9999999999"),
		sec("allow", "a_all", "rule", "*", "member", phone, "until", "9999999999"))
	h, _ := writable(t, fr)
	if rec := send(t, h, http.MethodDelete, "/api/rules/r_night", ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body)
	}
	b := batchOf(t, fr.writes[0])
	if b != "delete netryx.r_night\ndelete netryx.a_mine\ncommit netryx\n" {
		t.Errorf("batch:\n%s", b)
	}
	if rec := send(t, h, http.MethodDelete, "/api/rules/r_ghost", ""); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE of nothing: %d", rec.Code)
	}
}

func TestCutIsOnlyForTheDevicesOwnFlows(t *testing.T) {
	fr := house()
	h, fl := writable(t, fr)
	fl.live = append(fl.live, flows.Flow{
		MAC: phone, Local: netip.MustParseAddrPort("192.168.1.20:40000"),
		Remote: netip.MustParseAddrPort("149.154.167.50:443"), Proto: 6, Dir: flows.Out,
		App: enrich.App{ID: "telegram"},
	}, flows.Flow{
		MAC: phone, Local: netip.MustParseAddrPort("192.168.1.20:22"),
		Remote: netip.MustParseAddrPort("203.0.113.77:50000"), Proto: 6, Dir: flows.In,
	})
	own := `{"local":"192.168.1.20:40000","remote":"149.154.167.50:443","proto":6}`
	rec := send(t, h, http.MethodPost, "/api/devices/"+phone+"/conns/cut", own)
	if rec.Code != http.StatusOK || len(fr.cuts) != 1 {
		t.Fatalf("cut of its own flow: %d %s, %d cuts", rec.Code, rec.Body, len(fr.cuts))
	}
	if c := fr.cuts[0]; c.mac != phone || c.reply || c.proto != 6 || c.local.String() != "192.168.1.20:40000" {
		t.Errorf("cut %+v", c)
	}
	// An inbound flow is deleted by its reply tuple.
	rec = send(t, h, http.MethodPost, "/api/devices/"+phone+"/conns/cut", `{"local":"192.168.1.20:22","remote":"203.0.113.77:50000","proto":6}`)
	if rec.Code != http.StatusOK || len(fr.cuts) != 2 || !fr.cuts[1].reply {
		t.Fatalf("cut of an inbound flow: %d, %+v", rec.Code, fr.cuts)
	}
	// The laptop's flow cannot be cut by naming the phone, nor the phone's by
	// naming the laptop: the tuple must belong to the MAC in the URL.
	laptopFlow := `{"local":"192.168.1.10:51000","remote":"203.0.113.5:443","proto":6}`
	for _, c := range []struct{ mac, body string }{
		{phone, laptopFlow}, {laptop, own},
		{phone, `{"local":"192.168.1.20:40000","remote":"149.154.167.50:443","proto":17}`},
		{tv, own},
	} {
		n := len(fr.cuts)
		rec := send(t, h, http.MethodPost, "/api/devices/"+c.mac+"/conns/cut", c.body)
		if rec.Code != http.StatusNotFound || len(fr.cuts) != n {
			t.Errorf("%s %s: %d, %d new cuts", c.mac, c.body, rec.Code, len(fr.cuts)-n)
		}
	}
	// The spelling of the MAC in the URL does not matter.
	rec = send(t, h, http.MethodPost, "/api/devices/"+strings.ToUpper(phone)+"/conns/cut", own)
	if rec.Code != http.StatusOK {
		t.Errorf("upper-case MAC: %d", rec.Code)
	}
	for _, body := range []string{`{"local":"x","remote":"1.2.3.4:5","proto":6}`, `{"local":"1.2.3.4:5","remote":"1.2.3.4:6"}`, `{"local":"1.2.3.4:5","remote":"1.2.3.4:6","proto":1}`} {
		if rec := send(t, h, http.MethodPost, "/api/devices/"+phone+"/conns/cut", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
}

func TestABodyTooLargeOrWithUnknownFieldsIsRefused(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	const good = `{"what":"all","who":"all"}`
	big := `{"what":"all","who":"all","group":"` + strings.Repeat("a", 16<<10) + `"}`
	for name, c := range map[string]struct {
		body string
		code int
	}{
		"too large":      {big, http.StatusRequestEntityTooLarge},
		"unknown field":  {`{"what":"all","who":"all","admin":true}`, http.StatusBadRequest},
		"two values":     {good + good, http.StatusBadRequest},
		"trailing words": {good + " x", http.StatusBadRequest},
		"not JSON":       {`what=all`, http.StatusBadRequest},
		"empty":          {``, http.StatusBadRequest},
		"wrong type":     {`{"what":1,"who":"all"}`, http.StatusBadRequest},
		"an array":       {`[1]`, http.StatusBadRequest},
	} {
		rec := send(t, h, http.MethodPost, "/api/rules", c.body)
		if rec.Code != c.code {
			t.Errorf("%s: %d %.80s, want %d", name, rec.Code, rec.Body, c.code)
		}
	}
	// Exactly at the limit still reads.
	pad := strings.Repeat(" ", 16<<10-len(good))
	if rec := send(t, h, http.MethodPost, "/api/rules", good+pad); rec.Code != http.StatusOK {
		t.Errorf("a body of exactly 16 KiB: %d %s", rec.Code, rec.Body)
	}
	if len(fr.writes) != 1 {
		t.Errorf("%d writes; only the last body was good", len(fr.writes))
	}
}

func TestRulesShowStateHitsAndProblems(t *testing.T) {
	fr := house()
	r := rules.Rule{
		ID: "r_night", Enabled: true, Action: "block", What: "app", Targets: []string{"tiktok"},
		Who: "device", Members: []string{phone}, Days: []time.Weekday{time.Monday, time.Tuesday},
		From: 21*60 + 5, To: 7 * 60, Until: now0.Add(time.Hour),
	}
	bad := rules.Rule{ID: "r_bad", Enabled: true, What: "country", Targets: []string{"XX"}, Who: "all", From: -1, To: -1, Err: "unknown country XX"}
	held := rules.Rule{ID: "r_held", Enabled: true, What: "all", Who: "all", From: -1, To: -1}
	fr.state = []rules.RuleState{
		{Rule: r, ActiveNow: true, Hits: 42, Applied: true},
		{Rule: bad},
		{Rule: held, Problem: "not applied: syntax error"},
	}
	h, _ := writable(t, fr)
	rec := get(t, h, http.MethodGet, "/api/rules")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	list, _ := decode(t, rec).([]any)
	if len(list) != 3 {
		t.Fatalf("%d rules: %s", len(list), rec.Body)
	}
	a := list[0]
	for k, want := range map[string]any{
		"id": "r_night", "enabled": true, "what": "app", "who": "device", "from": "21:05", "to": "07:00",
		"activeNow": true, "hits": float64(42), "applied": true, "problem": "", "error": "", "group": "",
		"until": float64(now0.Add(time.Hour).Unix()),
	} {
		if got := field(a, k); got != want {
			t.Errorf("%s = %v, want %v", k, got, want)
		}
	}
	if d, _ := field(a, "days").([]any); len(d) != 2 || d[0] != "mon" || d[1] != "tue" {
		t.Errorf("days %v", field(a, "days"))
	}
	if m, _ := field(a, "members").([]any); len(m) != 1 || m[0] != phone {
		t.Errorf("members %v", field(a, "members"))
	}
	b := list[1]
	if field(b, "error") != "unknown country XX" || field(b, "applied") != false || field(b, "from") != "" || field(b, "until") != nil {
		t.Errorf("a rule the model refused: %v", b)
	}
	if d, ok := field(b, "days").([]any); !ok || len(d) != 0 {
		t.Errorf("days of a rule with none must be [] not %v", field(b, "days"))
	}
	if c := list[2]; field(c, "problem") != "not applied: syntax error" || field(c, "error") != "" {
		t.Errorf("a rule nft refused: %v", c)
	}
	// The answer to a write carries the state too.
	fr.state[0].Hits = 43
	rec = send(t, h, http.MethodPut, "/api/rules/r_night", `{"what":"app","targets":["tiktok"],"who":"device","members":["`+phone+`"]}`)
	if field(decode(t, rec), "hits") != float64(43) || field(decode(t, rec), "applied") != true {
		t.Errorf("PUT answer: %s", rec.Body)
	}
}

func TestGroupsAreNamedAndKeptWhileARuleNamesThem(t *testing.T) {
	fr := house()
	fr.secs = append(fr.secs, sec("rule", "r_kids", "action", "block", "what", "all", "who", "group", "group", "kids"))
	h, _ := writable(t, fr)
	rec := get(t, h, http.MethodGet, "/api/groups")
	list, _ := decode(t, rec).([]any)
	if len(list) != 1 || field(list[0], "id") != "kids" || field(list[0], "name") != "Діти" {
		t.Fatalf("groups: %s", rec.Body)
	}
	if m, _ := field(list[0], "members").([]any); len(m) != 1 || m[0] != phone {
		t.Errorf("members %v", field(list[0], "members"))
	}
	rec = send(t, h, http.MethodDelete, "/api/groups/kids", "")
	if rec.Code != http.StatusBadRequest || len(fr.writes) != 0 || !strings.Contains(errorOf(t, rec), "r_kids") {
		t.Errorf("delete while a rule names it: %d %s", rec.Code, rec.Body)
	}
	// Rename keeps the group's other options: a merge, not a replace.
	rec = send(t, h, http.MethodPut, "/api/groups/kids", `{"name":"Діти і гості"}`)
	if rec.Code != http.StatusOK || len(fr.writes) != 1 {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	if c := fr.writes[0][0]; c.Replace || c.Section != "kids" || c.Type != "group" || c.Set["name"][0] != "Діти і гості" {
		t.Errorf("rename change %+v", c)
	}
	// A new group, then its removal once no rule names it; devices lose it.
	if rec := send(t, h, http.MethodPut, "/api/groups/guests", `{"name":"Гості"}`); rec.Code != http.StatusOK {
		t.Errorf("new group: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{"All", "all", "X%20y", ""} {
		if rec := send(t, h, http.MethodPut, "/api/groups/"+bad, `{"name":"x"}`); rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("PUT group %q: %d", bad, rec.Code)
		}
	}
	if rec := send(t, h, http.MethodPut, "/api/groups/g1", `{"name":""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty name: %d", rec.Code)
	}
	fr.secs = fr.secs[:3] // without r_kids
	n := len(fr.writes)
	if rec := send(t, h, http.MethodDelete, "/api/groups/kids", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	got := batchOf(t, fr.writes[n])
	if got != "delete netryx.kids\nset netryx.d_aabbccddee02=device\ndelete netryx.d_aabbccddee02.group\ncommit netryx\n" {
		t.Errorf("batch:\n%s", got)
	}
	if rec := send(t, h, http.MethodDelete, "/api/groups/ghost", ""); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE of nothing: %d", rec.Code)
	}
	// The 33rd group is refused.
	big := house()
	for i := 0; i < rules.MaxGroups; i++ {
		big.secs = append(big.secs, sec("group", "g"+strconv.Itoa(i), "name", "x"))
	}
	h2, _ := writable(t, big)
	if rec := send(t, h2, http.MethodPut, "/api/groups/extra", `{"name":"x"}`); rec.Code != http.StatusBadRequest || len(big.writes) != 0 {
		t.Errorf("a group past the bound: %d", rec.Code)
	}
}

func TestADeviceIsNamedAndGroupedInItsOwnSection(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	// The laptop has no section yet: one is made, named from its MAC.
	rec := send(t, h, http.MethodPut, "/api/devices/"+strings.ToUpper(laptop), `{"name":"Ноутбук гостя's","group":"kids"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT device: %d %s", rec.Code, rec.Body)
	}
	c := fr.writes[0][0]
	if c.Section != "d_aabbccddee01" || c.Type != "device" || c.Replace {
		t.Fatalf("change %+v", c)
	}
	if c.Set["mac"][0] != laptop || c.Set["group"][0] != "kids" || strings.Contains(c.Set["name"][0], "'") {
		t.Errorf("set %v", c.Set)
	}
	if got := field(decode(t, rec), "name"); !strings.HasPrefix(got.(string), "Ноутбук гостя") {
		t.Errorf("answer %s", rec.Body)
	}
	// An existing section is reused under its own name, and an empty group
	// leaves the group.
	rec = send(t, h, http.MethodPut, "/api/devices/"+phone, `{"group":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear group: %d %s", rec.Code, rec.Body)
	}
	c = fr.writes[1][0]
	if c.Section != "d_aabbccddee02" {
		t.Errorf("section %s", c.Section)
	}
	if v, has := c.Set["group"]; !has || len(v) != 0 {
		t.Errorf("group must be an empty slice (a delete): %v", c.Set)
	}
	if _, has := c.Set["name"]; has {
		t.Errorf("name was not sent and must not be touched: %v", c.Set)
	}
	for name, tc := range map[string]struct{ mac, body string }{
		"no such group":   {laptop, `{"group":"ghosts"}`},
		"nothing to set":  {laptop, `{}`},
		"unknown field":   {laptop, `{"colour":"red"}`},
		"control chars":   {laptop, `{"name":"a\nb"}`},
		"not a MAC":       {"zz", `{"name":"x"}`},
		"too long a name": {laptop, `{"name":"` + strings.Repeat("я", 80) + `"}`},
	} {
		n := len(fr.writes)
		if rec := send(t, h, http.MethodPut, "/api/devices/"+tc.mac, tc.body); rec.Code != http.StatusBadRequest || len(fr.writes) != n {
			t.Errorf("%s: %d, %d new writes", name, rec.Code, len(fr.writes)-n)
		}
	}
	// A device the house has never seen is not named.
	if rec := send(t, h, http.MethodPut, "/api/devices/02:00:00:00:00:99", `{"name":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown device: %d", rec.Code)
	}
}

func TestMethodsAreListedPerRouteAndWritesNeedTheRules(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	for _, c := range []struct{ method, path, allow string }{
		{http.MethodDelete, "/api/rules", "GET, POST"},
		{http.MethodPatch, "/api/rules/r_night", "DELETE, PUT"},
		{http.MethodDelete, "/api/allow", "GET, POST"},
		{http.MethodGet, "/api/allow/a_1", "DELETE"},
		{http.MethodPost, "/api/groups", "GET"},
		{http.MethodPost, "/api/devices/" + phone, "GET, PUT"},
		{http.MethodGet, "/api/devices/" + phone + "/conns/cut", "POST"},
		{http.MethodPost, "/api/overview", "GET"},
	} {
		rec := send(t, h, c.method, c.path, "{}")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != c.allow {
			t.Errorf("%s %s: %d Allow %q, want %q", c.method, c.path, rec.Code, rec.Header().Get("Allow"), c.allow)
		}
	}
	// Without a rules engine nothing can be written and nothing is listed.
	fl, hist, inv, w, ap := fixture()
	plain := New(fl, hist, inv, w, ap, clock, nil)
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "/api/devices/" + phone}, {http.MethodPost, "/api/rules"}, {http.MethodGet, "/api/rules"},
		{http.MethodPost, "/api/allow"}, {http.MethodPost, "/api/devices/" + phone + "/conns/cut"},
	} {
		rec := send(t, plain, c.method, c.path, `{}`)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s without rules: %d", c.method, c.path, rec.Code)
		}
	}
}

// The console cuts a connection by naming its tuple, so the live view says
// each connection's tuple, in the form the cut route reads.
// The answer to a cut says whether netryx holds it: a cut the engine could
// not hold answers held false with the engine's note, never a bare success.
func TestACutAnswerSaysWhetherItIsHeld(t *testing.T) {
	fr := house()
	h, fl := writable(t, fr)
	fl.live = append(fl.live, flows.Flow{
		MAC: phone, Local: netip.MustParseAddrPort("192.168.1.20:40000"),
		Remote: netip.MustParseAddrPort("149.154.167.50:443"), Proto: 6, Dir: flows.Out,
	})
	own := `{"local":"192.168.1.20:40000","remote":"149.154.167.50:443","proto":6}`
	rec := send(t, h, http.MethodPost, "/api/devices/"+phone+"/conns/cut", own)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"cut":true,"held":true}` {
		t.Fatalf("a held cut: %d %s", rec.Code, rec.Body)
	}
	fr.cutNote = "the router could not hold the cut: the connection may come back on its next packet"
	rec = send(t, h, http.MethodPost, "/api/devices/"+phone+"/conns/cut", own)
	want := `{"cut":true,"held":false,"note":"the router could not hold the cut: the connection may come back on its next packet"}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("a cut that is not held: %d %s", rec.Code, rec.Body)
	}
}

func TestALiveConnectionSaysItsTupleSoItCanBeCut(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	rec := get(t, h, http.MethodGet, "/api/devices/"+laptop+"/live")
	conns, _ := field(decode(t, rec), "conns").([]any)
	if len(conns) == 0 {
		t.Fatalf("no connections: %s", rec.Body)
	}
	c := conns[0]
	local, _ := field(c, "local").(string)
	remote, _ := field(c, "remote").(string)
	if local != "192.168.1.10:51000" || remote != "203.0.113.5:443" {
		t.Fatalf("tuple %q -> %q", local, remote)
	}
	proto, _ := field(c, "proto").(float64)
	body := `{"local":"` + local + `","remote":"` + remote + `","proto":` + strconv.Itoa(int(proto)) + `}`
	if rec := send(t, h, http.MethodPost, "/api/devices/"+laptop+"/conns/cut", body); rec.Code != http.StatusOK || len(fr.cuts) != 1 {
		t.Errorf("cutting what the live view showed: %d %s", rec.Code, rec.Body)
	}
}

func TestAGroupCannotTakeTheNameOfAnotherSection(t *testing.T) {
	fr := house()
	fr.secs = append(fr.secs, sec("netryx", "main", "mode", "ingress"))
	h, _ := writable(t, fr)
	for _, id := range []string{"r_night", "d_aabbccddee02", "main"} {
		rec := send(t, h, http.MethodPut, "/api/groups/"+id, `{"name":"x"}`)
		if rec.Code != http.StatusConflict || len(fr.writes) != 0 {
			t.Errorf("PUT group %s: %d %s, %d writes", id, rec.Code, rec.Body, len(fr.writes))
		}
	}
	// A device section named after the MAC that another section holds.
	fr.secs = append(fr.secs, sec("group", "d_aabbccddee01", "name", "x"))
	if rec := send(t, h, http.MethodPut, "/api/devices/"+laptop, `{"name":"y"}`); rec.Code != http.StatusConflict || len(fr.writes) != 0 {
		t.Errorf("a device whose section name is taken: %d", rec.Code)
	}
}

func TestAGeneratedIDNeverTakesAnExistingName(t *testing.T) {
	fr := house()
	fr.secs = append(fr.secs, sec("group", "r_deadbeef", "name", "x"), sec("device", "a_deadbeef", "mac", tv))
	h, _ := writable(t, fr)
	seq := []string{"deadbeef", "deadbeef", "00000001"}
	old := randHex
	randHex = func() string { v := seq[0]; seq = seq[1:]; return v }
	defer func() { randHex = old }()
	rec := send(t, h, http.MethodPost, "/api/rules", `{"what":"all","who":"all"}`)
	if id := field(decode(t, rec), "id"); id != "r_00000001" {
		t.Errorf("rule id %v, want to skip the taken r_deadbeef: %s", id, rec.Body)
	}
	seq = []string{"deadbeef", "00000002"}
	rec = send(t, h, http.MethodPost, "/api/allow", `{"mac":"`+phone+`","minutes":5}`)
	if id := field(decode(t, rec), "id"); id != "a_00000002" {
		t.Errorf("allowance id %v: %s", id, rec.Body)
	}
}

func TestAnEndedAllowanceIsRemovedWithTheNextWrite(t *testing.T) {
	fr := house()
	past := strconv.FormatInt(now0.Add(-time.Second).Unix(), 10)
	at := strconv.FormatInt(now0.Unix(), 10)
	live := strconv.FormatInt(now0.Add(time.Hour).Unix(), 10)
	fr.secs = append(fr.secs,
		sec("allow", "a_old", "rule", "*", "member", phone, "until", past),
		sec("allow", "a_now", "rule", "*", "member", phone, "until", at),
		sec("allow", "a_live", "rule", "*", "member", phone, "until", live))
	h, _ := writable(t, fr)
	if rec := send(t, h, http.MethodPost, "/api/allow", `{"mac":"`+phone+`","minutes":5}`); rec.Code != http.StatusOK {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	b := batchOf(t, fr.writes[0])
	if !strings.Contains(b, "delete netryx.a_old\n") || !strings.Contains(b, "delete netryx.a_now\n") || strings.Contains(b, "delete netryx.a_live\n") {
		t.Errorf("batch:\n%s", b)
	}
	// Any write cleans, and a delete of the same section is not doubled.
	fr.secs = append(fr.secs, sec("allow", "a_old2", "rule", "*", "member", phone, "until", past))
	if rec := send(t, h, http.MethodDelete, "/api/allow/a_old2", ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE: %d", rec.Code)
	}
	if n := strings.Count(batchOf(t, fr.writes[1]), "delete netryx.a_old2\n"); n != 1 {
		t.Errorf("a_old2 deleted %d times", n)
	}
	if rec := send(t, h, http.MethodPut, "/api/groups/kids", `{"name":"K"}`); rec.Code != http.StatusOK || !strings.Contains(batchOf(t, fr.writes[2]), "delete netryx.a_old\n") {
		t.Errorf("a group write did not clean: %d", rec.Code)
	}
}

func TestAGroupChangeKeepsTheNameUCIAlreadyHas(t *testing.T) {
	fr := house()
	fr.labels = map[string]string{phone: "Старе ім'я", laptop: "Ноутбук"}
	h, _ := writable(t, fr)
	if rec := send(t, h, http.MethodPut, "/api/devices/"+phone, `{"group":"kids"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if _, has := fr.writes[0][0].Set["name"]; has {
		t.Errorf("the label overwrote the name UCI holds: %v", fr.writes[0][0].Set)
	}
	// A device UCI does not name yet takes the label's name along.
	rec := send(t, h, http.MethodPut, "/api/devices/"+laptop, `{"group":"kids"}`)
	if rec.Code != http.StatusOK || fr.writes[1][0].Set["name"][0] != "Ноутбук" || field(decode(t, rec), "name") != "Ноутбук" {
		t.Errorf("moved label: %d %v %s", rec.Code, fr.writes[1][0].Set, rec.Body)
	}
}

func TestAMovedLabelIsCleanedLikeASubmittedName(t *testing.T) {
	fr := house()
	fr.labels = map[string]string{laptop: "Guest's laptop " + strings.Repeat("я", 100), tv: "  TV\u0007 's  "}
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodPut, "/api/devices/"+laptop, `{"group":"kids"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("a label with an apostrophe and 214 bytes: %d %s", rec.Code, rec.Body)
	}
	n := fr.writes[0][0].Set["name"][0]
	if strings.Contains(n, "'") || len(n) > maxName || !strings.HasPrefix(n, "Guest\u2019s laptop") || !utf8.ValidString(n) {
		t.Errorf("moved name %q (%d bytes)", n, len(n))
	}
	if field(decode(t, rec), "name") != n {
		t.Errorf("the response shows %v, not the moved name", field(decode(t, rec), "name"))
	}
	rec = send(t, h, http.MethodPut, "/api/devices/"+tv, `{"group":"kids"}`)
	if rec.Code != http.StatusOK || fr.writes[1][0].Set["name"][0] != "TV \u2019s" {
		t.Errorf("control characters and spaces: %d %v", rec.Code, fr.writes[1][0].Set)
	}
}

func TestForeignUncommittedChangesRefuseTheWrite(t *testing.T) {
	fr := house()
	fr.writeErr = ErrPendingChanges
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodPost, "/api/rules", `{"what":"all","who":"all"}`)
	if rec.Code != http.StatusConflict || errorOf(t, rec) != "uci has unsaved changes: commit or revert them first" {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestTargetsAreDedupedAndCapped(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	if rec := send(t, h, http.MethodPost, "/api/rules", `{"what":"app","targets":["tiktok","telegram","tiktok"],"who":"all"}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := fr.writes[0][0].Set["target"]; len(got) != 2 || got[0] != "tiktok" || got[1] != "telegram" {
		t.Errorf("targets %v", got)
	}
	var many []string
	for i := 0; i < 65; i++ {
		many = append(many, "t"+strconv.Itoa(i))
	}
	body := `{"what":"app","who":"all","targets":["` + strings.Join(many, `","`) + `"]}`
	if rec := send(t, h, http.MethodPost, "/api/rules", body); rec.Code != http.StatusBadRequest || len(fr.writes) != 1 {
		t.Errorf("65 targets: %d", rec.Code)
	}
}

func TestARuleNftRefusedStillSaysSoInTheAnswer(t *testing.T) {
	fr := house()
	fr.notApplied = true
	h, _ := writable(t, fr) // State() has nothing for the new rule
	rec := send(t, h, http.MethodPost, "/api/rules", `{"what":"all","who":"all"}`)
	if rec.Code != http.StatusOK || field(decode(t, rec), "problem") != "not applied: nft: syntax error" || field(decode(t, rec), "applied") != false {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestAnUnknownDeviceIsAnsweredInUkrainian(t *testing.T) {
	h, _ := writable(t, house())
	rec := send(t, h, http.MethodPut, "/api/devices/02:00:00:00:00:99", `{"name":"x"}`)
	if rec.Code != http.StatusNotFound || errorOf(t, rec) != "no device with that MAC" {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

// The owner's writes never land in a section the agent owns: the model
// ignores agent_* device and group sections, so a rename written there would
// be silently lost, and a device pointed at such a group would sit in none.
func TestTheOwnersWritesNeverLandInAgentSections(t *testing.T) {
	fr := house()
	// The agent's device section for the laptop comes first, then the owner's.
	fr.secs = append([]uciconf.Section{
		sec("device", "agent_x", "mac", laptop, "name", "Renamed"),
		sec("group", "agent_g", "name", "Mine"),
	}, fr.secs...)
	fr.secs = append(fr.secs, sec("device", "d_aabbccddee01", "mac", laptop, "name", "Laptop"))
	h, _ := writable(t, fr)

	if rec := send(t, h, http.MethodPut, "/api/devices/"+laptop, `{"name":"Нова"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT device: %d %s", rec.Code, rec.Body)
	}
	if c := fr.writes[0][0]; c.Section != "d_aabbccddee01" {
		t.Errorf("the rename went to %s", c.Section)
	}

	// With only the agent's section for a MAC, the owner's write makes its own.
	fr2 := house()
	fr2.secs = append([]uciconf.Section{sec("device", "agent_x", "mac", laptop, "name", "Renamed")}, fr2.secs...)
	h2, _ := writable(t, fr2)
	if rec := send(t, h2, http.MethodPut, "/api/devices/"+laptop, `{"name":"Нова"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT device: %d %s", rec.Code, rec.Body)
	}
	if c := fr2.writes[0][0]; c.Section != "d_aabbccddee01" {
		t.Errorf("the owner's write went to %s", c.Section)
	}

	n := len(fr.writes)
	for name, tc := range map[string]struct{ method, path, body string }{
		"device into an agent group": {http.MethodPut, "/api/devices/" + phone, `{"group":"agent_g"}`},
		"create an agent group":      {http.MethodPut, "/api/groups/agent_new", `{"name":"x"}`},
		"rename an agent group":      {http.MethodPut, "/api/groups/agent_g", `{"name":"x"}`},
	} {
		if rec := send(t, h, tc.method, tc.path, tc.body); rec.Code != http.StatusBadRequest || len(fr.writes) != n {
			t.Errorf("%s: %d, %d new writes", name, rec.Code, len(fr.writes)-n)
		}
	}
	if rec := send(t, h, http.MethodDelete, "/api/groups/agent_g", ""); rec.Code != http.StatusNotFound || len(fr.writes) != n {
		t.Errorf("delete an agent group: %d", rec.Code)
	}
}

// A rule whose until has passed is deleted with the next write, as an ended
// allowance is, and does not take one of the 200 places: at 200 sections with
// five of them ended, a new rule is written and the five go.
func TestRulesPastTheirEndAreCleanedAndNotCounted(t *testing.T) {
	fr := house()
	past := strconv.FormatInt(now0.Add(-time.Minute).Unix(), 10)
	for i := 0; i < 5; i++ {
		fr.secs = append(fr.secs, sec("rule", fmt.Sprintf("r_old%d", i), "enabled", "1", "action", "block", "what", "all",
			"who", "device", "member", phone, "until", past))
	}
	for len(fr.secs) < 3+rules.MaxRules-1 {
		fr.secs = append(fr.secs, sec("rule", fmt.Sprintf("r_live%d", len(fr.secs)), "enabled", "1", "action", "block",
			"what", "all", "who", "device", "member", phone))
	}
	if n := countType(fr.secs, "rule"); n != rules.MaxRules {
		t.Fatalf("setup: %d rule sections", n)
	}
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodPost, "/api/rules", `{"what":"all","who":"device","members":["`+phone+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST at 200 with five ended: %d %s", rec.Code, rec.Body)
	}
	b := batchOf(t, fr.writes[0])
	for i := 0; i < 5; i++ {
		if !strings.Contains(b, fmt.Sprintf("delete netryx.r_old%d\n", i)) {
			t.Errorf("r_old%d not deleted:\n%s", i, b)
		}
	}
	if strings.Contains(b, "delete netryx.r_live") || strings.Contains(b, "delete netryx.r_night") {
		t.Errorf("a live rule was deleted:\n%s", b)
	}
	// A PUT that gives an ended rule a new until rewrites it, never deletes it.
	fresh := strconv.FormatInt(now0.Add(time.Hour).Unix(), 10)
	rec = send(t, h, http.MethodPut, "/api/rules/r_old0", `{"what":"all","who":"device","members":["`+phone+`"],"until":`+fresh+`}`)
	if rec.Code != http.StatusOK || strings.Count(batchOf(t, fr.writes[1]), "delete netryx.r_old0\n") != 1 ||
		!strings.Contains(batchOf(t, fr.writes[1]), "set netryx.r_old0.until='"+fresh+"'") {
		t.Errorf("PUT of an ended rule: %d\n%s", rec.Code, batchOf(t, fr.writes[1]))
	}
	// 200 live rules: the next one is refused.
	fr.secs = append(fr.secs[:3], fr.secs[8:]...)
	for countType(fr.secs, "rule") < rules.MaxRules {
		fr.secs = append(fr.secs, sec("rule", fmt.Sprintf("r_more%d", len(fr.secs)), "enabled", "1", "action", "block",
			"what", "all", "who", "device", "member", phone))
	}
	if rec := send(t, h, http.MethodPost, "/api/rules", `{"what":"all","who":"device","members":["`+phone+`"]}`); rec.Code != http.StatusBadRequest || errorOf(t, rec) != "more than 200 rules" {
		t.Errorf("POST at 200 live: %d %s", rec.Code, rec.Body)
	}
}

// R42: every rule lists its live allowances, so the page and the agent see
// what is lifted, until when and by whom.
func TestRulesListTheirLiveAllowances(t *testing.T) {
	fr := house()
	r := rules.Rule{ID: "r_night", Enabled: true, Action: "block", What: "app", Targets: []string{"tiktok"},
		Who: "device", Members: []string{phone}, From: -1, To: -1}
	until := now0.Add(30 * time.Minute)
	fr.state = []rules.RuleState{
		{Rule: r, Applied: true, Allows: []rules.LiveAllow{{ID: "agent_x", Member: phone, Until: until, By: "agent"}}},
		{Rule: rules.Rule{ID: "r_all", Enabled: true, What: "all", Who: "all", From: -1, To: -1}},
	}
	h, _ := writable(t, fr)
	list, _ := decode(t, get(t, h, http.MethodGet, "/api/rules")).([]any)
	if len(list) != 2 {
		t.Fatalf("rules: %v", list)
	}
	a, _ := field(list[0], "allows").([]any)
	if len(a) != 1 || field(a[0], "id") != "agent_x" || field(a[0], "member") != phone ||
		field(a[0], "until") != float64(until.Unix()) || field(a[0], "by") != "agent" {
		t.Errorf("allows %v", field(list[0], "allows"))
	}
	if a, ok := field(list[1], "allows").([]any); !ok || len(a) != 0 {
		t.Errorf("a rule with none lists [] not %v", field(list[1], "allows"))
	}
}

// R39: while the router agent's apply waits for its confirmation, a
// rollback would restore the file as it was before it: a write now would be
// erased with it, so it is refused with a word to wait.
func TestAWriteWaitsWhileTheAgentsApplyIsPending(t *testing.T) {
	fr := house()
	fr.writeErr = fmt.Errorf("wrapped: %w", ErrAgentPending)
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodPost, "/api/rules", `{"what":"all","who":"all"}`)
	if rec.Code != http.StatusConflict || errorOf(t, rec) != "the agent is changing the settings, try again in a minute" {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestFiltersAreWrittenAsTheirOwnSection(t *testing.T) {
	fr := house()
	fr.safeOK, fr.safeP = false, "DNS filter does not answer: refused"
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodGet, "/api/filters", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	got := decode(t, rec)
	if field(got, "forceDns") != false || field(got, "youtube") != "off" {
		t.Fatalf("GET /api/filters without a section = %v; want all off", got)
	}
	if sd, _ := field(got, "safeDns").(map[string]any); sd == nil || sd["healthy"] != false || !strings.Contains(fmt.Sprint(sd["problem"]), "refused") {
		t.Fatalf("safeDns = %v", field(got, "safeDns"))
	}
	rec = send(t, h, http.MethodPut, "/api/filters", `{"forceDns":true,"blockDot":true,"blockDoh":false,"safeSearch":true,"youtube":"strict"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	want := batchOf(t, []uciconf.Change{{Section: "filters", Type: "filters", Replace: true, Set: map[string][]string{
		"force_dns": {"1"}, "block_dot": {"1"}, "block_doh": {"0"}, "safesearch": {"1"}, "youtube": {"strict"},
	}}})
	if len(fr.writes) != 1 || batchOf(t, fr.writes[0]) != want {
		t.Fatalf("writes %v; want exactly\n%s", fr.writes, want)
	}
	if rec := send(t, h, http.MethodPut, "/api/filters", `{"youtube":"loud"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown YouTube level: %d %s", rec.Code, rec.Body)
	}
}

func TestAGroupCanBeFilteredThroughTheAPI(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodPut, "/api/groups/kids", `{"name":"Діти","filteredDns":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if b := batchOf(t, fr.writes[0]); !strings.Contains(b, "set netryx.kids.filtered_dns='1'\n") {
		t.Fatalf("batch\n%s", b)
	}
	fr.secs[0].Options["filtered_dns"] = []string{"1"}
	list := decodeList(t, send(t, h, http.MethodGet, "/api/groups", ""))
	if len(list) != 1 || list[0]["filteredDns"] != true {
		t.Fatalf("GET /api/groups = %v; want kids filtered", list)
	}
}

func TestALimitRuleIsWrittenAndShownWithItsUse(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodPost, "/api/rules", `{"action":"limit","minutes":60,"what":"app","targets":["tiktok"],"who":"device","members":["`+phone+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	b := batchOf(t, fr.writes[0])
	for _, want := range []string{".action='limit'\n", ".minutes='60'\n"} {
		if !strings.Contains(b, want) {
			t.Fatalf("missing %q in\n%s", want, b)
		}
	}
	if rec := send(t, h, http.MethodPost, "/api/rules", `{"action":"limit","minutes":0,"what":"app","targets":["tiktok"],"who":"all"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("a limit of 0 minutes: %d %s", rec.Code, rec.Body)
	}
	fr.state = []rules.RuleState{{Rule: rules.Rule{ID: "r_lim", Enabled: true, Action: "limit", What: "app", Targets: []string{"tiktok"},
		Who: "device", Members: []string{phone}, Minutes: 60, From: -1, To: -1}, Used: map[string]int{phone: 12}}}
	list := decodeList(t, send(t, h, http.MethodGet, "/api/rules", ""))
	if len(list) != 1 || list[0]["action"] != "limit" || list[0]["minutes"] != float64(60) {
		t.Fatalf("GET /api/rules = %v", list)
	}
	if used, _ := list[0]["used"].(map[string]any); used[phone] != float64(12) {
		t.Fatalf("used = %v", list[0]["used"])
	}
}

func TestUsageIsPerDevice(t *testing.T) {
	fr := house()
	until := clock().Add(5 * time.Hour)
	fr.usage = map[string][]rules.LimitUse{phone: {{Rule: "r_lim", What: "category", Targets: []string{"social"}, Used: 60, Limit: 60, SpentUntil: until,
		Apps: []rules.AppMinutes{{App: "tiktok", Used: 40}, {App: "instagram", Used: 20}}}}}
	h, _ := writable(t, fr)
	list := decodeList(t, send(t, h, http.MethodGet, "/api/usage?mac="+strings.ToUpper(phone), ""))
	if len(list) != 1 || list[0]["used"] != float64(60) || list[0]["spentUntil"] != float64(until.Unix()) {
		t.Fatalf("GET /api/usage = %v", list)
	}
	apps, _ := list[0]["apps"].([]any)
	if len(apps) != 2 || apps[0].(map[string]any)["app"] != "tiktok" || apps[0].(map[string]any)["used"] != float64(40) {
		t.Fatalf("apps = %v; want each used app with its minutes", list[0]["apps"])
	}
	if tg, _ := list[0]["targets"].([]any); len(tg) != 1 || tg[0] != "social" || list[0]["what"] != "category" {
		t.Fatalf("targets = %v", list[0]["targets"])
	}
	if rec := send(t, h, http.MethodGet, "/api/usage?mac=nope", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("a bad MAC: %d", rec.Code)
	}
	if rec := send(t, h, http.MethodGet, "/api/usage?mac=02:00:00:00:00:99", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown device: %d", rec.Code)
	}
}

func TestAdblockWritesOnlyEnabledOnASource(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	got := decode(t, send(t, h, http.MethodGet, "/api/adblock", ""))
	if field(got, "installed") != false {
		t.Fatalf("GET /api/adblock = %v; want not installed", got)
	}
	fr.abErr = ErrNoAdblock
	if rec := send(t, h, http.MethodPut, "/api/adblock/file_url_1", `{"url":"https://x.example/list.txt","enabled":true}`); rec.Code != http.StatusNotFound {
		t.Fatalf("not installed: %d %s", rec.Code, rec.Body)
	}
	fr.abErr = ErrNotSource
	if rec := send(t, h, http.MethodPut, "/api/adblock/file_url_9", `{"url":"https://x.example/list.txt","enabled":true}`); rec.Code != http.StatusNotFound {
		t.Fatalf("not a source: %d", rec.Code)
	}
	fr.abErr = ErrPendingChanges
	if rec := send(t, h, http.MethodPut, "/api/adblock/file_url_1", `{"url":"https://x.example/list.txt","enabled":true}`); rec.Code != http.StatusConflict {
		t.Fatalf("pending changes: %d", rec.Code)
	}
	fr.abErr = ErrSourceMoved
	if rec := send(t, h, http.MethodPut, "/api/adblock/file_url_1", `{"url":"https://x.example/list.txt","enabled":true}`); rec.Code != http.StatusConflict {
		t.Fatalf("the list changed under the page: %d", rec.Code)
	}
	fr.abErr = nil
	fr.ab = AdblockState{Installed: true, Sources: []AdblockSource{{ID: "file_url_1", Name: "Hagezi", URL: "https://x.example/list.txt"}}}
	rec := send(t, h, http.MethodPut, "/api/adblock/file_url_1", `{"url":"https://x.example/list.txt","enabled":true}`)
	if rec.Code != http.StatusAccepted || len(fr.abSet) != 1 || fr.abSet[0] != "file_url_1 https://x.example/list.txt true" {
		t.Fatalf("PUT: %d %s; calls %v", rec.Code, rec.Body, fr.abSet)
	}
	if note, _ := field(decode(t, rec), "restartNote").(string); !strings.Contains(note, "restart the router's DNS") {
		t.Fatalf("the answer must say adblock-fast restarts the router's DNS: %q", note)
	}
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	raw, _ := decode(t, rec).([]any)
	var out []map[string]any
	for _, x := range raw {
		m, _ := x.(map[string]any)
		out = append(out, m)
	}
	return out
}

func TestADeviceCanBeIsolatedAndApproved(t *testing.T) {
	fr := house()
	fr.sec = map[string]DeviceSecurity{phone: {Isolate: true, Quarantined: true, LikelySame: "02:00:00:00:00:01",
		Isolation: &rules.Isolation{Routed: true, Bridged: true, SSID: "Home", Text: "not isolated from its Wi-Fi neighbours on Home"}}}
	h, _ := writable(t, fr)
	if rec := send(t, h, http.MethodPut, "/api/devices/"+phone, `{"isolate":true}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT isolate: %d %s", rec.Code, rec.Body)
	}
	if got := batchOf(t, fr.writes[0]); !strings.Contains(got, "set netryx.d_aabbccddee02.isolate='1'") {
		t.Fatalf("isolate write:\n%s", got)
	}
	if rec := send(t, h, http.MethodPut, "/api/devices/"+phone, `{"approved":true,"group":"kids"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT approve: %d %s", rec.Code, rec.Body)
	}
	got := batchOf(t, fr.writes[1])
	if !strings.Contains(got, "set netryx.d_aabbccddee02.approved='1'") || !strings.Contains(got, "set netryx.d_aabbccddee02.group='kids'") {
		t.Fatalf("approve write:\n%s", got)
	}
	d := decode(t, send(t, h, http.MethodGet, "/api/devices/"+phone, ""))
	iso, _ := field(d, "isolation").(map[string]any)
	if field(d, "isolate") != true || field(d, "quarantined") != true || field(d, "likelySame") != "02:00:00:00:00:01" ||
		iso == nil || iso["ssid"] != "Home" || !strings.Contains(fmt.Sprint(iso["text"]), "not isolated") {
		t.Fatalf("GET device = %v", d)
	}
}

// spec 6.8: the built-in group New lists the devices in quarantine; it is
// the roster's, never written.
func TestTheNewGroupIsBuiltInAndReadOnly(t *testing.T) {
	fr := house()
	fr.pend = []rules.Pending{{MAC: "02:4e:58:00:00:09"}}
	h, _ := writable(t, fr)
	list := decodeList(t, send(t, h, http.MethodGet, "/api/groups", ""))
	var nw map[string]any
	for _, g := range list {
		if g["id"] == "new" {
			nw = g
		}
	}
	if nw == nil || nw["builtin"] != true || nw["name"] != "New" || fmt.Sprint(nw["members"]) != "[02:4e:58:00:00:09]" {
		t.Fatalf("groups = %v; want the built-in New with the held device", list)
	}
	for _, m := range []string{http.MethodPut, http.MethodDelete} {
		if rec := send(t, h, m, "/api/groups/new", `{"name":"x"}`); rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /api/groups/new: %d; want 405", m, rec.Code)
		}
	}
	if len(fr.writes) != 0 {
		t.Fatalf("the built-in group was written: %v", fr.writes)
	}
}

func TestAlertsAreListedWithTheTelegramState(t *testing.T) {
	fr := house()
	fr.alerts = AlertsState{Alerts: []alerts.Alert{{ID: "1", Kind: alerts.NewDevice, MAC: phone, At: clock()}},
		TelegramProblem: "Telegram is off in Hermes", CountryTriesSeen: true,
		QuarantineActive: false, QuarantineProblem: "turned off", GraceEnds: clock().Add(time.Hour)}
	h, _ := writable(t, fr)
	got := decode(t, send(t, h, http.MethodGet, "/api/alerts", ""))
	tg, _ := field(got, "telegram").(map[string]any)
	list, _ := field(got, "alerts").([]any)
	if len(list) != 1 || tg == nil || tg["ready"] != false || tg["problem"] != "Telegram is off in Hermes" || field(got, "countryTriesSeen") != true {
		t.Fatalf("GET /api/alerts = %v", got)
	}
	if q, _ := field(got, "quarantineState").(map[string]any); q == nil || q["active"] != false || q["problem"] != "turned off" || q["graceEnds"] != float64(clock().Add(time.Hour).Unix()) {
		t.Fatalf("quarantineState = %v", field(got, "quarantineState"))
	}
	if a, _ := list[0].(map[string]any); a["kind"] != "new_device" || a["mac"] != phone {
		t.Fatalf("alert = %v", list[0])
	}
	if s, _ := field(got, "settings").(map[string]any); s == nil || s["newDevice"] != true || s["channel"] != "telegram" || s["quarantine"] != true {
		t.Fatalf("settings = %v; want the defaults", field(got, "settings"))
	}
}

func TestAlertAndQuarantineSettingsAreWrittenAsTheirOwnSections(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	if rec := send(t, h, http.MethodPut, "/api/alerts", `{"newDevice":true,"blockedCountry":false,"upload":true,"channel":"console"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT alerts: %d %s", rec.Code, rec.Body)
	}
	want := batchOf(t, []uciconf.Change{{Section: "alerts", Type: "alerts", Replace: true, Set: map[string][]string{
		"new_device": {"1"}, "blocked_country": {"0"}, "upload": {"1"}, "channel": {"console"}}}})
	if batchOf(t, fr.writes[0]) != want {
		t.Fatalf("alerts write:\n%s\nwant\n%s", batchOf(t, fr.writes[0]), want)
	}
	if rec := send(t, h, http.MethodPut, "/api/alerts", `{"channel":"pigeon"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown channel: %d", rec.Code)
	}
	if rec := send(t, h, http.MethodPut, "/api/quarantine", `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT quarantine: %d %s", rec.Code, rec.Body)
	}
	want = batchOf(t, []uciconf.Change{{Section: "quarantine", Type: "quarantine", Replace: true, Set: map[string][]string{"enabled": {"0"}}}})
	if batchOf(t, fr.writes[1]) != want {
		t.Fatalf("quarantine write:\n%s", batchOf(t, fr.writes[1]))
	}
}

// Final review I7: isolating the device the owner is using cuts it off from
// this console, LuCI and SSH, and the agent cannot undo it; that one request
// is refused, from any other device it goes through.
func TestTheDeviceInUseCannotIsolateItself(t *testing.T) {
	fr := house()
	h, _ := writable(t, fr)
	req := httptest.NewRequest(http.MethodPut, "/api/devices/"+laptop, strings.NewReader(`{"isolate":true}`))
	req.RemoteAddr = "192.168.1.10:51515"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "the device you are using now") || len(fr.writes) != 0 {
		t.Fatalf("self-isolation: %d %s, %d writes; want 409 and nothing written", rec.Code, rec.Body, len(fr.writes))
	}
	req = httptest.NewRequest(http.MethodPut, "/api/devices/"+laptop, strings.NewReader(`{"isolate":true}`))
	req.RemoteAddr = "192.168.1.77:51515"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("from another device: %d %s", rec.Code, rec.Body)
	}
}

// An allowance section that opens nothing (no rule or an empty one, a member
// that is no device, a time that cannot be) is listed with why, so the owner
// sees it does not work; one in force and one that has ended are not.
func TestAllowancesThatOpenNothingAreListed(t *testing.T) {
	fr := house()
	live := strconv.FormatInt(now0.Add(time.Hour).Unix(), 10)
	fr.secs = append(fr.secs,
		sec("allow", "a_empty", "rule", "", "member", phone, "until", live),
		sec("allow", "a_ok", "rule", "*", "member", phone, "until", live),
		sec("allow", "a_old", "rule", "*", "member", phone, "until", strconv.FormatInt(now0.Add(-time.Hour).Unix(), 10)),
	)
	h, _ := writable(t, fr)
	rec := send(t, h, http.MethodGet, "/api/allow", "")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v struct {
		Invalid []struct{ ID, Member, Why string } `json:"invalid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Invalid) != 1 || v.Invalid[0].ID != "a_empty" || v.Invalid[0].Member != phone || !strings.Contains(v.Invalid[0].Why, "'*'") {
		t.Errorf("%s", rec.Body)
	}
}
