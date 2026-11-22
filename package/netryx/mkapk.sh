#!/bin/sh
# Build a real .apk for OpenWrt 25.12, the format the router actually installs.
#
#   ./package/netryx/mkapk.sh aarch64_cortex-a53 [version]
#
# apk v3 is the only writer of this format and OpenWrt's own rootfs ships
# apk-tools without mkpkg, so the writer comes from alpine:edge in a container.
# The same reasoning as hermes-openwrt, and the same container, so the estate
# has one way of doing this rather than two.
#
# The architecture string is the one the router reports in /etc/apk/arch, and
# apk will not look at a package declaring anything else: a Brume on
# aarch64_cortex-a53 cannot see a package built as aarch64_generic.
set -eu

usage() { echo "usage: mkapk.sh <apk-arch> [version]" >&2; exit 2; }

ARCH=${1:?$(usage)}
VERSION=${2:-${NETRYX_VERSION:-0.1.0}}
PKGREL=${PKGREL:-1}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
ALPINE=${ALPINE:-alpine:edge}
OUT="netryx-$VERSION-r$PKGREL.apk"

sh "$ROOT/package/netryx/build.sh" "$ARCH" "$VERSION" >/dev/null
WORK=$ROOT/build/$ARCH
find "$WORK/tree" -name .DS_Store -delete 2>/dev/null || true
# CONTROL is the opkg form; the apk metadata is passed as --info instead.
rm -rf "$WORK/tree/CONTROL"

cat > "$WORK/post-install" <<'POST'
#!/bin/sh
# The modules are loaded at boot by the entries the kmod packages install, but
# a fresh install has not rebooted yet.
for m in nf_conntrack nf_conntrack_netlink nfnetlink_log; do
	grep -q "^$m " /proc/modules 2>/dev/null || modprobe "$m" 2>/dev/null || true
done
/etc/init.d/netryx enable
# rpcd reads an ACL file when a session is created; a reload is what gives
# the sessions already open the grants of a new or changed one.
[ -x /etc/init.d/rpcd ] && /etc/init.d/rpcd reload 2>/dev/null
# fw4 reads its includes when it loads: the one this package ships lets a
# filtered device's redirected DNS through a zone whose input is REJECT.
command -v fw4 >/dev/null 2>&1 && fw4 -q reload 2>/dev/null
# On an UPGRADE the old binary is still the running process: the file on disk
# changed under it and nothing told it. Leaving that is how somebody installs a
# fix and watches the defect continue. On a FIRST install there is nothing
# running and nothing is started: the operator chooses what to observe, and a
# service that has been told nothing should not spend its first boot logging.
if [ -n "$(pgrep -f "/usr/sbin/netryx serve")" ]; then
	/etc/init.d/netryx restart
fi
exit 0
POST
# apk runs pre-deinstall on removal only (an upgrade runs post-upgrade), so
# this is where the rules table, which outlives every stop of the service on
# purpose, is taken away.
cat > "$WORK/pre-deinstall" <<'PRE'
#!/bin/sh
/etc/init.d/netryx stop 2>/dev/null
/etc/init.d/netryx disable 2>/dev/null
# Leave nothing of ours in the firewall behind: the capture's table and the
# rules' table.
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
PRE
# After the files are gone: the rpcd plugin went with them, and a reload drops
# the ubus object netryx, which rpcd would otherwise list until it restarts.
cat > "$WORK/post-deinstall" <<'POSTDEL'
#!/bin/sh
[ -x /etc/init.d/rpcd ] && /etc/init.d/rpcd reload 2>/dev/null
exit 0
POSTDEL
chmod 0755 "$WORK/post-install" "$WORK/pre-deinstall" "$WORK/post-deinstall"

# @measured 2026-09-15 on OpenWrt 25.12.5, kernel 6.12.94 (Flint 2): the netdev
# ingress chain needs nothing beyond kmod-nft-core, which the nftables package
# pulls in (apk info -R nftables). That router has no kmod-nft-netdev installed
# and the ingress table applied and matched traffic anyway: that package carries
# the netdev dup/fwd expressions, which this rule does not use.
#
# A comment inside a backslash-continued command ends the continuation, and the
# next line then runs as its own command. That produced a 281-byte "package"
# that installed nothing, which is why the packaging scripts are built and
# checked rather than read.
docker run --rm -i -v "$WORK:/work" -w /work "$ALPINE" apk mkpkg \
	--info "name:netryx" \
	--info "version:$VERSION-r$PKGREL" \
	--info "arch:$ARCH" \
	--info "license:MIT" \
	--info "origin:netryx" \
	--info "url:https://github.com/TAIPANBOX/netryx" \
	--info "description:netryx, a home network monitor that runs on the router: which device went where, what answered, and what is worth keeping. No cloud, no telemetry." \
	--info "depends:kmod-nf-conntrack kmod-nf-conntrack-netlink kmod-nfnetlink-log kmod-nft-bridge nftables ca-bundle" \
	--script "post-install:/work/post-install" \
	--script "post-upgrade:/work/post-install" \
	--script "pre-deinstall:/work/pre-deinstall" \
	--script "post-deinstall:/work/post-deinstall" \
	--files /work/tree \
	--output "/work/$OUT"

cp "$WORK/$OUT" "$ROOT/$OUT"
echo "$OUT  ($(du -h "$ROOT/$OUT" | cut -f1))"
