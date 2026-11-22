//go:build linux

package nflog

import (
	"os/exec"
	"strings"
)

// checkNFT runs the transaction and throws it away. The ruleset on the router
// is not touched, which is what makes this safe to run on a live house.
func checkNFT(script string) (string, error) {
	cmd := exec.Command("nft", "-c", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
