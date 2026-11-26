#!/bin/sh
# Run a command on forge against a copy of this tree.
#
#   scripts/forge.sh sh scripts/lab/forge-small-router-memory.sh
#
# For the lab steps that need forge itself: an incus VM or container, the real
# data kept there, the house network. Anything that compiles in a loop (the
# whole suite, -race, mutants, cross builds, the UI build, the gates and their
# teeth) runs in GitHub Actions (.github/workflows/ci.yml), not here and not on
# the laptop. The tree is copied without .git, build output, node_modules or the
# downloaded data, and the remote copies of those are kept between runs.
#
# On forge every command runs under forge-heavy when it is there: CPU held to
# 2.5 of 4 cores, the lowest CPU and IO priority, one heavy job at a time.
set -eu

[ $# -gt 0 ] || { echo "usage: scripts/forge.sh <command...>" >&2; exit 2; }

ROOT=$(cd "$(dirname "$0")/.." && pwd)
HOST=${FORGE_HOST:-forge}
DIR=${FORGE_DIR:-netryx-work}

rsync -a --delete \
	--exclude .git --exclude build/ --exclude web/node_modules/ --exclude web/dist-test/ --exclude data/ \
	--include 'internal/web/dist/.keep' --exclude 'internal/web/dist/*' \
	"$ROOT/" "$HOST:$DIR/"

# The command is passed as one quoted string, so the remote shell sees the same
# words the caller typed.
cmd=""
for a in "$@"; do
	q=$(printf "%s" "$a" | sed "s/'/'\\\\''/g")
	cmd="$cmd '$q'"
done
heavy='h=$HOME/.local/bin/forge-heavy; [ -x "$h" ] || h=""'
exec ssh -n "$HOST" "cd ~/$DIR && export PATH=/usr/local/go/bin:\$HOME/tools/node/bin:\$PATH && $heavy && exec \$h $cmd"
