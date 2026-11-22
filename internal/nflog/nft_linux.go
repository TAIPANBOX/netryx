//go:build linux

package nflog

import (
	"os/exec"
	"strings"
)

func runNFT(script string) (string, error) {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
