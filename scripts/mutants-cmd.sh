#!/bin/sh
# Break cmd/netryx on purpose, one fault at a time, and require a test to catch
# it. Coverage counts execution, not checking: this is what says the lines that
# run are lines an assertion can fail on.
#
#   sh scripts/mutants-cmd.sh
#
# Every mutant is a defect this code could plausibly have, and several of them
# are defects it or its neighbours actually had: counters summed across both
# directions, a silent zero when accounting is off, an unknown mode quietly
# becoming a working configuration.
set -u
cd "$(dirname "$0")/.." || exit 1
f=cmd/netryx/main.go
orig=$(mktemp)
cp "$f" "$orig"
trap 'cp "$orig" "$f"; rm -f "$orig"' EXIT INT TERM
caught=0
survived=0

mutant() { # mutant NAME "old|||new" TEST_REGEXP
	cp "$orig" "$f"
	MUT="$2" python3 - <<'PY' || { echo "  ??        $1: anchor missing"; return; }
import os, sys
old, new = os.environ["MUT"].split("|||")
p = "cmd/netryx/main.go"
s = open(p).read()
if old not in s:
    sys.exit(3)
open(p, "w").write(s.replace(old, new, 1))
PY
	if go test -run "$3" ./cmd/netryx/ >/dev/null 2>&1; then
		survived=$((survived + 1))
		echo "  SURVIVED  $1"
	else
		caught=$((caught + 1))
		echo "  caught    $1"
	fi
}

mutant "up and down summed into one counter" \
	'd.UpBytes += f.Up.Bytes|||d.UpBytes += f.Up.Bytes + f.Down.Bytes' TestSnapshotFolds
mutant "an unnamed device is left blank" \
	'if d.Name == "" {
				d.Name = k
			}|||_ = k' TestSnapshotCarries
mutant "the OFFLOAD mark is dropped" \
	'd.Offloaded = d.Offloaded || f.Offloaded|||_ = f.Offloaded' TestSnapshotFolds
mutant "accounting-off is not carried" \
	'AcctOff: st.AcctOff|||AcctOff: false' TestSnapshotCarries
mutant "a missing conntrack returns an empty report" \
	'return export.Snapshot{}, err
	}
	defer fh.Close()|||return export.Snapshot{}, nil
	}
	defer fh.Close()' TestSnapshotRefuses
mutant "an unknown mode quietly becomes ingress" \
	'return nflog.RuleOptions{}, fmt.Errorf("unknown mode %q: ingress or forward", mode)|||o.Mode = nflog.Ingress' TestRuleOptionsRefuses
mutant "a recoverable loss kills the reader" \
	'lost++
			log.Printf("the kernel lost copies (%d)", lost)
			continue|||return' TestReadIntoSurvives
mutant "the stop channel is ignored" \
	'select {
		case <-stop:
			return
		default:
		}|||_ = stop' TestReadIntoStops

cp "$orig" "$f"
echo "  $caught caught, $survived survived"
[ "$survived" -eq 0 ] || exit 1
go test ./cmd/netryx/ >/dev/null 2>&1 || { echo "  RESTORE FAILED: the tree is not green again"; exit 1; }
echo "  and the tree is green again"
