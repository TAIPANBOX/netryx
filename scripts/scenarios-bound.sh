#!/bin/sh
# Every scenario names a test, and every test it names exists. A mismatch is a
# defect in itself: a scenario nobody proves, or a proof nobody describes.
#
# The pattern includes digits on purpose. Without them TestRotatingIPv6... was
# read as "TestRotatingIPv" and reported missing, which is a check that cannot
# see its own subject.
set -e
cd "$(dirname "$0")/.."
missing=0
found=0
for t in $(grep -oE 'Test[A-Za-z0-9_]+' features/*.feature | cut -d: -f2 | sort -u); do
	if grep -qr "func $t(" internal/ cmd/ tools/ 2>/dev/null; then
		found=$((found + 1))
	else
		echo "scenario names a test that does not exist: $t"
		missing=$((missing + 1))
	fi
done
[ "$found" -gt 0 ] || { echo "no scenarios found at all: the check measured nothing"; exit 1; }
[ "$missing" -eq 0 ] || exit 1
echo "$found scenario bindings, all present"
