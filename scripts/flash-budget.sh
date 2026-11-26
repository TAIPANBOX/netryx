#!/bin/sh
# The daily file has to fit a router's flash and memory: under the profile of
# 64 busy devices, one day's file stays under a budget, and the 30 days kept
# stay under a heap budget once loaded. The test writes 31 days, so it runs in
# CI, by scripts/gates-have-teeth.sh:
#
#   sh scripts/flash-budget.sh
#
# NETRYX_DAILY_BUDGET_KB sets the file budget (16 KB by default) and
# NETRYX_DAILY_HEAP_BUDGET_KB the heap budget (1024 KB); the test itself
# decides, and this script refuses to call a run that measured nothing a pass.
set -eu
cd "$(dirname "$0")/.."

NETRYX_DAILY_BUDGET_KB=${NETRYX_DAILY_BUDGET_KB:-16}
NETRYX_DAILY_HEAP_BUDGET_KB=${NETRYX_DAILY_HEAP_BUDGET_KB:-1024}
export NETRYX_DAILY_BUDGET_KB NETRYX_DAILY_HEAP_BUDGET_KB

out=$(mktemp)
trap 'rm -f "$out"' EXIT
status=0
go test -count=1 -tags heavyprofile -run '^TestDailyProfile$' -v -timeout 10m ./internal/daily/ >"$out" 2>&1 || status=$?

file=$(sed -n "s/.*a day's file for 64 devices: \([0-9]*\) bytes.*/\1/p" "$out" | tail -1)
heap=$(sed -n 's/.*heap of [0-9]* loaded days: \([0-9.-]*\) KB.*/\1/p' "$out" | tail -1)
if [ -z "$file" ] || [ -z "$heap" ] || ! grep -q -- '--- \(PASS\|FAIL\): TestDailyProfile' "$out"; then
	cat "$out" >&2
	echo "flash-budget: the daily profile did not run, so nothing was measured" >&2
	exit 1
fi
echo "a day's file for 64 devices: $file bytes (budget $NETRYX_DAILY_BUDGET_KB KB); 30 loaded days: $heap KB of heap (budget $NETRYX_DAILY_HEAP_BUDGET_KB KB)"
if [ "$status" -ne 0 ]; then
	grep -E 'over (its|their) budget|FAIL' "$out" >&2 || true
	echo "flash-budget: the daily file is over its budget" >&2
	exit 1
fi
