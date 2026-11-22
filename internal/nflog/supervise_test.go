package nflog

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// What the kernel prints when a device under a live chain disappears, taken
// from the Flint on 2026-09-15: the devices line is gone entirely and the
// chain is still there. Anything that treats a missing line as a parse
// failure, or as "nothing installed, all is well", recreates the blindness.
const chainAfterDeviceLeft = `table netdev netryx {
	chain observe {
		type filter hook ingress priority filter - 1; policy accept;
		counter packets 12 bytes 900 comment "seen"
	}
}`

const chainWithDevices = `table netdev netryx {
	chain observe {
		type filter hook ingress devices = { "lan2", "phy0-ap0" } priority filter - 1; policy accept;
		counter packets 12 bytes 900 comment "seen"
	}
}`

// With exactly one device nft prints it in the singular and without braces.
// Taken from the Flint, 2026-09-15. A parser that knows only the plural form
// reads this as "attached to nothing" and reinstalls the ruleset every pass,
// for ever, which counts as working in every test and as churn on the router.
const chainWithOneDevice = `table netdev netryx {
	chain observe {
		type filter hook ingress device "lan5.99" priority filter - 1; policy accept;
		counter packets 0 bytes 0 comment "seen"
	}
}`

func TestTheKernelIsAskedWhatIsAttached(t *testing.T) {
	if d := ParseChainDevices(chainWithOneDevice); len(d) != 1 || d[0] != "lan5.99" {
		t.Fatalf("one device is printed in the singular: %v", d)
	}

	got := ParseChainDevices(chainWithDevices)
	if len(got) != 2 || got[0] != "lan2" || got[1] != "phy0-ap0" {
		t.Fatalf("the device list must come back unquoted and whole: %v", got)
	}
	if d := ParseChainDevices(chainAfterDeviceLeft); len(d) != 0 {
		t.Fatalf("a chain with no devices line is attached to nothing, got %v", d)
	}
	if d := ParseChainDevices(""); len(d) != 0 {
		t.Fatalf("no table at all is also nothing, got %v", d)
	}
	// Order is nft's business, not ours.
	if !SameDevices([]string{"a", "b"}, []string{"b", "a"}) {
		t.Fatal("the same devices in another order are the same devices")
	}
	for _, pair := range [][2][]string{
		{{"a"}, {"a", "b"}},
		{{"a", "b"}, {"a"}},
		{{"a", "a"}, {"a", "b"}},
		{{"a"}, nil},
	} {
		if SameDevices(pair[0], pair[1]) {
			t.Fatalf("%v and %v are not the same set", pair[0], pair[1])
		}
	}
}

// The whole point: a member that vanishes and comes back must not leave the
// watcher reporting silence.
func TestAMemberThatComesBackIsWatchedAgain(t *testing.T) {
	want := []string{"lan2", "phy0-ap0"}
	have := []string{"lan2", "phy0-ap0"}
	applied := 0
	var said []string
	s := &Supervisor{
		Want:   func() ([]string, error) { return want, nil },
		Have:   func() ([]string, error) { return have, nil },
		Apply:  func() error { applied++; have = append([]string{}, want...); return nil },
		Report: func(m string) { said = append(said, m) },
	}

	if changed, err := s.Check(); changed || err != nil {
		t.Fatalf("nothing changed, nothing to do: %v %v", changed, err)
	}
	if applied != 0 {
		t.Fatal("a steady bridge must not be reinstalled every pass")
	}

	// The access point is recreated: the kernel drops that hook silently.
	have = []string{"lan2"}
	changed, err := s.Check()
	if err != nil || !changed {
		t.Fatalf("a lost device must be noticed: %v %v", changed, err)
	}
	if applied != 1 || !SameDevices(have, want) {
		t.Fatalf("it must be reinstalled over the real list: %d %v", applied, have)
	}
	if len(said) != 1 || !strings.Contains(said[0], "phy0-ap0") {
		t.Fatalf("a silent repair is still a silence: %v", said)
	}

	// A new member appears: same treatment.
	want = append(want, "phy1-ap0")
	if changed, err := s.Check(); err != nil || !changed {
		t.Fatalf("a new member must be picked up: %v %v", changed, err)
	}
	if applied != 2 {
		t.Fatalf("expected a second install, got %d", applied)
	}
}

func TestAnEmptyOrBrokenBridgeIsNotReinstalledOverAndOver(t *testing.T) {
	var applied int
	s := &Supervisor{
		Want:  func() ([]string, error) { return nil, nil },
		Have:  func() ([]string, error) { return []string{"lan2"}, nil },
		Apply: func() error { applied++; return nil },
	}
	// A bridge is briefly empty during a reload. Installing a chain with an
	// empty device list is a syntax error, so this must wait, not try.
	if _, err := s.Check(); err == nil {
		t.Fatal("an empty bridge is not something to install")
	}
	if applied != 0 {
		t.Fatal("nothing may be installed over an empty device list")
	}

	// The kernel refusing to answer is not a licence to guess either.
	s.Want = func() ([]string, error) { return []string{"lan2"}, nil }
	s.Have = func() ([]string, error) { return nil, errors.New("nft: permission denied") }
	if _, err := s.Check(); err == nil || applied != 0 {
		t.Fatalf("an unreadable kernel state must stop the pass: %d", applied)
	}

	// And a failing install is reported, not swallowed.
	s.Have = func() ([]string, error) { return nil, nil }
	s.Apply = func() error { applied++; return errors.New("nft: no such device") }
	if _, err := s.Check(); err == nil || !strings.Contains(err.Error(), "no such device") {
		t.Fatalf("the reason must survive: %v", err)
	}
}

// The loop must survive what it is there for: a bridge that is empty for a
// few seconds during a reload, then comes back.
func TestTheLoopSurvivesAReload(t *testing.T) {
	// The loop runs in its own goroutine, so what it reads and writes here is
	// guarded, and the test waits for it to return before reading.
	var mu sync.Mutex
	state := []string{}
	applied := 0
	var said []string
	s := &Supervisor{
		Want: func() ([]string, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), state...), nil
		},
		Have: func() ([]string, error) { return nil, nil },
		Apply: func() error {
			mu.Lock()
			defer mu.Unlock()
			applied++
			return nil
		},
		Report: func(m string) {
			mu.Lock()
			defer mu.Unlock()
			said = append(said, m)
		},
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { s.Run(stop, time.Millisecond); close(done) }()
	time.Sleep(20 * time.Millisecond) // empty: reports once, keeps going
	mu.Lock()
	state = []string{"lan2"}
	mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	close(stop)
	<-done
	if applied == 0 {
		t.Fatal("the loop must install once the bridge has members again")
	}
	empties := 0
	for _, m := range said {
		if strings.Contains(m, "no devices") {
			empties++
		}
	}
	if empties != 1 {
		t.Fatalf("the same complaint every tick buries everything else: %d of %v", empties, said)
	}
}
