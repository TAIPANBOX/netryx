#!/bin/sh
# Runs ON a Beryl AX (busybox ash) with netryx installed and not yet started:
# its memory idle, through a typical rule set and through ten countries, with
# the nft process's peak, the kernel's sets, the temperature and any OOM kill
# (docs/small-router-memory-measured.md, the Beryl section). Every rule names
# devices that are not on the router, so no client's traffic changes. Rules are
# written as UCI sections and applied with a reload, the agent's path, so the
# console needs no sign-in. The run stops at STOP_TEMP millidegrees (default
# the temperature at start plus 12 C, at most 75 C).
#   sh beryl-memory.sh [memory_limit]      (default 128MiB)
set -u
D=/tmp/nxm; mkdir -p $D; : > $D/run.log
LIMIT=${1:-128MiB}
log() { echo "$(date +%H:%M:%S) $*" | tee -a $D/run.log; }
kb() { awk -v k="$2" '$1==k":"{print $2}' "$1" 2>/dev/null; }
T0=$(cat /sys/class/thermal/thermal_zone0/temp)
STOP=${STOP_TEMP:-$((T0 + 12000))}; [ "$STOP" -gt 75000 ] && STOP=75000
log "start temp=$T0 stop=$STOP MemAvailable=$(kb /proc/meminfo MemAvailable) SUnreclaim=$(kb /proc/meminfo SUnreclaim)"

uci -q batch <<EOF
set netryx.quarantine=quarantine
set netryx.quarantine.enabled='0'
set netryx.main.memory_limit='$LIMIT'
commit netryx
EOF

sampler() {
	nh=0
	while [ ! -e $D/stopsamp ]; do
		p=$(pidof netryx); s=/proc/$p/status
		for q in $(pidof nft); do v=$(kb /proc/$q/status VmHWM); [ -n "$v" ] && [ "$v" -gt "$nh" ] && nh=$v; done
		t=$(cat /sys/class/thermal/thermal_zone0/temp)
		[ "$t" -ge "$STOP" ] && touch $D/hot
		echo "$(date +%s) $(kb $s VmRSS) $(kb $s VmHWM) $(kb $s RssAnon) $(kb /proc/meminfo MemAvailable) $(kb /proc/meminfo SUnreclaim) $nh $t $(cut -d' ' -f1 /proc/loadavg)" >> $D/samples
		sleep 1
	done
}
hot() { [ -e $D/hot ] && { log "temperature reached $STOP, stopping"; return 0; }; return 1; }
snap() {
	p=$(pidof netryx); s=/proc/$p/status
	log "$1 pid=$p rss=$(kb $s VmRSS) anon=$(kb $s RssAnon) hwm=$(kb $s VmHWM) memavail=$(kb /proc/meminfo MemAvailable) sunrec=$(kb /proc/meminfo SUnreclaim) nftpeak=$(tail -1 $D/samples | cut -d' ' -f7) temp=$(cat /sys/class/thermal/thermal_zone0/temp) sets=$(nft -t list sets inet netryx_rules 2>/dev/null | grep -c '^[[:space:]]*set ') rules.nft=$(wc -c < /etc/netryx/rules.nft 2>/dev/null) oom=$(dmesg | grep -c 'Killed process')"
}
applied() { netryx ctl rules 2>/dev/null | tr '{' '\n' | grep -o '"id":"r_[0-9]*"\|"applied":[a-z]*\|"problem":"[^"]*"' | tr '\n' ' '; }
reload() { /etc/init.d/netryx reload; }
A=02:4e:58:00:00:01 B=02:4e:58:00:00:02 C=02:4e:58:00:00:03 E=02:4e:58:00:00:04 F=02:4e:58:00:00:05 NOW=02:4e:58:00:00:99

