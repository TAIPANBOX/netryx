#!/bin/sh
# Runs on the Mac. The lab's Wi-Fi for the Flint 2 stage: the Flint 2 is an
# access point on its 5 GHz radio and the two Beryl AX routers are its
# clients, so the runs have real Wi-Fi devices without a person joining one.
#   up       on each Beryl a station on radio1 in a network of its own
#            (nxsta, DHCP, routes only in table 201, so the Beryl's own
#            default route and management path never move); then on the
#            Flint 2 one hidden WPA3 SSID nx-lab on radio1, 3 dBm, bridged into
#            lan, admitting only the two stations' addresses
#   down     removes all of it and puts each radio1 back to its snapshot
#   status   association, leases and routes, changes nothing
#   fetch <beryl> <https-url>
#            fetch the URL over IPv4 through that Beryl's station; prints the
#            route taken, the bytes fetched and uclient-fetch's exit code
#            (0 on a successful answer, 8 on a 404; it prints no status
#            code), and exits with that code. The Beryls carry no curl, and uclient-fetch
#            cannot bind to an interface, so for the fetch only each IPv4
#            address of the URL's host is routed through table 201 (ip rule
#            pref 3), and the rules are deleted afterwards.
# Arm the rollback (flint2-arm.sh, HOST=...) on all three routers before up or
# down. `down` needs the snapshot in /root/netryx-pre on each router.
#
# The passphrase lives only on forge (~/.netryx-flint2-wifipsk, 0600) and on
# the routers: it goes to each router on stdin, into `uci batch`, never into a
# command line or this repository.
set -eu
AP=${AP:-flint2}
STAS=${STAS:-beryl1 beryl2}
PSKFILE='~/.netryx-flint2-wifipsk'
SSID=nx-lab
TABLE=201

psk() {
	k=$(ssh forge "cat $PSKFILE")
	[ ${#k} -ge 16 ] || { echo "no passphrase in forge:$PSKFILE" >&2; exit 1; }
	printf '%s\n' "$k"
}

# radio1's disabled and txpower back to what the snapshot holds (an option the
# snapshot lacks is deleted), as uci batch lines
restore_radio() {
	ssh "$1" 'set -e
		[ -f /root/netryx-pre/wireless ] || { echo "no snapshot in /root/netryx-pre on this router" >&2; exit 1; }
		# an empty save dir: pending changes in /tmp/.uci must not colour the
		# snapshot read
		e=$(mktemp -d)
		for o in disabled txpower; do
			v=$(uci -q -c /root/netryx-pre -t "$e" get wireless.radio1.$o) || v=""
			if [ -n "$v" ]; then uci set wireless.radio1.$o="$v"; else uci -q delete wireless.radio1.$o || true; fi
		done
		rmdir "$e"'
}

sta_up() {
	psk | ssh "$1" "set -e
		read -r k
		for s in wireless.nxlab_sta network.nxsta network.nxsta_out network.nxsta_src; do uci -q delete \$s || true; done
		{
			echo 'set network.nxsta=interface'
			echo 'set network.nxsta.proto=dhcp'
			echo 'set network.nxsta.ip4table=$TABLE'
			echo 'set network.nxsta.peerdns=0'
			echo 'set network.nxsta_out=rule'
			echo 'set network.nxsta_out.out=nxsta'
			echo 'set network.nxsta_out.lookup=$TABLE'
			echo 'set network.nxsta_src=rule'
			echo 'set network.nxsta_src.src=192.168.124.0/24'
			echo 'set network.nxsta_src.lookup=$TABLE'
			echo 'set wireless.nxlab_sta=wifi-iface'
			echo 'set wireless.nxlab_sta.device=radio1'
			echo 'set wireless.nxlab_sta.mode=sta'
			echo 'set wireless.nxlab_sta.network=nxsta'
			echo 'set wireless.nxlab_sta.ifname=nxsta0'
			echo 'set wireless.nxlab_sta.ssid=$SSID'
			echo 'set wireless.nxlab_sta.encryption=sae'
			printf \"set wireless.nxlab_sta.key='%s'\\n\" \"\$k\"
			echo 'set wireless.radio1.disabled=0'
			echo 'set wireless.radio1.txpower=3'
		} | uci batch
		uci commit network
		uci commit wireless
		/etc/init.d/network reload
		wifi reload"
}

sta_mac() {
	i=0
	while [ $i -lt 30 ]; do
		m=$(ssh "$1" 'cat /sys/class/net/nxsta0/address 2>/dev/null' || true)
		[ -n "$m" ] && { echo "$m"; return 0; }
		sleep 1; i=$((i + 1))
	done
	echo "$1: no nxsta0 after 30 s" >&2
	exit 1
}

ap_up() { # ap_up <mac>...
	list=""
	for m in "$@"; do list="$list
			echo 'add_list wireless.nxlab_ap.maclist=$m'"; done
	psk | ssh "$AP" "set -e
		read -r k
		uci -q delete wireless.nxlab_ap || true
		{
			echo 'set wireless.nxlab_ap=wifi-iface'
			echo 'set wireless.nxlab_ap.device=radio1'
			echo 'set wireless.nxlab_ap.mode=ap'
			echo 'set wireless.nxlab_ap.network=lan'
			echo 'set wireless.nxlab_ap.ssid=$SSID'
			echo 'set wireless.nxlab_ap.hidden=1'
			echo 'set wireless.nxlab_ap.encryption=sae'
			printf \"set wireless.nxlab_ap.key='%s'\\n\" \"\$k\"
			echo 'set wireless.nxlab_ap.isolate=0'
			echo 'set wireless.nxlab_ap.macfilter=allow'$list
			echo 'set wireless.radio1.disabled=0'
			echo 'set wireless.radio1.txpower=3'
		} | uci batch
		uci commit wireless
		wifi reload"
}

cmd=${1:-}
case $cmd in
up)
	macs=""
	for h in $STAS; do
		sta_up "$h"
		macs="$macs $(sta_mac "$h")"
	done
	echo "stations:$macs"
	ap_up $macs
	echo "access point $SSID up on $AP for:$macs"
	;;
down)
	ssh "$AP" 'uci -q delete wireless.nxlab_ap || true'
	restore_radio "$AP"
	ssh "$AP" 'uci commit wireless; wifi reload'
	for h in $STAS; do
		ssh "$h" 'for s in wireless.nxlab_sta network.nxsta network.nxsta_out network.nxsta_src; do uci -q delete $s || true; done'
		restore_radio "$h"
		ssh "$h" 'uci commit network; uci commit wireless; /etc/init.d/network reload; wifi reload'
	done
	echo "Wi-Fi of the lab down on $AP $STAS"
	;;
