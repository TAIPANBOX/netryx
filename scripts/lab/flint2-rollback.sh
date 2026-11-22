#!/bin/sh
# Runs ON the router (busybox ash). Puts the router back to the snapshot in
# /root/netryx-pre: stops netryx, deletes its nft tables, reverts its own dhcp
# section, copies the saved config files back and reloads what changed. When
# /root/netryx-pre/openwrt-mcp.extra exists it also restores
# /etc/config/openwrt-mcp and restarts openwrt-mcp.
# Every step is tolerant: it never stops halfway, and works with netryx absent.
#   sh netryx-rollback.sh            roll back now
#   sh netryx-rollback.sh wait N     sleep N seconds, then roll back unless
#                                    /root/netryx-disarm exists (arm, boot guard)
PRE=/root/netryx-pre
PIDF=/var/run/netryx-sleeper.pid
DISARM=/root/netryx-disarm

if [ "$1" = wait ]; then
	i=0
	while [ "$i" -lt "${2:-0}" ]; do sleep 1; i=$((i + 1)); done
	rm -f "$PIDF"
	if [ -e "$DISARM" ]; then
		logger -t netryx-rollback "disarmed, rollback not run"
		exit 0
	fi
fi

if [ ! -d "$PRE" ]; then
	logger -t netryx-rollback "no snapshot in $PRE, nothing restored"
	exit 1
fi

# busybox here has no timeout: run in the background and kill after 20 s
limited() {
	"$@" &
	cp=$!
	n=0
	while [ "$n" -lt 20 ] && kill -0 "$cp" 2>/dev/null; do sleep 1; n=$((n + 1)); done
	if kill -0 "$cp" 2>/dev/null; then
		kill "$cp" 2>/dev/null
		sleep 1
		kill -9 "$cp" 2>/dev/null
		logger -t netryx-rollback "timed out: $*"
	fi
}
if [ -x /etc/init.d/netryx ]; then
	limited /etc/init.d/netryx stop
	limited /etc/init.d/netryx disable
fi
for t in "inet netryx_rules" "bridge netryx_iso" "inet netryx" "netdev netryx"; do
	nft delete table $t 2>/dev/null
done

uci -q revert dhcp.netryx_safe

changed=""
for f in $(cat "$PRE/FILES" 2>/dev/null); do
	if [ -e "$PRE/$f" ]; then
		cmp -s "$PRE/$f" "/etc/config/$f" || changed="$changed $f"
		cp "$PRE/$f" "/etc/config/$f"
	else
		# the snapshot recorded the file as absent
		[ -e "/etc/config/$f" ] && changed="$changed $f"
		rm -f "/etc/config/$f"
	fi
	[ "$f" = dhcp ] || uci -q revert "$f"
done

# openwrt-mcp's own policy file is outside FILES: flint2-mcp-reads.sh add saves
# it here once, and it goes back with the rest
if [ -e "$PRE/openwrt-mcp.extra" ]; then
	cmp -s "$PRE/openwrt-mcp.extra" /etc/config/openwrt-mcp || mcp_changed=1
	cp "$PRE/openwrt-mcp.extra" /etc/config/openwrt-mcp
	uci -q revert openwrt-mcp
	if [ -n "${mcp_changed:-}" ] && [ -x /etc/init.d/openwrt-mcp ]; then
		limited /etc/init.d/openwrt-mcp restart
		changed="$changed openwrt-mcp"
	fi
fi

has() { grep -qx "$1" "$PRE/FILES" 2>/dev/null; }
has wireless && wifi reload
has firewall && /etc/init.d/firewall reload
case " $changed " in *" network "*) /etc/init.d/network reload ;; esac
case " $changed " in *" dhcp "*) /etc/init.d/dnsmasq restart ;; esac

logger -t netryx-rollback "rolled back to $PRE (changed:${changed:- none})"
exit 0