rm -f $D/stopsamp $D/hot $D/samples
/etc/init.d/netryx start; sleep 3
log "netryx pid=$(pidof netryx) env: $(tr '\0' '\n' < /proc/$(pidof netryx)/environ | grep GOMEMLIMIT)"
sampler & SP=$!
sleep 150; snap idle
hot || {
	until=$(( $(date +%s) + 3600 ))
	uci -q batch <<EOF
set netryx.kids=group
set netryx.kids.name='kids'
set netryx.d_024e58000001=device
set netryx.d_024e58000001.mac='$A'
set netryx.d_024e58000001.group='kids'
set netryx.d_024e58000003=device
set netryx.d_024e58000003.mac='$C'
set netryx.d_024e58000003.group='kids'
set netryx.d_024e58000005=device
set netryx.d_024e58000005.mac='$F'
set netryx.d_024e58000005.group='kids'
set netryx.r_00000001=rule
set netryx.r_00000001.enabled='1'
set netryx.r_00000001.action='block'
set netryx.r_00000001.what='country'
add_list netryx.r_00000001.target='CN'
add_list netryx.r_00000001.target='RU'
add_list netryx.r_00000001.target='KP'
add_list netryx.r_00000001.target='IR'
add_list netryx.r_00000001.target='BY'
set netryx.r_00000001.who='device'
add_list netryx.r_00000001.member='$NOW'
set netryx.r_00000002=rule
set netryx.r_00000002.enabled='1'
set netryx.r_00000002.action='block'
set netryx.r_00000002.what='category'
add_list netryx.r_00000002.target='ads'
set netryx.r_00000002.who='device'
add_list netryx.r_00000002.member='$NOW'
set netryx.r_00000003=rule
set netryx.r_00000003.enabled='1'
set netryx.r_00000003.action='block'
set netryx.r_00000003.what='app'
add_list netryx.r_00000003.target='tiktok'
add_list netryx.r_00000003.target='roblox'
add_list netryx.r_00000003.target='instagram'
add_list netryx.r_00000003.target='snapchat'
set netryx.r_00000003.who='group'
set netryx.r_00000003.group='kids'
set netryx.r_00000004=rule
set netryx.r_00000004.enabled='1'
set netryx.r_00000004.action='block'
set netryx.r_00000004.what='category'
add_list netryx.r_00000004.target='gaming'
add_list netryx.r_00000004.target='video'
add_list netryx.r_00000004.target='social'
set netryx.r_00000004.who='group'
set netryx.r_00000004.group='kids'
set netryx.r_00000004.from='21:00'
set netryx.r_00000004.to='07:00'
set netryx.r_00000005=rule
set netryx.r_00000005.enabled='1'
set netryx.r_00000005.action='limit'
set netryx.r_00000005.minutes='60'
set netryx.r_00000005.what='app'
add_list netryx.r_00000005.target='youtube'
set netryx.r_00000005.who='group'
set netryx.r_00000005.group='kids'
set netryx.r_00000006=rule
set netryx.r_00000006.enabled='1'
set netryx.r_00000006.action='block'
set netryx.r_00000006.what='all'
set netryx.r_00000006.who='device'
add_list netryx.r_00000006.member='$E'
set netryx.r_00000006.until='$until'
commit netryx
EOF
	reload; sleep 40; snap typical; log "typical applied: $(applied)"
	for i in 1 2 3 4 5 6; do uci -q delete netryx.r_0000000$i; done
	for x in kids d_024e58000001 d_024e58000003 d_024e58000005; do uci -q delete netryx.$x; done
	uci commit netryx; reload; sleep 30; snap typical-deleted
}
ma=$(kb /proc/meminfo MemAvailable)
if hot; then :
elif [ "$ma" -lt 200000 ]; then log "ten countries skipped: MemAvailable $ma kB is under 200 MB"
else
	{ echo "set netryx.r_00000010=rule"; echo "set netryx.r_00000010.enabled='1'"; echo "set netryx.r_00000010.action='block'"
	  echo "set netryx.r_00000010.what='country'"
	  for c in US CN DE GB RU JP FR NL BR IN; do echo "add_list netryx.r_00000010.target='$c'"; done
	  echo "set netryx.r_00000010.who='device'"; echo "add_list netryx.r_00000010.member='$NOW'"; echo "commit netryx"; } | uci -q batch
	reload; sleep 40; snap ten-countries; log "ten applied: $(applied)"
	uci -q delete netryx.r_00000010; uci commit netryx; reload; sleep 30; snap ten-deleted
fi
touch $D/stopsamp; wait $SP
/etc/init.d/netryx stop
log "max temp=$(awk '{print $8}' $D/samples | sort -n | tail -1) nft peak=$(awk '{print $7}' $D/samples | sort -n | tail -1) min memavail=$(awk '{print $5}' $D/samples | sort -n | head -1) oom=$(dmesg | grep -c 'Killed process')"
log done
