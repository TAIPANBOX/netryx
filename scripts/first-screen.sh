#!/bin/sh
# Prints the files the page's script imports statically, transitively (from"./x.js"
# or import"./x.js"; a dynamic import("./x.js") is not one), one path per line
# relative to the dist, sorted, app.js itself left out: the files the browser
# fetches before anything shows, so the login form needs them before anyone
# signs in. web/build.sh writes this list beside the page as first-screen.txt.
#
#   sh scripts/first-screen.sh internal/web/dist
set -eu

dist=${1:?usage: first-screen.sh <dist>}
[ -s "$dist/app.js" ] || { echo "first-screen: no $dist/app.js" >&2; exit 1; }

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
		[ -s "$dist/$p" ] || { echo "first-screen: $f imports $p, which is not there" >&2; exit 1; }
		seen="$seen$p "
		queue="$queue $p"
	done
done
for f in $seen; do [ "$f" = app.js ] || echo "$f"; done | LC_ALL=C sort
