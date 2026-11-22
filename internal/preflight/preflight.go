// Package preflight turns "it does not work" into a sentence that names the
// missing piece.
//
// netryx stands on three kernel facilities. On a router that lacks one of them
// the symptom is an empty dashboard, which reads as a quiet network rather
// than as a broken install. Every check here therefore names the package to
// install and the one-line command that fixes it.
package preflight

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Need is one kernel facility and how a person gets it.
type Need struct {
	What    string // what it is for, in the operator's terms
	Module  string // the kernel module
	Package string // the OpenWrt package that carries it
	OK      bool
	Detail  string
	// FixCmd replaces the install line where installing a package is not the
	// answer. A check whose advice is "install this" when the real fix is a
	// setting sends the operator to the wrong place.
	FixCmd string
}

// Fix is the command that installs what is missing.
func (n Need) Fix() string {
	if n.OK {
		return ""
	}
	if n.FixCmd != "" {
		return n.FixCmd
	}
	return "opkg install " + n.Package + "  (or: apk add " + n.Package + ")"
}

// Report is the outcome of the checks.
type Report struct {
	Needs   []Need
	Missing int
}

func (r Report) Error() error {
	if r.Missing == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d thing(s) to fix:\n", r.Missing)
	for _, n := range r.Needs {
		if n.OK {
			continue
		}
		// Not everything that can be wrong is a missing kernel module, and
		// printing "needs - (-)" for the rest makes the sentence unreadable
		// exactly where somebody is trying to read it.
		if n.Module == "-" || n.Module == "" {
			fmt.Fprintf(&b, "  %s: %s\n    %s\n", n.What, n.Detail, n.Fix())
			continue
		}
		fmt.Fprintf(&b, "  %s needs %s (%s): %s\n    %s\n", n.What, n.Module, n.Package, n.Detail, n.Fix())
	}
	return errors.New(b.String())
}

// Loaded reports whether a module is in /proc/modules. Built-in facilities do
// not appear there, so a caller treats "not listed" as a hint, never a verdict
// on its own: the authority is whether the socket actually works.
func Loaded(procModules string, module string) bool {
	b, err := os.ReadFile(procModules)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		name, _, _ := strings.Cut(line, " ")
		if name == module {
			return true
		}
	}
	return false
}

// Explain turns an error from a netlink open or dump into the same sentence a
// missing module would produce, because that is what such an error nearly
// always means in the field.
func Explain(what, module, pkg string, err error) Need {
	n := Need{What: what, Module: module, Package: pkg}
	if err == nil {
		n.OK = true
		return n
	}
	switch {
	case strings.Contains(err.Error(), "invalid argument"),
		strings.Contains(err.Error(), "protocol not supported"),
		strings.Contains(err.Error(), "not supported"):
		n.Detail = "the kernel refused the request, which on OpenWrt means the module is not installed"
	case strings.Contains(err.Error(), "operation not permitted"):
		n.Detail = "permission denied: this needs CAP_NET_ADMIN, so run it as the service user procd gives it"
	default:
		n.Detail = err.Error()
	}
	return n
}

// Check folds the individual results into one report.
func Check(needs ...Need) Report {
	r := Report{Needs: needs}
	for _, n := range needs {
		if !n.OK {
			r.Missing++
		}
	}
	return r
}
