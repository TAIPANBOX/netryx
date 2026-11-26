package rules

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
