#!/bin/sh
# Does this binary match the architecture the package claims?
#
#   scripts/elf-arch.sh <binary> <goarch>
#
# A cross-built binary cannot be run on the build host, so the check is what it
# IS rather than what it does. An x86 binary inside an aarch64 package installs
# cleanly and then does nothing, and the router says only "cannot execute".
set -eu
bin=${1:?usage: elf-arch.sh <binary> <goarch>}
want=${2:?usage: elf-arch.sh <binary> <goarch>}

[ -f "$bin" ] || { echo "no such file: $bin" >&2; exit 1; }
magic=$(dd if="$bin" bs=1 count=4 2>/dev/null | od -An -tx1 | tr -d ' \n')
[ "$magic" = "7f454c46" ] || { echo "not an ELF binary ($magic): $bin" >&2; exit 1; }
machine=$(dd if="$bin" bs=1 skip=18 count=2 2>/dev/null | od -An -tx1 | tr -d ' \n')
case "$want:$machine" in
	arm64:b700|amd64:3e00|arm:2800|mips:0008) ;;
	*) echo "ELF machine $machine does not match $want: $bin" >&2; exit 1 ;;
esac
echo "$bin is ELF for $want (machine $machine)"
