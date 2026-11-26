package rules

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const ruRule = `
config rule 'ru'
	option action 'block'
	option what 'country'
	list target 'RU'
	option who 'all'
`

const gbRule = `
config rule 'gb'
	option action 'block'
	option what 'country'
	list target 'GB'
	option who 'all'
`

// The need is nft's peak, which holds the kernel's netryx ruleset and the
// script together, plus the kernel's new sets; the factors are the ones
// measured (docs/small-router-memory-measured.md).
func TestTheApplyNeedIsCountedFromTheKernelsRulesAndTheScript(t *testing.T) {
	for _, tc := range []struct {
		arch         string
		held, script int
		want         uint64
	}{
		{"arm64", 1000, 100, 22*1100 + 7*100},
		{"amd64", 1000, 100, 16*1100 + 7*100},
		{"mipsle", 1000, 100, 22*1100 + 7*100},
		{"arm64", 0, 0, 0},
	} {
		if got := applyNeed(tc.held, tc.script, tc.arch); got != tc.want {
			t.Errorf("%s held %d script %d: %d, want %d", tc.arch, tc.held, tc.script, got, tc.want)
		}
	}
}

// What is kept free beyond the need is a margin of 64 MB, measured from
// beryl1: the room is enough at the need plus the margin and not a byte less.
func TestTheMarginIsKeptOnBothSidesOfTheLine(t *testing.T) {
	const need = 100 << 20
	if !roomFor(need+memMargin, need) {
		t.Error("the need plus the margin is enough")
	}
	if roomFor(need+memMargin-1, need) {
		t.Error("a byte under the need plus the margin is not enough")
	}
	if memMargin != 64<<20 {
		t.Errorf("the margin is %d", memMargin)
	}
}

