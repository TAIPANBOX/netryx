#!/bin/sh
# Build the netryx package for OpenWrt.
#
# @measured 2026-09-15 on OpenWrt 25.12.5, kernel 6.12.94 (Flint 2): the netdev
# ingress chain needs nothing beyond kmod-nft-core, which the nftables package
# pulls in. `apk list -I | grep nft` on that router shows no kmod-nft-netdev,
# and the ingress table applied and matched traffic anyway: that package
# carries the netdev dup/fwd expressions, which this rule does not use.
#
# The point of the Depends line: a person installs one package and the router
# pulls the kernel facilities the service cannot work without. Without it the
# symptom is an empty dashboard, which reads as a quiet network rather than a
# broken install, and that is the worst failure a monitor can have.
#
#   kmod-nf-conntrack-netlink  the connection table over netlink: exact tuples,
#                              per-direction counters, the NAT mapping
#   kmod-nfnetlink-log         the handshake packets, selected by the kernel
#   kmod-nft-bridge            the isolation table in the bridge family (2c)
#   kmod-nf-conntrack          the tracking itself (usually already there,
#                              named anyway so the package stands alone)
#   ca-bundle                  the roots the router checks a brand's site
#                              against when it fetches a favicon; without it
#                              every fetch fails as a bad certificate
#
# Depends uses opkg's comma-separated form; apk takes the same names with
# spaces. Getting the separator wrong yields a package that installs and then
# cannot run, which is exactly what this line exists to prevent.
set -eu

usage() { echo "usage: mkipk.sh <staging-dir> <ipk-arch> [version]" >&2; exit 2; }

TREE=${1:?$(usage)}
ARCH=${2:?$(usage)}
VERSION=${3:-${NETRYX_VERSION:-0.1.0}}
PKGREL=${PKGREL:-1}
PKG=netryx

CTRL="$TREE/CONTROL"
mkdir -p "$CTRL"
cat > "$CTRL/control" <<CONTROL
Package: $PKG
Version: $VERSION-r$PKGREL
Depends: kmod-nf-conntrack, kmod-nf-conntrack-netlink, kmod-nfnetlink-log, kmod-nft-bridge, nftables, ca-bundle
Source: https://github.com/TAIPANBOX/netryx
Section: net
Architecture: $ARCH
Maintainer: TAIPANBOX <yukosemail@gmail.com>
License: MIT
Description: netryx, a home network monitor that runs on the router: which
 device went where, what answered, and what is worth keeping. No cloud, no
 telemetry, and the packets of ordinary traffic are never stored.
CONTROL

# The modules are loaded at boot by /etc/modules.d entries the kmod packages
# install, but a fresh install has not rebooted yet, so the service loads them
# itself and says so if it cannot.
cat > "$CTRL/postinst" <<'POSTINST'
#!/bin/sh
for m in nf_conntrack nf_conntrack_netlink nfnetlink_log; do
	grep -q "^$m " /proc/modules 2>/dev/null || modprobe "$m" 2>/dev/null || true
done
[ -n "${IPKG_INSTROOT:-}" ] || /etc/init.d/netryx enable
# rpcd reads an ACL file when a session is created; a reload is what gives
# the sessions already open the grants of a new or changed one.
[ -n "${IPKG_INSTROOT:-}" ] || { [ -x /etc/init.d/rpcd ] && /etc/init.d/rpcd reload 2>/dev/null; } || true
# fw4 reads its includes when it loads: the one this package ships lets a
# filtered device's redirected DNS through a zone whose input is REJECT.
[ -n "${IPKG_INSTROOT:-}" ] || { command -v fw4 >/dev/null 2>&1 && fw4 -q reload 2>/dev/null; } || true
# An upgrade leaves the old binary running unless something restarts it.
if [ -n "$(pgrep -f "/usr/sbin/netryx serve")" ]; then
	/etc/init.d/netryx restart
fi 2>/dev/null || true
exit 0
POSTINST
# opkg runs prerm with "remove" when the package is removed; an upgrade does
# not run the old package's prerm, and the guard keeps it that way should one
# ever pass "upgrade". The rules table outlives every stop of the service on
# purpose, so removal is where it goes.
cat > "$CTRL/prerm" <<'PRERM'
#!/bin/sh
[ "${1:-remove}" = "remove" ] || exit 0
[ -n "${IPKG_INSTROOT:-}" ] && exit 0
/etc/init.d/netryx stop 2>/dev/null
/etc/init.d/netryx disable 2>/dev/null
nft delete table inet netryx 2>/dev/null
nft delete table inet netryx_rules 2>/dev/null
nft delete table bridge netryx_iso 2>/dev/null
# The safe-search resolver is netryx's own dnsmasq instance: stop it and drop
# its section, which lives in uncommitted changes (a revert of that section
# only, so nobody else's pending dhcp change is committed or lost). A copy
# someone committed into /etc/config/dhcp (adblock-fast commits dhcp when it is
# turned on) is cut from the file itself: a uci commit would also commit every
# other pending dhcp change.
/etc/init.d/dnsmasq stop netryx_safe 2>/dev/null
uci -q revert dhcp.netryx_safe
# The copy is written beside the file under a name nobody can know before
# (a dot name, which uci does not read) and moved over it in one step.
if grep -q "netryx_safe" /etc/config/dhcp 2>/dev/null && t=$(mktemp /etc/config/.netryx-dhcp.XXXXXX); then
	awk "/^config[ \t]/ { skip = (\$0 ~ /netryx_safe/) } !skip" /etc/config/dhcp >"$t" &&
		chmod 0644 "$t" && mv -f "$t" /etc/config/dhcp
	rm -f "$t"
fi
exit 0
PRERM
# After the files are gone: the rpcd plugin went with them, and a reload drops
# the ubus object netryx, which rpcd would otherwise list until it restarts.
cat > "$CTRL/postrm" <<'POSTRM'
#!/bin/sh
[ "${1:-remove}" = "remove" ] || exit 0
[ -n "${IPKG_INSTROOT:-}" ] && exit 0
[ -x /etc/init.d/rpcd ] && /etc/init.d/rpcd reload 2>/dev/null
exit 0
POSTRM
chmod 0755 "$CTRL/postinst" "$CTRL/prerm" "$CTRL/postrm"
echo "control written for $PKG $VERSION-r$PKGREL ($ARCH)"
