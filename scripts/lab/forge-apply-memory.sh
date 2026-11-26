#!/bin/sh
# The peak memory of netryx's apply at five GOMEMLIMIT values, on a throwaway
# clone of the lab template (docs/apply-memory-measured.md):
#
#   scripts/forge.sh sh scripts/lab/forge-apply-memory.sh
#
# Runs on forge from the tree scripts/forge.sh copied: a clone of nxlab-golden,
# scripts/lab/apply-memory.sh on it (about 25 minutes), the samples, the gctrace
# lines and the log back to build/mem/out, then the clone stopped, which
# deletes it. Needs ~/.ssh/netryx_vm, ~/.config/netryx-lab/nxlab-root (the
# template's root password, 0600) and ~/netryx-geo/data (the address table's
# source, for the country codes).
set -u
ROOT=$(pwd); W=$ROOT/build/mem; mkdir -p "$W/out"; cd "$W" || exit 1
O="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o BatchMode=yes -o ConnectTimeout=5 -i $HOME/.ssh/netryx_vm"
NAME=${NAME:-nxmem}
cp "$ROOT/scripts/lab/apply-memory.sh" run.sh
cp "$ROOT/scripts/lab/flint2-hosts.txt" hosts.txt
rm -f cc.*; zcat "$HOME/netryx-geo/data/ip2asn-combined.tsv.gz" | cut -f4 | sort -u | grep -x '[A-Z][A-Z]' | split -l 64 - cc.
~/.local/bin/forge-lab clone nxlab "$NAME" >/dev/null 2>&1 || { echo "clone failed"; exit 1; }
for i in $(seq 1 40); do IP=$(incus list "$NAME" -c 4 -f csv | grep -o '10\.[0-9.]*' | head -1); [ -n "$IP" ] && break; sleep 3; done
for i in $(seq 1 20); do ssh $O root@$IP true </dev/null && break; sleep 3; done
echo "clone $NAME at $IP"
ssh $O root@$IP 'mkdir -p /tmp/mem' </dev/null
for f in run.sh hosts.txt cc.*; do ssh $O root@$IP "cat > /tmp/mem/$f" < $f; done
ssh $O root@$IP 'umask 077; cat > /tmp/mem/pass' < "$HOME/.config/netryx-lab/nxlab-root"
ssh $O root@$IP 'sh /tmp/mem/run.sh' </dev/null
for f in run.log none.samples m160.samples m128.samples m96.samples m64.samples none.gc m160.gc m128.gc m96.gc m64.gc; do
	ssh $O root@$IP "cat /tmp/mem/$f" </dev/null > out/$f 2>/dev/null
done
~/.local/bin/forge-lab stop "$NAME" </dev/null >/dev/null 2>&1
echo "stopped; $NAME left: $(incus list -c n -f csv | grep -cx "$NAME")"
