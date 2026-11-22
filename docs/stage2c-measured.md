# Stage 2c: what the lab VM showed

Lab: netryx-lab on forge (OpenWrt 25.12.6 x86-64, kernel 6.12.112), fakehouse
devices on `br-lan`: `fh-tv` 02:4e:58:00:00:01 (192.168.50.211), `fh-laptop`
…:02 (.212), `fh-phone` …:03 (.213), `fh-camera` …:04 (.214), `fh-console`
…:05 (.215); router 192.168.50.1, fd21:b397:60f5::1. Each section ends with
the decision it fixed for the stage's plan.

## 1. A bridge port's `isolated` flag does not isolate a device

| ports marked isolated | camera to laptop | camera to tv |
|---|---|---|
| none | answered | answered |
| camera | answered | answered |
| camera and laptop | lost | answered |

@measured `echo 1 > /sys/class/net/v-camera/brport/isolated; ip netns exec fh-camera ping -c 2 -W 1 192.168.50.212` (and the other rows), flags put back to 0, 2026-11-21

An isolated port reaches every port that is not isolated; only two isolated
ports lose each other. Marking the camera's port keeps it from nobody.

Decision for Task 3: no bridge port flags. Bridged isolation is done in
software by a `bridge` table; a wired device whose port is on a hardware
switch is shown as not isolated from its wired neighbours (Task 5).

## 2. The bridge family

`apk add kmod-nft-bridge` installed `kmod-nft-bridge-6.12.112-r1`, 20 KiB.
With `table bridge t { chain iso_fwd { type filter hook forward priority -5;
policy accept; ether saddr 02:4e:58:00:00:04 counter drop; ether daddr
02:4e:58:00:00:04 counter drop } }`:

- camera to laptop and laptop to camera: lost (10 and 2 frames counted);
- laptop to tv: answered;
- camera to the router (ping), DNS from the router, a DHCP lease
  (`udhcpc: lease of 192.168.50.214 obtained from 192.168.50.1`), and
  `https://openwrt.org/` (200): all worked. Frames to and from the router use
  the bridge's input and output hooks, not forward.

A chain named `fwd` is refused by nft (`fwd` is a keyword).

@measured `apk add kmod-nft-bridge; nft -f <the table>; ip netns exec fh-camera ping/nslookup/udhcpc/curl`, 2026-11-21

Decision for Task 3: the package depends on `kmod-nft-bridge`; the bridge
table drops frames from and to an isolated MAC in its forward hook only.

## 3. The router's input, and the quarantine form

`table inet t2` with a regular chain `iso_in` (`meta l4proto { tcp, udp } th
dport { 53, 553 } return; udp dport { 67, 123, 547 } return; meta l4proto
ipv6-icmp return; ct state established,related return; counter drop`) jumped
from an input chain at `priority filter - 5`:

- isolated camera: `http://192.168.50.1/` 000 (the laptop got 200), DNS from
  the router answered, a DHCP lease obtained, the internet 200;
- IPv6: after `ip link set eth0 down; up` the camera got its SLAAC address
  `fd21:b397:60f5:0:4e:58ff:fe00:4`, pinged `fd21:b397:60f5::1`, got AAAA
  answers from it, and `http://[fd21:b397:60f5::1]/` gave 000;
- the quarantine form, `meta iiftype ether iifname { "br-lan" } ether saddr
  != @approved jump iso_in` with the laptop and the tv approved: the camera was
  held exactly as above and the laptop was not.

The VM has no WireGuard interface, so a non-Ethernet packet against `meta
iiftype ether` was not exercised there; the compiler test pins the match.

@measured the two tables above loaded in turn, `ip netns exec fh-<dev> curl / nslookup / udhcpc / ping -6`, 2026-11-21

Decision for Task 3: those input lines, in that order, and the quarantine
match with `meta iiftype ether` and the house interfaces.

## 4. A try towards a blocked country reaches the nflog reader

`ether saddr 02:4e:58:00:00:02 ip daddr 5.8.0.1 limit rate 10/minute log
prefix "netryx-try r_x" group 9` before the reject: three tries from the
laptop gave three copies, each `prefix="netryx-try r_x" hook=2 hwaddr=02:4e:58:00:00:02 peer=5.8.0.1`,
read by a throwaway program over `internal/nflog` (not committed); the reject
counted 3.

@measured scratch table plus the throwaway reader on group 9 (netryx holds group 7), `ip netns exec fh-laptop curl http://5.8.0.1/` three times, 2026-11-21

