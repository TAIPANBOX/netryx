#!/bin/sh
# Runs ON flint2: one line a minute into /tmp/nxload/samples until
# /tmp/nxload/stop exists (and once more with "final" as $1).
D=/tmp/nxload
line() {
	p=$(pidof netryx); h=$(pidof hermes 2>/dev/null | awk '{print $1}')
	[ -n "$h" ] || h=$(for q in /proc/[0-9]*; do grep -q hermes_cli/main.py $q/cmdline 2>/dev/null && echo ${q#/proc/} && break; done)
	s=/proc/$p/status
	kv() { awk -v k="$1:" '$1 == k {print $2}' "$2"; }
	t=$(cat /sys/class/thermal/thermal_zone0/temp)
	echo "$(date +%T) nx_rss=$(kv VmRSS $s) nx_hwm=$(kv VmHWM $s) nx_anon=$(kv RssAnon $s) nx_file=$(kv RssFile $s) nx_thr=$(kv Threads $s)" \
		"hermes_rss=$(kv VmRSS /proc/$h/status) memavail=$(kv MemAvailable /proc/meminfo) slab=$(kv Slab /proc/meminfo) sunreclaim=$(kv SUnreclaim /proc/meminfo)" \
		"ct=$(cat /proc/sys/net/netfilter/nf_conntrack_count) load=$(cut -d' ' -f1 /proc/loadavg) temp=$t" \
		"req=$(cat $D/log.* 2>/dev/null | wc -l) ok=$(cat $D/log.* 2>/dev/null | grep -c '^0$')"
}
if [ "$1" = once ]; then line; exit 0; fi
while [ ! -e $D/stop ]; do line >> $D/samples; sleep 60; done
line >> $D/samples
