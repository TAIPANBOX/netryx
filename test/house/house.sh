#!/bin/sh
# A house in a container: one router and as many devices as asked for, each
# with its own hardware address, its own DHCP name and its own traffic.
#
# This exists because the claim netryx is built on, "which device went where",
# can only be exercised on a box that ROUTES devices, and the one router in the
# real house is a closed appliance. Here the router is ours, the devices are
# ours, and the whole thing is built and destroyed in a minute.
#
# Runs inside a privileged Linux container. Nothing survives it.
set -eu
N=${N:-4}
LAN=192.168.90
GROUP=7

log() { echo "== $*"; }

apk add -q nftables iproute2 dnsmasq curl ca-certificates >/dev/null 2>&1

log "будую дім: роутер і $N пристроїв"
ip link add br-lan type bridge
ip addr add $LAN.1/24 dev br-lan
ip link set br-lan up

i=1
while [ "$i" -le "$N" ]; do
	ns="dev$i"
	ip netns add "$ns"
	ip link add "v$i" type veth peer name "e$i"
	ip link set "v$i" master br-lan up
	ip link set "e$i" netns "$ns"
	# A hardware address per device, which is the key everything is filed
	# under: this is what proves one device is one row.
	ip netns exec "$ns" ip link set "e$i" address "02:00:00:00:00:0$i"
	ip netns exec "$ns" ip link set lo up
	ip netns exec "$ns" ip link set "e$i" up
	i=$((i + 1))
done

log "резолвер і DHCP"
mkdir -p /var/lib/misc
dnsmasq --interface=br-lan --bind-interfaces \
	--dhcp-range=$LAN.50,$LAN.150,12h \
	--dhcp-leasefile=/tmp/dhcp.leases \
	--log-facility=/tmp/dnsmasq.log --pid-file=/tmp/dnsmasq.pid

log "маршрутизація назовні"
sysctl -qw net.ipv4.ip_forward=1
OUT=$(ip route show default | awk '{print $5; exit}')
cat >/tmp/nat.nft <<NFT
table inet nat {
	chain post { type nat hook postrouting priority srcnat; policy accept;
		ip saddr $LAN.0/24 oifname "$OUT" masquerade
	}
}
NFT
nft -f /tmp/nat.nft

log "кожен пристрій бере адресу по DHCP, зі своїм іменем"
names="phone-lounge laptop-study tv-kitchen camera-hall printer-study tablet-den console-den speaker-hall"
i=1
for name in $names; do
	[ "$i" -le "$N" ] || break
	ns="dev$i"
	ip netns exec "$ns" udhcpc -i "e$i" -x "hostname:$name" -n -q -f -t 5 >/dev/null 2>&1 || true
	ip netns exec "$ns" ip route add default via $LAN.1 2>/dev/null || true
	echo "  $name -> $(ip netns exec "$ns" ip -4 addr show "e$i" | sed -n 's/.*inet \([0-9.]*\).*/\1/p')"
	i=$((i + 1))
done

log "оренди, які роздав резолвер:"
cat /tmp/dhcp.leases 2>/dev/null | awk '{print "  "$2"  "$3"  "$4}'

log "запускаю netryx на роутері цього дому"
/work/netryx serve -mode ingress -watch-iface br-lan -addr 127.0.0.1:8788 \
	-leases /tmp/dhcp.leases -dns=true >/tmp/netryx.log 2>&1 &
sleep 3
grep -qE "слухаю|listening" /tmp/netryx.log || { echo "не піднявся:"; cat /tmp/netryx.log; exit 1; }
echo "  правило в ядрі: $(nft list chain netdev netryx observe | grep -oE 'devices = \{[^}]*\}' | head -1)"

log "пристрої ходять в інтернет, кожен своїм шляхом"
sites="example.com www.wikipedia.org www.debian.org www.kernel.org api.github.com www.rfc-editor.org"
i=1
while [ "$i" -le "$N" ]; do
	ns="dev$i"
	n=1
	for s in $sites; do
		[ "$n" -le 2 ] || break
		ip netns exec "$ns" nslookup "$s" $LAN.1 >/dev/null 2>&1 || true
		ip netns exec "$ns" curl -s -o /dev/null --max-time 6 "https://$s/" || true
		n=$((n + 1))
	done
	# each device gets a different pair, so the table must not look the same
	sites="$(echo $sites | awk '{ $(NF+1)=$1; $1=""; print }')"
	i=$((i + 1))
done
sleep 3

log "що побачив netryx:"
wget -qO- http://127.0.0.1:8788/api/snapshot | tr -d ' \n' > /tmp/snap.json 2>/dev/null

log "рядки таблиці:"
tr '}' '\n' </tmp/snap.json | grep '"mac"' | \
	sed -E 's/.*"name":"([^"]*)","addr":"([^"]*)","mac":"([^"]*)".*/  \1  \2  \3/' | head -12
echo
sites_line=$(grep -o '"sites":\[[^]]*\]' /tmp/snap.json | head -4)
echo "  призначення: $sites_line"
echo
log "перевірка:"
macs=$(grep -o '"mac":"02:00:00:00:00:0[0-9]"' /tmp/snap.json | sort -u | wc -l | tr -d ' ')
named=$(grep -oE '"name":"(phone-lounge|laptop-study|tv-kitchen|camera-hall|printer-study|tablet-den|console-den|speaker-hall)"' /tmp/snap.json | sort -u | wc -l | tr -d ' ')
sites_seen=$(grep -o '"sites":\[[^]]*\]' /tmp/snap.json | tr ',' '\n' | grep -c '"' || true)
hs=$(grep -o '"handshakes":[0-9]*' /tmp/snap.json | cut -d: -f2 | awk '{s+=$1} END{print s+0}')
echo "  пристроїв за апаратною адресою: $macs з $N"
echo "  названих з оренди DHCP: $named з $N"
echo "  розпізнаних призначень: $sites_seen"
echo "  рукостискань усього: $hs"
[ "$macs" -ge "$N" ] || { echo "  ПРОВАЛ: не всі пристрої видно"; exit 1; }
[ "$named" -ge "$N" ] || { echo "  ПРОВАЛ: не всі названі з DHCP"; exit 1; }
[ "$sites_seen" -ge "$N" ] || { echo "  ПРОВАЛ: замало розпізнаних призначень"; exit 1; }
[ "$hs" -ge "$N" ] || { echo "  ПРОВАЛ: замало рукостискань"; exit 1; }
echo "  усе зійшлось: кожен пристрій окремим рядком, з іменем і з тим, куди ходив"