Decision for Task 6: the try line is a separate rule before the country
rule's reject, on the capture's log group; the device is `HWAddr`.

## 5. Hermes's bot settings

From hermes-openwrt (`package/hermes-agent/files/hermes-agent.config` and
`.init`, commit 1aee237): `/etc/config/hermes-agent`, `config platform
'telegram'` with `option enabled` (default 0), `option token_file` (default
`/etc/hermes-agent/telegram.token`, created 0600 root:root, the package never
writes a token into it), `option home_channel` (the chat scheduled jobs and
notifications go to, optional) and `list allow_user_id`. The CA bundle
`/etc/ssl/certs/ca-certificates.crt` is in the VM.

Decision for Task 7: as the plan states; the chat is `home_channel`, else the
single `allow_user_id`.

Correction, 2026-11-21: the path above is the name of the source file, not
where it lands. The package installs it as `/etc/config/hermes` (build.sh
copies `hermes-agent.config` there, the init does `config_load hermes`);
netryx reads that path since then. On the Flint 2 (hermes-agent
0.21.5-r25) the old path found no bot ("Hermes is not installed"), and with
`/etc/config/hermes-agent` made a symlink to `/etc/config/hermes` the section
shapes above delivered two alerts to the owner's chat.

@measured `ln -s /etc/config/hermes /etc/config/hermes-agent` and the alerts
run of `docs/flint2-measured.md` section 5 (`scripts/lab/flint2-run.sh api PUT
/api/alerts` with channel telegram, two alerts raised, `GET /api/alerts`),
Flint 2, 2026-11-21

## 6. Wi-Fi and hardware switch facts

`/sys/class/net/v-camera/phys_switch_id` gives `read error: Not supported`
on a veth (it reads only on a hardware switch port).

@measured `cat /sys/class/net/v-camera/phys_switch_id`, 2026-11-21

The VM has no radio (`ubus call network.wireless status`: Not found), and
netifd's source could not be fetched from here. Client isolation is read from
`/etc/config/wireless` instead: each `wifi-iface` section's `ssid`,
`network` and `isolate`, matched by the SSID hostapd reports for the client.

Decision for Task 5: a Wi-Fi device counts as isolated from its SSID
neighbours only when every `wifi-iface` with its SSID has `isolate '1'`; the
Flint 2 stage checks it on hardware.

## 7. The stage's live run

On the final build, with a baseline dated two days back (so the grace was
over), fakehouse and two new namespaces in the VM:

- isolation of the camera: camera to laptop and laptop to camera lost, laptop
  to tv answered; the camera's `http://192.168.50.1/` 000 (the laptop got
  200), its DNS, a DHCP lease and `https://openwrt.org/` (200) worked;
- a new device `fh-new` (02:4e:58:00:00:aa): from its first packets no house
  (laptop lost, the router's page 000), DNS and the internet (200); within a
  tick it was a new_device alert; approved through the API (login, CSRF
  token; the same PUT without the token got 403), it then reached the laptop;
- a second one, `fh-new2` (hostname kettle), was listed in New with
  `quarantined: true`, held from its first packets, and its card showed the
  banner with Approve;
- a country rule (RU) for the laptop with the alert on: ten tries to an
  address in `cc_ru_v4`, the rule's counter 10, exactly one blocked_country
  alert naming the laptop and RU;
- `kill -9` of netryx and a stop of the service: the camera and the held
  device stayed cut off; after a reboot of the VM the camera was isolated
  again and the quarantine line was in force;
- Telegram: a lab stand-in on forge (its own CA appended to the VM's bundle,
  `api.telegram.org` sent to it through `/etc/hosts` and a DNAT, a dummy
  token 0600 root): three `POST /bot<token>/sendMessage` to chat 42, the
  country try first, each alert then marked sent; the token file at 0644 made
  the console say Telegram is off, with the reason; all of it removed after;
- screenshots of the overview, Security and the two device cards at 1440 and
  390 px, no horizontal scroll.

Three defects were found by this run and fixed with a test that failed
first: the quarantine took a WAN-side neighbour for a new device
(`TestTheQuarantineSeesOnlyTheHouse`), a MAC only the history held came back
at every tick (`TestTheHistoryFeedsTheBaselineAndNeverTheTicks`), and a
newcomer known only from a lease was dropped in the same look
(`TestANewcomerWithNoTimeIsStillListed`).

nft lists the quarantine match without `meta iiftype ether`: nft adds that
dependency for an `ether` match itself and does not print it.

@measured live run on netryx-lab, build 0e75c693a937a9f1, 2026-11-21 (commands in the stage 2c ledger)
