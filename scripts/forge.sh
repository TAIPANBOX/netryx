#!/bin/sh
# Run a command on forge against a copy of this tree.
#
#   scripts/forge.sh go test ./...
#   scripts/forge.sh sh web/build.sh
#
# Anything that compiles in a loop (the whole suite, -race, mutants, cross
# builds, the UI build) runs there, not on the laptop: its build cache once grew
# to 84 GB. The tree is copied without .git, build output, node_modules or the
# downloaded data, and the remote copies of those are kept between runs.
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
exec ssh -n "$HOST" "cd ~/$DIR && export PATH=/usr/local/go/bin:\$HOME/tools/node/bin:\$PATH && $cmd"
