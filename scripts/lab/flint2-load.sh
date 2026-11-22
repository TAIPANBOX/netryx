#!/bin/sh
# Runs on the Mac. The synthetic house load of docs/flint2-measured.md
# section 6, made on the Flint 2 itself: busybox ip has no netns and the
# kernel no veth, so each test device is a network and mount namespace with a
# TAP inside, and flint2-tapns.py copies its frames to a TAP that is a port of
# br-lan. Every device runs flint2-worker.sh loops (short HTTPS HEAD requests
# to random names of flint2-hosts.txt, through the router's resolver and NAT).
# Arm the rollback first (flint2-arm.sh arm N). up also pushes two helpers
# for use by hand: nsexec.py (run a command inside a device's namespaces) and
# mcpcall.py (one openwrt-mcp tool call, docs/flint2-measured.md section 7).
#   up              push the scripts to /tmp/nxload and start two devices,
#                   a (94:83:c4:3e:12:5b, .66) and b (94:83:c4:3e:12:d3, .67),
#                   6 workers each, their resolver keepers and the
#                   per-minute sampler (/tmp/nxload/samples)
#   status          the latest samples and the bridge's ports
#   down            stop everything, check br-lan has only its own ports
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
D=/tmp/nxload
WORKERS=${WORKERS:-6}

push() {
	ssh flint2 "mkdir -p $D"
	for f in tapns.py worker.sh sample.sh slab.sh mcpcall.py fixdns.py nsexec.py hosts.txt; do
		ssh flint2 "cat > $D/$f" <"$HERE/flint2-$f"
	done
}

case ${1:-} in
up)
	push
	ssh flint2 "cd $D && rm -f stop log.* samples relay.* fixdns.log
		start-stop-daemon -S -b -m -p /var/run/nxload-a.pid -x /usr/bin/python3 -- -I $D/tapns.py a 94:83:c4:3e:12:5b 192.168.124.66 $WORKERS
		start-stop-daemon -S -b -m -p /var/run/nxload-b.pid -x /usr/bin/python3 -- -I $D/tapns.py b 94:83:c4:3e:12:d3 192.168.124.67 $WORKERS
		sleep 3
		for n in a b; do start-stop-daemon -S -b -m -p /var/run/nxload-f\$n.pid -x /usr/bin/python3 -- -I $D/fixdns.py \$(cat /var/run/nxload-\$n.pid) \$n; done
		start-stop-daemon -S -b -m -p /var/run/nxload-s.pid -x /bin/sh -- $D/sample.sh
		brctl show br-lan"
	;;
status)
	ssh flint2 "tail -n 3 $D/samples; brctl show br-lan"
	;;
down)
	# the stop file ends the relays (and their workers), the keepers and the
	# sampler within a minute; the kills make it at once
	ssh flint2 "touch $D/stop
		for n in a b fa fb s; do p=\$(cat /var/run/nxload-\$n.pid 2>/dev/null); [ -n \"\$p\" ] && kill \$p 2>/dev/null; rm -f /var/run/nxload-\$n.pid; done
		sleep 2
		for q in /proc/[0-9]*; do [ \${q#/proc/} = \$\$ ] && continue
			grep -q /tmp/nxload/worker.sh \$q/cmdline 2>/dev/null && kill \${q#/proc/} 2>/dev/null; done
		sleep 1
		ip link show nxla >/dev/null 2>&1 && echo 'nxla still there' || echo 'nxla gone'
		ip link show nxlb >/dev/null 2>&1 && echo 'nxlb still there' || echo 'nxlb gone'
		brctl show br-lan"
	;;
*)
	echo "usage: $0 up|status|down" >&2
	exit 2
	;;
esac
