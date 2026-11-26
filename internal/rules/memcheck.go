package rules

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// An apply of country sets is refused before nft runs when the router has
// not the memory for it: on a 512 MB router every country needs an nft process
// of 170 to 230 MB, and the OOM killer, which settles it otherwise, takes the
// largest process, which may be another service
// (docs/small-router-memory-measured.md).

// nftPerByte is nft's peak per byte of the ruleset it holds, which is the
// kernel's netryx ruleset (nft reads it whole into its cache) and the script:
// 22 measured on the Beryl AX (aarch64, ten countries), 16 on x86-64 (every
// country). An architecture not measured takes the larger.
func nftPerByte(goarch string) uint64 {
	if goarch == "amd64" {
		return 16
	}
	return 22
}

// setPerByte is the kernel's memory for new sets per byte of script: 6.9
// measured on the Beryl AX (39 MB of SUnreclaim for 5.7 MB).
const setPerByte = 7

// memMargin is kept free beyond the need: on the Beryl AX the need fell 18 MB
// short of what the ten-country apply took (netryx's own growth beside it),
// and the OOM killer was reached at 9 MB available on the 512 MB clone.
const memMargin = 64 << 20

// applyNeed is the memory an apply takes: nft over the ruleset the kernel
// holds and the script, and the kernel's new sets.
func applyNeed(held, script int, goarch string) uint64 {
	return nftPerByte(goarch)*uint64(held+script) + setPerByte*uint64(script)
}

// roomFor says whether avail holds the need with the margin left free.
func roomFor(avail, need uint64) bool { return avail >= need+memMargin }

// ReadMemAvailable reads MemAvailable from /proc/meminfo, in bytes.
func ReadMemAvailable() (uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return parseMemAvailable(f)
}

func parseMemAvailable(r io.Reader) (uint64, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "MemAvailable:" {
			kb, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("MemAvailable %q: %w", f[1], err)
			}
			return kb * 1024, nil
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("no MemAvailable in meminfo")
}

// heldBytes is the size of the ruleset the kernel holds: the one netryx
// applied last, or at start the rules.nft the init loaded; nothing without
// the table.
func (e *Engine) heldBytes(present bool) int {
	if !present {
		return 0
	}
	e.mu.Lock()
	h, known := e.held, e.heldKnown
	e.mu.Unlock()
	if known {
		return h
	}
	if fi, err := os.Stat(filepath.Join(e.o.StateDir, "rules.nft")); err == nil {
		return int(fi.Size())
	}
	return 0
}

// tooBig is why an apply that loads new country sets would not fit, "" when
// it would, when it loads none (a delete or an app rule only frees or keeps
// what the router held), or when the memory cannot be read.
func (e *Engine) tooBig(present bool, script string, fresh int) string {
	if fresh == 0 || e.o.MemAvailable == nil {
		return ""
	}
	avail, err := e.o.MemAvailable()
	if err != nil {
		e.o.Logf("available memory not read (%v): the apply is not checked against it", err)
		return ""
	}
	need := applyNeed(e.heldBytes(present), len(script), runtime.GOARCH)
	if roomFor(avail, need) {
		return ""
	}
	return fmt.Sprintf("too little memory to apply: nft and the new sets would need about %d MiB, %d MiB is available and %d MiB is kept free",
		(need+1<<20-1)>>20, avail>>20, memMargin>>20)
}
