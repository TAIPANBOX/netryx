#!/bin/sh
# Build the console page into internal/web/dist, where the Go binary embeds it.
#
#   scripts/forge.sh sh web/build.sh
#
# Runs on forge, never on the laptop. Also bundles src/*.test.ts for node, so
# `node --test web/dist-test/` runs them without a test framework.
set -eu
cd "$(dirname "$0")"
out=../internal/web/dist

npm ci --no-audit --no-fund --loglevel=error
npx tsc --noEmit -p .

# Clear the last build but keep .keep, which holds the directory in git.
find "$out" -mindepth 1 ! -name .keep -exec rm -rf {} +

# The app icons: tools/appmap writes each app's icon and brand colour
# (data/apps-meta.json), and tools/iconpack turns that into dist/icons/<id>.svg
# and dist/icons/manifest.json, which the page loads from the console itself.
# Both need the data scripts/fetch-data.sh downloads; without it the page is
# built with no icon pack and every app is a letter tile. Data that is there
# but broken (a slug Simple Icons does not have, path data with markup, a
# colour that disagrees) fails the build on purpose, so a bad icon never ships.
data=../data
brands=$data/simple-icons.json
[ -f "$brands" ] || brands=$data/simple-icons/data/simple-icons.json
if [ -d "$data/v2fly/data" ] && [ -d "$data/simple-icons/icons" ] && [ -f "$brands" ]; then
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	(cd .. && go run ./tools/appmap -v2fly data/v2fly/data -catalog tools/appmap/catalog.tsv \
		-brands "web/$brands" -o "$tmp/appmap.tsv" -meta data/apps-meta.json) >/dev/null
	(cd .. && go run ./tools/iconpack -si data/simple-icons/icons -meta data/apps-meta.json -out internal/web/dist/icons)
	echo "icons: $(find "$out/icons" -name '*.svg' | wc -l | tr -d ' ') svg, manifest $(wc -c <"$out/icons/manifest.json" | tr -d ' ') bytes"
else
	echo "icons: no data/v2fly or data/simple-icons (run scripts/fetch-data.sh data); every app is a letter tile"
fi
npx esbuild src/main.tsx --bundle --minify --format=esm --target=es2020 \
	--jsx-factory=h --jsx-fragment=Fragment --legal-comments=none \
	--log-level=warning --outfile="$out/app.js"
cp src/index.html src/style.css "$out/"

rm -rf dist-test
for t in src/*.test.ts; do
	npx esbuild "$t" --bundle --platform=node --format=esm --sourcemap=inline --log-level=warning \
		--outfile="dist-test/$(basename "$t" .ts).mjs"
done

# The page is served from a router over Wi-Fi: the budget is on what crosses
# the air, which is the gzipped bundle.
raw=$(wc -c <"$out/app.js" | tr -d ' ')
gz=$(gzip -9c "$out/app.js" | wc -c | tr -d ' ')
budget=61440
echo "app.js: $raw bytes, gzip $gz bytes (budget $budget, slack $((budget - gz)))"
[ "$gz" -lt "$budget" ] || { echo "app.js is over the 60 KB gzip budget" >&2; exit 1; }
