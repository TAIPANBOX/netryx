#!/bin/sh
# netryx on a small router's memory, on a throwaway clone of the lab template
# with limits.memory set (docs/small-router-memory-measured.md):
#
#   scripts/forge.sh sh -c 'MEM=512MiB sh scripts/lab/forge-small-router-memory.sh'
#   scripts/forge.sh sh -c 'MEM=512MiB MODE=answers sh scripts/lab/forge-small-router-memory.sh'
#
# Runs on forge from the tree scripts/forge.sh copied. The clone is made as
# forge-lab clone makes one (ephemeral, Secure Boot off for OpenWrt, 2 vCPU),
# with the memory limit added, since forge-lab has no option for it; then
# scripts/lab/small-router-memory.sh on it (about 35 minutes), the results back
# to build/small/out, and the clone stopped, which deletes it. Needs
# ~/.ssh/netryx_vm, ~/.config/netryx-lab/nxlab-root (0600) and ~/netryx-geo/data.
set -u
ROOT=$(pwd); W=$ROOT/build/small; mkdir -p "$W/out"; cd "$W" || exit 1
O="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o BatchMode=yes -o ConnectTimeout=5 -i $HOME/.ssh/netryx_vm"
NAME=${NAME:-nxsmall}; MEM=${MEM:-512MiB}
cp "$ROOT/scripts/lab/small-router-memory.sh" run.sh
cp "$ROOT/scripts/lab/flint2-hosts.txt" hosts.txt
rm -f cc.*; zcat "$HOME/netryx-geo/data/ip2asn-combined.tsv.gz" | cut -f4 | sort -u | grep -x '[A-Z][A-Z]' | split -l 64 - cc.
incus init nxlab-golden "$NAME" --ephemeral --vm -c limits.memory="$MEM" -c limits.cpu=2 -c security.secureboot=false </dev/null >/dev/null || { echo "init failed"; exit 1; }
incus start "$NAME" </dev/null || { echo "start failed"; incus delete -f "$NAME" </dev/null; exit 1; }
for i in $(seq 1 40); do IP=$(incus list "$NAME" -c 4 -f csv </dev/null | grep -o '10\.[0-9.]*' | head -1); [ -n "$IP" ] && break; sleep 3; done
for i in $(seq 1 20); do ssh $O root@$IP true </dev/null && break; sleep 3; done
echo "clone $NAME at $IP, limits.memory=$(incus config get "$NAME" limits.memory </dev/null)"
ssh $O root@$IP 'mkdir -p /tmp/mem' </dev/null
for f in run.sh hosts.txt cc.*; do ssh $O root@$IP "cat > /tmp/mem/$f" < $f; done
ssh $O root@$IP 'umask 077; cat > /tmp/mem/pass' < "$HOME/.config/netryx-lab/nxlab-root"
echo "$IP" > ip
ssh $O root@$IP "sh /tmp/mem/run.sh ${MODE:-}" </dev/null
for p in m160 m128 none; do for x in samples gc oom nft.typical nft.all; do
	ssh $O root@$IP "cat /tmp/mem/$p.$x" </dev/null > out/$p.$x 2>/dev/null
done; done
ssh $O root@$IP 'cat /tmp/mem/run.log' </dev/null > out/run.log
for x in answer.1 answer.2 answer.3 answer.4 ctl.rules; do ssh $O root@$IP "cat /tmp/mem/$x" </dev/null > out/$x 2>/dev/null; done
~/.local/bin/forge-lab stop "$NAME" </dev/null >/dev/null 2>&1
echo "stopped; $NAME left: $(incus list -c n -f csv </dev/null | grep -cx "$NAME")"
