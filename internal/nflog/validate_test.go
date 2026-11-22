package nflog

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
)

// The check command exists to turn "the dashboard is empty" into a sentence.
// If it validated a ruleset that is not the one the watcher installs, it would
// pass on a router where the watcher then fails, which is worse than not
// checking at all.
func TestValidateChecksTheRulesetThatWillBeInstalled(t *testing.T) {
	var seen string
	restore := nftCheck
	nftCheck = func(script string) (string, error) { seen = script; return "", nil }
	t.Cleanup(func() { nftCheck = restore })

	o := DefaultRule("eth0")
	o.Mode = Forward
	if err := Validate(o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "table inet netryx") || !strings.Contains(seen, "hook forward") {
		t.Fatalf("the forward mode must be checked as a forward ruleset:\n%s", seen)
	}
	if strings.Contains(seen, "netdev") {
		t.Fatalf("and not as a netdev one:\n%s", seen)
	}

	// A dry run that says nothing about what failed sends the operator back to
	// guessing, so nft's own message is what comes out.
	nftCheck = func(string) (string, error) {
		return "/dev/stdin:5:31-45: Error: Could not process rule: No such file or directory\nmore noise", errors.New("exit status 1")
	}
	err := Validate(o)
	if err == nil {
		t.Fatal("a kernel that refuses the ruleset is a failure")
	}
	if !strings.Contains(err.Error(), "Could not process rule") {
		t.Fatalf("nft's own words must survive: %v", err)
	}
	if strings.Contains(err.Error(), "more noise") {
		t.Fatalf("one line is the message, not the whole output: %v", err)
	}

	// The ingress mode is the one that installs something different from what
	// it validates if this goes wrong, and it is also the one that cannot be
	// reached off a router: hence the sysfs seam.
	sysfs := fakeSysfs(t, map[string][]string{"br-lan": {"lan2", "phy0-ap0"}})
	restoreFS := sysfsNet
	sysfsNet = sysfs
	t.Cleanup(func() { sysfsNet = restoreFS })
	// Ingress installs two tables, so every script is kept and the netdev one
	// is the first.
	var scripts []string
	nftCheck = func(script string) (string, error) { scripts = append(scripts, script); return "", nil }
	ing := DefaultRule("br-lan")
	ing.Mode = Ingress
	if err := Validate(ing); err != nil {
		t.Fatal(err)
	}
	seen = scripts[0]
	if !strings.Contains(seen, "table netdev netryx") {
		t.Fatalf("the ingress mode must be checked as a netdev ruleset:\n%s", seen)
	}
	if !strings.Contains(seen, "devices = { lan2, phy0-ap0 }") {
		t.Fatalf("and against the devices it will really attach to:\n%s", seen)
	}
	// A bridge that is not there must fail the check rather than quietly
	// validating something else.
	gone := DefaultRule("br-gone")
	gone.Mode = Ingress
	gone.Own = false
	nftCheck = func(script string) (string, error) { seen = script; return "", nil }
	if err := Validate(gone); err != nil && !strings.Contains(err.Error(), "member") {
		// not a bridge: it stands for itself, and nft is the one that judges
		if !strings.Contains(seen, "devices = { br-gone }") {
			t.Fatalf("an unknown name is passed to nft to judge:\n%s", seen)
		}
	}

	// The own-traffic table is installed beside the netdev one, so a check
	// that validates only the netdev half passes on a router where the other
	// half is refused: the service then starts, installs one table, fails on
	// the second, and the operator is told nothing was wrong.
	var seenAll []string
	nftCheck = func(script string) (string, error) { seenAll = append(seenAll, script); return "", nil }
	both := DefaultRule("br-lan")
	both.Mode = Ingress
	both.Own = true
	if err := Validate(both); err != nil {
		t.Fatal(err)
	}
	if len(seenAll) != 2 {
		t.Fatalf("both tables must be checked, got %d", len(seenAll))
	}
	if !strings.Contains(seenAll[1], "hook output") {
		t.Fatalf("the second must be the router's own traffic:\n%s", seenAll[1])
	}

	// And when nft is not there at all, the error from running it is the
	// answer: there is no output to quote.
	nftCheck = func(string) (string, error) { return "", errors.New(`exec: "nft": executable file not found in $PATH`) }
	if err := Validate(o); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("a missing nft must be reported as such: %v", err)
	}
}

// A monitor that exits on the busiest minute is a monitor that is not there
// when it matters, and this one would also leave its rule behind when it went.
func TestLostCopiesAreNotALostRun(t *testing.T) {
	if !Recoverable(syscall.ENOBUFS) {
		t.Fatal("lost copies must be survivable")
	}
	if !Recoverable(fmt.Errorf("netlink: %w", syscall.ENOBUFS)) {
		t.Fatal("and survivable through a wrapping too")
	}
	for _, err := range []error{
		syscall.EPERM,    // the socket was never allowed
		syscall.ENOTSOCK, // it is gone
		errors.New("netlink: no such file or directory"),
		nil,
	} {
		if Recoverable(err) {
			t.Fatalf("%v is not a lost copy, it must end the run through the cleanup path", err)
		}
	}
}

// A bridge with no member ports renders `devices = { }`, which nft rejects as
// a syntax error somewhere in the middle of the script. The operator then gets
// a parser complaint about a brace, and the tool that was supposed to turn
// "the dashboard is empty" into a sentence has produced a riddle instead.
//
// Found on 2026-09-15 on a cloud machine, where a bridge was created for the
// test and nothing was plugged into it.
func TestValidateSaysTheBridgeHasNoMembersRatherThanLettingNftGuess(t *testing.T) {
	sysfs := fakeSysfs(t, map[string][]string{"br-lan": {}})
	restoreFS := sysfsNet
	sysfsNet = sysfs
	t.Cleanup(func() { sysfsNet = restoreFS })

	called := false
	restore := nftCheck
	nftCheck = func(string) (string, error) { called = true; return "", nil }
	t.Cleanup(func() { nftCheck = restore })

	o := DefaultRule("br-lan")
	o.Mode = Ingress
	err := Validate(o)
	if err == nil {
		t.Fatal("a bridge with nothing in it cannot be watched, and saying so is the point")
	}
	if !strings.Contains(err.Error(), "br-lan") {
		t.Fatalf("the message must name the bridge the operator asked for: %v", err)
	}
	if called {
		t.Fatalf("and it must not spend a dry run on a ruleset that cannot be valid: %v", err)
	}
}
