#!/bin/sh
# Every gate in this repo is planted with its own fault and must go red, must
# stay green on something that is not a fault, and must refuse to report
# success when its subject is missing.
#
# A gate that cannot fail is worse than no gate: it is a green light nobody
# checks. This script is the check on the checks.
set -eu
cd "$(dirname "$0")/.."
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fails=0

red() { # red <name> <command...>: the command MUST fail
	name=$1; shift
	if "$@" >"$work/out" 2>&1; then
		echo "TOOTHLESS: $name stayed green on a planted fault"; fails=$((fails + 1))
	else
		echo "ok (red on fault): $name"
	fi
}

green() { # green <name> <command...>: the command MUST pass
	name=$1; shift
	if "$@" >"$work/out" 2>&1; then
		echo "ok (green on good):  $name"
	else
		echo "FALSE ALARM: $name went red on a good tree:"; sed 's/^/    /' "$work/out"; fails=$((fails + 1))
	fi
}

# ---- gate: scenarios name tests that exist -----------------------------------
green "scenarios-bound, real tree" sh scripts/scenarios-bound.sh

# A scenario naming a test nobody wrote.
cp -R features "$work/features.bak"
printf '\n  Scenario: a promise with nothing behind it\n    # -> TestNobodyEverWroteThis\n' >>features/ingress.feature
red "scenarios-bound, scenario without a test" sh scripts/scenarios-bound.sh
rm -rf features && mv "$work/features.bak" features

# And the failure this gate was written after: no subjects at all must be an
# error, not a pass. A check that measured nothing must never say OK.
mkdir -p "$work/empty/features" "$work/empty/scripts" "$work/empty/internal"
cp scripts/scenarios-bound.sh "$work/empty/scripts/"
: >"$work/empty/features/none.feature"
red "scenarios-bound, nothing to measure" sh "$work/empty/scripts/scenarios-bound.sh"

# ---- gate: the packaged binary is really that architecture -------------------
# Built here rather than mocked: the gate reads ELF bytes, so a fake file would
# test the fake.
GOOS=linux GOARCH=arm64 go build -o "$work/netryx.arm64" ./cmd/netryx
GOOS=linux GOARCH=amd64 go build -o "$work/netryx.amd64" ./cmd/netryx
green "elf-arch, right arch"        sh scripts/elf-arch.sh "$work/netryx.arm64" arm64
red   "elf-arch, wrong arch"        sh scripts/elf-arch.sh "$work/netryx.amd64" arm64
printf 'not an elf at all' >"$work/notelf"
red   "elf-arch, not a binary"      sh scripts/elf-arch.sh "$work/notelf" arm64
red   "elf-arch, subject missing"   sh scripts/elf-arch.sh "$work/gone" arm64

