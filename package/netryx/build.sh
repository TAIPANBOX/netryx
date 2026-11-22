#!/bin/sh
# Build netryx for a router and lay out the package tree.
#
#   ./package/netryx/build.sh aarch64_cortex-a53 [version]
#
# The binary is static and CGO-free on purpose: OpenWrt is musl, the build host
# is not, and a package that needs a matching libc on the router is a package
# that fails on half the targets it claims to support.
set -eu

usage() { echo "usage: build.sh <openwrt-arch> [version]" >&2; exit 2; }

ARCH=${1:?$(usage)}
VERSION=${2:-${NETRYX_VERSION:-0.1.0}}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OUT=${OUT:-$ROOT/build}
TREE=$OUT/$ARCH/tree

case "$ARCH" in
	aarch64*) GOARCH=arm64; GOARM= ;;
	x86_64)   GOARCH=amd64; GOARM= ;;
	mips_*)   GOARCH=mips;  GOARM=; export GOMIPS=softfloat ;;
	arm_cortex-a7*|arm_cortex-a9*) GOARCH=arm; GOARM=7 ;;
	*) echo "unknown architecture: $ARCH" >&2; exit 2 ;;
esac

rm -rf "$TREE"
mkdir -p "$TREE/usr/sbin" "$TREE/etc/init.d" "$TREE/etc/config"

# The console page is embedded from internal/web/dist when the binary is
# built, so it is built first wherever node can install its dependencies. A
# binary without it serves only a page that says it was not built, and that
# is not shipped unless somebody asks for it by name.
if command -v npm >/dev/null 2>&1; then
	if ! sh "$ROOT/web/build.sh"; then
		[ "${NETRYX_ALLOW_NO_UI:-0}" = 1 ] || { echo "the console page failed to build" >&2; exit 1; }
		echo "warning: the console page failed to build; going on because NETRYX_ALLOW_NO_UI=1" >&2
	fi
fi
if [ ! -f "$ROOT/internal/web/dist/index.html" ]; then
	if [ "${NETRYX_ALLOW_NO_UI:-0}" = 1 ]; then
		echo "warning: packaging without the console page (NETRYX_ALLOW_NO_UI=1): / will say it was not built" >&2
	else
		echo "refusing to package: the console page is not built (no internal/web/dist/index.html)." >&2
		echo "run web/build.sh where node is installed, or set NETRYX_ALLOW_NO_UI=1 to ship the API alone" >&2
		exit 1
	fi
fi

echo "building netryx $VERSION for $ARCH (GOARCH=$GOARCH${GOARM:+ GOARM=$GOARM})"
CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH ${GOARM:+GOARM=$GOARM} \
	go build -C "$ROOT" -trimpath -ldflags="-s -w -X main.version=$VERSION" \
	-o "$TREE/usr/sbin/netryx" ./cmd/netryx

install -m 0755 "$ROOT/package/netryx/files/netryx.init" "$TREE/etc/init.d/netryx"
# A sysupgrade keeps only what /lib/upgrade/keep.d lists: without this the
# history and labels in /etc/netryx are wiped with the firmware.
mkdir -p "$TREE/lib/upgrade/keep.d"
printf '/etc/netryx/\n' >"$TREE/lib/upgrade/keep.d/netryx"
[ -f "$ROOT/package/netryx/files/netryx.config" ] &&
	install -m 0644 "$ROOT/package/netryx/files/netryx.config" "$TREE/etc/config/netryx"
# The rpcd ACL the console's login checks: a LuCI user with its read grant
# may view, with its write grant may change. Without it every change is
# refused. Its read grant also carries ubus session access and destroy:
# measured in the lab VM, without them uhttpd answers -32002 to a session
# asking its own grants (a login whose only group is netryx) and to every
# session ending itself (root included), so logout would leave the rpcd
# session open. uhttpd fills in the caller's own session id, so a session
# can ask about and end only itself.
mkdir -p "$TREE/usr/share/rpcd/acl.d"
install -m 0644 "$ROOT/package/netryx/files/netryx.acl.json" "$TREE/usr/share/rpcd/acl.d/netryx.json"
# The ubus object netryx, the agent's read through openwrt-mcp's ubus_call
# (docs/agent.md): an rpcd exec plugin that runs `netryx rpcd`.
mkdir -p "$TREE/usr/libexec/rpcd"
install -m 0755 "$ROOT/package/netryx/files/netryx.rpcd" "$TREE/usr/libexec/rpcd/netryx"
# LuCI's "Network console" entry (Network menu): the page, its menu entry, the
# ACL that lets a LuCI user call the handoff, and the ubus object luci.netryx
# that mints the one-time code. Inert on a router without LuCI.
install -m 0755 "$ROOT/package/netryx/files/luci/luci.netryx.rpcd" "$TREE/usr/libexec/rpcd/luci.netryx"
install -m 0644 "$ROOT/package/netryx/files/luci/acl.json" "$TREE/usr/share/rpcd/acl.d/luci-app-netryx.json"
mkdir -p "$TREE/usr/share/luci/menu.d" "$TREE/www/luci-static/resources/view/netryx"
install -m 0644 "$ROOT/package/netryx/files/luci/menu.json" "$TREE/usr/share/luci/menu.d/luci-app-netryx.json"
install -m 0644 "$ROOT/package/netryx/files/luci/console.js" "$TREE/www/luci-static/resources/view/netryx/console.js"

