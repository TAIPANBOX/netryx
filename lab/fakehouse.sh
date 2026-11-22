#!/bin/sh
# fakehouse: a handful of household devices living in network namespaces on an
# OpenWrt router, each with its own port in br-lan, its own MAC, a DHCP lease
# with a hostname, and its own habits on the real internet.
#
#   fakehouse.sh up      create the devices and start their traffic
#   fakehouse.sh down    stop the traffic and remove every namespace and port
#   fakehouse.sh status  what is running
#
# It exists so the console can be filled and judged without a person carrying
# phones around: the router sees these exactly as it would see real devices on
# a LAN port (the frames enter br-lan through a member port, the router routes
# and NATs them). The one thing they cannot exercise is hardware offload: a
# veth is not a port the switch's flow engine can take over.
#
# Needs: ip-full, kmod-veth, curl, ca-bundle. Volumes are capped (curl
# --limit-rate, sleeps) to a few hundred MB an hour in total.
set -u

BR=${BR:-br-lan}
STATE=/tmp/fakehouse
HERE=$(cd "$(dirname "$0")" && pwd)
# name  mac                hostname
DEVICES="tv 02:4e:58:00:00:01 Smart-TV
laptop 02:4e:58:00:00:02 MacBook-Pro
phone 02:4e:58:00:00:03 iPhone
camera 02:4e:58:00:00:04 Yard-Camera
console 02:4e:58:00:00:05 Xbox"

dhcp_script() {
	cat > "$STATE/udhcpc.sh" <<'EOF'
#!/bin/sh
case "$1" in
bound|renew)
	ip addr flush dev "$interface"
	ip addr add "$ip/${mask:-24}" dev "$interface"
	[ -n "${router:-}" ] && ip route replace default via "${router%% *}" dev "$interface"
	;;
deconfig) ip addr flush dev "$interface" ;;
esac
EOF
	chmod 755 "$STATE/udhcpc.sh"
}

up() {
	mkdir -p "$STATE"
	dhcp_script
	echo "$DEVICES" | while read -r name mac host; do
		ns=fh-$name
		ip netns list | grep -q "^$ns" && continue
		ip netns add "$ns"
		ip link add "v-$name" type veth peer name eth0 netns "$ns"
		ip link set "v-$name" master "$BR" up
		ip -n "$ns" link set eth0 address "$mac"
		ip -n "$ns" link set lo up
		ip -n "$ns" link set eth0 up
		mkdir -p "/etc/netns/$ns"
		echo "nameserver ${BR_ADDR:-192.168.50.1}" > "/etc/netns/$ns/resolv.conf"
		ip netns exec "$ns" udhcpc -i eth0 -q -n -t 5 -x "hostname:$host" -s "$STATE/udhcpc.sh" >/dev/null 2>&1
		ip netns exec "$ns" sh "$HERE/habits.sh" "$name" </dev/null >/dev/null 2>&1 &
		echo "$host ($mac) up: $(ip -n "$ns" -4 addr show eth0 | awk '/inet /{print $2}')"
	done
}

down() {
	for ns in $(ip netns list | awk '/^fh-/{print $1}'); do
		# Every process in the namespace is a habit loop or its curl.
		for p in $(ip netns pids "$ns"); do kill "$p" 2>/dev/null; done
		ip netns del "$ns"
		rm -rf "/etc/netns/$ns"
	done
	for l in $(ip -o link show | awk -F': ' '/v-/{print $2}' | cut -d@ -f1); do
		ip link del "$l" 2>/dev/null
	done
	echo "fakehouse down"
}

status() {
	ip netns list | grep '^fh-' || echo "no devices"
	for ns in $(ip netns list | awk '/^fh-/{print $1}'); do
		echo "$ns: $(ip netns pids "$ns" | wc -l) processes"
	done
}

case "${1:-}" in
up) up ;;
down) down ;;
status) status ;;
*) echo "usage: fakehouse.sh up|down|status" >&2; exit 2 ;;
esac
