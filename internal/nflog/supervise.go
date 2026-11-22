package nflog

import (
	"fmt"
	"time"
)

// Supervisor keeps the installed rule matching the bridge it was asked to
// watch.
//
// The failure it exists for is silent by construction. A netdev chain is
// attached to devices, and when one of them goes away - a `wifi reload`, a
// channel change that recreates the access point, a cable unplugged, a WDS
// peer associating - the kernel removes that device's hook and reports
// nothing. Traffic on the device that comes back is then never seen again,
// and the dashboard shows a quiet network, which is exactly what it shows
// when the house is asleep.
//
// So the question is not "did I install it" but "is it still attached to what
// the bridge is made of now", and only the kernel can answer that.
type Supervisor struct {
	// Want is what the bridge is made of, read from sysfs.
	Want func() ([]string, error)
	// Have is what the kernel says the chain is attached to.
	Have func() ([]string, error)
	// Apply reinstalls the ruleset.
	Apply func() error
	// Report is how a change is made visible. Silence here would recreate the
	// problem one level up.
	Report func(string)
}

// Check does one pass and says whether it reinstalled anything.
func (s *Supervisor) Check() (bool, error) {
	want, err := s.Want()
	if err != nil {
		return false, fmt.Errorf("reading the bridge: %w", err)
	}
	if len(want) == 0 {
		// A bridge with no members cannot be watched, and installing a chain
		// with an empty device list is a syntax error. Wait for a member.
		return false, fmt.Errorf("no devices to watch")
	}
	have, err := s.Have()
	if err != nil {
		return false, err
	}
	if SameDevices(want, have) {
		return false, nil
	}
	if err := s.Apply(); err != nil {
		return false, fmt.Errorf("reinstalling: %w", err)
	}
	if s.Report != nil {
		s.Report(fmt.Sprintf("devices changed: was %v, now %v, rule reinstalled", have, want))
	}
	return true, nil
}

// Run checks every interval until stop is closed. Errors are reported and the
// loop continues: a bridge that is briefly empty during a reload must not end
// the run.
func (s *Supervisor) Run(stop <-chan struct{}, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	var lastErr string
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if _, err := s.Check(); err != nil {
				// The same message every tick would bury everything else, so
				// it is reported when it changes.
				if s.Report != nil && err.Error() != lastErr {
					s.Report("supervisor: " + err.Error())
				}
				lastErr = err.Error()
				continue
			}
			lastErr = ""
		}
	}
}
