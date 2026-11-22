#!/bin/sh
# Runs on the Mac. The rollback guard for every router change of the Flint 2
# stage; HOST=flint2 (default), beryl1 or beryl2. The Beryls restore wireless
# and network only.
#   snapshot          save the config files and nft ruleset to /root/netryx-pre
#                     once (a second snapshot refuses)
#   arm N             in N seconds run the rollback, unless disarmed
#   disarm            cancel the timer, remove the boot guard line
#   boot-guard N      /etc/rc.local runs the rollback N seconds after a boot
#                     unless /root/netryx-disarm exists
#   restore           run the rollback now
set -eu
HOST=${HOST:-flint2}
HERE=$(cd "$(dirname "$0")" && pwd)
case $HOST in
flint2) FILES="firewall wireless network dhcp netryx" ;;
*) FILES="wireless network" ;;
esac
RB=/root/netryx-rollback.sh
MARK='# netryx-boot-guard'

# written beside the target and moved in, so a dropped link never leaves a
# truncated script; a failed push stops the caller (set -e) before any sleeper
push() { ssh "$HOST" "cat > $RB.new && mv $RB.new $RB" <"$HERE/flint2-rollback.sh"; }
stop_sleeper() {
	ssh "$HOST" 'p=$(cat /var/run/netryx-sleeper.pid 2>/dev/null)
		[ -n "$p" ] && kill "$p" 2>/dev/null
		rm -f /var/run/netryx-sleeper.pid; exit 0'
}
num() { case ${1:-} in '' | *[!0-9]*) echo "need a number of seconds" >&2; exit 2 ;; esac; }

cmd=${1:-}
case $cmd in
snapshot)
	push
	ssh "$HOST" "set -e
		[ ! -e /root/netryx-pre ] || { echo 'snapshot exists, refusing to overwrite' >&2; exit 1; }
		d=/root/netryx-pre.tmp; rm -rf \$d; mkdir \$d
		: > \$d/FILES
		for f in $FILES; do
			if [ -e /etc/config/\$f ]; then cp /etc/config/\$f \$d/\$f; fi
			echo \$f >> \$d/FILES
		done
		nft list ruleset > \$d/nft.ruleset
		(cd \$d && md5sum \$(ls | grep -v -e FILES -e md5sums) > md5sums)
		mv \$d /root/netryx-pre
		echo snapshot saved: \$(cat /root/netryx-pre/FILES)"
	;;
arm)
	num "${2:-}"
	push
	stop_sleeper
	ssh "$HOST" "rm -f /root/netryx-disarm
		start-stop-daemon -S -b -m -p /var/run/netryx-sleeper.pid -x /bin/sh -- $RB wait $2
		logger -t netryx-rollback 'armed for $2 s'"
	echo "armed on $HOST for $2 s"
	;;
disarm)
	stop_sleeper
	ssh "$HOST" "touch /root/netryx-disarm
		sed -i '/netryx-boot-guard/d' /etc/rc.local
		logger -t netryx-rollback disarmed"
	echo "disarmed on $HOST"
	;;
boot-guard)
	num "${2:-}"
	push
	ssh "$HOST" "rm -f /root/netryx-disarm
		sed -i '/netryx-boot-guard/d' /etc/rc.local
		sed -i '/^exit 0/i [ -e /root/netryx-disarm ] || start-stop-daemon -S -b -m -p /var/run/netryx-sleeper.pid -x /bin/sh -- $RB wait $2 $MARK' /etc/rc.local
		grep -q netryx-boot-guard /etc/rc.local && tail -n 3 /etc/rc.local"
	;;
restore)
	push
	stop_sleeper
	ssh "$HOST" "sh $RB"
	;;
*)
	echo "usage: HOST=flint2|beryl1|beryl2 $0 snapshot|arm N|disarm|boot-guard N|restore" >&2
	exit 2
	;;
esac
