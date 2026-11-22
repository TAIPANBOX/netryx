//go:build linux

package nflog

import (
	"os/exec"
	"strings"
)

func listNFT(table string) (string, error) {
	out, err := exec.Command("nft", "list", "chain", "netdev", table, "observe").CombinedOutput()
	if err != nil {
		// A table that is not there is an answer, not a failure: it means
		// nothing is installed, which the caller fixes by installing it.
		if strings.Contains(string(out), "No such file or directory") {
			return "", nil
		}
		return strings.TrimSpace(string(out)), err
	}
	return string(out), nil
}