# ---- gate: the package is a package -----------------------------------------
# Built here from the real tree rather than mocked: the check reads the tree
# and the file together, and a fake of either would test the fake.
if [ -d build/aarch64_cortex-a53/tree ] && ls build/aarch64_cortex-a53/*.apk >/dev/null 2>&1; then
	real_tree=build/aarch64_cortex-a53/tree
	real_pkg=$(ls -S build/aarch64_cortex-a53/*.apk | head -1)
	green "package-sane, real package"   sh scripts/package-sane.sh "$real_tree" "$real_pkg"
	printf 'not a package' >"$work/tiny.apk"
	red   "package-sane, empty package"  sh scripts/package-sane.sh "$real_tree" "$work/tiny.apk"
	cp -R "$real_tree" "$work/tree"
	rm -f "$work/tree/etc/init.d/netryx"
	red   "package-sane, tree missing the service" sh scripts/package-sane.sh "$work/tree" "$real_pkg"
	cp -R "$real_tree" "$work/tree2"
	rm -f "$work/tree2/lib/upgrade/keep.d/netryx"
	red   "package-sane, history not kept across sysupgrade" sh scripts/package-sane.sh "$work/tree2" "$real_pkg"
	cp -R "$real_tree" "$work/tree3"
	rm -f "$work/tree3/usr/share/rpcd/acl.d/netryx.json"
	red   "package-sane, no rpcd ACL for the login" sh scripts/package-sane.sh "$work/tree3" "$real_pkg"
	cp -R "$real_tree" "$work/tree4"
	rm -f "$work/tree4/usr/libexec/rpcd/netryx"
	red   "package-sane, no rpcd plugin for the ubus object" sh scripts/package-sane.sh "$work/tree4" "$real_pkg"
	cp -R "$real_tree" "$work/tree4b"
	printf '#!/bin/sh\nexec /usr/sbin/netryx serve "$@"\n' >"$work/tree4b/usr/libexec/rpcd/netryx"
	red   "package-sane, rpcd plugin runs something else" sh scripts/package-sane.sh "$work/tree4b" "$real_pkg"
	cp -R "$real_tree" "$work/tree4d"
	{ printf '#!/bin/sh\nrm -f /etc/config/netryx\n'; tail -n +2 package/netryx/files/netryx.rpcd; } >"$work/tree4d/usr/libexec/rpcd/netryx"
	red   "package-sane, rpcd plugin does more than run netryx rpcd" sh scripts/package-sane.sh "$work/tree4d" "$real_pkg"
	cp -R "$real_tree" "$work/tree4c"
	chmod 0644 "$work/tree4c/usr/libexec/rpcd/netryx"
	red   "package-sane, rpcd plugin not executable" sh scripts/package-sane.sh "$work/tree4c" "$real_pkg"
	cp -R "$real_tree" "$work/tree5"
	printf 'not the registry\n' >"$work/tree5/usr/share/netryx/oui.txt"
	red   "package-sane, vendor list not the registry" sh scripts/package-sane.sh "$work/tree5" "$real_pkg"
	rm -f "$work/tree5/usr/share/netryx/oui.txt"
	red   "package-sane, no vendor list" sh scripts/package-sane.sh "$work/tree5" "$real_pkg"
	cp -R "$real_tree" "$work/tree6"
	rm -f "$work/tree6/usr/share/netryx/appmap.tsv"
	red   "package-sane, no app map" sh scripts/package-sane.sh "$work/tree6" "$real_pkg"
	cp -R "$real_tree" "$work/tree7"
	rm -f "$work/tree7/usr/share/netryx/ip2asn-combined.tsv.gz"
	red   "package-sane, no address table" sh scripts/package-sane.sh "$work/tree7" "$real_pkg"
	printf 'not gzip' >"$work/tree7/usr/share/netryx/ip2asn-combined.tsv.gz"
	red   "package-sane, address table not gzip" sh scripts/package-sane.sh "$work/tree7" "$real_pkg"
	cp -R "$real_tree" "$work/tree8"
	sed -i.bak '/Permission is hereby granted/d' "$work/tree8/usr/share/netryx/NOTICE"
	rm -f "$work/tree8/usr/share/netryx/NOTICE.bak"
	red   "package-sane, app map without its licence" sh scripts/package-sane.sh "$work/tree8" "$real_pkg"
	cp -R "$real_tree" "$work/tree9"
	chmod 0775 "$work/tree9/usr/sbin/netryx"
	red   "package-sane, group-writable binary" sh scripts/package-sane.sh "$work/tree9" "$real_pkg"
	chmod 0755 "$work/tree9/usr/sbin/netryx"
	chmod 0775 "$work/tree9/usr/share/netryx"
	red   "package-sane, group-writable directory" sh scripts/package-sane.sh "$work/tree9" "$real_pkg"
	chmod 0755 "$work/tree9/usr/share/netryx"
	chmod 0700 "$work/tree9/usr/sbin/netryx"
	red   "package-sane, binary not 0755" sh scripts/package-sane.sh "$work/tree9" "$real_pkg"
	cp -R package/netryx "$work/pkgs"
	sed '/nft delete table inet netryx_rules/d' package/netryx/mkapk.sh >"$work/pkgs/mkapk.sh"
	red   "package-sane, apk removal leaves the rules table" env NETRYX_PKG_SCRIPTS="$work/pkgs" sh scripts/package-sane.sh "$real_tree" "$real_pkg"
	cp package/netryx/mkapk.sh "$work/pkgs/mkapk.sh"
	sed '/nft delete table inet netryx_rules/d' package/netryx/mkipk.sh >"$work/pkgs/mkipk.sh"
	red   "package-sane, opkg removal leaves the rules table" env NETRYX_PKG_SCRIPTS="$work/pkgs" sh scripts/package-sane.sh "$real_tree" "$real_pkg"
	sed 's|^/etc/init.d/netryx enable$|/etc/init.d/netryx enable; nft delete table inet netryx_rules|' package/netryx/mkapk.sh >"$work/pkgs/mkapk.sh"
	cp package/netryx/mkipk.sh "$work/pkgs/mkipk.sh"
	red   "package-sane, an upgrade deletes the rules table" env NETRYX_PKG_SCRIPTS="$work/pkgs" sh scripts/package-sane.sh "$real_tree" "$real_pkg"
	sed 's|^uci -q revert dhcp.netryx_safe$|uci -q revert dhcp.netryx_safe; uci -q commit dhcp|' package/netryx/mkapk.sh >"$work/pkgs/mkapk.sh"
	red   "package-sane, removal commits everybody's dhcp changes" env NETRYX_PKG_SCRIPTS="$work/pkgs" sh scripts/package-sane.sh "$real_tree" "$real_pkg"
	sed 's|^	awk "/^config.*|	cp /etc/config/dhcp "$t" \&\&|' package/netryx/mkipk.sh >"$work/pkgs/mkipk.sh"
	red   "package-sane, removal leaves a committed safe section" env NETRYX_PKG_SCRIPTS="$work/pkgs" sh scripts/package-sane.sh "$real_tree" "$real_pkg"
	cp package/netryx/mkipk.sh "$work/pkgs/mkipk.sh"
	sed 's|^if grep -q "netryx_safe" /etc/config/dhcp 2>/dev/null \&\& t=.*|t=/tmp/netryx.dhcp; if grep -q netryx_safe /etc/config/dhcp; then|' package/netryx/mkapk.sh >"$work/pkgs/mkapk.sh"
	red   "package-sane, removal writes a fixed path under /tmp" env NETRYX_PKG_SCRIPTS="$work/pkgs" sh scripts/package-sane.sh "$real_tree" "$real_pkg"
	cp -R "$real_tree" "$work/tree4"
	rm -f "$work/tree4/usr/share/nftables.d/chain-pre/input/netryx.nft"
	red   "package-sane, no fw4 include for the safe resolver" sh scripts/package-sane.sh "$work/tree4" "$real_pkg"
	sed 's/ kmod-nft-bridge//' package/netryx/mkapk.sh >"$work/pkgs/mkapk.sh"
	red   "package-sane, no kmod-nft-bridge for isolation" env NETRYX_PKG_SCRIPTS="$work/pkgs" sh scripts/package-sane.sh "$real_tree" "$real_pkg"
	sed '/nft delete table bridge netryx_iso/d' package/netryx/mkapk.sh >"$work/pkgs/mkapk.sh"
	red   "package-sane, removal leaves the isolation table" env NETRYX_PKG_SCRIPTS="$work/pkgs" sh scripts/package-sane.sh "$real_tree" "$real_pkg"
	sed 's|^/etc/init.d/netryx enable$|/etc/init.d/netryx enable; nft delete table bridge netryx_iso|' package/netryx/mkapk.sh >"$work/pkgs/mkapk.sh"
	red   "package-sane, an upgrade deletes the isolation table" env NETRYX_PKG_SCRIPTS="$work/pkgs" sh scripts/package-sane.sh "$real_tree" "$real_pkg"
	cp package/netryx/mkapk.sh "$work/pkgs/mkapk.sh"
	green "package-sane, scripts copy restored" env NETRYX_PKG_SCRIPTS="$work/pkgs" sh scripts/package-sane.sh "$real_tree" "$real_pkg"
	red   "package-sane, subject missing" sh scripts/package-sane.sh "$work/nothing" "$real_pkg"
else
	echo "SKIPPED: package-sane has nothing built to check (run package/netryx/build.sh first)"
	fails=$((fails + 1))
fi

# ---- gate: the console fetches nothing from outside the router ---------------
# Checked against the real build (web/build.sh), so the green case proves the
# namespace names Preact carries are not a false alarm; faults go into copies.
if [ -s internal/web/dist/app.js ]; then
	green "ui-offline, real build" sh scripts/ui-offline.sh internal/web/dist web/src
	cp -R internal/web/dist "$work/dist"
	cp -R web/src "$work/src"
	printf '\nfetch("https://x");\n' >>"$work/dist/app.js"
	red   "ui-offline, an outside address in the bundle" sh scripts/ui-offline.sh "$work/dist" "$work/src"
	cp internal/web/dist/app.js "$work/dist/app.js"
	printf '<link rel="stylesheet" href="http://cdn.example/x.css">\n' >>"$work/dist/index.html"
	red   "ui-offline, an outside address in the page" sh scripts/ui-offline.sh "$work/dist" "$work/src"
	cp internal/web/dist/index.html "$work/dist/index.html"
	printf '<script src="//cdn.example/x.js"></script>\n' >>"$work/dist/index.html"
	red   "ui-offline, a scheme-less address in the page" sh scripts/ui-offline.sh "$work/dist" "$work/src"
	cp internal/web/dist/index.html "$work/dist/index.html"
	printf '\nnew WebSocket("wss://x.example/s");\n' >>"$work/dist/app.js"
	red   "ui-offline, a wss:// address in the bundle" sh scripts/ui-offline.sh "$work/dist" "$work/src"
	cp internal/web/dist/app.js "$work/dist/app.js"
	printf '\nnew WebSocket("ws://x.example/s");\n' >>"$work/dist/app.js"
	red   "ui-offline, a ws:// address in the bundle" sh scripts/ui-offline.sh "$work/dist" "$work/src"
	cp internal/web/dist/app.js "$work/dist/app.js"
	printf '\nexport function bad(e: HTMLElement, s: string) { e.innerHTML = s; }\n' >>"$work/src/main.tsx"
	red   "ui-offline, innerHTML in the sources" sh scripts/ui-offline.sh "$work/dist" "$work/src"
	cp web/src/main.tsx "$work/src/main.tsx"
	green "ui-offline, copies restored" sh scripts/ui-offline.sh "$work/dist" "$work/src"
	rm "$work/dist/app.js"
	red   "ui-offline, bundle missing" sh scripts/ui-offline.sh "$work/dist" "$work/src"
	red   "ui-offline, sources missing" sh scripts/ui-offline.sh internal/web/dist "$work/nosrc"
else
	echo "SKIPPED: ui-offline has no build to check (run web/build.sh first)"
	fails=$((fails + 1))
fi

# ---- gate: everything a person reads is English ------------------------------
# Faults go into copies of the real sources; a test file's Cyrillic is data and
# must not trip it.
green "english-only, real tree" sh scripts/english-only.sh
mkdir -p "$work/eng"
cp -R web/src "$work/eng/src"
cp -R internal/alerts "$work/eng/alerts"
green "english-only, copies"     sh scripts/english-only.sh "$work/eng/src" "$work/eng/alerts"
printf '\nexport const NOTE = "\320\235\320\276\320\262\320\270\320\271";\n' >>"$work/eng/src/ui.tsx"
red   "english-only, Cyrillic on the page" sh scripts/english-only.sh "$work/eng/src" "$work/eng/alerts"
cp web/src/ui.tsx "$work/eng/src/ui.tsx"
printf '\nconst alertText = "\320\242\321\200\320\270\320\262\320\276\320\263\320\260"\n' >>"$work/eng/alerts/telegram.go"
red   "english-only, Cyrillic in a Telegram alert" sh scripts/english-only.sh "$work/eng/src" "$work/eng/alerts"
cp internal/alerts/telegram.go "$work/eng/alerts/telegram.go"
printf '\nconst device = "\320\232\320\260\320\274\320\265\321\200\320\260"\n' >>"$work/eng/alerts/telegram_test.go"
green "english-only, a Cyrillic device name in a test" sh scripts/english-only.sh "$work/eng/src" "$work/eng/alerts"
mkdir -p "$work/eng/house"
cp test/house/house.sh "$work/eng/house/house.sh"
green "english-only, the house test script" sh scripts/english-only.sh "$work/eng/house"
printf '\nlog "\320\277\320\265\321\200\320\265\320\262\321\226\321\200\320\272\320\260"\n' >>"$work/eng/house/house.sh"
red   "english-only, Cyrillic in a test script's output" sh scripts/english-only.sh "$work/eng/house"
red   "english-only, subject missing" sh scripts/english-only.sh "$work/eng/none"

[ "$fails" -eq 0 ] || { echo "$fails gate(s) without teeth"; exit 1; }
echo "every gate went red on its own fault and stayed green on a good tree"
