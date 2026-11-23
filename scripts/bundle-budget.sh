#!/bin/sh
# The page is served from a router over Wi-Fi: what counts is what crosses the
# air, gzipped. The first screen (app.js and every chunk it imports statically,
# which the browser fetches before anything shows) has its budget, so it does
# not grow with every new screen; each chunk has one of its own, and all the
# files together a total. A screen loaded on demand (import()) is not part of
# the first screen.
#
#   sh scripts/bundle-budget.sh internal/web/dist
set -eu

dist=${1:?usage: bundle-budget.sh <dist>}
FIRST=61440   # 60 KB
CHUNK=24576   # 24 KB
TOTAL=163840  # 160 KB

[ -s "$dist/app.js" ] || { echo "bundle-budget: no $dist/app.js, so nothing was measured" >&2; exit 1; }

gz() { gzip -9c "$1" | wc -c | tr -d ' '; }
fail=0

# The first screen: app.js and, transitively, every static import of it
# (from"./x.js" or import"./x.js"; a dynamic import("./x.js") is not one).
seen=" app.js "
queue="app.js"
while [ -n "$queue" ]; do
	set -- $queue
	f=$1; shift; queue="$*"
	dir=$(dirname "$f")
	for ref in $(grep -aoE '(from|import)"\./[^"]+\.js"' "$dist/$f" | sed -E 's/^(from|import)"\.\///; s/"$//'); do
		p=$ref
		[ "$dir" = "." ] || p="$dir/$ref"
		case "$seen" in *" $p "*) continue ;; esac
		[ -s "$dist/$p" ] || { echo "$f imports $p, which is not there" >&2; fail=1; continue; }
		seen="$seen$p "
		queue="$queue $p"
	done
done
first=0
for f in $seen; do first=$((first + $(gz "$dist/$f"))); done
echo "first screen: $(echo $seen | wc -w | tr -d ' ') files, gzip $first bytes (budget $FIRST, slack $((FIRST - first)))"
[ "$first" -lt "$FIRST" ] || { echo "the first screen is over its budget of $FIRST bytes gzipped" >&2; fail=1; }

total=$(gz "$dist/app.js")
n=0
for c in "$dist"/chunks/*.js; do
	[ -e "$c" ] || continue
	s=$(gz "$c")
	n=$((n + 1))
	total=$((total + s))
	if [ "$s" -ge "$CHUNK" ]; then
		echo "$(basename "$c") is over the chunk budget: $s bytes gzipped, budget $CHUNK" >&2
		fail=1
	fi
done
echo "chunks: $n, all files together gzip $total bytes (budget $TOTAL, slack $((TOTAL - total)))"
[ "$total" -lt "$TOTAL" ] || { echo "the bundle and its chunks are over the total budget of $TOTAL bytes gzipped" >&2; fail=1; }
exit $fail
