package nflog

import (
	"fmt"
	"regexp"
	"strings"
)

// nftList is the seam for reading back what the kernel actually has.
var nftList = listNFT

var (
	devicesLine = regexp.MustCompile(`devices\s*=\s*\{([^}]*)\}`)
	// With exactly one device nft prints `device "lan5.99"`, no braces and no
	// plural. @measured on the Flint. Reading only the plural form is a
	// parser that answers "attached to nothing" on every single-port bridge.
	oneDeviceLine = regexp.MustCompile(`hook\s+\w+\s+device\s+"?([A-Za-z0-9_.-]+)"?`)
)

// ParseChainDevices reads the device list out of an nft chain listing.
//
// The reason this exists rather than trusting what was installed: when a
// device disappears the kernel removes that device's hook and says nothing.
// @measured on the Flint, 2026-09-15, with a VLAN sub-interface deleted under
// a live chain: nft then prints the chain with NO devices line at all, and the
// table is still there. An empty answer is therefore the normal shape of the
// failure, not a parse error.
func ParseChainDevices(listing string) []string {
	m := devicesLine.FindStringSubmatch(listing)
	if m == nil {
		if one := oneDeviceLine.FindStringSubmatch(listing); one != nil {
			return []string{one[1]}
		}
		return nil
	}
	var out []string
	for _, f := range strings.Split(m[1], ",") {
		f = strings.TrimSpace(strings.Trim(strings.TrimSpace(f), `"`))
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// InstalledDevices asks the kernel which devices the observing chain is
// attached to right now.
func InstalledDevices(table string) ([]string, error) {
	out, err := nftList(table)
	if err != nil {
		return nil, fmt.Errorf("reading the installed chain: %w", err)
	}
	return ParseChainDevices(out), nil
}

// SameDevices compares two device lists as sets, because the order nft prints
// is not promised to be the order sysfs lists.
func SameDevices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
		if seen[v] < 0 {
			return false
		}
	}
	return true
}
