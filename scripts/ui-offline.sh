#!/bin/sh
# The built console fetches nothing from outside the router and writes no
# markup from strings.
#
#   sh scripts/ui-offline.sh internal/web/dist [web/src]
#
# Every file in <dist> and in the sources <src> (default web/src) must name no
# http://, https://, ws:// or wss:// address, and no scheme-less //host/ one,
# which a browser loads over the page's own scheme. The XML namespace identifiers Preact passes to
# createElementNS (SVG, MathML, XLink, XHTML) are names, never fetched, and are
# the only addresses allowed.
#
# Markup from strings is checked in the sources and in the page and its
# stylesheet, not in app.js: Preact's own diff names innerHTML and
# dangerouslySetInnerHTML to support a prop that only our code could pass, so
# the bundle carries the words whether or not they are used. The sources are
# where a use would be, and they must have none.
set -eu

dist=${1:?usage: ui-offline.sh <dist> [<src>]}
src=${2:-$(dirname "$0")/../web/src}
if [ ! -s "$dist/app.js" ]; then
	echo "measured nothing: $dist/app.js is missing or empty (run web/build.sh)" >&2
	exit 1
fi
if [ -z "$(find "$src" -name '*.ts*' 2>/dev/null)" ]; then
	echo "measured nothing: no sources under $src" >&2
	exit 1
fi

fails=0
report() { # report <file> <what was found>
	echo "$1:"
	echo "$2" | sed 's/^/    /'
	fails=$((fails + 1))
}

urls() {
	sed \
		-e 's#http://www\.w3\.org/2000/svg##g' \
		-e 's#http://www\.w3\.org/1999/xlink##g' \
		-e 's#http://www\.w3\.org/1998/Math/MathML##g' \
		-e 's#http://www\.w3\.org/1999/xhtml##g' \
		-e 's#http://www\.w3\.org/XML/1998/namespace##g' \
		"$1" | grep -o -E '(https?|wss?)://[^"'"'"' )<>]*|(^|[("'"'"'= ])//[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+[/:"'"'"')]' | sort -u || true
}

markup() {
	grep -o -E 'dangerouslySetInnerHTML|innerHTML|outerHTML|insertAdjacentHTML|document\.write' "$1" | sort -u || true
}

n=0
for f in $(find "$dist" "$src" -type f ! -name .keep | sort); do
	n=$((n + 1))
	found=$(urls "$f")
	case "$f" in
	"$dist"/*.js) ;;
	*) found=$(printf '%s\n%s' "$found" "$(markup "$f")" | sed '/^$/d') ;;
	esac
	[ -z "$found" ] || report "$f" "$found"
done

[ "$fails" -eq 0 ] || { echo "ui-offline: $fails of $n file(s) reach outside the router or write markup from strings" >&2; exit 1; }
echo "ui-offline: $n file(s), nothing fetched from outside, no markup from strings"
