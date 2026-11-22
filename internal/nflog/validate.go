package nflog

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
)

// nftCheck is the seam for the dry run, for the same reason nftRun is one.
var nftCheck = checkNFT

// Validate asks nft whether this ruleset would be accepted, without applying
// it. `nft -c` runs the whole transaction and aborts it, so the answer comes
// from the kernel rather than from a syntax parser: a build without the netdev
// family, or without dynamic sets, or an interface that has gone away, is
// reported here instead of at the moment the watcher tries to install it.
//
// This is what makes `netryx check` worth running. The failure it prevents is
// the quiet one: a watcher that installed nothing, read an empty log group,
// and reported a silent network.
func Validate(o RuleOptions) error {
	script := RuleSet(o)
	if o.Mode == Ingress {
		devices, err := DevicesFor(sysfsNet, o.Interfaces)
		if err != nil {
			return fmt.Errorf("reading the bridge's members: %w", err)
		}
		if len(devices) == 0 {
			// nft would render `devices = { }` and answer with a parser
			// complaint about a brace, which tells the operator nothing about
			// the bridge being empty. @measured 2026-09-15: that is exactly
			// what a fresh bridge with nothing plugged in produced.
			return fmt.Errorf("%s has no member devices to watch: an ingress hook attaches to ports, and this bridge has none", strings.Join(o.Interfaces, ", "))
		}
		script = NetdevRuleSet(o, devices)
	}
	if err := dryRun(script); err != nil {
		return err
	}
	if o.Mode == Ingress && o.Own {
		// The output chain is a second table, installed beside the first, so
		// it needs its own answer from the kernel.
		return dryRun(OwnRuleSet(o))
	}
	return nil
}

func dryRun(script string) error {
	out, err := nftCheck(script)
	if err == nil {
		return nil
	}
	if out == "" {
		return err
	}
	// nft's own message names the line and the expression, and it is more
	// useful than anything this package could say about it.
	return fmt.Errorf("%s", firstLine(out))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}

// Recoverable says whether a receive error means "copies were lost" rather
// than "this socket is finished".
//
// The kernel sets ENOBUFS on the socket when a burst arrived faster than the
// reader drained it, and returns it once per episode. Treating that as fatal
// is how a monitor dies on exactly the minute worth watching, and, because the
// process exits rather than returns, how it leaves its table installed behind
// it.
func Recoverable(err error) bool {
	return errors.Is(err, syscall.ENOBUFS)
}