status)
	ssh "$AP" "iw dev 2>/dev/null | grep -E 'Interface|ssid|txpower'; for d in \$(iw dev | awk '/Interface/ {print \$2}'); do iw dev \$d station dump | grep -E '^Station|signal:'; done; true"
	for h in $STAS; do
		echo "== $h"
		ssh "$h" "iw dev nxsta0 link 2>/dev/null | grep -E 'Connected|SSID|signal' || echo 'nxsta0: not associated'
			ip -4 addr show dev nxsta0 2>/dev/null | grep inet || true
			ip route show table $TABLE; ip route show default"
	done
	;;
fetch)
	h=${2:?usage: $0 fetch <beryl> <https-url>}
	url=${3:?usage: $0 fetch <beryl> <https-url>}
	case $url in https://*) ;; *) echo "an https URL, please" >&2; exit 2 ;; esac
	name=${url#https://}; name=${name%%/*}
	ssh "$h" "ips=\$(nslookup '$name' 2>/dev/null | awk '/^Name:/ {n=1} n && /^Address: [0-9.]+\$/ {print \$2}')
		[ -n \"\$ips\" ] || { echo 'no IPv4 address for $name' >&2; exit 1; }
		clean() { while ip rule del pref 3 2>/dev/null; do :; done; }
		clean
		for a in \$ips; do ip rule add to \$a/32 lookup $TABLE pref 3; done
		for a in \$ips; do ip route get \$a | head -1; done
		# uclient-fetch prints no status line: it exits 0 on a successful
		# answer and 8 on a 404 (measured on a Beryl, 2026-11-21)
		wget -4 -q -O /tmp/nx-fetch.body --timeout=20 '$url'; rc=\$?
		echo \"fetched \$(cat /tmp/nx-fetch.body 2>/dev/null | wc -c) bytes, uclient-fetch exit \$rc\"
		rm -f /tmp/nx-fetch.body
		clean
		exit \$rc"
	;;
*)
	echo "usage: $0 up|down|status|fetch <beryl> <url>" >&2
	exit 2
	;;
esac
