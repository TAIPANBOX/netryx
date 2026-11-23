#!/bin/sh
# Is this actually a package, or a file that merely has the right name?
#
#   scripts/package-sane.sh <tree-dir> <package-file>
#
# Written after a comment placed inside a backslash-continued command ended the
# continuation, so the packaging tool ran without its arguments and produced a
# 281-byte "package" that installed nothing. Everything was green: the build
# succeeded, the file existed, and the name was right.
set -eu
tree=${1:?usage: package-sane.sh <tree-dir> <package-file>}
pkg=${2:?usage: package-sane.sh <tree-dir> <package-file>}

fail() { echo "$1" >&2; exit 1; }

[ -d "$tree" ] || fail "no package tree: $tree"
[ -f "$pkg" ] || fail "no package file: $pkg"

for f in usr/sbin/netryx etc/init.d/netryx etc/config/netryx; do
	[ -f "$tree/$f" ] || fail "the package tree is missing $f"
done
[ -x "$tree/etc/init.d/netryx" ] || fail "the init script must be executable"
# History and labels live in /etc/netryx; without this list a sysupgrade
# wipes them.
grep -qx '/etc/netryx/' "$tree/lib/upgrade/keep.d/netryx" 2>/dev/null ||
	fail "the package does not keep /etc/netryx/ across a sysupgrade (lib/upgrade/keep.d/netryx)"
# The console's login checks this ACL for every change; without it nobody may
# change anything.
grep -q '"control"' "$tree/usr/share/rpcd/acl.d/netryx.json" 2>/dev/null ||
	fail "the package does not ship the rpcd ACL the login checks (usr/share/rpcd/acl.d/netryx.json)"
# The agent's read through openwrt-mcp is the ubus object netryx: an rpcd exec
# plugin that runs the binary's read-only `rpcd` and nothing else.
cmp -s "$(dirname "$0")/../package/netryx/files/netryx.rpcd" "$tree/usr/libexec/rpcd/netryx" ||
	fail "the package does not ship the ubus object's rpcd plugin running netryx rpcd (usr/libexec/rpcd/netryx)"
# LuCI's "Network console" entry: the page, its menu entry, the ubus object
# luci.netryx that mints the one-time code (a plugin running netryx rpcd-luci
# and nothing else), and an ACL that grants a LuCI user that one call only.
cmp -s "$(dirname "$0")/../package/netryx/files/luci/luci.netryx.rpcd" "$tree/usr/libexec/rpcd/luci.netryx" ||
	fail "the package does not ship the LuCI handoff plugin running netryx rpcd-luci (usr/libexec/rpcd/luci.netryx)"
cmp -s "$(dirname "$0")/../package/netryx/files/luci/acl.json" "$tree/usr/share/rpcd/acl.d/luci-app-netryx.json" ||
	fail "the package's LuCI ACL is not the one that grants the handoff alone (usr/share/rpcd/acl.d/luci-app-netryx.json)"
grep -q '"netryx/console"' "$tree/usr/share/luci/menu.d/luci-app-netryx.json" 2>/dev/null ||
	fail "the package does not ship LuCI's Network console menu entry (usr/share/luci/menu.d/luci-app-netryx.json)"
grep -q "object: 'luci.netryx', method: 'handoff'" "$tree/www/luci-static/resources/view/netryx/console.js" 2>/dev/null ||
	fail "the package does not ship LuCI's Network console page (www/luci-static/resources/view/netryx/console.js)"
# The config names three data files; a router package carries all of them,
# each what it claims to be. Without them the console shows no country, no
# app or no maker, and nothing on the router says why.
share=$(cd "$tree" && pwd)/usr/share/netryx
# The address table and the vendor list are packs the service maps
# read-only. Each is checked here the way the service checks it before use
# (magic, version, every count and offset, the order of its records, the
# SHA-256 at its end) by tools/datapack, which is pure Go and runs on this
# host whatever the router's architecture. NETRYX_DATAPACK names a built
# copy of the tool; otherwise it is run from the source beside this script.
root=$(cd "$(dirname "$0")/.." && pwd)
verify() { # verify <kind> <file>
	if [ -n "${NETRYX_DATAPACK:-}" ]; then
		"$NETRYX_DATAPACK" -verify -kind "$1" "$2"
	else
		command -v go >/dev/null 2>&1 || fail "cannot check the data packs: no go on PATH and no NETRYX_DATAPACK"
		GOOS= GOARCH= GOARM= go run -C "$root" ./tools/datapack -verify -kind "$1" "$2"
	fi
}
for spec in "oui oui.pack vendor list" "geo ip2asn.pack address table"; do
	set -- $spec
	kind=$1 file=$2; shift 2
	[ -f "$share/$file" ] ||
		fail "the package does not carry the $* the config names (usr/share/netryx/$file)"
	verify "$kind" "$share/$file" >/dev/null ||
		fail "the $* in the package does not pass its check (usr/share/netryx/$file)"
