#!/bin/sh
# Runs ON flint2: Slab, SUnreclaim, MemAvailable (kB), netryx RSS, and the
# elements of every country set in inet netryx_rules. With every country
# loaded (484 sets) this is itself a memory event: the nft listing grew to
# 175 MB RSS on the Flint 2 and MemAvailable fell to 214 MB.
m() { awk -v k="$1:" '$1 == k {print $2}' /proc/meminfo; }
n=$(nft list sets inet netryx_rules 2>/dev/null | grep -c 'set cc_')
e=0
for s in $(nft list sets inet netryx_rules 2>/dev/null | awk '$1 == "set" && $2 ~ /^cc_/ {print $2}'); do
	c=$(nft list set inet netryx_rules $s | tr ',' '\n' | grep -cE '[0-9a-f][.:/-]')
	e=$((e + c))
done
echo "$(date +%T) slab=$(m Slab) sunreclaim=$(m SUnreclaim) memavail=$(m MemAvailable) nx_rss=$(awk '$1=="VmRSS:"{print $2}' /proc/$(pidof netryx)/status) cc_sets=$n cc_elements=$e"
