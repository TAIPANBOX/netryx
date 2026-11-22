# Stage 2b: what the lab VM showed

Lab: netryx-lab on forge (OpenWrt 25.12.6 x86-64, dnsmasq 2.93, fakehouse
devices `fh-tv` 02:4e:58:00:00:01 and `fh-laptop` 02:4e:58:00:00:02,
house address 192.168.50.1, house ULA fd21:b397:60f5::1). Each section ends
with the decision it fixed for the stage's plan.

## 1. A second dnsmasq instance through UCI

The stock init (`/etc/init.d/dnsmasq`) takes an instance name:
`start_service`, `stop_service` and so `restart` act on the one `dnsmasq`
section named by `$1`.

```
uci -q batch <<EOF
set dhcp.netryx_safe=dnsmasq
set dhcp.netryx_safe.port=5354
set dhcp.netryx_safe.noresolv=1
add_list dhcp.netryx_safe.server=127.0.0.1#53
add_list dhcp.netryx_safe.interface=lan
set dhcp.netryx_safe.nonwildcard=1
set dhcp.netryx_safe.localservice=0
set dhcp.netryx_safe.leasefile=/tmp/netryx_safe.leases
add_list dhcp.netryx_safe.address=/www.google.com/216.239.38.120
add_list dhcp.netryx_safe.address=/www.google.com/2001:4860:4802:32::78
add_list dhcp.netryx_safe.address=/use-application-dns.net/
set dhcp.netryx_safe.extraconftext=no-dhcp-interface=br-lan
commit dhcp
EOF
/etc/init.d/dnsmasq start netryx_safe
/etc/init.d/dnsmasq restart netryx_safe
```

- The main instance kept its pids (3173, 3198) through start and two
  restarts of `netryx_safe`.
- The instance listens on 5354 on the house addresses (192.168.50.1,
  fd21:b397:60f5::1, link-local) and on loopback.
- It answers `www.google.com` with 216.239.38.120 and, once the AAAA line is
  there, 2001:4860:4802:32::78; `use-application-dns.net` NXDOMAIN;
  `example.com` normally (forwarded to the main resolver).