done
[ -s "$share/appmap.tsv" ] ||
	fail "the package does not carry the app map the config names (usr/share/netryx/appmap.tsv)"
grep -q 'Permission is hereby granted, free of charge' "$share/NOTICE" 2>/dev/null ||
	fail "the package ships appmap.tsv without the v2fly MIT licence text in usr/share/netryx/NOTICE"

# Modes come from build.sh, never from the builder's umask: a group-writable
# binary on the router is one any member of its group can replace.
writable=$(find "$tree" ! -type l \( -perm -020 -o -perm -002 \) ! -path "$tree/CONTROL" ! -path "$tree/CONTROL/*" | head -n 3)
[ -z "$writable" ] || fail "group- or world-writable in the package tree: $(echo $writable)"
for f in usr/sbin/netryx etc/init.d/netryx usr/libexec/rpcd/netryx usr/libexec/rpcd/luci.netryx; do
	[ "$(ls -l "$tree/$f" | cut -c1-10)" = "-rwxr-xr-x" ] || fail "$f must be mode 0755, is $(ls -l "$tree/$f" | cut -c1-10)"
done

# Removing the package removes the rules table (spec 6.1), which every stop of
# the service leaves on purpose; an upgrade must not, or the house is open
# until the new service applies. Checked in the scripts the packages are made
# with: apk's pre-deinstall and opkg's prerm run on removal only.
scripts=${NETRYX_PKG_SCRIPTS:-$(dirname "$0")/../package/netryx}
heredoc() { awk -v start="cat > \"\$$2\" <<'$3'" -v end="$3" '$0 == start { on = 1; next } on && $0 == end { on = 0 } on' "$1"; }
for spec in "mkapk.sh WORK/pre-deinstall PRE" "mkipk.sh CTRL/prerm PRERM"; do
	set -- $spec
	[ -f "$scripts/$1" ] || fail "no packaging script $scripts/$1"
	heredoc "$scripts/$1" "$2" "$3" | grep -qx 'nft delete table inet netryx_rules 2>/dev/null' ||
		fail "$1: removal ($2) does not delete the rules table inet netryx_rules"
done
# A filtered device's DNS is redirected to the safe resolver on 553 before
# the input chain; in a zone whose input is REJECT (a guest network that
# allows only 53) it would be refused. The package carries an fw4 include that
# accepts exactly what was redirected there, and the install reloads fw4.
grep -qx 'meta l4proto { tcp, udp } th dport 553 ct status dnat accept comment "netryx: DNS sent to the safe resolver"' \
	"$tree/usr/share/nftables.d/chain-pre/input/netryx.nft" 2>/dev/null ||
	fail "the package does not carry the fw4 include that lets redirected DNS reach the safe resolver (usr/share/nftables.d/chain-pre/input/netryx.nft)"
for spec in "mkapk.sh WORK/post-install POST" "mkipk.sh CTRL/postinst POSTINST"; do
	set -- $spec
	heredoc "$scripts/$1" "$2" "$3" | grep -q 'fw4 -q reload' ||
		fail "$1: the install ($2) does not reload fw4, so the include is not in force"
done