# A filtered device's DNS is redirected to the safe resolver (port 553) before
# the input chain, so a zone whose input is REJECT would refuse it: fw4 puts
# this line at the head of its input chain and accepts exactly what was
# redirected there (docs/stage2b-measured.md, section 7).
mkdir -p "$TREE/usr/share/nftables.d/chain-pre/input"
install -m 0644 "$ROOT/package/netryx/files/netryx-safe.nft" "$TREE/usr/share/nftables.d/chain-pre/input/netryx.nft"

# The data files the console names flows with, when scripts/fetch-data.sh
# and tools/appmap have produced them. The service runs without them (no
# countries, no apps, no makers), so a build for development may leave them
# out, but a router package carries all three the config names
# (scripts/package-sane.sh refuses one without them). Their licences go
# beside them.
DATA=${NETRYX_DATA:-$ROOT/data}
SHARE=$TREE/usr/share/netryx
shipped=""
for f in ip2asn-combined.tsv.gz appmap.tsv apps-meta.json oui.txt; do
	if [ -f "$DATA/$f" ]; then
		mkdir -p "$SHARE"
		install -m 0644 "$DATA/$f" "$SHARE/$f"
		shipped="$shipped $f"
	fi
done
# The icon pack is embedded in the binary rather than shipped as a file, but
# its licence still travels with the package.
icons=""
[ -f "$ROOT/internal/web/dist/icons/manifest.json" ] && icons=1
if [ -n "$shipped" ] || [ -n "$icons" ]; then
	mkdir -p "$SHARE"
	{
		echo "Data files shipped with netryx, and their licences."
		case "$shipped" in *ip2asn-combined.tsv.gz*)
			echo
			echo "ip2asn-combined.tsv.gz"
			echo "  From iptoasn.com (https://iptoasn.com/). Public domain, under the Open"
			echo "  Data Commons Public Domain Dedication and License (PDDL) v1.0:"
			echo "  https://opendatacommons.org/licenses/pddl/1-0/"
			;;
		esac
		case "$shipped" in *oui.txt*)
			echo
			echo "oui.txt"
			echo "  The IEEE Registration Authority's public listing of MA-L assignments"
			echo "  (https://standards-oui.ieee.org/oui/oui.txt), as IEEE publishes it."
			;;
		esac
		case "$shipped" in *apps-meta.json*)
			echo
			echo "apps-meta.json"
			echo "  Generated by tools/appmap from its catalog: each app's icon source and"
			echo "  brand colour, the colours taken from Simple Icons (see below)."
			;;
		esac
		if [ -n "$icons" ] || case "$shipped" in *apps-meta.json*) true ;; *) false ;; esac; then
			echo
			echo "Brand icons and colours (Simple Icons)"
			echo "  The console's brand icons, embedded in /usr/sbin/netryx, are regenerated"
			echo "  by tools/iconpack from the path data of Simple Icons"
			echo "  (https://github.com/simple-icons/simple-icons), which is released under"
			echo "  CC0 1.0 Universal: https://creativecommons.org/publicdomain/zero/1.0/"
			echo "  The brands are trademarks of their owners; showing an icon does not mean"
			echo "  a brand endorses netryx. Icons of brands without a free logo are not"
			echo "  shipped: the router fetches each one once from the brand's own site when"
			echo "  fetch_icons is on."
		fi
		case "$shipped" in *appmap.tsv*)
			echo
			echo "appmap.tsv"
			echo "  Generated by tools/appmap from v2fly/domain-list-community"
			echo "  (https://github.com/v2fly/domain-list-community), under its MIT licence:"
			echo
			;;
		esac
	} >"$SHARE/NOTICE"
	case "$shipped" in *appmap.tsv*)
		# The MIT licence asks for its own text to travel with the work.
		[ -f "$DATA/v2fly/LICENSE" ] || {
			echo "appmap.tsv is shipped but $DATA/v2fly/LICENSE is missing: its licence must travel with it" >&2
			exit 1
		}
		sed 's/^/  /' "$DATA/v2fly/LICENSE" >>"$SHARE/NOTICE"
		;;
	esac
	chmod 0644 "$SHARE/NOTICE"
	echo "data files:${shipped:- none}${icons:+, icon pack} (with NOTICE)"
else
	echo "no data files in $DATA: the console will show no countries and no apps"
fi

# Every mode is set here, never left to the builder's umask (forge's 002
# made the binary and every directory group-writable): directories and
# executables 0755, everything else 0644.
find "$TREE" -type d -exec chmod 0755 {} +
find "$TREE" -type f -exec chmod 0644 {} +
chmod 0755 "$TREE/usr/sbin/netryx" "$TREE/etc/init.d/netryx" "$TREE/usr/libexec/rpcd/netryx" "$TREE/usr/libexec/rpcd/luci.netryx"

sh "$ROOT/package/netryx/mkipk.sh" "$TREE" "$ARCH" "$VERSION"

bin=$TREE/usr/sbin/netryx
sh "$ROOT/scripts/elf-arch.sh" "$bin" "$GOARCH"
size=$(wc -c < "$bin")
echo "tree at $TREE, $size bytes"
