#!/bin/sh
# Runs on the Mac. Helpers for the enforcement runs of the Flint 2 stage
# (docs/flint2-measured.md section 2). beryl1 and beryl2 are Wi-Fi clients of
# the Flint 2 (scripts/lab/flint2-wifi.sh up); their traffic reaches the Flint 2
# only for the addresses routed into table 201 (ip rule pref 3).
#   tunnel                  the console tunnel 127.0.0.1:18788 -> flint2 127.0.0.1:8788
#   login                   log the console in as the rpcd login netryxlab; the
#                           password stays on forge (~/.netryx-flint2pw) and goes
#                           to curl on stdin; cookie and CSRF token in $NXRUN
#   api METHOD PATH [JSON]  one console call with the session (logs in again on
#                           a 401); prints the body
#   route <beryl> add|del <ip>...
#                           route these addresses through the station (pref 3)
#   bg <beryl> <name> <url> <bytes/s>
#                           a download through the station in the background,
#                           read at about that rate (0: as fast as it comes) and
#                           thrown away; /tmp/nx-bg-<name>.log gets its start,
#                           the bytes read each second and its end with
#                           uclient-fetch's exit code
#   bgstop <beryl> <name>   stop it
#   fetch <beryl> <url> [resolver]
#                           one fetch through the station, its bytes, exit code
#                           and seconds; with a resolver (192.168.124.1) the
#                           name is looked up there first, as a house device does
#   cut <mac> <remote-ip> [s]
#                           press the console's cut on the device's live
#                           connection to that address and watch its conntrack
#                           line on the Flint 2
#   watch <remote-ip> <s>   conntrack lines with that address, a line per change
#   sample <beryl> <mac> <s> [step]
#                           station rx counters beside the console's bytes and
#                           live rate of the device, every step seconds
#   rx <beryl>              nxsta0 rx bytes and packets
#   ct <ip>                 the Flint 2's conntrack lines with that address
#   temps                   cpu-thermal of the three routers and the slack to
#                           the lowest trip point
#   offload off|sw|hw       fw4's flow offloading on the Flint 2 (arm first):
#                           off deletes both options, as the snapshot has them
set -eu
NXRUN=${NXRUN:-${TMPDIR:-/tmp}/nx-run}
PORT=18788
BASE=http://127.0.0.1:$PORT
TABLE=201
mkdir -p "$NXRUN"
chmod 700 "$NXRUN"

login() {
	# printf is a builtin on forge's shell: the password never reaches argv
	ssh forge 'printf "{\"user\":\"netryxlab\",\"password\":\"%s\"}" "$(cat ~/.netryx-flint2pw)"' |
		curl -s -c "$NXRUN/jar" -H 'Content-Type: application/json' --data-binary @- "$BASE/api/login" >"$NXRUN/login.json"
	sed -n 's/.*"csrf":"\([^"]*\)".*/\1/p' "$NXRUN/login.json" >"$NXRUN/csrf"
	[ -s "$NXRUN/csrf" ] || { echo "login failed" >&2; exit 1; }
	sed 's/"csrf":"[^"]*"/"csrf":"(set)"/' "$NXRUN/login.json"
	echo
}

call() { # call METHOD PATH [JSON] -> status in $NXRUN/status, body in $NXRUN/body
	if [ -n "${3:-}" ]; then
		printf '%s' "$3" | curl -s -b "$NXRUN/jar" -o "$NXRUN/body" -w '%{http_code}' -X "$1" \
			-H 'Content-Type: application/json' -H "X-Netryx-CSRF: $(cat "$NXRUN/csrf" 2>/dev/null)" \
			--data-binary @- "$BASE$2" >"$NXRUN/status"
	else
		curl -s -b "$NXRUN/jar" -o "$NXRUN/body" -w '%{http_code}' -X "$1" \
			-H "X-Netryx-CSRF: $(cat "$NXRUN/csrf" 2>/dev/null)" "$BASE$2" >"$NXRUN/status"
	fi
}

route() { # route <beryl> add|del <ip>...
	h=$1 op=$2
	shift 2
	for a in "$@"; do
		if [ "$op" = add ]; then
			ssh "$h" "ip rule del to $a/32 lookup $TABLE pref 3 2>/dev/null; ip rule add to $a/32 lookup $TABLE pref 3"
		else
			ssh "$h" "while ip rule del to $a/32 lookup $TABLE pref 3 2>/dev/null; do :; done"
		fi
	done
}