# Removal takes the safe resolver's section away without committing anybody
# else's pending dhcp changes (adblock-fast keeps its own uncommitted): a copy
# committed into /etc/config/dhcp is cut from the file itself, never through
# `uci commit`. Run here against a copy, with the commands it calls stood in.
for spec in "mkapk.sh WORK/pre-deinstall PRE" "mkipk.sh CTRL/prerm PRERM"; do
	set -- $spec
	box=$(mktemp -d)
	mkdir -p "$box/bin" "$box/init.d" "$box/config"
	printf "config dnsmasq\n\toption domain 'lan'\n\nconfig dnsmasq 'netryx_safe'\n\toption port '553'\n\tlist address '/www.google.com/216.239.38.120'\n\nconfig dhcp 'lan'\n\toption interface 'lan'\n" >"$box/config/dhcp"
	for c in uci nft; do
		printf '#!/bin/sh\necho "%s $*" >>"%s/calls"\n' "$c" "$box" >"$box/bin/$c"
		chmod +x "$box/bin/$c"
	done
	for c in netryx dnsmasq; do
		printf '#!/bin/sh\necho "%s $*" >>"%s/calls"\n' "$c" "$box" >"$box/init.d/$c"
		chmod +x "$box/init.d/$c"
	done
	heredoc "$scripts/$1" "$2" "$3" | sed -e "s|/etc/config/|$box/config/|g" -e "s|/etc/init.d/|$box/init.d/|g" >"$box/remove.sh"
	PATH="$box/bin:$PATH" sh "$box/remove.sh" >/dev/null 2>&1
	grep -q 'commit' "$box/calls" 2>/dev/null &&
		fail "$1: removal ($2) runs uci commit, which commits everybody's pending dhcp changes"
	grep -q 'netryx_safe' "$box/config/dhcp" &&
		fail "$1: removal ($2) leaves a committed netryx_safe section in /etc/config/dhcp"
	{ grep -q "config dhcp 'lan'" "$box/config/dhcp" && grep -q "option domain 'lan'" "$box/config/dhcp"; } ||
		fail "$1: removal ($2) cut more than the netryx_safe section out of /etc/config/dhcp"
	# Run as root: a fixed name under /tmp is a file anyone could have put a
	# symlink at beforehand.
	heredoc "$scripts/$1" "$2" "$3" | grep -q '/tmp/' &&
		fail "$1: removal ($2) writes a fixed path under /tmp"
	ls -A "$box/config" | grep -qv '^dhcp$' &&
		fail "$1: removal ($2) leaves a temporary file in /etc/config"
	rm -rf "$box"
done

# Isolation needs the bridge family (kmod-nft-bridge, docs/stage2c-measured.md
# section 2): without it the whole ruleset, the boot copy included, fails to
# load. Removal takes the bridge table away with the rules table; an upgrade
# leaves it.
grep -q -- '--info "depends:[^"]*kmod-nft-bridge' "$scripts/mkapk.sh" ||
	fail "mkapk.sh: the package does not depend on kmod-nft-bridge, which isolation needs"
grep -q '^Depends:.*kmod-nft-bridge' "$scripts/mkipk.sh" ||
	fail "mkipk.sh: the package does not depend on kmod-nft-bridge, which isolation needs"
for spec in "mkapk.sh WORK/pre-deinstall PRE" "mkipk.sh CTRL/prerm PRERM"; do
	set -- $spec
	heredoc "$scripts/$1" "$2" "$3" | grep -qx 'nft delete table bridge netryx_iso 2>/dev/null' ||
		fail "$1: removal ($2) does not delete the isolation table bridge netryx_iso"
done
heredoc "$scripts/mkapk.sh" WORK/post-install POST | grep -q 'netryx_iso' &&
	fail "mkapk.sh: post-install, which also runs on upgrade, touches the isolation table"

heredoc "$scripts/mkapk.sh" WORK/post-install POST | grep -q 'netryx_rules' &&
	fail "mkapk.sh: post-install, which also runs on upgrade, touches the rules table"
heredoc "$scripts/mkipk.sh" CTRL/postinst POSTINST | grep -q 'netryx_rules' &&
	fail "mkipk.sh: postinst, which also runs on upgrade, touches the rules table"

size() { wc -c <"$1" | tr -d ' '; }
bin=$(size "$tree/usr/sbin/netryx")
got=$(size "$pkg")

# The binary compresses to roughly a third of its size, so a package smaller
# than a quarter of it cannot be carrying it.
floor=$((bin / 4))
[ "$got" -ge "$floor" ] || fail "package is $got bytes, which cannot contain a $bin-byte binary (floor $floor)"
echo "$pkg is $got bytes around a $bin-byte binary, tree complete"
