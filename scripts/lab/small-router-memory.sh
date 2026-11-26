#!/bin/sh
# Runs on a throwaway nxlab-golden clone with limits.memory set (512MiB for the
# Beryl AX), put there by scripts/lab/forge-small-router-memory.sh: netryx's
# memory when idle, under load, through the apply of a typical rule set and of
# every country, with the nft process's own peak and the kernel's sets, and
# whether anything reaches the OOM killer (docs/small-router-memory-measured.md).
# With the argument `answers` it runs only the four country writes at no limit
# and keeps every answer. Needs /tmp/mem/pass (the console's root password), /tmp/mem/cc.* (country
# codes, one a line, 64 a file) and /tmp/mem/hosts.txt.
set -u
D=/tmp/mem
P=$(cat $D/pass); rm -f $D/pass
log() { echo "$(date +%H:%M:%S) $*" >> $D/run.log; }
kb() { awk -v k="$2" '$1==k":"{print $2}' "$1" 2>/dev/null; }

log "MemTotal=$(kb /proc/meminfo MemTotal) MemAvailable=$(kb /proc/meminfo MemAvailable)"

# gctrace to the log, and a log big enough to keep it.
grep -q GODEBUG /etc/init.d/netryx || sed -i '/\[ \$# -gt 0 \] && procd_set_param env/i\	set -- "$@" GODEBUG=gctrace=1' /etc/init.d/netryx
uci set system.@system[0].log_size=8192; uci commit system; /etc/init.d/log restart; sleep 2

# Every nft run's own peak: a wrapper reads the child's VmHWM until it exits.
if [ ! -e /usr/sbin/nft.real ]; then
	mv /usr/sbin/nft /usr/sbin/nft.real
	cat > /usr/sbin/nft <<'EOF'
#!/bin/sh
# Without job control a background command's stdin is /dev/null before any
# redirection, so `<&0` would give nft nothing: the input goes by fd 3.
exec 3<&0
/usr/sbin/nft.real "$@" <&3 3<&- &
p=$!; h=0
while :; do
	st=$(cat /proc/$p/status 2>/dev/null) || break
	case "$st" in *"State:	Z"*) break ;; esac
	v=$(echo "$st" | awk '$1=="VmHWM:"{print $2}'); [ -n "$v" ] && h=$v
	usleep 20000 2>/dev/null || sleep 1
done
wait $p; rc=$?
echo "$(date +%s) $h $rc $(echo "$*" | cut -c1-40)" >> /tmp/mem/nft.log
exit $rc
EOF
	chmod 0755 /usr/sbin/nft
fi
log "nft wrapper: $(head -1 /usr/sbin/nft) usleep=$(command -v usleep)"
# The wrapper must pass the ruleset through: a set made through it has to exist.
echo 'add table inet nxwrap; add set inet nxwrap t { type ipv4_addr; }' | nft -f - && nft.real list set inet nxwrap t >/dev/null 2>&1 \
	&& log "nft wrapper passes input" || { log "nft wrapper loses input"; exit 1; }
nft.real delete table inet nxwrap

sh /root/fakehouse.sh up >/dev/null 2>&1
cat > $D/worker.sh <<'EOF'
D=/tmp/mem; N=$(wc -l < $D/hosts.txt)
while [ ! -e $D/stopload ]; do
	h=$(sed -n "$((RANDOM % N + 1))p" $D/hosts.txt)
	curl -s -o /dev/null -I -m 4 "https://$h/"
done
EOF
load_up() { rm -f $D/stopload; for ns in fh-laptop fh-phone; do for i in 1 2 3 4 5 6; do
	ip netns exec $ns sh $D/worker.sh >/dev/null 2>&1 &
done; done; }
load_down() { touch $D/stopload; sleep 6; }

NOW=02:4e:58:00:00:99   # a device that is not in the house, as in flint2-measured section 6
TV=02:4e:58:00:00:01 LAPTOP=02:4e:58:00:00:02 PHONE=02:4e:58:00:00:03 CAMERA=02:4e:58:00:00:04 CONSOLE=02:4e:58:00:00:05