host_ips() { # host_ips <beryl> <url> [resolver]
	name=${2#*://}
	name=${name%%/*}
	name=${name%%:*}
	case $name in *[!0-9.]*) ;; *) echo "$name"; return ;; esac
	ssh "$1" "nslookup '$name' ${3:-} 2>/dev/null | awk '/^Name:/ {n=1} n && /^Address: [0-9.]+\$/ {print \$2}'"
}

cmd=${1:-}
[ $# -gt 0 ] && shift
case $cmd in
tunnel)
	if curl -s -o /dev/null -m 3 "$BASE/"; then echo "tunnel up"; exit 0; fi
	ssh -f -N -o ExitOnForwardFailure=yes -L 127.0.0.1:$PORT:127.0.0.1:8788 flint2
	echo "tunnel opened"
	;;
login) login ;;
api)
	call "$1" "$2" "${3:-}"
	if [ "$(cat "$NXRUN/status")" = 401 ]; then
		login >/dev/null
		call "$1" "$2" "${3:-}"
	fi
	echo "HTTP $(cat "$NXRUN/status") at $(python3 -c 'import time; print("%.2f" % time.time())')" >&2
	cat "$NXRUN/body"
	echo
	;;
route) route "$@" ;;
bg)
	h=$1 name=$2 url=$3 rate=$4
	ips=$(host_ips "$h" "$url" "${RESOLVER:-}")
	[ -n "$ips" ] || { echo "no IPv4 address for $url" >&2; exit 1; }
	# the addresses bg routes are kept in /tmp/nx-bg-<name>.rules on the Beryl,
	# so bgstop takes away exactly what bg added
	# shellcheck disable=SC2086
	ssh "$h" "echo $(echo "${RESOLVER:-} $ips" | tr '\n' ' ') > /tmp/nx-bg-$name.rules"
	[ -z "${RESOLVER:-}" ] || route "$h" add "$RESOLVER"
	# shellcheck disable=SC2086
	route "$h" add $ips
	if [ "$rate" -ge 65536 ]; then bs=65536 cnt=$((rate / 65536)); elif [ "$rate" -gt 0 ]; then bs=$rate cnt=1; else bs=65536 cnt=1024; fi
	ssh "$h" "cat > /tmp/nx-bg-$name.sh" <<EOF
#!/bin/sh
L=/tmp/nx-bg-$name.log
echo "start \$(date +%s) $url" > \$L
{ wget -4 -q -T 30 -O - '$url'; echo "end \$(date +%s) uclient-fetch exit \$?" >> \$L; } | {
	t=0
	while :; do
		n=\$(dd bs=$bs count=$cnt 2>/dev/null | wc -c)
		[ "\${n:-0}" = 0 ] && break
		[ $rate -gt 0 ] && sleep 1
		t=\$((t + 1))
		echo "read \$(date +%s) \$n" >> \$L
	done
}
EOF
	ssh "$h" "start-stop-daemon -S -b -m -p /var/run/nx-bg-$name.pid -x /bin/sh -- /tmp/nx-bg-$name.sh"
	echo "bg $name on $h: $url via $(echo "$ips" | tr '\n' ' ')at $rate B/s"
	;;
bgstop)
	h=$1 name=$2
	# the Beryls run no uclient-fetch of their own: every one is a run's
	# (uclient-fetch does not stop on TERM while it waits: KILL)
	# (this shell's own command line names the script too: skip it)
	ssh "$h" "for q in /proc/[0-9]*; do [ \${q#/proc/} = \$\$ ] && continue
			grep -q nx-bg-$name.sh \$q/cmdline 2>/dev/null && kill -9 \${q#/proc/} 2>/dev/null; done
		for q in \$(pidof uclient-fetch); do kill -9 \$q 2>/dev/null; done
		for a in \$(cat /tmp/nx-bg-$name.rules 2>/dev/null); do
			while ip rule del to \$a/32 lookup $TABLE pref 3 2>/dev/null; do :; done
		done
		rm -f /var/run/nx-bg-$name.pid /tmp/nx-bg-$name.rules; tail -n 2 /tmp/nx-bg-$name.log 2>/dev/null; true"
	;;
fetch)
	h=$1 url=$2 res=${3:-}
	ips=""
	# whatever happens after this point, the rules this fetch added go
	# (the resolver's and each address's)
	# shellcheck disable=SC2086
	trap 'route "$h" del $res $ips' EXIT
	trap 'exit 130' INT TERM
	[ -z "$res" ] || route "$h" add "$res"
	ips=$(host_ips "$h" "$url" "$res")
	[ -n "$ips" ] || { echo "no IPv4 address for $url" >&2; exit 1; }
	# shellcheck disable=SC2086
	route "$h" add $ips
	ssh "$h" "t0=\$(date +%s); wget -4 -q -T ${T:-10} -O /tmp/nx-fetch.body '$url'; rc=\$?
		echo \"\$(date +%T) fetched \$(wc -c < /tmp/nx-fetch.body 2>/dev/null || echo 0) bytes, exit \$rc, \$((\$(date +%s) - t0)) s, via $(echo "$ips" | tr '\n' ' ')\"
		rm -f /tmp/nx-fetch.body" || true
	;;
cut)
	# cut <mac> <remote-ip> [seconds]: press the console's cut on the device's
	# live connection to that address, and watch its conntrack line on the
	# Flint 2 (state, flags, NAT port; a line per change, times in Unix
	# seconds) from 2 s before to the end
	mac=$1 ip=$2 secs=${3:-20}
	call GET "/api/devices/$mac/live"
	[ "$(cat "$NXRUN/status")" = 401 ] && { login >/dev/null; call GET "/api/devices/$mac/live"; }
	c=$(grep -o "\"proto\":[0-9]*,\"port\":[0-9]*,\"local\":\"[^\"]*\",\"remote\":\"$ip:[0-9]*\"" "$NXRUN/body" | head -1)
	[ -n "$c" ] || { echo "no live connection of $mac to $ip in the console" >&2; exit 1; }
	proto=$(echo "$c" | sed 's/.*"proto":\([0-9]*\).*/\1/')
	local_=$(echo "$c" | sed 's/.*"local":"\([^"]*\)".*/\1/')
	remote=$(echo "$c" | sed 's/.*"remote":"\([^"]*\)".*/\1/')
	lport=${local_##*:}
	echo "live connection: $local_ -> $remote proto $proto"
	ssh flint2 "e0=\$(date +%s); u0=\$(cut -d' ' -f1 /proc/uptime); end=\$((e0 + $secs)); last=x
		while [ \$(date +%s) -lt \$end ]; do
			l=\$(grep -F 'sport=$lport dport=' /proc/net/nf_conntrack | grep -F 'dst=$ip ' |
				awk '{fl=\"\"; for (i = 1; i <= NF; i++) { if (\$i ~ /^\\[/) fl = fl \" \" \$i; if (\$i ~ /^dport=/) np = \$i }; print \$6 fl \" nat-\" np}')
			[ \"\$l\" != \"\$last\" ] && echo \"\$(awk -v u0=\$u0 -v e0=\$e0 '{printf \"%.2f\", e0 + \$1 - u0}' /proc/uptime) \${l:-(no entry)}\"
			last=\$l
		done" >"$NXRUN/cutwatch" &
	sleep 2
	t=$(python3 -c 'import time; print("%.2f" % time.time())')
	call POST "/api/devices/$mac/conns/cut" "{\"local\":\"$local_\",\"remote\":\"$remote\",\"proto\":$proto}"
	echo "cut posted at $t: HTTP $(cat "$NXRUN/status") $(cat "$NXRUN/body")"
	wait
	cat "$NXRUN/cutwatch"
	;;
watch)
	# watch <remote-ip> <seconds>: every conntrack line on the Flint 2 with
	# that remote address, one output line per change: Unix time, then per
	# entry the device port, state, flags and NAT port
	ip=$1 secs=$2
	ssh flint2 "e0=\$(date +%s); u0=\$(cut -d' ' -f1 /proc/uptime); end=\$((e0 + $secs)); last=x
		while [ \$(date +%s) -lt \$end ]; do
			l=\$(grep -F 'dst=$ip ' /proc/net/nf_conntrack |
				awk '{fl=\"\"; sp=\"\"; for (i = 1; i <= NF; i++) { if (\$i ~ /^\\[/) fl = fl \" \" \$i; if (\$i ~ /^sport=/ && sp == \"\") sp = \$i; if (\$i ~ /^dport=/) np = \$i }; printf \"%s %s%s nat-%s; \", sp, \$6, fl, np}')
			[ \"\$l\" != \"\$last\" ] && echo \"\$(awk -v u0=\$u0 -v e0=\$e0 '{printf \"%.2f\", e0 + \$1 - u0}' /proc/uptime) \${l:-(no entry)}\"
			last=\$l
		done"
	;;
sample)
	# sample <beryl> <mac> <seconds> [step]: every step s (default 2) the
	# station's rx counters beside the console's last-24 h bytes and live rate
	# of that device (the live poll also makes the console sync every 2 s)
	h=$1 mac=$2 secs=$3 step=${4:-2}
	end=$(($(date +%s) + secs))
	echo "time rx_bytes rx_packets console_down console_up live_down_Bps live_flows"
	while [ "$(date +%s)" -lt "$end" ]; do
		r=$(ssh "$h" 'echo "$(cat /sys/class/net/nxsta0/statistics/rx_bytes) $(cat /sys/class/net/nxsta0/statistics/rx_packets)"')
		call GET "/api/devices/$mac/live"
		[ "$(cat "$NXRUN/status")" = 401 ] && { login >/dev/null; call GET "/api/devices/$mac/live"; }
		lr=$(sed -n 's/.*"rate":{"up":\([0-9.e+]*\),"down":\([0-9.e+]*\)}.*"flows":\([0-9]*\).*/\2 \3/p' "$NXRUN/body")
		call GET "/api/devices/$mac"
		d=$(sed -n 's/.*"last24h":{"down":\([0-9]*\),"up":\([0-9]*\)}.*/\1 \2/p' "$NXRUN/body")
		echo "$(date +%T) $r ${d:-? ?} ${lr:-? ?}"
		sleep "$step"
	done
	;;
rx) ssh "$1" 'echo "$(date +%s) rx_bytes $(cat /sys/class/net/nxsta0/statistics/rx_bytes) rx_packets $(cat /sys/class/net/nxsta0/statistics/rx_packets)"' ;;
ct) ssh flint2 "grep -F '$1' /proc/net/nf_conntrack || echo '(no entry for $1)'" ;;
temps)
	for h in flint2 beryl1 beryl2; do
		ssh "$h" 'for z in /sys/class/thermal/thermal_zone*; do t=$(cat $z/temp); p=$(cat $z/trip_point_0_temp 2>/dev/null || echo 0)
			echo "$(cat /proc/sys/kernel/hostname) $(cat $z/type) $((t / 100))e-1 C, slack $(((p - t) / 100))e-1 C"; done' | sed 's/\([0-9]\)\([0-9]\)e-1/\1.\2/g'
	done
	;;
offload)
	case ${1:-} in
	off) set_="uci -q delete firewall.@defaults[0].flow_offloading; uci -q delete firewall.@defaults[0].flow_offloading_hw" ;;
	sw) set_="uci set firewall.@defaults[0].flow_offloading=1; uci -q delete firewall.@defaults[0].flow_offloading_hw" ;;
	hw) set_="uci set firewall.@defaults[0].flow_offloading=1; uci set firewall.@defaults[0].flow_offloading_hw=1" ;;
	*) echo "offload off|sw|hw" >&2; exit 2 ;;
	esac
	# a firewall change is made only under the rollback: the sleeper that
	# flint2-arm.sh arm starts must be alive and not disarmed
	ssh flint2 "p=\$(cat /var/run/netryx-sleeper.pid 2>/dev/null)
		if [ -z \"\$p\" ] || ! kill -0 \"\$p\" 2>/dev/null || [ -e /root/netryx-disarm ]; then
			echo 'the rollback is not armed on flint2: run scripts/lab/flint2-arm.sh arm <seconds> first; nothing changed' >&2
			exit 3
		fi
		$set_; uci commit firewall; /etc/init.d/firewall reload >/dev/null 2>&1; uci show firewall.@defaults[0] | grep flow_offloading || echo 'flow_offloading: absent'; nft list flowtables | sed -n '/flowtable/,/}/p'"
	;;
*)
	echo "usage: $0 tunnel|login|api|route|bg|bgstop|fetch|cut|watch|sample|rx|ct|temps|offload ..." >&2
	exit 2
	;;
esac
