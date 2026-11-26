#!/bin/sh
# Runs on a throwaway nxlab-golden clone as root, put there by
# scripts/lab/forge-apply-memory.sh: the peak memory of netryx while it applies
# a big rule set, at five GOMEMLIMIT values (docs/apply-memory-measured.md).
# Each phase restarts netryx with gctrace on, waits 3 minutes under the house's
# load, writes 4 rules of every country and 5 app and category rules through
# the console, deletes them, and samples memory every second throughout.
# Needs /tmp/mem/pass (the console's root password), /tmp/mem/cc.* (country
# codes, one a line, 64 a file) and /tmp/mem/hosts.txt.
set -u
D=/tmp/mem
P=$(cat $D/pass); rm -f $D/pass
log() { echo "$(date +%H:%M:%S) $*" >> $D/run.log; }
kb() { awk -v k="$2" '$1==k":"{print $2}' "$1" 2>/dev/null; }

# gctrace to the log, and a log big enough to keep it.
grep -q GODEBUG /etc/init.d/netryx || sed -i '/\[ \$# -gt 0 \] && procd_set_param env/i\	set -- "$@" GODEBUG=gctrace=1' /etc/init.d/netryx
grep -c GODEBUG /etc/init.d/netryx >> $D/run.log
uci set system.@system[0].log_size=16384; uci commit system; /etc/init.d/log restart; sleep 2

# The house: fakehouse's habits plus 12 request loops in two of its devices.
sh /root/fakehouse.sh up >/dev/null 2>&1
cat > $D/worker.sh <<'EOF'
D=/tmp/mem; N=$(wc -l < $D/hosts.txt)
while [ ! -e $D/stopload ]; do
	h=$(sed -n "$((RANDOM % N + 1))p" $D/hosts.txt)
	curl -s -o /dev/null -I -m 4 "https://$h/"
done
EOF
for ns in fh-laptop fh-phone; do for i in 1 2 3 4 5 6; do
	ip netns exec $ns sh $D/worker.sh >/dev/null 2>&1 &
done; done
log "load up"

NOW=02:4e:58:00:00:99   # a device that is not in the house, as in flint2-measured section 6

sampler() {
	f=$1
	while [ ! -e $D/stopsamp ]; do
		p=$(pidof netryx); s=/proc/$p/status
		n=0; for q in $(pidof nft); do v=$(kb /proc/$q/status VmRSS); n=$((n + ${v:-0})); done
		echo "$(date +%s) $(kb $s VmRSS) $(kb $s VmHWM) $(kb $s RssAnon) $(kb /proc/meminfo MemAvailable) $(kb /proc/meminfo SUnreclaim) $n $(cut -d' ' -f1 /proc/loadavg)" >> $f
		sleep 1
	done
}

post() { # path body -> answer in $D/ans
	curl -s -o $D/ans -w '%{http_code} %{time_total}' -b $D/cj -H "X-Netryx-CSRF: $CSRF" \
		-H 'Content-Type: application/json' -X "$1" --data-binary "$2" "http://127.0.0.1:8788$3"
}

phase() { # label memory_limit
	L=$1
	uci set netryx.main.memory_limit="$2"; uci commit netryx
	/etc/init.d/netryx restart; sleep 3
	PID=$(pidof netryx)
	log "$L pid=$PID env: $(tr '\0' '\n' < /proc/$PID/environ | grep -E 'GOMEMLIMIT|GODEBUG' | tr '\n' ' ')"
	sleep 180
	s=/proc/$PID/status
	log "$L plateau rss=$(kb $s VmRSS) anon=$(kb $s RssAnon) hwm=$(kb $s VmHWM) memavail=$(kb /proc/meminfo MemAvailable) sunrec=$(kb /proc/meminfo SUnreclaim) ct=$(cat /proc/sys/net/netfilter/nf_conntrack_count)"
	printf '{"user":"root","password":"%s"}' "$P" > $D/lb
	code=$(curl -s -o $D/login.json -w %{http_code} -c $D/cj -H 'Content-Type: application/json' --data-binary @$D/lb http://127.0.0.1:8788/api/login)
	rm -f $D/lb
	CSRF=$(jsonfilter -i $D/login.json -e '@.csrf'); rm -f $D/login.json
	log "$L login $code"
	c0=$(cut -d' ' -f14,15 /proc/$PID/stat | awk '{print $1+$2}'); w0=$(cut -d' ' -f1 /proc/uptime)
	rm -f $D/stopsamp; sampler $D/$L.samples & SP=$!
	sleep 5
	ids=
	for f in $D/cc.*; do
		t=$(sed 's/.*/"&"/' $f | tr '\n' , | sed 's/,$//')
		r=$(post POST "{\"action\":\"block\",\"what\":\"country\",\"targets\":[$t],\"who\":\"device\",\"members\":[\"$NOW\"]}" /api/rules)
		id=$(jsonfilter -i $D/ans -e '@.id'); ids="$ids $id"
		log "$L country $(wc -l < $f) codes: $r id=$id $(head -c 160 $D/ans)"
	done
	for b in \
		'"what":"app","targets":["youtube","netflix"],"who":"device","members":["02:4e:58:00:00:01"]' \
		'"what":"category","targets":["social","video"],"who":"device","members":["02:4e:58:00:00:03"]' \
		'"what":"category","targets":["gaming"],"who":"device","members":["02:4e:58:00:00:05"]' \
		'"what":"category","targets":["ads"],"who":"device","members":["02:4e:58:00:00:02"]' \
		'"what":"app","targets":["tiktok","instagram","facebook","discord","twitch","steam","roblox"],"who":"device","members":["02:4e:58:00:00:04"]'; do
		r=$(post POST "{\"action\":\"block\",$b}" /api/rules)
		id=$(jsonfilter -i $D/ans -e '@.id'); ids="$ids $id"
		log "$L app: $r id=$id $(head -c 160 $D/ans)"
	done
	sleep 20
	log "$L applied: sets=$(nft -t list sets inet netryx_rules 2>/dev/null | grep -c '^[[:space:]]*set ') rules.nft=$(wc -c < /etc/netryx/rules.nft) hwm=$(kb $s VmHWM)"
	for id in $ids; do
		r=$(post DELETE '' /api/rules/$id)
		log "$L delete $id: $r"
	done
	sleep 20
	c1=$(cut -d' ' -f14,15 /proc/$PID/stat | awk '{print $1+$2}'); w1=$(cut -d' ' -f1 /proc/uptime)
	log "$L cpu ticks=$((c1 - c0)) wall=$(awk -v a=$w0 -v b=$w1 'BEGIN{print b-a}')s"
	touch $D/stopsamp; wait $SP
	log "$L end hwm=$(kb $s VmHWM) rss=$(kb $s VmRSS) anon=$(kb $s RssAnon)"
	logread | grep "netryx\[$PID\]" | grep ' gc [0-9]' > $D/$L.gc
	log "$L gc lines=$(wc -l < $D/$L.gc)"
	rm -f $D/cj
}

phase none none
phase m160 160MiB
phase m128 128MiB
phase m96 96MiB
phase m64 64MiB
touch $D/stopload; sh /root/fakehouse.sh down >/dev/null 2>&1
log "done"