sampler() {
	while [ ! -e $D/stopsamp ]; do
		p=$(pidof netryx); s=/proc/$p/status
		echo "$(date +%s) $p $(kb $s VmRSS) $(kb $s VmHWM) $(kb $s RssAnon) $(kb /proc/meminfo MemAvailable) $(kb /proc/meminfo SUnreclaim) $(cut -d' ' -f1 /proc/loadavg)" >> $1
		sleep 1
	done
}
api() { # method path body -> code and seconds; answer in $D/ans
	curl -s -o $D/ans -w '%{http_code} %{time_total}' -b $D/cj -H "X-Netryx-CSRF: $CSRF" \
		-H 'Content-Type: application/json' -X "$1" --data-binary "${3:-}" "http://127.0.0.1:8788$2"
}
login() {
	printf '{"user":"root","password":"%s"}' "$P" > $D/lb
	code=$(curl -s -o $D/login.json -w %{http_code} -c $D/cj -H 'Content-Type: application/json' --data-binary @$D/lb http://127.0.0.1:8788/api/login)
	rm -f $D/lb; CSRF=$(jsonfilter -i $D/login.json -e '@.csrf'); rm -f $D/login.json
	log "$L login $code"
}
rule() { # label body
	r=$(api POST /api/rules "$2"); id=$(jsonfilter -i $D/ans -e '@.id' 2>/dev/null)
	ids="$ids $id"; log "$L $1: $r id=$id $(grep -o '"error":"[^"]*"' $D/ans)"
}
drop() { for id in $ids; do r=$(api DELETE /api/rules/$id); log "$L delete $id: $r"; done; ids=; }
snap() { # label
	s=/proc/$PID/status
	log "$L $1 pid=$(pidof netryx) rss=$(kb $s VmRSS) anon=$(kb $s RssAnon) hwm=$(kb $s VmHWM) memavail=$(kb /proc/meminfo MemAvailable) sunrec=$(kb /proc/meminfo SUnreclaim) ct=$(cat /proc/sys/net/netfilter/nf_conntrack_count) sets=$(nft.real -t list sets inet netryx_rules 2>/dev/null | grep -c '^[[:space:]]*set ') oom=$(dmesg | grep -c -i -E 'out of memory|oom-kill|killed process')"
}

phase() { # label memory_limit
	L=$1
	uci set netryx.main.memory_limit="$2"; uci commit netryx
	load_down
	/etc/init.d/netryx restart; sleep 3
	PID=$(pidof netryx)
	log "$L pid=$PID env: $(tr '\0' '\n' < /proc/$PID/environ | grep -E 'GOMEMLIMIT|GODEBUG' | tr '\n' ' ')"
	sleep 150; snap idle
	load_up; sleep 150; snap plateau
	login
	rm -f $D/stopsamp; sampler $D/$L.samples & SP=$!
	: > $D/nft.log
	ids=
	r=$(api PUT /api/groups/kids '{"name":"kids"}'); log "$L group: $r $(head -c 120 $D/ans)"
	for m in $TV $PHONE $CONSOLE; do r=$(api PUT /api/devices/$m '{"group":"kids"}'); log "$L member $m: $r"; done
	rule countries '{"action":"block","what":"country","targets":["CN","RU","KP","IR","BY"],"who":"all"}'
	rule ads '{"action":"block","what":"category","targets":["ads"],"who":"all"}'
	rule kids-apps '{"action":"block","what":"app","targets":["tiktok","roblox","instagram","snapchat"],"who":"group","group":"kids"}'
	rule kids-night '{"action":"block","what":"category","targets":["gaming","video","social"],"who":"group","group":"kids","from":"21:00","to":"07:00"}'
	rule kids-youtube '{"action":"limit","minutes":60,"what":"app","targets":["youtube"],"who":"group","group":"kids"}'
	rule pause-camera "{\"what\":\"all\",\"who\":\"device\",\"members\":[\"$CAMERA\"],\"until\":$(( $(date +%s) + 3600 ))}"
	sleep 30; snap typical
	cp $D/nft.log $D/$L.nft.typical
	drop; sleep 20; snap typical-deleted
	: > $D/nft.log
	for f in $D/cc.*; do
		t=$(sed 's/.*/"&"/' $f | tr '\n' , | sed 's/,$//')
		rule "country $(wc -l < $f)" "{\"action\":\"block\",\"what\":\"country\",\"targets\":[$t],\"who\":\"device\",\"members\":[\"$NOW\"]}"
	done
	sleep 20; snap all-countries
	cp $D/nft.log $D/$L.nft.all
	drop; sleep 20; snap all-deleted
	touch $D/stopsamp; wait $SP
	logread | grep "netryx\[$PID\]" | grep ' gc [0-9]' > $D/$L.gc
	dmesg | grep -i -E 'out of memory|oom-kill|killed process' > $D/$L.oom
	log "$L end gc=$(wc -l < $D/$L.gc) oom_lines=$(wc -l < $D/$L.oom)"
	r=$(api DELETE /api/groups/kids); log "$L group deleted: $r"
	rm -f $D/cj
}

# answers: the four country writes at no limit, every answer kept, to see what
# the console says when the OOM killer takes nft.
answers() {
	L=answers
	uci set netryx.main.memory_limit=none; uci commit netryx
	/etc/init.d/netryx restart; sleep 3; PID=$(pidof netryx)
	load_up; sleep 150; snap plateau
	login
	n=0
	for f in $D/cc.*; do
		n=$((n + 1)); t=$(sed 's/.*/"&"/' $f | tr '\n' , | sed 's/,$//')
		r=$(api POST /api/rules "{\"action\":\"block\",\"what\":\"country\",\"targets\":[$t],\"who\":\"device\",\"members\":[\"$NOW\"]}")
		cp $D/ans $D/answer.$n
		log "$L write $n: $r applied=$(jsonfilter -i $D/ans -e '@.applied') problem=$(jsonfilter -i $D/ans -e '@.problem')"
	done
	sleep 20; snap after
	netryx ctl rules > $D/ctl.rules 2>&1
	log "$L kills=$(dmesg | grep -c 'Killed process')"
}

if [ "${1:-}" = answers ]; then
	answers
else
	phase m160 160MiB
	phase m128 128MiB
	phase none none
fi
load_down; sh /root/fakehouse.sh down >/dev/null 2>&1
log "done"
