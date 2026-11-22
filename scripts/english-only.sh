#!/bin/sh
# Everything a person reads from netryx is English: the console page, every
# message the service returns into it, Telegram alerts, netryx ctl and the
# command line's own output.
#
#   sh scripts/english-only.sh [dir...]     (default: web/src internal cmd)
#
# Every source file under the dirs, tests excepted, must carry no Cyrillic
# letter. Tests are left out on purpose: they feed the service names a
# household may write in any script (a device called in Ukrainian must pass
# through untouched), and that is data, not copy.
#
# Cyrillic is matched by its UTF-8 bytes (U+0400 to U+04FF lead with 0xD0 to
# 0xD3), so the gate needs no grep -P and reads the same on macOS and forge.
set -eu

[ "$#" -gt 0 ] || set -- web/src internal cmd
files=$(find "$@" -type f \( -name '*.go' -o -name '*.ts' -o -name '*.tsx' -o -name '*.html' -o -name '*.css' -o -name '*.sh' \) \
	! -name '*_test.go' ! -name '*.test.ts' 2>/dev/null | sort)
if [ -z "$files" ]; then
	echo "measured nothing: no sources under $*" >&2
	exit 1
fi

cyr=$(printf '[\320-\323][\200-\277]')
n=0
fails=0
for f in $files; do
	n=$((n + 1))
	hits=$(LC_ALL=C grep -n "$cyr" "$f" || true)
	if [ -n "$hits" ]; then
		echo "$f:"
		echo "$hits" | sed 's/^/    /'
		fails=$((fails + 1))
	fi
done

[ "$fails" -eq 0 ] || { echo "english-only: $fails of $n file(s) carry Cyrillic a person would read" >&2; exit 1; }
echo "english-only: $n file(s), no Cyrillic outside the tests"