// An apply that would not fit is refused before nft runs: the kernel keeps
// its ruleset, rules.nft stays as it was, and the rule says why.
func TestAnApplyThatWouldNotFitIsRefusedBeforeNft(t *testing.T) {
	r := newRig(t, ruRule, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	before := len(r.nft.applies())
	rulesNft, _ := os.ReadFile(filepath.Join(r.state, "rules.nft"))

	r.write(ruRule + gbRule)
	r.mu.Lock()
	r.mem = memMargin - 1
	r.mu.Unlock()
	if err := r.e.Reload(); err == nil {
		t.Fatal("a refused apply is an error")
	}
	if a := r.nft.applies(); len(a) != before {
		t.Fatalf("nft ran though the apply would not fit:\n%s", a[len(a)-1].stdin)
	}
	if !r.nft.objects["set cc_ru_v4"] || r.nft.objects["set cc_gb_v4"] {
		t.Fatalf("the kernel's ruleset changed: %v", r.nft.objects)
	}
	if b, _ := os.ReadFile(filepath.Join(r.state, "rules.nft")); string(b) != string(rulesNft) {
		t.Fatal("rules.nft was rewritten")
	}
	s := r.state1("gb")
	if s.Applied || !strings.HasPrefix(s.Problem, "not applied: too little memory") || !strings.Contains(s.Problem, "64 MiB is kept free") {
		t.Fatalf("the rule does not say why: %+v", s)
	}
}

// With room enough the same change applies.
func TestAnApplyThatFitsGoesAhead(t *testing.T) {
	r := newRig(t, ruRule, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.write(ruRule + gbRule)
	r.mu.Lock()
	r.mem = memMargin + 64<<20
	r.mu.Unlock()
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if !r.nft.objects["set cc_gb_v4"] || !r.state1("gb").Applied {
		t.Fatalf("the apply did not go ahead: %+v", r.state1("gb"))
	}
}

// A change that adds no country set (a delete, an app rule) is never held
// back: nft holds the ruleset the router already held, and only a shrinking
// ruleset lets a starved router out.
func TestAChangeThatAddsNoCountrySetIsNotHeldBack(t *testing.T) {
	r := newRig(t, ruRule+gbRule, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	before := len(r.nft.applies())
	r.mu.Lock()
	r.mem = 1
	r.mu.Unlock()
	r.write(ruRule)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(r.nft.applies()) == before || !r.state1("ru").Applied {
		t.Fatalf("a delete was held back: %+v", r.state1("ru"))
	}
}

// At start the ruleset the kernel holds is the one the init loaded from
// rules.nft, so its size counts until netryx applies its own.
func TestTheRulesHeldAtStartAreReadFromRulesNft(t *testing.T) {
	r := newRig(t, ruRule, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.state, "rules.nft"), make([]byte, 5000), 0o600); err != nil {
		t.Fatal(err)
	}
	e2 := NewEngine(r.e.o)
	if got := e2.heldBytes(true); got != 5000 {
		t.Errorf("held at start: %d, want the size of rules.nft", got)
	}
	if got := e2.heldBytes(false); got != 0 {
		t.Errorf("without the table nothing is held: %d", got)
	}
	if err := e2.Reload(); err != nil {
		t.Fatal(err)
	}
	full, _ := os.ReadFile(filepath.Join(r.state, "rules.nft"))
	if got := e2.heldBytes(true); got != len(full) {
		t.Errorf("after an apply: %d, want the ruleset just applied (%d)", got, len(full))
	}
	// What this process applied is kept, not read back: a rules.nft that
	// could not be written (a full flash) must not stand for the kernel.
	if err := os.Remove(filepath.Join(r.state, "rules.nft")); err != nil {
		t.Fatal(err)
	}
	if got := e2.heldBytes(true); got != len(full) {
		t.Errorf("without rules.nft after an apply: %d, want %d", got, len(full))
	}
}

// A router whose memory cannot be read applies as before, and says so.
func TestUnreadableMemoryDoesNotHoldTheRulesBack(t *testing.T) {
	r := newRig(t, ruRule, winter(t))
	r.mu.Lock()
	r.memErr = errors.New("no /proc/meminfo")
	r.mu.Unlock()
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if !r.state1("ru").Applied {
		t.Fatalf("not applied: %+v", r.state1("ru"))
	}
}

func TestMemAvailableIsReadFromMeminfo(t *testing.T) {
	got, err := parseMemAvailable(strings.NewReader("MemTotal:         496724 kB\nMemFree:          214428 kB\nMemAvailable:     334456 kB\n"))
	if err != nil || got != 334456*1024 {
		t.Errorf("%d %v", got, err)
	}
	if _, err := parseMemAvailable(strings.NewReader("MemTotal: 1 kB\n")); err == nil {
		t.Error("a meminfo without MemAvailable is an error")
	}
	if _, err := parseMemAvailable(strings.NewReader("MemAvailable: lots kB\n")); err == nil {
		t.Error("a number that is not one is an error")
	}
}

// On Linux the router's own meminfo is read; elsewhere there is none to read.
func TestTheRoutersMeminfoIsRead(t *testing.T) {
	got, err := ReadMemAvailable()
	if _, statErr := os.Stat("/proc/meminfo"); statErr != nil {
		if err == nil {
			t.Fatal("no meminfo, yet a figure")
		}
		return
	}
	if err != nil || got == 0 {
		t.Fatalf("%d %v", got, err)
	}
}

// The line is drawn at the need counted from the ruleset the kernel holds and
// the script, with the margin left: at it the apply goes ahead, a byte under
// it is refused.
func TestTheLineIsDrawnAtTheCountedNeed(t *testing.T) {
	r := newRig(t, ruRule, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	held := r.e.heldBytes(true)
	if held == 0 {
		t.Fatal("setup: the kernel holds a ruleset")
	}
	script := strings.Repeat("x", 1000)
	need := applyNeed(held, len(script), runtime.GOARCH)
	r.mu.Lock()
	r.mem = need + memMargin
	r.mu.Unlock()
	if msg := r.e.tooBig(true, script, 1); msg != "" {
		t.Errorf("at the line: %s", msg)
	}
	r.mu.Lock()
	r.mem = need + memMargin - 1
	r.mu.Unlock()
	if msg := r.e.tooBig(true, script, 1); msg == "" {
		t.Error("a byte under the line is refused")
	}
	if msg := r.e.tooBig(true, script, 0); msg != "" {
		t.Errorf("no new country set, no check: %s", msg)
	}
}

// A refusal is not for ever: once the memory is there, the next reload
// applies, and rules.nft follows.
func TestARefusalClearsOnceTheMemoryIsThere(t *testing.T) {
	r := newRig(t, ruRule, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.write(ruRule + gbRule)
	r.mu.Lock()
	r.mem = 1
	r.mu.Unlock()
	if err := r.e.Reload(); err == nil {
		t.Fatal("setup: refused")
	}
	before, _ := os.ReadFile(filepath.Join(r.state, "rules.nft"))
	r.mu.Lock()
	r.mem = 0
	r.mu.Unlock()
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(r.state, "rules.nft"))
	if !r.state1("gb").Applied || !r.nft.objects["set cc_gb_v4"] || string(after) == string(before) {
		t.Fatalf("the apply did not follow the memory: %+v", r.state1("gb"))
	}
}

// After a restart the new process would load every country set again,
// which needs nft over the kernel's ruleset twice. When that does not fit,
// the country sets the kernel holds are kept as they are, the rules apply,
// and the log says so; nothing is opened.
func TestARestartWithoutTheMemoryKeepsTheKernelsCountrySets(t *testing.T) {
	r := newRig(t, ruRule+gbRule, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	before := len(r.nft.applies())
	r.mu.Lock()
	r.mem = 1
	r.mu.Unlock()
	e2 := NewEngine(r.e.o)
	if err := e2.Reload(); err != nil {
		t.Fatalf("the restart was refused: %v", err)
	}
	a := r.nft.applies()
	if len(a) == before {
		t.Fatal("the restart applied nothing")
	}
	if strings.Contains(a[len(a)-1].stdin, "add element inet netryx_rules cc_") {
		t.Fatalf("the country sets were loaded again:\n%s", a[len(a)-1].stdin)
	}
	if !r.nft.objects["set cc_ru_v4"] || !r.nft.objects["set cc_gb_v4"] {
		t.Fatalf("a country set went: %v", r.nft.objects)
	}
	for _, id := range []string{"ru", "gb"} {
		for _, s := range e2.State() {
			if s.ID == id && !s.Applied {
				t.Errorf("%s not applied: %+v", id, s)
			}
		}
	}
	r.mu.Lock()
	logs := strings.Join(r.logs, "\n")
	r.mu.Unlock()
	if !strings.Contains(logs, "country sets kept as the kernel held them") {
		t.Errorf("the log does not say the sets were kept:\n%s", logs)
	}
	// The next reload of the same process takes them as loaded.
	if err := e2.Reload(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	n := strings.Count(strings.Join(r.logs, "\n"), "country sets kept as the kernel held them")
	r.mu.Unlock()
	if n != 1 {
		t.Errorf("the sets were kept %d times, want once", n)
	}
}

// An app rule loads no country set: it is not held back on a starved router.
func TestAnAppRuleIsNotHeldBackOnAStarvedRouter(t *testing.T) {
	r := newRig(t, ruRule, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.mem = 1
	r.mu.Unlock()
	r.write(ruRule + ytForLaptop)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if !r.state1("yt").Applied {
		t.Fatalf("an app rule was held back: %+v", r.state1("yt"))
	}
}

// A refusal that does not change is logged once, not at every retry.
func TestARefusalIsLoggedOnceWhileItHolds(t *testing.T) {
	now := winter(t)
	r := newRig(t, ruRule, now)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.write(ruRule + gbRule)
	r.mu.Lock()
	r.mem = 1
	r.mu.Unlock()
	if err := r.e.Reload(); err == nil {
		t.Fatal("setup: refused")
	}
	for i := 1; i <= 3; i++ {
		at := now.Add(time.Duration(i) * 6 * time.Minute)
		r.set(at, time.Duration(i)*6*time.Minute)
		r.e.Tick(at)
	}
	r.mu.Lock()
	n := 0
	for _, l := range r.logs {
		if strings.Contains(l, "too little memory") {
			n++
		}
	}
	r.mu.Unlock()
	if n != 1 {
		t.Errorf("the refusal was logged %d times", n)
	}
}

// A rule whose country sets were kept from the kernel is enforced, with the
// ranges it had: it says so, and is still applied.
func TestAKeptCountryRuleSaysItsRangesAreFromBefore(t *testing.T) {
	r := newRig(t, ruRule, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.mem = 1
	r.mu.Unlock()
	e2 := NewEngine(r.e.o)
	if err := e2.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, s := range e2.State() {
		if s.ID == "ru" && (!s.Applied || !strings.Contains(s.Note, "kept from the kernel")) {
			t.Fatalf("the kept rule does not say so: %+v", s)
		}
	}
}

// Kept sets are loaded again once the memory is there, at the next retry,
// without a change of the config, and the note goes.
func TestKeptCountrySetsAreFilledOnceTheMemoryIsThere(t *testing.T) {
	now := winter(t)
	r := newRig(t, ruRule, now)
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.mem = 1
	r.mu.Unlock()
	e2 := NewEngine(r.e.o)
	if err := e2.Reload(); err != nil {
		t.Fatal(err)
	}
	before := len(r.nft.applies())
	r.mu.Lock()
	r.mem = 0
	r.mu.Unlock()
	at := now.Add(6 * time.Minute)
	r.set(at, 6*time.Minute)
	e2.Tick(at)
	a := r.nft.applies()
	if len(a) == before || !strings.Contains(a[len(a)-1].stdin, "cc_ru_v4") || !strings.Contains(a[len(a)-1].stdin, "add element") {
		t.Fatalf("the kept sets were not loaded again: %d applies", len(a)-before)
	}
	for _, s := range e2.State() {
		if s.ID == "ru" && (s.Note != "" || !s.Applied) {
			t.Fatalf("after the fill: %+v", s)
		}
	}
}

// A country kept from the kernel is tried again at each reload without
// holding the rest back, even once other countries were loaded since.
func TestAStaleCountryNeverHoldsTheRestBack(t *testing.T) {
	r := newRig(t, ruRule, winter(t))
	if err := r.e.Reload(); err != nil {
		t.Fatal(err)
	}
	r.write(ruRule + gbRule)
	// The restart: loading RU and GB does not fit, GB alone does.
	r.mu.Lock()
	r.mem, r.memSeq = 1, []uint64{1, 1 << 40}
	r.mu.Unlock()
	e2 := NewEngine(r.e.o)
	if err := e2.Reload(); err != nil {
		t.Fatalf("the restart: %v", err)
	}
	if !r.nft.objects["set cc_gb_v4"] {
		t.Fatal("setup: GB was loaded")
	}
	// RU is still the kernel's, and there is no memory to load it: the
	// reload keeps it again rather than refusing everything.
	if err := e2.Reload(); err != nil {
		t.Fatalf("a stale country held the rest back: %v", err)
	}
	for _, s := range e2.State() {
		if s.ID == "ru" && !strings.Contains(s.Note, "kept from the kernel") {
			t.Errorf("ru: %+v", s)
		}
	}
}
