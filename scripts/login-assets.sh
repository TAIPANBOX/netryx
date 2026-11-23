#!/bin/sh
# The login form must load before anyone signs in: every file the page's
# script imports statically is in the list the console opens without a
# session (first-screen.txt, written by web/build.sh), and the list names
# nothing else, so no other chunk is open.
#
#   sh scripts/login-assets.sh internal/web/dist
set -eu

dist=${1:?usage: login-assets.sh <dist>}
# The dist as given, before moving to the repository's root.
case "$dist" in /*) ;; *) dist="$(pwd)/$dist" ;; esac
cd "$(dirname "$0")/.."
if [ ! -s "$dist/app.js" ] || [ ! -f "$dist/first-screen.txt" ]; then
	echo "login-assets: no $dist/app.js or $dist/first-screen.txt, so nothing was measured" >&2
	exit 1
fi
want=$(mktemp)
have=$(mktemp)
trap 'rm -f "$want" "$have"' EXIT
sh scripts/first-screen.sh "$dist" >"$want"
grep -v '^$' "$dist/first-screen.txt" | LC_ALL=C sort >"$have" || true
missing=$(comm -23 "$want" "$have")
extra=$(comm -13 "$want" "$have")
fail=0
if [ -n "$missing" ]; then
	echo "login-assets: the page's script imports these before a login, and they are not opened:" >&2
	echo "$missing" >&2
	fail=1
fi
if [ -n "$extra" ]; then
	echo "login-assets: these are opened without a login and the page's script does not import them:" >&2
	echo "$extra" >&2
	fail=1
fi
echo "login form: $(wc -l <"$want" | tr -d ' ') chunk(s) imported statically, all opened, nothing else"
exit $fail
