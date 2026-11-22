#!/bin/sh
# Runs on the Mac. Grants, or takes back, the agent's read of netryx on the
# Flint 2 through openwrt-mcp: one policy section, `netryx_reads`, that lets
# the client call the ubus object netryx (`ubus_call`, scope `netryx.*`), whose
# six methods are reads (docs/agent.md, "Reading through openwrt-mcp"). It is
# not a hermes_main_* section, which the hermes-agent init writes afresh at
# each start. Writes are not in it; they stay `uci_apply` with a confirm.
#   add      write the policy section, then restart openwrt-mcp
#   remove   delete the section, then restart openwrt-mcp
#   check    print the section as it stands (changes nothing)
#   keep     delete the saved copy so a later rollback cannot revert other
#            openwrt-mcp edits; refuses while the rollback is armed
# Order: flint2-arm.sh arm, add, check, flint2-arm.sh disarm, keep.
# CLIENT is the name the agent was paired under (default hermes-main, the name
# the hermes-agent package pairs its main profile under); DAYS is the
# life of the grant (default 30). add and remove refuse unless the rollback of
# scripts/lab/flint2-arm.sh arm is armed, as `flint2-run.sh offload` does.
# `add` first saves /etc/config/openwrt-mcp to /root/netryx-pre/openwrt-mcp.extra
# (once, never overwritten); flint2-rollback.sh puts that copy back and restarts
# openwrt-mcp, so the armed rollback covers this change. `remove` is the normal
# way back and deletes the saved copy too. Both refuse while openwrt-mcp holds an unconfirmed apply
# (/etc/openwrt-mcp/pending.json, the record netryx's 409 reads), and add
# refuses when it cannot make a well-formed expiry, since an empty one would
# never expire.
set -eu
CLIENT=${CLIENT:-hermes-main}
DAYS=${DAYS:-30}
SEC=netryx_reads
case $CLIENT in '' | *[!A-Za-z0-9_-]*) echo "CLIENT must be letters, digits, - or _" >&2; exit 2 ;; esac
case $DAYS in '' | *[!0-9]*) echo "DAYS must be a number" >&2; exit 2 ;; esac

armed() {
	ssh flint2 'p=$(cat /var/run/netryx-sleeper.pid 2>/dev/null)
		if [ -z "$p" ] || ! kill -0 "$p" 2>/dev/null || [ -e /root/netryx-disarm ]; then
			echo "the rollback is not armed on flint2: run scripts/lab/flint2-arm.sh arm <seconds> first; nothing changed" >&2
			exit 3
		fi' || exit $?
}

# refuse while openwrt-mcp has an apply not yet confirmed; a record that cannot
# be read counts as pending, as in netryx
pending() {
	cat <<'P'
f=/etc/openwrt-mcp/pending.json
if [ -e $f ]; then
	c=$(tr -d ' \n\t' <$f 2>/dev/null) || c=unreadable
	case $c in '' | '[]') ;; *) echo 'openwrt-mcp holds an unconfirmed apply (or its record cannot be read): confirm or let it roll back first; nothing changed' >&2; exit 6 ;; esac
fi
P
}

restart() { echo '/etc/init.d/openwrt-mcp restart >/dev/null 2>&1; sleep 1; /etc/init.d/openwrt-mcp status 2>&1 | head -1'; }

case ${1:-} in
add)
	armed
	ssh flint2 "set -e
		$(pending)
		[ -d /root/netryx-pre ] || { echo 'no snapshot in /root/netryx-pre: run flint2-arm.sh snapshot first; nothing changed' >&2; exit 5; }
		E=\$(date -u -d @\$(( \$(date +%s) + $DAYS * 86400 )) +%Y-%m-%dT%H:%M:%SZ 2>/dev/null) || E=''
		case \$E in
		[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z) ;;
		*) echo 'cannot compute an expiry date on the router: refusing, an empty one would never expire; nothing changed' >&2; exit 4 ;;
		esac
		if [ ! -e /root/netryx-pre/openwrt-mcp.extra ]; then
			if [ -e /etc/config/openwrt-mcp ]; then cp /etc/config/openwrt-mcp /root/netryx-pre/openwrt-mcp.extra; else : > /root/netryx-pre/openwrt-mcp.extra; fi
		fi
		uci -q delete openwrt-mcp.$SEC || true
		uci set openwrt-mcp.$SEC=policy
		uci set openwrt-mcp.$SEC.client='$CLIENT'
		uci add_list openwrt-mcp.$SEC.tools='ubus_call'
		uci add_list openwrt-mcp.$SEC.scopes='netryx.*'
		uci set openwrt-mcp.$SEC.expires=\"\$E\"
		uci set openwrt-mcp.$SEC.enabled='1'
		uci commit openwrt-mcp
		$(restart)
		uci show openwrt-mcp.$SEC"
	;;
remove)
	armed
	ssh flint2 "set -e
		$(pending)
		uci -q delete openwrt-mcp.$SEC || true
		uci commit openwrt-mcp
		$(restart)
		rm -f /root/netryx-pre/openwrt-mcp.extra
		uci -q show openwrt-mcp.$SEC || echo '$SEC: absent'"
	;;
keep)
	ssh flint2 'p=$(cat /var/run/netryx-sleeper.pid 2>/dev/null)
		if [ -n "$p" ] && kill -0 "$p" 2>/dev/null && [ ! -e /root/netryx-disarm ]; then
			echo "the rollback is still armed on flint2: run scripts/lab/flint2-arm.sh disarm first; nothing changed" >&2
			exit 7
		fi
		rm -f /root/netryx-pre/openwrt-mcp.extra
		echo "saved openwrt-mcp copy removed: a rollback no longer touches /etc/config/openwrt-mcp"' || exit $?
	;;
check)
	ssh flint2 "uci -q show openwrt-mcp.$SEC || echo '$SEC: absent'"
	;;
*)
	echo "usage: $0 add|remove|check|keep" >&2
	exit 2
	;;
esac