- **The instance inherits every `dhcp` section without an `instance` option**:
  its generated `/var/etc/dnsmasq.conf.netryx_safe` carried
  `dhcp-range=set:lan,192.168.50.100,...`. With
  `extraconftext=no-dhcp-interface=<device>` for each house device it listens
  on, a DHCP request from `fh-tv` got exactly one DISCOVER/OFFER/ACK in the
  log (the main instance's). It still opens 0.0.0.0:67, unused.

@measured `uci -q batch` (above) then `/etc/init.d/dnsmasq restart netryx_safe; pgrep -a dnsmasq; netstat -lnup | grep 5354; ip netns exec fh-tv udhcpc -n -q -t 3 -i <port> -s /bin/true; logread | grep dnsmasq-dhcp` 2026-11-21

Decision for Task 6: the section shape above, with one
`no-dhcp-interface=<device>` line per house device in `extraconftext`
(newline separated), `interface` listing the house networks, and
`/etc/init.d/dnsmasq start|restart|stop netryx_safe` as the only commands
netryx runs. Without the `no-dhcp-interface` lines the house would get two
DHCP servers; that is a test (the section netryx writes always carries them).

## 2. Redirect, and what the capture sees

Scratch table, loaded with `nft -f`:

```
table inet s2b {
  chain dns { type nat hook prerouting priority dstnat - 5; policy accept;
    iifname br-lan ether saddr 02:4e:58:00:00:01 meta l4proto { tcp, udp } th dport 53 counter redirect to :5354
    iifname br-lan fib daddr type != local meta l4proto { tcp, udp } th dport 53 counter redirect to :53
  }
  chain out { type filter hook output priority filter - 1; policy accept;
    udp sport 53 counter comment "sport53"
    udp sport 5354 counter comment "sport5354"
    tcp sport 5354 counter comment "tcp5354"
  }
}
```

- `fh-tv`: `nslookup www.google.com 8.8.8.8` and
  `nslookup www.google.com 192.168.50.1` both answered 216.239.38.120 (the
  safe address); `nslookup www.google.com fd21:b397:60f5::1` (IPv6, to the
  router) the same.
- `fh-laptop`: `nslookup www.google.com 8.8.8.8` answered by the router's main
  resolver with Google's ordinary address (counter on the second line).
- Answers from the 5354 instance left with **source port 5354** at the output
  hook (`sport5354` counted 6, `sport53` counted only the laptop's 2). The
  redirect is undone after the output hook.
- TCP was not exercised: busybox nslookup has no TCP mode and the VM has no
  dig. The redirect line matches both protocols by `meta l4proto`.

@measured `nft -f /tmp/s2b.nft; ip netns exec fh-tv nslookup www.google.com 8.8.8.8; ...; nft list table inet s2b` 2026-11-21

Decision for Task 6: the capture's DNS answer rule takes the safe resolver's
port as well as 53 (only answers the router itself sends: the rule sits on the
output hook), so passive DNS keeps reading what filtered devices resolve.

Later decision (an automated security review of the first build): the safe
resolver answers on **553**, not 5354. A port below 1024 can be bound only by
root, so while the instance is down no local process can take its port and
have its answers believed by the capture. The measurements above were made
on 5354; the stage's live run repeats the redirect and the capture on 553.

## 3. Safe-search hosts

Through the main resolver, 2026-11-21:

| host | A | AAAA |
|---|---|---|
| forcesafesearch.google.com | 216.239.38.120 | 2001:4860:4802:32::78 |
| strict.bing.com | 150.171.27.16, 150.171.28.16 | 2620:1ec:33:1::16, 2620:1ec:33::16 |
| restrict.youtube.com | 216.239.38.120 | 2001:4860:4802:32::78 |
| restrictmoderate.youtube.com | 216.239.38.119 | 2001:4860:4802:32::77 |

@measured `nslookup <host> 127.0.0.1` 2026-11-21

Decision for Task 6: resolve these four through the main resolver (never
hard-code them; the table above is today's), write one `address=` line per
front name and family, refresh daily.

## 4. adblock-fast

`apk add adblock-fast` installed 1.2.4-r4. Its UCI: `config adblock-fast
'config'` (`enabled`, `dns 'dnsmasq.servers'`) and **anonymous** `file_url`
sections (`name`, `url`, `action 'block'`, `enabled`).

Turning `config.enabled` and one source on, then
`/etc/init.d/adblock-fast restart`: it took **22 s**, downloaded the list
(169750 domains) and **restarted every dnsmasq instance**, the main one
included (pids 3173/3198 became 6291/6319, and `netryx_safe` restarted too).

@measured `uci set adblock-fast.config.enabled=1; uci set adblock-fast.@file_url[1].enabled=1; uci commit; time /etc/init.d/adblock-fast restart; pgrep -a dnsmasq` 2026-11-21

Decisions for Task 7: sources are addressed as `@file_url[<n>]` with the
source's url checked before a write (the index can shift); the write
changes only `enabled`; the panel states that adblock-fast restarts the
router's DNS, for about 20 s. That restart is adblock-fast's own, run only when the
owner flips a switch; netryx itself still never restarts the main resolver.

The VM was put back: `netryx_safe` stopped and removed, adblock-fast left
installed and disabled.

## 5. The section lives in uncommitted changes

Found during the stage's live run: adblock-fast keeps its dhcp override
(`serversfile` on every dnsmasq section) as **uncommitted** uci changes, so a
resolver that waited for no pending dhcp changes never started beside it, and
one that committed its section would have made adblock-fast's runtime change
permanent. `uci -t <dir>` does not isolate a commit: libuci still read the
default delta directory and wrote adblock-fast's line into
`/etc/config/dhcp`.

What works is what adblock-fast itself does: write the section without a
commit. The dnsmasq init reads the config with pending changes applied.

```
printf "revert dhcp.netryx_safe\nset dhcp.netryx_safe=dnsmasq\nset dhcp.netryx_safe.port=553\n...\n" | uci -q batch
/etc/init.d/dnsmasq restart netryx_safe
nslookup -port=553 www.google.com 127.0.0.1
uci revert dhcp.netryx_safe; uci -q changes dhcp
```

- `/etc/config/dhcp` stayed byte for byte the same (`cmp` with the copy).
- The instance listened on 553 (five sockets) and answered the safe address.
- `uci revert dhcp.netryx_safe` dropped only that section's changes;
  adblock-fast's pending `serversfile` stayed pending.
- The main instance kept its pids (6762, 6791) throughout.

@measured `uci -q batch` (above, no commit) then `cmp /etc/config/dhcp <copy>; /etc/init.d/dnsmasq restart netryx_safe; netstat -lnup | grep :553; uci revert dhcp.netryx_safe; uci -q changes dhcp` 2026-11-21

Decisions: the section is written as `revert`, `delete`, then the options,
never committed; turning it off is `revert` plus an uncommitted `delete`
(which also hides a copy someone committed); a reboot forgets it, so the
rules file the init loads at boot never redirects to it, and the redirect
returns with the first apply after netryx has the resolver answering.

When the owner turns adblock-fast on from the console, adblock-fast itself runs
`uci commit dhcp`, and that commits whatever is pending in dhcp, the
`netryx_safe` section included (seen in the live run: the section, with
adblock-fast's `serversfile` added to it, was in `/etc/config/dhcp` after the
switch). netryx never commits; a committed copy is harmless: after a reboot the
instance starts early (the boot rules still do not redirect to it), and when
the filters are off netryx stops it and hides the section with an uncommitted
delete.

## 6. The stage's live run

On the final build, with fakehouse, luci and adblock-fast in the VM:

- forced DNS: `fh-laptop` asking 8.8.8.8 for `openwrt.lan` got 192.168.50.1;
- DoT: `curl https://1.1.1.1:853/` from `fh-laptop` exit 7 (refused),
  `hits_f_dot` 1, port 443 answered 301;
- a DoH category rule for the laptop: `curl https://dns.google/resolve?...`
  exit 7 on the laptop, 200 on the tv;
- the filtered group (tv): `www.google.com` via 8.8.8.8 answered 216.239.38.120
  and 2001:4860:4802:32::78, `www.youtube.com` 216.239.38.119, `www.bing.com`
  the strict addresses; the laptop got YouTube's ordinary address; the main
  dnsmasq kept pids 6762/6791 and `/etc/config/dhcp` stayed byte for byte;
- the safe instance stopped by hand: back with the tv answered safely again
  after 18 s;
- an adblock-fast list switched through the API (login, CSRF): 202 with the
  restart note, 409 for a stale url;
- a 1-minute Telegram limit for the laptop: `netryx ctl usage` showed used 1 of
  1 with `spentUntil` at local midnight, `api.telegram.org` exit 7 on the
  laptop and 302 on the tv; the clock set to 23:59:45: after midnight used 0,
  spent empty, the laptop answered 302;
- an agent `filters` section: both redirects stayed;
- a reboot of the VM: the boot rules had only the forced redirect, and within
  seconds of netryx starting the filtered redirect was back and the tv
  answered safely; the DoH rule still held.

@measured live run on netryx-lab, build 2837901ac464bd05, 2026-11-21 (commands and outputs in the stage 2b ledger)

## 7. A zone whose input is REJECT

Found by the final review: the redirect to 553 happens before the input
chain, and fw4 accepts what was DNAT'ed only in zones with port forwards. The
lan zone was set to input REJECT with a rule accepting 53 (what a guest network
allows), uncommitted, and fw4 reloaded, with the filtered tv:

- without an include: the tv asking 8.8.8.8 got "Connection refused"; the
  laptop (not filtered, redirected to 53) was answered;
- with `/usr/share/nftables.d/chain-pre/input/netryx.nft` holding
  `meta l4proto { tcp, udp } th dport 553 ct status dnat accept`, which fw4
  puts at the head of its `input` chain: the tv got 216.239.38.120 and
  2001:4860:4802:32::78; the laptop asking 192.168.50.1 on port 553 directly
  (no DNAT) was still refused.

@measured `uci set firewall.@zone[0].input=REJECT` plus a Guest-DNS rule, `fw4 -q reload; ip netns exec fh-tv nslookup www.google.com 8.8.8.8; ip netns exec fh-laptop nslookup -port=553 www.google.com 192.168.50.1`, then `uci revert firewall`, build 3a5035d5ad9fa398, 2026-11-21

Decision: the package ships that include and the install reloads fw4.

The removal's cut of a committed section was run on the VM's busybox against a
copy of `/etc/config/dhcp` with a `netryx_safe` section and a section after it:
the section went, the next stayed, no temporary file was left, the mode stayed
0644.

@measured the removal's `if grep ... fi` block from `mkapk.sh`, paths moved to `/root/i3cfg/`, on netryx-lab 2026-11-21
