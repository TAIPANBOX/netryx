#!/bin/sh
# Download the data files the console names flows with.
#
#   sh scripts/fetch-data.sh data
#
# ip2asn-combined.tsv.gz  iptoasn.com, public domain (PDDL v1.0): address
#                         ranges to country, ASN and organisation.
# v2fly/                  v2fly/domain-list-community at a pinned commit, MIT:
#                         domains grouped by service, the input of tools/appmap.
# simple-icons/icons/     Simple Icons at a pinned commit, CC0: one SVG per brand,
# simple-icons.json       and each brand's hex colour. The input of tools/iconpack.
# oui.txt                 the IEEE Registration Authority's public MA-L listing:
#                         hardware address prefixes to the maker that holds them.
#
# Run on forge or in the package build, never in the router's hot path. Each
# file is checked for being what it claims before it replaces the old one.
set -eu

OUT=${1:?usage: fetch-data.sh <outdir>}
V2FLY_REF=${V2FLY_REF:-bc3a8f5e24792303cf82bfff33b1ff164a0e4b3c}
SI_REF=${SI_REF:-32e030086fdc605196391aab28519b14dc4295b4}
mkdir -p "$OUT"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL --retry 3 -o "$tmp/ip2asn.gz" https://iptoasn.com/data/ip2asn-combined.tsv.gz
lines=$(gzip -dc "$tmp/ip2asn.gz" | wc -l)
if [ "$lines" -lt 400000 ]; then
	echo "ip2asn: only $lines lines, refusing it" >&2
	exit 1
fi
mv "$tmp/ip2asn.gz" "$OUT/ip2asn-combined.tsv.gz"
echo "ip2asn-combined.tsv.gz: $lines ranges"

curl -fsSL --retry 3 -o "$tmp/v2fly.tgz" \
	"https://codeload.github.com/v2fly/domain-list-community/tar.gz/$V2FLY_REF"
mkdir "$tmp/v2fly"
tar -xzf "$tmp/v2fly.tgz" -C "$tmp/v2fly" --strip-components=1
lists=$(ls "$tmp/v2fly/data" | wc -l)
if [ "$lists" -lt 500 ]; then
	echo "v2fly: only $lists lists, refusing it" >&2
	exit 1
fi
rm -rf "$OUT/v2fly"
mv "$tmp/v2fly" "$OUT/v2fly"
echo "v2fly: $lists lists ($V2FLY_REF)"

curl -fsSL --retry 3 -o "$tmp/simple-icons.tgz" \
	"https://codeload.github.com/simple-icons/simple-icons/tar.gz/$SI_REF"
mkdir "$tmp/simple-icons"
tar -xzf "$tmp/simple-icons.tgz" -C "$tmp/simple-icons" --strip-components=1
icons=$(find "$tmp/simple-icons/icons" -name '*.svg' | wc -l)
if [ "$icons" -lt 3000 ]; then
	echo "simple-icons: only $icons icons, refusing it" >&2
	exit 1
fi
if [ ! -f "$tmp/simple-icons/data/simple-icons.json" ]; then
	echo "simple-icons: no data/simple-icons.json in the archive, refusing it" >&2
	exit 1
fi
rm -rf "$OUT/simple-icons" "$OUT/simple-icons.json"
mkdir "$OUT/simple-icons"
mv "$tmp/simple-icons/icons" "$OUT/simple-icons/icons"
mv "$tmp/simple-icons/data/simple-icons.json" "$OUT/simple-icons.json"
echo "simple-icons: $icons icons ($SI_REF)"

curl -fsSL --retry 3 -o "$tmp/oui.txt" https://standards-oui.ieee.org/oui/oui.txt
regs=$(grep -c '(hex)' "$tmp/oui.txt" || true)
if [ "$regs" -lt 30000 ]; then
	echo "oui.txt: only $regs registrations, refusing it" >&2
	exit 1
fi
mv "$tmp/oui.txt" "$OUT/oui.txt"
echo "oui.txt: $regs registrations"
