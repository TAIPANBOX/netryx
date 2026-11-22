#!/bin/sh
# Runs ON flint2 inside a tapns.py namespace: short HTTPS requests (HEAD, 4 s
# at most) to random names of hosts.txt until /tmp/nxload/stop exists; one
# line per request (exit code) in /tmp/nxload/log.<id>.
# $RANDOM is busybox ash's (not POSIX sh).
D=/tmp/nxload
N=$(wc -l < $D/hosts.txt)
while [ ! -e $D/stop ]; do
	h=$(sed -n "$((RANDOM % N + 1))p" $D/hosts.txt)
	wget -q -T 4 --spider "https://$h/" >/dev/null 2>&1
	echo $? >> $D/log.$1
done
