#!/bin/sh
# The history store has to fit a router: with every ring full under the heavy
# profile (30 busy devices, 31 days), its live heap stays under a budget. The
# test builds the whole month in memory, so it runs in CI, by
# scripts/gates-have-teeth.sh:
#
#   sh scripts/memory-budget.sh
#
# NETRYX_HEAP_BUDGET_MB sets the budget (24 MB by default); the test itself
# decides, and this script refuses to call a run that measured nothing a pass.
set -eu
cd "$(dirname "$0")/.."

NETRYX_HEAP_BUDGET_MB=${NETRYX_HEAP_BUDGET_MB:-24}
export NETRYX_HEAP_BUDGET_MB

out=$(mktemp)
trap 'rm -f "$out"' EXIT
status=0
go test -count=1 -tags heavyprofile -run '^TestHeavyProfile$' -v -timeout 30m ./internal/rollup/ >"$out" 2>&1 || status=$?

heap=$(sed -n 's/.*heap with every ring full[^:]*: \([0-9.]*\) MB.*/\1/p' "$out" | tail -1)
written=$(sed -n 's/.*written a day: \([0-9]*\) KB.*/\1/p' "$out" | tail -1)
if [ -z "$heap" ] || ! grep -q -- '--- \(PASS\|FAIL\): TestHeavyProfile' "$out"; then
	cat "$out" >&2
	echo "memory-budget: the heavy profile did not run, so nothing was measured" >&2
	exit 1
fi
echo "heap with every ring full: $heap MB (budget $NETRYX_HEAP_BUDGET_MB MB); written a day: ${written:-?} KB"
if [ "$status" -ne 0 ]; then
	grep -E 'over its budget|FAIL' "$out" >&2 || true
	echo "memory-budget: the history store is over its budget of $NETRYX_HEAP_BUDGET_MB MB" >&2
	exit 1
fi
