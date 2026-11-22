package preflight

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The point of the package: a missing module must produce a sentence naming
// the package to install, not an empty dashboard.
func TestAMissingModuleNamesItsPackage(t *testing.T) {
	n := Explain("connection tracking over netlink", "nf_conntrack_netlink",
		"kmod-nf-conntrack-netlink", errors.New("netlink: invalid argument"))
	if n.OK {
		t.Fatal("an error is not an OK result")
	}
	if !strings.Contains(n.Detail, "module is not installed") {
		t.Fatalf("the detail must say what it means: %q", n.Detail)
	}
	fix := n.Fix()
	if !strings.Contains(fix, "kmod-nf-conntrack-netlink") || !strings.Contains(fix, "apk add") {
		t.Fatalf("the fix must be a command a person can run: %q", fix)
	}

	rep := Check(n, Explain("packet log", "nfnetlink_log", "kmod-nfnetlink-log", nil))
	if rep.Missing != 1 {
		t.Fatalf("one of the two is missing: %d", rep.Missing)
	}
	err := rep.Error()
	if err == nil || !strings.Contains(err.Error(), "kmod-nf-conntrack-netlink") {
		t.Fatalf("the report must name the missing package: %v", err)
	}
	if strings.Contains(err.Error(), "nfnetlink_log") {
		t.Fatal("a facility that works must not be listed as a problem")
	}
}

// Permission is a different failure from absence, and telling them apart is
// the difference between installing a package and fixing a service account.
func TestPermissionIsNotConfusedWithAbsence(t *testing.T) {
	n := Explain("packet log", "nfnetlink_log", "kmod-nfnetlink-log",
		errors.New("netfilter netlink socket: operation not permitted"))
	if !strings.Contains(n.Detail, "CAP_NET_ADMIN") {
		t.Fatalf("a permission error must say so: %q", n.Detail)
	}
	if strings.Contains(n.Detail, "not installed") {
		t.Fatal("a permission error must not send someone installing packages")
	}
}

func TestEverythingPresentIsSilent(t *testing.T) {
	rep := Check(
		Explain("a", "m1", "p1", nil),
		Explain("b", "m2", "p2", nil),
	)
	if rep.Missing != 0 || rep.Error() != nil {
		t.Fatalf("nothing missing means no error: %+v", rep)
	}
}

func TestLoadedReadsProcModules(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "modules")
	if err := os.WriteFile(p, []byte(
		"nf_conntrack_netlink 40960 0 - Live 0x0000000000000000\n"+
			"nfnetlink 16384 3 nf_conntrack_netlink - Live 0x0000000000000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Loaded(p, "nf_conntrack_netlink") {
		t.Fatal("a listed module must be found")
	}
	if Loaded(p, "nfnetlink_log") {
		t.Fatal("a module that is not listed must not be claimed")
	}
	if Loaded(filepath.Join(dir, "nope"), "nfnetlink") {
		t.Fatal("a missing file is not evidence of a loaded module")
	}
}
