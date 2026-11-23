#!/bin/sh
# The interventions journal has to fit a router's flash: under 50
# interventions a day for 30 days, what it writes a day (each append at the
# 10 KiB measured on f2fs, each rewrite its whole size) stays under a budget.
# Run on forge:
#
#   scripts/forge.sh sh scripts/journal-budget.sh
#
# NETRYX_JOURNAL_BUDGET_KB sets the budget a day (640 KB by default); the test
# decides, and this script refuses to call a run that measured nothing a pass.
set -eu
cd "$(dirname "$0")/.."

NETRYX_JOURNAL_BUDGET_KB=${NETRYX_JOURNAL_BUDGET_KB:-640}
export NETRYX_JOURNAL_BUDGET_KB

out=$(mktemp)
trap 'rm -f "$out"' EXIT
status=0
go test -count=1 -tags heavyprofile -run '^TestJournalProfile$' -v -timeout 10m ./internal/journal/ >"$out" 2>&1 || status=$?

kb=$(sed -n 's/.*journal written a day: \([0-9.]*\) KB.*/\1/p' "$out" | tail -1)
if [ -z "$kb" ] || ! grep -q -- '--- \(PASS\|FAIL\): TestJournalProfile' "$out"; then
	cat "$out" >&2
	echo "journal-budget: the journal profile did not run, so nothing was measured" >&2
	exit 1
fi
echo "journal written a day: $kb KB (budget $NETRYX_JOURNAL_BUDGET_KB KB)"
if [ "$status" -ne 0 ]; then
	grep -E 'over its budget|FAIL' "$out" >&2 || true
	echo "journal-budget: the journal is over its budget" >&2
	exit 1
fi
