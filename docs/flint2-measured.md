# The Flint 2 stage: what the real routers showed

Lab: three routers on OpenWrt 25.12.6, reached through forge.

- **flint2**: GL-MT6000 (mediatek/filogic, kernel 6.12.112, 1 GB RAM), the
  router netryx runs on. WAN `eth1` 10.40.11.24 (DHCP), house `br-lan`
  192.168.124.1/24 (DHCP pool .100 to .249), management `lan2` 10.40.90.24
  (forge is 10.40.90.1, `ec:1a:c3:11:4d:93`). Hermes (test bot), openwrt-mcp
  and rollcall run there too.
- **beryl1**, **beryl2**: GL-MT3000 (512 MB), Wi-Fi clients of the Flint 2 for
  the run. Station `nxsta0` on their 5 GHz radio: beryl1 `94:83:c4:3e:12:5b`
  (192.168.124.166), beryl2 `94:83:c4:3e:12:d3` (192.168.124.136). Their own
  management (`eth0.23`, `eth0.24`) and default route stay on wire.
- Wi-Fi of the run (`scripts/lab/flint2-wifi.sh`): one hidden WPA3-SAE SSID
  `nx-lab` on the Flint 2's radio1 (channel 36, HE80, 3 dBm), bridged into
  `lan`, `macfilter allow` with exactly the two station addresses, client
  isolation off. Each station is a network of its own (`nxsta`, DHCP) whose
  routes go only into table 201; `ip rule` sends to table 201 what leaves by
  `nxsta0` or comes from 192.168.124.0/24. @decided 2026-11-21: the radios of
  the three routers are on for this stage only, hidden, allowlisted, at 3 dBm,
  and off at the end.

Every change on a router was made under the rollback of `scripts/lab/flint2-arm.sh`,
armed before and disarmed after a login check. Temperatures are the
`cpu-thermal` zone; the lowest trip point on all three is 125 C.

## 1. Install, the Wi-Fi clients, the quarantine baseline

### The package

`package/netryx/build.sh aarch64_cortex-a53` on forge, then
`scripts/lab/forge-mkapk.sh` (the `apk mkpkg` of `package/netryx/mkapk.sh`, in
an incus Alpine edge container, apk-tools 3.0.9): `netryx-0.1.0-r1.apk`,
16 712 295 bytes, around a 10 420 384-byte binary; the build is reproducible
(the helper run twice gave the same sha256). The package carries the address
table, the app map, the brand meta and the IEEE vendor list (`oui.txt`,
40 538 registrations, 6 658 506 bytes), with NOTICE.
`scripts/package-sane.sh` and `scripts/elf-arch.sh` green on it.

@measured `scripts/forge.sh sh package/netryx/build.sh aarch64_cortex-a53; scripts/forge.sh sh scripts/lab/forge-mkapk.sh aarch64_cortex-a53; sh scripts/package-sane.sh build/aarch64_cortex-a53/tree build/aarch64_cortex-a53/netryx-0.1.0-r1.apk; sh scripts/elf-arch.sh build/aarch64_cortex-a53/tree/usr/sbin/netryx arm64` on forge, 2026-11-21

The first build left every mode to forge's umask (002): the binary went onto
the router 0775 and the directories netryx creates
(`/usr/share/netryx`, `/usr/share/nftables.d/chain-pre/input`) 0775. build.sh
now sets them (directories and executables 0755, other files 0644) and
`package-sane.sh` fails a tree with anything group- or world-writable. The
rebuilt package, `netryx-0.1.0-r2.apk` (16 712 288 bytes), upgraded the
running one in place in 2 s (`(1/1) Upgrading netryx (0.1.0-r1 -> 0.1.0-r2)`,
the service restarted by post-upgrade, the config and its alerts section kept,
the baseline file unchanged); `/usr/sbin/netryx` is now `-rwxr-xr-x`. apk did
not change the modes of directories that already existed, so the three
directories the first install created were set to 0755 by hand.

@measured `apk add --allow-untrusted /tmp/netryx-0.1.0-r2.apk; ls -l /usr/sbin/netryx; ls -ld /etc /usr /usr/sbin /usr/share/netryx`, flint2, 2026-11-21

The vendor list was not in any package before this stage: the config named
`/usr/share/netryx/oui.txt` and nothing shipped it. `package-sane.sh` now
fails a tree without it (red on the tree built before the fix), and the
console names the maker on the router (forge: "Ugreen Group Limited", the
Beryls: "GL Technologies (Hong Kong) Limited").

### The install

`/etc/config/netryx` was written first (the shipped config plus
`config alerts 'alerts'` / `option channel 'console'`), then
`apk add --allow-untrusted netryx-0.1.0-r1.apk`:

| | |
|---|---|
| time of `apk add` | 4 s |
| pulled from the feed | `kmod-nf-conntrack-netlink`, `kmod-nfnetlink-log`, `kmod-nft-bridge` (6.12.112-r1) |
| the config written before | kept; the shipped copy went to `netryx.apk-new` (removed) |
| started by the install | no (post-install enables only); started by hand after the check |
| start to listening on 127.0.0.1:8788 | 13 s (data files loaded) |
| tables after the start | `netdev netryx`, `inet netryx`, `inet netryx_rules`, `bridge netryx_iso` |
| RSS at 13 s / 6 min 17 s | 74.6 MB / 101.8 MB (VmHWM 101.8 MB), 12 threads |
| MemAvailable before / at 6 min | 533 MB / 478 MB (Hermes 226.7 MB RSS beside it) |
| Telegram | `"problem":"alerts stay in the console, as configured","ready":false` |

@measured `apk add --allow-untrusted /tmp/netryx-0.1.0-r1.apk` timed with `date +%s`; `/etc/init.d/netryx start`; `grep VmRSS /proc/$(pidof netryx)/status`; `netryx ctl alerts`, flint2, 2026-11-21

The console's login: a dedicated rpcd login (ACL group `netryx` for read and
write) over an ssh tunnel to 127.0.0.1:8788: `/` 200, `/api/devices` 401
before the login, `POST /api/login` 200 with `canWrite: true` and the zone
`Europe/London`, then `/api/session`, `/api/alerts` and `/api/devices` 200.

@measured `ssh -L 18788:127.0.0.1:8788 flint2`; `curl http://127.0.0.1:18788/api/login` (body from a file on forge), then the three GETs with the cookie, 2026-11-21

### The Wi-Fi clients

`flint2-wifi.sh up` (all three routers armed): both stations associated within
seconds of the access point coming up, signal -41 and -42 dBm at 3 dBm, both
got a lease from the Flint 2 (.166, .136). The Beryls' main routing table was
the same before and after (default via their WAN, management on `eth0.23` /
`eth0.24`); `ip route get 1.1.1.1` still leaves by `eth0`, and with
`oif nxsta0` by the station.

The Beryls carry no curl, and `uclient-fetch` cannot bind to an interface, so
`flint2-wifi.sh fetch` routes only the URL's IPv4 addresses through table 201
for the fetch:

| through the station | beryl1 | beryl2 |
|---|---|---|
| `https://openwrt.org/` (200 from the Mac) | 47 616 bytes, exit 0 | 47 616 bytes, exit 0 |
| `https://downloads.openwrt.org/nx-no-such-file` (404 from the Mac) | exit 8 | exit 8 |

The console lists both as Wi-Fi devices on `phy1-ap0`, SSID `nx-lab`, 5 GHz,
with their host names and the bytes of the fetches.

@measured `scripts/lab/flint2-wifi.sh up; scripts/lab/flint2-wifi.sh status; scripts/lab/flint2-wifi.sh fetch beryl1 https://openwrt.org/` (and beryl2, and the 404 URL); `netryx ctl devices`, 2026-11-21

### The quarantine baseline

`/etc/netryx/known-macs.json` was taken at the first start (21:54:50 UTC) with
forge's `lan2` address in it, and the two stations joined it when they first
appeared (21:58:20), inside the first day:

```
{"version":1,"created":1795298090,"macs":["94:83:c4:3e:12:5b","94:83:c4:3e:12:d3","ec:1a:c3:11:4d:93"]}
```

`netryx ctl alerts` says `"quarantineState":{"active":false,"graceEnds":1795384490,"problem":""}`:
the grace ends 2026-11-22 21:54:50 UTC, and no device is quarantined. The WAN
gateway (`eth1`) is not in the baseline.

@measured `cat /etc/netryx/known-macs.json; netryx ctl alerts; netryx ctl devices`, flint2, 2026-11-21

### Temperatures

| | flint2 | beryl1 | beryl2 |
|---|---|---|---|
| before | 40.0 C (slack 85.0) | 53.6 C (71.4) | 54.9 C (70.1) |
| after the install | 40.0 C | | |
| Wi-Fi up | 41.8 C | 55.4 C | 56.4 C |
| end of the section | 42.0 C (83.0) | 55.8 C (69.2) | 56.6 C (68.4) |

@measured `cat /sys/class/thermal/thermal_zone*/temp` on each router, 2026-11-21

## 2. Enforcement with flow offloading off, software and hardware

Each scenario ran at least once in each of three fw4 settings (the repeats
are recorded where they were run), switched under the
rollback (`scripts/lab/flint2-run.sh offload off|sw|hw`):

| mode | fw4 defaults | flowtable `ft` | conntrack flag on a running download |
|---|---|---|---|
| off | no `flow_offloading` (as the snapshot) | none | none |
| software | `flow_offloading 1` | devices `br-lan`, `eth1`, `lan2`, hook ingress | `[OFFLOAD]` |
| hardware | `flow_offloading 1`, `flow_offloading_hw 1` | devices `eth1`, `lan1`..`lan5`, `phy1-ap0`, `flags offload`, `counter` | `[HW_OFFLOAD]` |

netryx's own copy chain hooks the same member devices (`phy1-ap0` among them)
at priority `filter - 1`, one before the flowtable.

In hardware mode no flow of a Wi-Fi client is bound in the PPE: `mt7915e`
runs with `wed_enable=N` (OpenWrt's default), and `/sys/kernel/debug/ppe0` and
`ppe1` list only unbound entries of the router's own connections while
conntrack shows `[HW_OFFLOAD]` on beryl1's download. The hardware column is
therefore the hardware flowtable setting with Wi-Fi flows forwarded by the CPU;
a flow the PPE carries (a wired client, or Wi-Fi with WED on) is not in it.

@measured `cat /sys/module/mt7915e/parameters/wed_enable; cat /sys/kernel/debug/ppe0/entries /sys/kernel/debug/ppe1/entries; grep 78.46.170.2 /proc/net/nf_conntrack` on flint2 during a download, 2026-11-21

**Far ends.** forge cannot be one: the lab network gives a lab segment the
internet and nothing else, forge's own services included (homelab
`lab-net/README.md`), and a test server on 10.40.11.1 was refused from the
Flint 2 and from beryl1's station alike. So the far ends are on the internet,
and every download is read on the Beryl by a slow reader (`dd` of a few
blocks, then `sleep 1`), which holds the TCP window and keeps the volume small:

- `fsn1-speed.hetzner.com` (78.46.170.2, DE): the app `hetzner`, named by the
  handshake, an app with no organisation ranges; a 32 KB/s stream for the
  window, cut and allowance runs, about 1 MB/s for 30 s for the numbers.
- `mirror.selectel.ru` (31.184.217.1, in `cc_ru_v4` as
  31.184.208.0-31.184.230.255): a 32 KB/s download of a large file for the
  country rule, and its 2328-byte index for new connections. 2.56.24.1 is in
  the set too but answers nothing on port 80.

Rules, allowances and cuts went through the console's API with the session
and its CSRF token, as the page sends them. Times below are Unix seconds of
the Flint 2's clock (conntrack) or the Mac's (API answers), about 0.3 s apart.

@measured `scripts/lab/flint2-run.sh tunnel; scripts/lab/flint2-run.sh login`, then the commands under each table below, beryl1 and beryl2 on `nx-lab`, netryx 0.1.0-r2, 2026-11-21 22:22-23:20 London

### Country rule (RU) for beryl1

A 32 KB/s download from beryl1 to mirror.selectel.ru runs; the rule is
written with `POST /api/rules`; conntrack is read in a loop on the Flint 2 and
beryl1's station counters every few seconds.

| | off | software | hardware |
|---|---|---|---|
| the running download | entry `CLOSE` (reset) 0.3 s before the answer came back, gone 12 s later | kept `[OFFLOAD]` and moving 35 KB/s for 15.3 s, then gone (the engine's next 15 s tick) | entry gone 0.5 s before the answer came back |
| station bytes after the rule | +1.3 KB in 10 s | +560 KB in 15 s, then nothing | +1.8 KB in 26 s |
| a new connection from beryl1 | refused, 0 s | refused, 0 s | refused, 0 s |
| a new connection from beryl2 | 2328 bytes | 2328 bytes | 2328 bytes |
| rule counter / console `hits` | 8 packets / 7 | 2 / 2 | 2 / 2 |
| console | `applied: true`, `activeNow: true`, no problem | same | same |

@measured in each mode: `scripts/lab/flint2-run.sh fetch beryl1 http://mirror.selectel.ru/; scripts/lab/flint2-run.sh fetch beryl2 http://mirror.selectel.ru/; scripts/lab/flint2-run.sh bg beryl1 s1 http://mirror.selectel.ru/alpine/latest-stable/releases/x86_64/alpine-extended-3.24.4-x86_64.iso 32768; scripts/lab/flint2-run.sh watch 31.184.217.1 20 &; scripts/lab/flint2-run.sh api POST /api/rules '{"action":"block","what":"country","targets":["RU"],"who":"device","members":["94:83:c4:3e:12:5b"]}'; scripts/lab/flint2-run.sh rx beryl1` (every 2 to 5 s), `scripts/lab/flint2-run.sh bgstop beryl1 s1; scripts/lab/flint2-run.sh fetch beryl1 http://mirror.selectel.ru/; scripts/lab/flint2-run.sh fetch beryl2 http://mirror.selectel.ru/`; on flint2 `nft list counter inet netryx_rules hits_r_abaaf4fd` (off), `hits_r_022a843c` (software), `hits_r_3b3672e6` (hardware); `scripts/lab/flint2-run.sh api GET /api/rules`, 2026-11-21

The software run left the download moving for 15.3 s although the write
reloads the engine and cuts what the new state covers at once; it was ended by
the engine's next tick. A second software run later (rule `r_b27e7921`) ended
the `[OFFLOAD]` download at the write (entry gone 0.2 s before the answer, the
station flat after). So the cut at the write was missed once in two software
runs and in neither the off nor the hardware run; the cause is not known.

@measured the commands above in software mode, rules `r_022a843c` (written 1795301250) and `r_b27e7921` (written 1795302849), 2026-11-21

### Scheduled app rule at its window edge

A rule `what app, target hetzner, who device beryl1, from HH:MM to HH:MM`,
the edge two to three minutes ahead in the router's time (Europe/London, UTC
in November), written while a 32 KB/s hetzner stream of beryl1 runs.

| | off | software | hardware |
|---|---|---|---|
| edge (Unix s) | 1795300380 | 1795301460 | 1795302360 |
| the stream's entry | `CLOSE` at 379.79, gone at 393.99 | gone at 460.15 | gone at 360.38 |
| came back | no | no | no |
| station bytes in the 36-48 s after | +3.3 KB | +2.4 KB | +2.9 KB |
| the rule's cut set after the edge | `94:83:c4:3e:12:5b . 78.46.170.2 . tcp . 43080`, 9m08s left | `94:83:c4:3e:12:5b . 78.46.170.2 . tcp . 46922`, 9m13s left | `94:83:c4:3e:12:5b . 78.46.170.2 . tcp . 51382`, 9m12s left |
| `app_hetzner_v4` | 78.46.170.2 (from an earlier lookup through the router) | empty | 78.46.170.2 |
| `activeNow` | false before, true after | same | same |

The other check of the brief, a new connection by another app to the same
shared front, is held by the unit test `TestACutPairIsTheRebornFlowOnly` and
the VM measurement of stage 2 (section 4 of `docs/stage2-measured.md`): the
hetzner speed server is no shared front, and the Beryl has no curl.

@measured `scripts/lab/flint2-run.sh api POST /api/rules` with `{"action":"block","what":"app","targets":["hetzner"],"who":"device","members":["94:83:c4:3e:12:5b"],"from":"22:33","to":"23:33"}` (off, rule `r_5e1ad6d3`), the same with `22:51`/`23:51` (software, `r_7d5dc96c`) and `23:06`/`23:56` (hardware, `r_8fcc5580`); then `scripts/lab/flint2-run.sh bg beryl1 s2 https://fsn1-speed.hetzner.com/1GB.bin 32768; scripts/lab/flint2-run.sh watch 78.46.170.2 60 &; scripts/lab/flint2-run.sh rx beryl1` every 2 to 4 s; on flint2 `nft list set inet netryx_rules cut_r_5e1ad6d3_v4` (`cut_r_7d5dc96c_v4`, `cut_r_8fcc5580_v4`) and `nft list set inet netryx_rules app_hetzner_v4`, 2026-11-21

### The cut button on a live connection

`POST /api/devices/94:83:c4:3e:12:5b/conns/cut` with the local, remote and protocol the
live view lists, on a 32 KB/s hetzner stream; conntrack read in a loop.

| | off | software | hardware |
|---|---|---|---|
| answer | `{"cut":true}` | `{"cut":true}` | `{"cut":true}` |
| entry deleted | at once (under 0.3 s) | at once | at once |
| the connection | came back 0.2 s later on the same NAT port, `[ASSURED]`, and carried 1.05 MB in the next 30 s (run twice, came back both times) | run three times: ended once (reborn `[UNREPLIED]`, no reply), came back `[OFFLOAD]` on the same port twice | run three times: ended twice, came back `[HW_OFFLOAD]` on the same port once, 544 KB after |

Conntrack picks a cut connection up again on its next packet (tcp_loose), as
it did for a rule's cut before the cut sets (stage2-measured section 4); the
button has no cut set behind it. Whether it ends depends on which side sends
first after the delete: the far end's packet meets no NAT mapping and is
reset by the WAN zone, the device's packet is a new entry with the same port.

@measured `scripts/lab/flint2-run.sh bg beryl1 s3 https://fsn1-speed.hetzner.com/1GB.bin 32768; sleep 9; scripts/lab/flint2-run.sh rx beryl1; scripts/lab/flint2-run.sh cut 94:83:c4:3e:12:5b 78.46.170.2 15; scripts/lab/flint2-run.sh rx beryl1; sleep 15; scripts/lab/flint2-run.sh rx beryl1; scripts/lab/flint2-run.sh ct 78.46.170.2; scripts/lab/flint2-run.sh bgstop beryl1 s3` (the cut sends the tuple the live view lists, for example `{"local":"192.168.124.166:60774","remote":"78.46.170.2:443","proto":6}`; the device ports were 60774 and 40646 off, 55758, 44152 and 44080 in software, 38368, 52020 and 53050 in hardware), 2026-11-21

### An allowance of 5 minutes

`POST /api/allow {"rule":"r_5e1ad6d3","mac":"94:83:c4:3e:12:5b","minutes":5}` (software `r_7d5dc96c`, hardware `r_8fcc5580`) while the
rule is in force; a small hetzner fetch every 5 to 6 s around its end, the
name looked up through the router's resolver first, as a house device does.

| | off | software | hardware |
|---|---|---|---|
| before | refused, 0 s | the first fetch went through (the set was still empty; its lookup filled it), the second refused | refused, 0 s |
| beryl2 | 324 bytes | 324 bytes | 324 bytes |
| during | 324 bytes | 324 bytes | 324 bytes |
| last fetch through / first refused, seconds from `until` | -2 / +4 | -7 / -1 | -3 / +3 |
| console after | `allows: []` | `allows: []` | `allows: []` |

@measured `T=8 scripts/lab/flint2-run.sh fetch beryl1 https://fsn1-speed.hetzner.com/ 192.168.124.1; scripts/lab/flint2-run.sh fetch beryl2 https://fsn1-speed.hetzner.com/ 192.168.124.1; scripts/lab/flint2-run.sh api POST /api/allow '{"rule":"r_5e1ad6d3","mac":"94:83:c4:3e:12:5b","minutes":5}'` (the rule id of each mode), then `T=4 scripts/lab/flint2-run.sh fetch beryl1 https://fsn1-speed.hetzner.com/ 192.168.124.1` every 5 to 6 s from until-20 to until+40 and `scripts/lab/flint2-run.sh api GET /api/rules`, 2026-11-21

### The console's numbers

About 1 MB/s for 30 s from beryl1 (hetzner `1GB.bin`, read slowly, so far
less than 1 GB), the console polled every 2 to 3 s (`/api/devices/94:83:c4:3e:12:5b` and
its live view) beside beryl1's station counters. The station counts frames;
the console counts IP bytes, so the comparison is made both raw and with 14
bytes of Ethernet header taken off each frame.

| | off | software | hardware |
|---|---|---|---|
| station bytes (frames) | 24 740 473 (16 400) | 25 919 270 (17 204) | 29 005 899 (19 230) |
| console, `last24h.down` growth | 24 319 217 | 25 220 202 | 28 695 133 |
| error, raw / without headers | -1.70 % / -0.78 % | -2.70 % / -1.78 % | -1.07 % / -0.14 % |
| console final after the transfer ended | within 2 s | within 3 s | within 3 s |
| live rate while it ran | 0.10 to 1.49 MB/s | 0.09 to 1.43 MB/s | 0.23 to 1.89 MB/s |
| live rate at the first poll after | 0 | 0 | 0 |

The console counted 0.14 to 1.78 % fewer bytes than the station received
(headers taken off); the cause of that undercount was not established.

No 15-second step showed in hardware mode: the console grew at every poll,
which fits the flows being forwarded by the CPU (no PPE binding, above).

@measured in each mode `scripts/lab/flint2-run.sh sample beryl1 94:83:c4:3e:12:5b 70 &; scripts/lab/flint2-run.sh bg beryl1 s5 https://fsn1-speed.hetzner.com/1GB.bin 1048576; sleep 30; scripts/lab/flint2-run.sh bgstop beryl1 s5`, 2026-11-21

### Names under offload

The hetzner rule (an app with no organisation ranges) read through
`GET /api/rules`, whose `applied` and `problem` the page shows on the rule:

| netryx `mode` | off | software | hardware |
|---|---|---|---|
| `ingress` (the default) | `applied: true`, no problem | `applied: true`, no problem | `applied: true`, no problem |
| `forward`, netryx started in that offload state | `applied: true`, no problem | `applied: false`, `names cannot be seen: turn on capture and passive_dns` | same as software |
| `forward`, started with offload off, offload then turned on | | `applied: true`, no problem, at once and 40 s later | |

Two things the run showed: in forward mode the offload setting is read only
when netryx starts (a firewall reload turning offloading on leaves the rule
saying it works until netryx restarts), and the reason given names capture
and passive DNS, which were both on; the cause was forward mode under flow
offloading.

@measured on flint2 `uci set netryx.main.mode=forward; uci commit netryx; /etc/init.d/netryx restart`, then `scripts/lab/flint2-run.sh api GET /api/rules`; `scripts/lab/flint2-run.sh offload sw` with netryx still in forward mode and `scripts/lab/flint2-run.sh api GET /api/rules` at once and 40 s later; `/etc/init.d/netryx restart` and the read again; under `scripts/lab/flint2-run.sh offload hw` the same restart and read; each time back with `uci set netryx.main.mode=ingress; uci commit netryx; /etc/init.d/netryx restart`, 2026-11-21

### Survival

A country rule (RU) for beryl1 with offload off, a new connection to
mirror.selectel.ru tried from beryl1 (and beryl2 as the control) at each point:

| | beryl1 | beryl2 | kernel |
|---|---|---|---|
| rule in force | refused, 0 s | 2328 bytes | |
| 1 s after `kill -9` of netryx (no process) | refused, 0 s | 2328 bytes | all four netryx tables and `cc_ru_v4` in place |
| procd respawned it (listening 16 s after the kill) | refused, 0 s | | |
| `service netryx stop` (no process) | refused, 0 s | 2328 bytes | `inet netryx_rules` and `bridge netryx_iso` kept, the copy tables gone; rule counter 5 packets |
| `service netryx start` | refused, 0 s | | console `hits: 6`, `applied: true` |

No reboot in this section: the stage's Flint 2 reboot was moved to a later
section, where it checks the country rule and the quarantine together.

@measured `scripts/lab/flint2-run.sh api POST /api/rules '{"action":"block","what":"country","targets":["RU"],"who":"device","members":["94:83:c4:3e:12:5b"]}'` (rule `r_b27e7921`), then on flint2 `kill -9 $(pidof netryx)`, `service netryx stop`, `service netryx start`, with `T=6 scripts/lab/flint2-run.sh fetch beryl1 http://mirror.selectel.ru/; scripts/lab/flint2-run.sh fetch beryl2 http://mirror.selectel.ru/` and on flint2 `nft list tables; nft list counter inet netryx_rules hits_r_b27e7921` after each, 2026-11-21 23:14-23:16 London

### Temperatures

| | flint2 | beryl1 | beryl2 |
|---|---|---|---|
| before | 43.6 C (slack 81.3) | 57.1 C (67.8) | 57.6 C (67.3) |
| off, numbers run | 43.7 C | 56.8 C | 57.4 C |
| software, numbers run | 44.6 C | 57.1 C | 57.9 C |
| hardware, numbers run | 44.9 C | 57.6 C | 58.2 C |
| end | 45.3 C (79.6) | 57.9 C (67.0) | 58.0 C (66.9) |

At about 1 MB/s nothing heated: the Flint 2 moved 1.7 C over the hour.

@measured `scripts/lab/flint2-run.sh temps` before, during and after each run, 2026-11-21

## 3. Isolation on real Wi-Fi and the switch ports

beryl1 isolated through `PUT /api/devices/94:83:c4:3e:12:5b {"isolate":true}`
(session and CSRF token); its address went into `isolated` in both tables
at once. Each check from beryl1's station; "neighbour" is beryl2
(192.168.124.136) as beryl1's neighbour table shows it after two pings
(beryl2's firewall drops the pings, but its kernel answers ARP), the router's
SSH is dropbear's banner read with `nc`.

| from beryl1 | not isolated | isolated, `isolate '0'` on nx-lab | isolated, `isolate '1'` on nx-lab |
|---|---|---|---|
| beryl2 (neighbour) | `REACHABLE`, lladdr | `REACHABLE`, lladdr | `INCOMPLETE`, no lladdr |
| the router, ping | 2 of 2 | 0 of 2 | 0 of 2 |
| the router's page (`http://192.168.124.1/`) | 664 bytes | 0 bytes | 0 bytes |
| the router's SSH | banner `SSH-2.0-dropbear` (read from beryl2) | no banner | no banner |
| DNS from the router | 3 address lines | 3 | 3 |
| DHCP renew | lease obtained | lease obtained (23:23:38) | lease obtained (23:24:56) |
| the internet (`https://openwrt.org/`) | | 47 616 bytes, exit 0 | |
| the card's `isolation` | | `ssidIsolated: false`, "not isolated from its Wi-Fi neighbours on nx-lab: turn on client isolation for this network or move the device to the guest network" | `ssidIsolated: true`, "isolated from its Wi-Fi neighbours on nx-lab" |

With `isolate '0'` two stations of one SSID reach each other inside the
driver: frames never reach `br-lan`, so neither the inet nor the bridge table
sees them, and the console says so on the card. With `isolate '1'`
(`ap_isolate=1` in hostapd) they lose each other and the warning goes. The
counters moved: `hits_iso` in `inet netryx_rules` 6 packets after the probes,
in `bridge netryx_iso` 1 frame of 28 bytes (an ARP). NTP: the router serves none
(`system.ntp.enable_server 0`); `iso_in` lets udp 123 through (`udp dport
{ 67, 123, 547 } return`).

@measured `scripts/lab/flint2-run.sh api PUT /api/devices/<mac> '{"isolate":true}'`, then from beryl1 `ip neigh flush; ping -I <own> 192.168.124.136; ip neigh show`, `ping 192.168.124.1`, `wget http://192.168.124.1/`, `nc 192.168.124.1 22`, `nslookup openwrt.org 192.168.124.1`, `kill -USR1 <udhcpc>`, `scripts/lab/flint2-run.sh fetch beryl1 https://openwrt.org/`; `uci set wireless.nxlab_ap.isolate=1; wifi reload` and again, 2026-11-21 23:22-23:25 London

**DHCPv6 for an isolated device.** The Flint 2's `lan` already serves DHCPv6
and RAs (`dhcpv6 server`, `ra server`, `ra_flags managed-config other-config`),
so nothing was enabled. beryl1, isolated, ran `odhcp6c -N force -P 0` on its
station for 12 s: odhcpd listed a lease for its DUID
(`000300019483c43e125b`): the address `fd4a:7ebf:7cb6::a6b` and a delegated
prefix `fd4a:7ebf:7cb6:4::`. odhcp6c released both when it was stopped (no
lease and no route left).

@measured `start-stop-daemon -S -b -x /usr/sbin/odhcp6c -- -s /bin/true -N force -P 0 -v -e nxsta0` on beryl1 for 12 s; `ubus call dhcp ipv6leases` on flint2, 2026-11-21 23:25 London

**Switch ports.** `phys_switch_id` reads `00000000` and a newline
(`30 30 30 30 30 30 30 30 0a`) on `lan1` to `lan5` (the MT7531 switch), and
`read error: Not supported` on `eth1` (WAN) and on `phy1-ap0`. The console's
detection (`onHardwareSwitch`, `cmd/netryx/console.go`) calls a port a
hardware switch port when that file reads non-empty. Its test,
`TestAPortOnAHardwareSwitchIsToldApart`, feeds exactly these bytes
(`"00000000\n"`) for `lan2` and a port with no readable file for the others,
and passes: `lan2` is told apart as a switch port, a veth, a missing port and
`../etc` are not. `TestAWiredDeviceOnAHardwareSwitchSaysWhatIsNotCovered`
(the card's text for such a port) passes too. No wired client was on a
`br-lan` port during the run; forge on `lan2` is the ssh path and `lan2` is
no bridge port, so it was left unisolated.

@measured `cat /sys/class/net/<port>/phys_switch_id; hexdump -C /sys/class/net/lan1/phys_switch_id` on flint2, 2026-11-21; `FORGE_DIR=netryx-t456 scripts/forge.sh go test ./cmd/netryx -run TestAPortOnAHardwareSwitchIsToldApart -v -count=1` and `... ./internal/rules -run TestAWiredDeviceOnAHardwareSwitchSaysWhatIsNotCovered`, PASS, 2026-11-22

## 4. Quarantine on real Wi-Fi, and the reboot

**Grace ended for the run.** netryx stopped, `known-macs.json` written with
`created` two days back and the same three addresses (forge's `lan2`, both
stations), netryx started: `quarantineState` `{"active":true,"graceEnds":null}`,
and both tables carry `iifname "br-lan" ether saddr != @approved`.

**A newcomer on real Wi-Fi.** beryl2's station rejoined with the random
address `86:e8:e1:64:b2:85` (added to the access point's allowlist):

| beryl2 as `86:e8:e1:64:b2:85` | |
|---|---|
| beryl1 (neighbour on the same SSID) | `REACHABLE`: frames between stations stay in the driver, as for isolation |
| the router: ping / page / SSH | 0 of 2 / 0 bytes / no banner |
| DNS, DHCP renew | 3 address lines, lease obtained |
| the internet | `https://openwrt.org/` 47 616 bytes; mirror.selectel.ru 2328 bytes |
| alert | one `new_device` at 23:26:54, 15 s after the join |
| console | in the built-in group New, `quarantined: true`, `likelySame: 94:83:c4:3e:12:d3` (its own host name), the card's text says it is not isolated from its Wi-Fi neighbours on nx-lab |
| counters | `hits_iso` 30 packets (inet), 49 frames (bridge) |

@measured `printf '{"version":1,"created":<now-172800>,...}' > /etc/netryx/known-macs.json` with netryx stopped; `uci set wireless.nxlab_sta.macaddr=86:e8:e1:64:b2:85` on beryl2; the probes of section 3 from beryl2; `scripts/lab/flint2-run.sh api GET /api/devices/<mac>`, `GET /api/groups`, `netryx ctl alerts`, 2026-11-21 23:26-23:28 London

**The reboot.** A country rule (RU) for beryl1 was written through the API
(beryl1 refused, beryl2 2328 bytes), the held newcomer left as it was,
`flint2-arm.sh boot-guard 900` armed, then `reboot` at 23:28:19:

| | |
|---|---|
| ssh back | 41 s after the command |
| uptime 28 s | netryx starting (pid 4536, not yet listening); `inet netryx_rules` and `bridge netryx_iso` loaded from the boot copy, `approved` with the three addresses |
| 23:29:09, uptime about 29 s | beryl1 to mirror.selectel.ru refused, 0 s |
| 23:29:11 | beryl2 2328 bytes; netryx listening (its log line, same second) |
| 23:29:12 to 23:29:30 (uptime 50.6 s at the end) | the newcomer (lease 192.168.124.188 after the boot): the router's ping, page and SSH lost, DNS answered |
| login check | boot guard disarmed; the rule `applied: true`, `hits: 1` |

So the RU rule held from the boot copy 2 s before netryx was listening, and
the newcomer was held after the boot, probed once netryx was up.

@measured `scripts/lab/flint2-arm.sh boot-guard 900; ssh flint2 reboot`; at once after ssh returned `nft list tables`, `scripts/lab/flint2-run.sh fetch beryl1|beryl2 http://mirror.selectel.ru/`, the probes of section 3 from beryl2, `logread | grep netryx`, 2026-11-21 23:28-23:30 London

**Approval.** `PUT /api/devices/86:e8:e1:64:b2:85 {"approved":true}`: the
address joined `approved` at once, and beryl2 reached the router (page 664
bytes, ping 2 of 2). The address left `held-macs.json` at the next look.

**The real baseline back.** netryx stopped, the file written before the run
put back byte for byte (`created` 1795298090, three addresses, grace to
2026-11-22 21:54:50 UTC), the two test addresses written into
`held-macs.json` so a lease left behind cannot bring them into the baseline
during the grace, netryx started: `quarantineState`
`{"active":false,"graceEnds":1795384490}`, `isolated` holds exactly the two
test addresses (held by name through the grace, as designed).

@measured `cp /root/nx-known-macs.real.json /etc/netryx/known-macs.json` with netryx stopped; `netryx ctl alerts; nft list set inet netryx_rules isolated`, 2026-11-21 23:34 London

## 5. Alerts through the real test bot

@decided 2026-11-21: up to three messages from the lab's test bot to the
owner's chat for this run, no presses.

Before the switch every alert in the store was marked `sent: console` (the
console channel marks what it shows), so a switch to Telegram sends nothing
old. `PUT /api/alerts` with `channel telegram`, then 35 s:

- nothing was sent, and `telegram` said
  `"problem":"Hermes is not installed: alerts stay in the console","ready":false`.
  Hermes is installed (`hermes-agent-0.21.5-r25`, `hermes-agent-telegram-0.21.5-r2`),
  but it keeps its settings in `/etc/config/hermes` (`config platform
  'telegram'`: `enabled`, `token_file`, `allow_all`, `allow_user_id`), and
  netryx reads `/etc/config/hermes-agent`, the name in the hermes-openwrt
  source of stage 2c. On this router netryx cannot reach the bot at all, and
  the reason it gives is wrong.
- To measure the send itself, `/etc/config/hermes-agent` was made a symlink
  to `/etc/config/hermes` for the run (the token stays in Hermes's file, 0600
  root, one owner id): `"ready":true,"problem":""`, still nothing old sent.

| alert | raised | sent |
|---|---|---|
| `blocked_country`, beryl1, DE (a rule for DE, one try to 78.46.170.2) | 23:32:07 | `telegram`, within 6 s |
| `new_device`, `96:36:8f:2d:80:bf` (beryl2 on a second random address) | 23:32:57 | `telegram`, within 15 s |

Two messages in all. The channel went back to `console` and the symlink was
removed at once; `telegram` says `alerts stay in the console, as configured`.
A country try for beryl1 and RU was not raised again: it had been told that
local day (section 2), and the store kept the key through the reboot.

@measured `scripts/lab/flint2-run.sh api PUT /api/alerts '{"newDevice":true,"blockedCountry":true,"upload":true,"channel":"telegram"}'`; `ln -s /etc/config/hermes /etc/config/hermes-agent`; `T=4 scripts/lab/flint2-run.sh fetch beryl1 http://78.46.170.2/`; `uci set wireless.nxlab_sta.macaddr=96:36:8f:2d:80:bf` on beryl2; `scripts/lab/flint2-run.sh api GET /api/alerts` every 6 s; channel back to console, `rm /etc/config/hermes-agent`, 2026-11-21 23:30-23:34 London

### Temperatures of sections 3 to 5

| | flint2 | beryl1 | beryl2 |
|---|---|---|---|
| before | 45.4 C (slack 79.5) | 57.7 C (67.2) | 58.2 C (66.7) |
| before the reboot | 45.6 C | 58.0 C | 58.0 C |
| before the Wi-Fi went down | 45.6 C | 58.2 C | 58.3 C |
| end, radios off | 43.9 C (81.0) | 56.7 C (68.2) | 56.8 C (68.1) |

@measured `scripts/lab/flint2-run.sh temps`, 2026-11-21

### State left

`flint2-wifi.sh down` on all three: `wireless` (and the Beryls' `network`)
equal to the snapshot byte for byte, radio1 disabled, no `nxsta0` and no
access point, the Beryls' main routing table as before the run and no rule
for table 201. The RU rule of the reboot stayed in force through section 5
and was deleted with the DE rule after it (`DELETE /api/rules/r_9be31d11`,
`DELETE /api/rules/r_1c2fc103`, 23:34:10); the two device sections
(`d_9483c43e125b`, `d_86e8e164b285`) were deleted with `uci delete` while
netryx was stopped. netryx runs on the Flint 2 with `main` and `alerts`
(channel `console`) only: no rule, allowance or device section, no country or
app set in the kernel. Left from the run: `held-macs.json` with the two test
addresses (`86:e8:e1:64:b2:85`, `96:36:8f:2d:80:bf`), held by name in
`isolated` until the grace ends; `/root/nx-known-macs.real.json`, the copy of
the real baseline; four alerts in the store (two `sent: console`, two
`sent: telegram`). The rollback is disarmed on all three.

## 6. Memory, and the items left from stage 1

Figures from `/proc/meminfo` (Slab, SUnreclaim, MemAvailable) and
`/proc/<pid>/status` (VmRSS, VmHWM, RssAnon). The kernel has no
`/proc/slabinfo` and no per-cache SLUB counters, so set memory is read as the
SUnreclaim delta, after `echo 2 > /proc/sys/vm/drop_caches`. netryx's
`/metrics` carries no Go runtime figures (`go_memstats_*`), so the Go heap was
not read directly: RssAnon (anonymous resident memory, which is the Go heap,
stacks and runtime) stands in for it.

Units: `/proc` counts in kB of 1024 bytes; every "MB" in this section is
that figure divided by 1000 (so 1 MB here is 1 024 000 bytes, and 160 MiB is
163.8 MB). Bytes per element are counted with 1 kB = 1024 bytes.

### Country sets in the kernel

One rule per row, for one device (`94:83:c4:3e:12:5b`, absent at the time),
written with `POST /api/rules`, measured 10 s after the answer, then deleted.
A rule takes at most 64 targets (`more than 64 targets in a rule`), so every
country (242 codes in the address table) took four rules.

| rule | sets | elements | SUnreclaim | per element | netryx RSS after |
|---|---|---|---|---|---|
| none | 0 | 0 | 37.0 MB | | 77.4 MB |
| US | 2 | 70 929 | +10.8 MB | 156 B | 119.7 MB |
| 10 countries (US CN DE GB RU JP FR NL BR IN) | 20 | 161 649 | +23.4 MB | 148 B | 171.8 MB |
| all 242, four rules | 484 | 296 384 | +43.9 MB | 152 B | 176.5 MB (VmHWM 176.0) |

Every delete brought SUnreclaim back to within 0.7 MB of the start. The four
all-country writes answered 9 to 25 s each (the later ones longer), the
ruleset was then 7 690 706 bytes of text, and MemAvailable went from 532 MB
to 371 MB. Listing those 484 sets with `nft list sets` took the `nft`
process itself to 175 MB RSS and MemAvailable to 214 MB: on this router a
full listing of an all-country ruleset is a memory event of its own.

@measured `scripts/lab/flint2-run.sh api POST /api/rules '{"action":"block","what":"country","targets":[...],"who":"device","members":["94:83:c4:3e:12:5b"]}'`, `api DELETE /api/rules/<id>`, `sh scripts/lab/flint2-slab.sh` on flint2 after `drop_caches`, 2026-11-21 23:43-23:50 London

### netryx under a synthetic house load

`scripts/lab/flint2-load.sh` (the same commands by hand): the Flint 2's
busybox `ip` has no `netns` and its kernel no `veth`, so each test device is
a network and mount namespace with a TAP inside, its frames copied by a
small relay to a second TAP that is a port of `br-lan`. Two devices, with the
addresses of the released Beryl stations (already in the quarantine's
baseline), 6 loops each of HTTPS `HEAD` requests (4 s at most) to random
names of a list of 327 sites, through the router's resolver and NAT: about
13 requests a second (8 answered), 4 000 to 7 400 conntrack entries, about
140 KB/s down. The relay and its helpers held about 35 MB of RSS beside it.
Hermes stayed at 212.7 to 213.6 MB RSS throughout.

| | netryx RSS | RssAnon | VmHWM | MemAvailable | conntrack |
|---|---|---|---|---|---|
| fresh process, idle, 3 s after start | 52.2 MB | 44.5 MB | | 548 MB | |
| idle 15 min after a restart (before the country rules) | 77.4 MB | 68.5 MB | 77.4 MB | 552 MB | 66 |
| the same process after the country rules, then 9 min of load | 150.9 to 183.6 MB | 142 to 174 MB | 199.7 MB | 412 to 454 MB | 1 700 to 7 200 |
| restarted under the load, minutes 1 to 5 (30 s samples) | 109.7, 123.8, 133.0, 126.6, 133.2, 133.8, 133.4, 132.1, 134.0, 134.6 MB | 125.5 MB at the end | 134.9 MB | 459 to 489 MB | 3 900 to 4 500 |
| that process, all countries written under the load | 165, 174, 195, 213 MB after each of the four writes | 204 MB | **241.9 MB** | 309 MB | 5 600 to 7 300 |
| while the four rules were deleted | 222.2 MB | 213.1 MB | 241.9 MB | **186 MB** | 7 400 |
| 1 min after, load stopped | 177.6 MB | | 241.9 MB | 438 MB | |

The restarted process plateaued at 133 to 135 MB within two minutes and held
there for five samples; the load ended after the all-country check below,
17 minutes after it started. The first process
carried the heap the country rules had grown and gave it back only slowly
(161 MB idle when the load started). The writes of a big rule set are the
peak, not the traffic: the heap grows by up to 100 MB while netryx builds
and applies them, and an `nft` process runs beside it.

@measured `sh scripts/lab/flint2-sample.sh` every minute (and every 30 s after the restart) on flint2 under `scripts/lab/flint2-load.sh up`; `/etc/init.d/netryx restart` at 23:59:12; the four all-country writes at 00:04:30-00:05:24 and their deletes at 00:06:44-00:06:59, 2026-11-21/22 London

### The budget and GOMEMLIMIT

**`GOMEMLIMIT=160MiB` (163.8 MB) in the init script's environment, `GOGC`
left at 100, for a 1 GB router.** It was adopted as the default (init script
option `memory_limit '160MiB'`, dd3b5ff) after a run with the limit set,
through the all-countries apply under load, measured in section 8.

What it rests on, and how far that reaches:

- The plateau measured here is of a process 3 to 6 minutes old, with two
  test devices: 125.5 MB RssAnon (134.6 MB RSS, 9 MB of it the binary's file
  pages), 38 MB under the candidate. A house of more devices over days was
  not measured on the router.
- The forge heavy profile (30 busy devices, every ring full) measured 100.9 MB
  of Go heap only; RssAnon adds stacks and the runtime to that, so it is not
  a like-for-like figure beside the plateau above.
- The peak is the apply of a big rule set: 241.9 MB VmHWM, beside an `nft`
  process and the sets' own 44 MB in the kernel, with MemAvailable at 186 MB
  (533 MB before netryx was installed). A soft limit makes the collector run
  harder during such an apply instead of letting the heap grow; how far it
  holds the peak, and what it costs an apply that already takes 9 to 25 s a
  write, is what the run with the limit set has to show. If the live heap
  passed the limit the collector would run continuously (Go caps it at about
  half the CPU) rather than the process being killed. A 512 MB router needs
  its own figure.

### The items left from stage 1

| | |
|---|---|
| `GET /api/devices` under the load | 55 to 100 ms through the tunnel, 13 to 35 ms for the cheapest call (`/api/rules`) beside it: about 40 to 65 ms in the service for 4 devices |
| text search `GET /api/flows?q=...&limit=200` under the load | `q=google` 65 to 79 ms (76 803 bytes), no match 60 to 85 ms (the whole journal read), `q=e` 26 to 47 ms (stops at 200 rows) |
| `Host: flint2` and `Host: flint2.lan` (the router's host name) | **421** `misdirected request` |
| `Host: 192.168.124.1` | passes the check (401 for the session cookie, scoped to 127.0.0.1) |
| `Host: evil.example`, `Host: 10.40.11.24` (the WAN address) | 421 |
| `oui.txt` | present, the console names the makers ("GL Technologies (Hong Kong) Limited", "Ugreen Group Limited") |
| PPPoE gateway probe | not applicable: the WAN is DHCP (`eth1`, 10.40.11.24) |
| `255.255.255.255` as a service row (seen in section 1) | not among the top clients of the last 24 h during this run (`router` is the only service row) |

The 421 for the host name is the console's DNS-rebinding defence as the
invariant states it ("Only the router's own names reach the console": a name
passes only when it is the listen address, `hostAllowed` in
`cmd/netryx/console.go`). The plan of this stage expected the router's host
name to answer; that expectation differed from the invariant, and the router
behaved as the invariant says.

2026-11-22: the console now admits the router's own host name and that name in
its local domain (`flint2`, `flint2.lan`); the 421 above was measured before that change.

@measured `curl -b <jar> -w '%{http_code} %{time_total}' http://127.0.0.1:18788/api/devices`, `.../api/flows?q=...`, the same with `-H 'Host: ...'` through `scripts/lab/flint2-run.sh tunnel`, during the load, 2026-11-21 23:56-23:59 London

### Temperatures of sections 6 and 7

flint2 `cpu-thermal`: 43.0 C before (slack 82.0), 43.0 to 44.7 C during the
load (44.7 C at the all-country writes, slack 80.3), 42.7 C at the end
(slack 82.3).

@measured the `temp` field of `scripts/lab/flint2-sample.sh`; `/sys/class/thermal/thermal_zone0/temp`, 2026-11-21/22

## 7. The agent path through openwrt-mcp

openwrt-mcp 0.5.0.11 on the Flint 2 listens on `127.0.0.1:8730/mcp`. A lab
client `nxlab` was paired for the run (its token kept in a 0600 file on the
router, never shown) and granted as `docs/agent.md` says:
`openwrt-mcp allow nxlab exec netryx 1d` and
`openwrt-mcp allow nxlab uci_apply,uci_confirm 'netryx.agent_*' 1d`; the
config reloaded by itself (no restart). The calls went from the router itself
through `scripts/lab/flint2-mcpcall.py` (MCP over HTTP, initialize then one
`tools/call`; a confirm token is kept in a file and never printed). No model
was called and nothing went to Telegram.

**Reads.** The agent's read path is `netryx ctl`, over netryx's root-only
socket (`docs/agent.md`); netryx has no ubus object (`ubus -v list netryx`:
`Not found`). Through openwrt-mcp that is `exec netryx ctl ...`, which needs
the `argv` forms of `docs/agent.md` in the exec policy, a change on Hermes's
side of openwrt-mcp that is the owner's to make. The reads were made with
`netryx ctl rules` as root on the router, as the check of each step.

**An apply that is confirmed.** `uci_apply` (timeout 180 s) of `agent_t8a`:
`block`, `what all`, `who device`, `member` the second test device
(`94:83:c4:3e:12:d3`, a namespace of the load of section 6).

| | |
|---|---|
| answer | `Applied 5 change(s) to netryx and reloaded.` `ROLLBACK ARMED` (3m0s), netryx `SIGHUP: reading the rules again` 4 s later |
| `netryx ctl rules`, 6 s after | `agent_t8a` `applied: true`, `activeNow: true`, `hits: 582` |
| the blocked device, 15 s | none succeeded (its requests ended with `uclient-fetch` exit 4) |
| the other device, same 15 s | 45 successful requests |
| `uci_confirm` | `Confirmed. Rollback cancelled; changes to netryx are permanent.` |
| removed by `uci_apply` `delete` + `uci_confirm` | `ctl rules` `[]`, the device back (71 requests in 15 s) |

**An apply that is not confirmed.** The same rule as `agent_t8b`, timeout
60 s, no confirm:

| | |
|---|---|
| 23:54:22 applied, 8 s after | `applied: true`, `hits: 1082`; the blocked device 0 requests in 12 s, the other 54 |
| a console write meanwhile (`POST /api/rules`, `PUT /api/devices/<mac>`) | both `409` `{"error":"the agent is changing the settings, try again in a minute"}`; netryx logged `the openwrt-mcp agent is waiting for a netryx change to be confirmed: the write is held` |
| 23:55:22 | openwrt-mcp `rolled back netryx (timeout)`; netryx `SIGHUP` 3 s later |
| after | `agent_t8b` absent from `/etc/config/netryx`, `ctl rules` `[]`, no `agent_t8b` line in `inet netryx_rules`; the device back (50 requests in 15 s) |
| a console write after the rollback | `PUT /api/alerts` `200` |

@measured `python3 -I flint2-mcpcall.py uci_apply|uci_confirm '<json>'` on flint2; `netryx ctl rules`; `scripts/lab/flint2-run.sh api POST /api/rules ...`, `api PUT /api/devices/94:83:c4:3e:12:d3 ...`; the load's per-request exit codes counted every 15 s, 2026-11-21 23:51-23:56 London

`uci_apply` ends with `ubus call uci reload_config`. The first apply of the
run also reloaded hostapd and dnsmasq (their configs had been written back
by hand earlier, without a reload); dnsmasq's reload replaced
`/tmp/resolv.conf`, which the load's namespaces had bound over, so for
about a minute both test devices lost their resolver (the harness, not
netryx: `scripts/lab/flint2-fixdns.py` binds it again each second since). The second
and third applies reloaded only netryx.

The pairing, the two grants and the token file were removed at the end
(`openwrt-mcp unpair nxlab`, `/etc/config/openwrt-mcp` copied back from the
copy taken before; the token store holds the same two entries as before).

## 8. The fixed build on the router

The build with the fixes of D1 to D5 and the memory limit (`dd3b5ff`):
`netryx-0.1.0-r3.apk` (16 714 456 bytes, sha256 `621e3f3a...656622b`), built
as in section 1 with `PKGREL=3`; `package-sane.sh`, `elf-arch.sh` and
`gates-have-teeth.sh` green on forge. It upgraded the running r2 in place
under the rollback in 4 s (`(1/1) Upgrading netryx (0.1.0-r2 -> 0.1.0-r3)`,
restarted by post-upgrade, listening 19 s later), the config and its
`alerts` section (channel `console`) kept, the shipped copy
(`netryx.apk-new`) removed. The router's config has no `memory_limit`, so
the init's default applies: the service's environment carries
`GOMEMLIMIT=160MiB`. The console login answered (`canWrite: true`), and the
rollback was disarmed after it.

@measured `scripts/forge.sh sh package/netryx/build.sh aarch64_cortex-a53; PKGREL=3 sh scripts/lab/forge-mkapk.sh aarch64_cortex-a53; sh scripts/package-sane.sh build/aarch64_cortex-a53/tree build/aarch64_cortex-a53/netryx-0.1.0-r3.apk; sh scripts/elf-arch.sh build/aarch64_cortex-a53/tree/usr/sbin/netryx arm64; sh scripts/gates-have-teeth.sh` on forge; `scripts/lab/flint2-arm.sh arm 900; apk add --allow-untrusted /tmp/netryx-0.1.0-r3.apk`; `tr '\0' '\n' < /proc/$(pidof netryx)/environ | grep GOMEMLIMIT`, flint2, 2026-11-22 00:34-00:36 London

The device for D1 and the limit run is the namespace device `a` of
`scripts/lab/flint2-load.sh` (`94:83:c4:3e:12:5b`, 192.168.124.66), under the
load of section 6 (two devices, 6 loops each), flow offloading off; the
Beryls were not used.

### D1: the cut button holds the connection

A 32 KB/s HTTP download from the device to `fsn1-speed.hetzner.com`
(78.46.170.2:80), device port 52868, cut with the console's button:

| | |
|---|---|
| answer | `{"cut":true,"held":true}` |
| conntrack | the entry deleted at once; no entry with port 52868 in the 30 s after |
| `nft list set inet netryx_rules cut_v4` | `94:83:c4:3e:12:5b . 78.46.170.2 . tcp . 52868 expires 9m31s560ms` |
| the download | ended 6 s after the cut (`uclient-fetch` exit 4), nothing read after it |
| a new connection to the same host, port 42848 | answered: 1 000 000 bytes in under 2 s |

Before the fix the same press with offloading off brought the connection
back 0.2 s later on the same NAT port (section 2). The element expired after
its 10 minutes.

@measured `start-stop-daemon -S -b ... nsexec.py <pid of device a> /bin/sh d1.sh` (wget of `http://fsn1-speed.hetzner.com/1GB.bin` read 32 KB a second); `scripts/lab/flint2-run.sh cut 94:83:c4:3e:12:5b 78.46.170.2 30`; `nft list set inet netryx_rules cut_v4`; `nsexec.py <pid> sh -c 'wget -q -T 10 -O - http://fsn1-speed.hetzner.com/1GB.bin | head -c 1000000 | wc -c'`, flint2, 2026-11-22 00:36-00:37 London

### D5: the bot is found where Hermes keeps it

With the channel `console`, the console's alerts state says
`"problem":"alerts stay in the console, as configured","ready":false`: that
state names the channel first, so it reads the same whether the bot can be
reached or not, and the channel was not changed. The console's own
`alerts.ReadBot(alerts.HermesConfig, os.Stat)`, built from the same tree as
the lab probe `scripts/lab/readbot-probe` for arm64 (not shipped, removed
from the router after the run) and run there, answered `config /etc/config/hermes found=true reason=""
tokenfile=/etc/hermes-agent/telegram.token chat_set=true` (the token file is
only looked at, not read). `/etc/config/hermes-agent` does not exist (the
symlink of section 5 is gone). Nothing was sent: the store holds the same 33
alerts, 31 `sent: console` and the 2 `sent: telegram` of section 5.

@measured `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/nxbotcheck ./scripts/lab/readbot-probe` on forge, `/tmp/nxbotcheck` run once on flint2 and removed; `scripts/lab/flint2-run.sh api GET /api/alerts`; `netryx ctl alerts`, 2026-11-22 00:36-00:45 London

### D3: the reason a rule cannot see names

The router runs in `ingress` mode with capture and passive DNS on, so names
are seen there. The reason text is held by `TestTheBlindReasonNamesItsCause`
(six cases) and `TestAnAppRuleSaysItCannotSeeNamesWhenBlind`.

### The all-countries apply with GOMEMLIMIT=160MiB

The same check as section 6: the process 6 minutes old, 5 of them under the
load, then
the 242 country codes of the address table as four rules for device `a`
(64, 64, 64 and 50 codes), then the four deleted; `flint2-sample.sh` every
2 s. Units as in section 6 (kB / 1000).

| | without the limit (section 6) | with `GOMEMLIMIT=160MiB` |
|---|---|---|
| plateau under the load, before the writes | 133 to 135 MB RSS, 125.5 MB RssAnon | 135.5 to 136.3 MB RSS, 126.4 to 127.2 MB RssAnon |
| the four writes, answered in | 9 to 25 s each | 9.3, 11.6, 13.2, 16.9 s |
| all four applied | | `applied: true` each, 484 sets, rules.nft 10 470 123 bytes |
| netryx VmHWM | **241.9 MB** | **158.0 MB** |
| netryx RssAnon, highest | 204 MB after the writes, 213.1 MB during the deletes | 148.9 MB (during the deletes) |
| MemAvailable, lowest | **186 MB** (during the deletes) | **187.7 MB** (during the third write) |
| SUnreclaim with the four rules | +43.9 MB | +44.1 MB (42.3 to 86.3 MB), back to 43.4 MB after the deletes |
| the four deletes, answered in | about 15 s in all | 14.1, 7.4, 4.6, 2.9 s |
| 1 min after | 177.6 MB RSS, load stopped | 151.8 MB RSS, 421 MB MemAvailable, load still running |

The limit took 83.9 MB off netryx's peak, and the peak stayed 5.8 MB under
the limit (163.8 MB), with no write slower than without it. The lowest
MemAvailable did not move: it falls during an apply, when an `nft` process
runs beside netryx and the kernel builds the new sets, which a limit on
netryx's heap does not touch. The load average went to 5.9 during the
deletes (12 request loops beside them); temperatures below.

@measured `sh /tmp/nxload/sample.sh once` every 30 s, then every 2 s (`while [ ! -e /tmp/nxload/faststop ]; do sh /tmp/nxload/sample.sh once >> /tmp/nxload/fastsamples; sleep 2; done`) on flint2 under `scripts/lab/flint2-load.sh up`; the four bodies (64, 64, 64 and 50 codes) made on forge by `zcat build/aarch64_cortex-a53/tree/usr/share/netryx/ip2asn-combined.tsv.gz | cut -f4 | sort -u | grep -x '[A-Z][A-Z]' | split -l 64 - cc.` (the four files copied to the Mac) and posted with `for f in cc.*; do scripts/lab/flint2-run.sh api POST /api/rules "{\"action\":\"block\",\"what\":\"country\",\"targets\":[$(sed 's/.*/"&"/' $f | paste -sd, -)],\"who\":\"device\",\"members\":[\"94:83:c4:3e:12:5b\"]}"; done`, `netryx ctl rules`, `api DELETE /api/rules/<id>` four times, 2026-11-22 00:38-00:44 London

### Temperatures of section 8

flint2 `cpu-thermal`: 41.5 C before the load (slack 83.5), 43.8 C at most
during the writes and deletes (slack 81.2), 42.4 C at the end (slack 82.6).

@measured `/sys/class/thermal/thermal_zone0/temp` and the `temp` field of `flint2-sample.sh`, 2026-11-22

### State at release

- netryx `0.1.0-r3` installed and running, `GOMEMLIMIT=160MiB` in its
  environment; `/etc/config/netryx` has `main`, `alerts` (channel `console`)
  and `quarantine` with `enabled '0'` (the console: "turned off, new devices
  reach the house at once"); no rule, allowance or device section, no country
  or app set in the kernel, `cut_v4` empty.
- `held-macs.json` is `[]`; `/root/nx-known-macs.real.json` removed (it was
  byte for byte the baseline in `/etc/netryx/known-macs.json`).
- The load is down: no `nxla`/`nxlb`, `br-lan` has lan1, lan3, lan4, lan5
  only, `/tmp/nxload` and the package in `/tmp` removed, no helper process
  left.
- The lab rpcd login `netryxlab` removed (rpcd reloaded) and its password
  file on forge deleted; the console answers `/` 200 and `/api/devices` 401.
- The rollback disarmed: no boot-guard line in `/etc/rc.local`, no sleeper.
  `/root/netryx-pre` (the snapshot before the stage) and
  `/root/netryx-rollback.sh` kept.
- Hermes, openwrt-mcp and rollcall untouched and running; flow offloading
  absent from the firewall config (off), as before the stage.

## 9. The read grant applied, 2026-11-22

@decided 2026-11-22 Hermes reads netryx through openwrt-mcp, and only reads
it. On the Flint 2 (hermes-agent 0.21.5-r26, openwrt-mcp 0.5.0.11, netryx
0.1.0-r3) the grant went in under the rollback:

- the snapshot of section 1 renamed to `/root/netryx-pre.2026-11-21` (it was
  taken before netryx was installed, so a rollback from it would have removed
  netryx), and a fresh one taken: firewall, wireless, network, dhcp, netryx;
- `flint2-arm.sh arm 900`, then `CLIENT=hermes-main flint2-mcp-reads.sh add`:
  the section `netryx_reads` for the client `hermes-main` (the name the
  package paired, `openwrt-mcp clients`; the script's old default `hermes`
  names no client on this router), `exec`, scope `netryx`, the six `argv`
  forms, `expires '2026-12-22T06:52:09Z'`; openwrt-mcp logged
  `reloaded config (11 policies)` and kept serving on `127.0.0.1:8730`;
- `flint2-arm.sh disarm`, then `keep`: the saved openwrt-mcp copy removed, so
  no later rollback reverts the grant. netryx, openwrt-mcp and the Hermes
  gateway running after; cpu-thermal 41.7 C.

@measured `scripts/lab/flint2-arm.sh snapshot|arm 900|disarm`, `CLIENT=hermes-main scripts/lab/flint2-mcp-reads.sh add|keep|check`, `logread -e openwrt-mcp`, Flint 2, 2026-11-22 06:52-06:58 London

A one-off `hermes chat
--provider claude` run from a root shell answered `HTTP 401: invalid
x-api-key`, since only the gateway's init hands the provider its key, and the
key file is not read from here; the gateway's own jobs below hold it.

@measured `HERMES_HOME=/srv/hermes hermes chat -Q --provider claude -q '<exec netryx ctl status, devices, serve>'` on flint2, 2026-11-22 06:53 London

### The agent's own call, and the grant withdrawn

Two one-off jobs of the running gateway (`hermes cron create 1m ... --repeat 1
--deliver local --provider claude --model claude-haiku-5-5`, the gateway holds
the provider key) asked the agent to run `netryx ctl status` and `devices`
through openwrt-mcp's `exec`, and `netryx serve -help`. Both answered that the
session has no `exec` tool on the openwrt server; the read job tried
`ubus_list netryx` instead (audit: `hermes-main` `ubus_list` `exit status 4`)
and nothing reached `exec`. The cause is the package, not the grant: the
hermes-agent package writes `exec` into `tools.exclude` of its
`mcp_servers.openwrt` entry in every profile (`MCP_HIDDEN`, its invariant 18:
the agent's client is never granted `exec`). The grant was then removed under
the rollback (`flint2-arm.sh arm 600`, `flint2-mcp-reads.sh remove`,
`disarm`): `openwrt-mcp policies` names no netryx policy, netryx and the
gateway running.

@measured `hermes cron runs`, `/srv/hermes/cron/output/{2ee1321fd341,d897f83305d5}/2026-11-22_06-59-2*.md`, `/etc/openwrt-mcp/audit.jsonl`, `grep -n MCP_HIDDEN /usr/libexec/hermes-set-toolsets`, `scripts/lab/flint2-mcp-reads.sh remove`, Flint 2, 2026-11-22 06:58-07:01 London

## 10. The read over ubus, 2026-11-22

@decided 2026-11-22 the agent reads netryx over a read-only ubus object. On
the Flint 2, under the rollback (`flint2-arm.sh snapshot`, `arm 1500`; the
snapshot of section 9 renamed `/root/netryx-pre.2026-11-22a`):

- `apk add --allow-untrusted netryx-0.1.0-r4.apk` (sha256 `e7f22594...a923b`,
  built on forge by `package/netryx/build.sh` and `scripts/lab/forge-mkapk.sh`):
  the post-upgrade's `rpcd reload` registered the object with no rpcd restart,
  `ubus -v list netryx` listing `status`, `devices`, `rules`, `alerts` and
  `apps`/`usage` with `{"mac":"String"}`. The first calls, a second after the
  upgrade, answered `{"error": "the service does not answer on
  /var/run/netryx.sock ..."}` while netryx restarted, and the right answers
  once its socket was back.
- Answers: `status` 7153 bytes, `devices` 2286 (3 devices keyed by MAC, as
  many as `netryx ctl devices` lists), `rules` 22 (`{"rules": {}}`), `alerts`
  6872; `apps` of the busiest device listed its apps, `usage` `[]`.
- Refusals: `status {"x":1}` answered `status takes no argument "x"`,
  `apps {"mac":"../rules"}` `apps needs mac, ...`, `serve` `Method not found`
  (rpcd, exit 253).
- `CLIENT=hermes-main flint2-mcp-reads.sh add`: `netryx_reads`, `ubus_call`,
  `netryx.*`, `expires '2026-12-22T08:23:37Z'`; after
  `/etc/init.d/hermes-agent restart` the section was still there (the init
  rewrote only its own eight `hermes_main_*` sections).
- `disarm`, `keep`; netryx, openwrt-mcp, rpcd and the gateway running,
  cpu-thermal 42.9 C.

@measured `ubus -v list netryx`, `ubus call netryx <method> [args]`, `netryx ctl devices | jsonfilter`, `uci show openwrt-mcp.netryx_reads` before and after `/etc/init.d/hermes-agent restart`, Flint 2, 2026-11-22 08:22-08:28 London

The agent's call from a scheduled job does not reach openwrt-mcp: two one-off
jobs (`hermes cron create 1m ... --repeat 1 --deliver local --provider claude`)
called `ubus_call netryx.devices`, `rules`, `status`, `serve` and `apps`, and
each was refused by the hermes-agent package itself, `a scheduled job cannot
change the router (ubus_call netryx.devices)`, with no line in openwrt-mcp's
audit. In a scheduled job its unlock plugin (`openwrt_unlock.py`, `_refuses`)
counts as reads only the ubus methods the init lists in
`HERMES_OPENWRT_READ_UBUS`, a fixed list without `netryx.*`; outside a
scheduled job it does not filter.

@measured `hermes cron runs`, `/srv/hermes/cron/output/{f07d5398628b,558287d14a3b}/2026-11-22_08-25-*.md`, `grep 2026-11-22T08:2 /etc/openwrt-mcp/audit.jsonl` (no line), `sed -n 735,800p /usr/lib/hermes-agent/site-packages/openwrt_unlock.py`, Flint 2, 2026-11-22 08:24-08:26 London

### The agent's read in its chat

In the test bot's chat, with the model set to `claude-haiku-5-5` on the
`claude` provider, the owner asked for the devices netryx sees through the
object `netryx`. openwrt-mcp's audit holds exactly one call for it,
`{"client":"hermes-main","tool":"ubus_call","scope":"netryx.devices","outcome":"OK","duration_ms":19}`
at 09:20:06, and no other call from 08:30 on; `netryx_reads` is the one
policy that grants `netryx.*` to that client. The agent answered with the
three devices `ubus call netryx devices` keys on the router
(94:83:c4:3e:12:5b, 94:83:c4:3e:12:d3 and ec:1a:c3:11:4d:93), each with its
name, address and whether it was online.

@measured `grep netryx /etc/openwrt-mcp/audit.jsonl`, `ubus call netryx devices | jsonfilter -e '@.devices.*.mac'`, the agent's answer in the test bot's chat as the owner read it, Flint 2, 2026-11-22 09:20 London

## 11. Memory after the packed history and the mapped tables (r5)

netryx 0.1.0-r5, built from main 2813ae0 (TAIPANBOX/netryx#20 packed hours
and days, #21 the address table and the vendor list as mapped packs), against
the r4 that ran before it. `memory_limit` left at its default, 160MiB. Root is
an overlay on f2fs. CPU is the process's utime + stime from `/proc/<pid>/stat`
over the stated interval, as a share of one core; memory from
`/proc/<pid>/status`.

| | CPU | VmRSS | RssAnon | RssFile |
|---|---|---|---|---|
| r4, idle, up a day | 0.2 % | 96.4 MB | 89.5 MB | 6.9 MB |
| r5, 20 s after the upgrade | | 35.5 MB | 11.2 MB | 24.3 MB |
| r5, idle, minutes 1 to 2 | 0.2 % | 44.2 MB | 19.9 MB | 24.3 MB |
| r5 under the synthetic house load (section 6), 30 s samples over 5 min, 2 700 to 4 600 conntrack entries | 4 to 5 % | 64.8 to 84.3 MB (VmHWM 84.3) | 40.4 to 59.9 MB | 24.4 MB |
| r4 under the same load, section 6, restarted under it | | 133 to 135 MB plateau | 125.5 MB | |

- The packs are mapped on this file system: RssAnon stayed at 11 MB at start;
  read into memory they would have added about 15 MB of it. RssFile is their
  pages, which the kernel may reclaim.
- The upgrade kept the existing config, whose `geo` and `oui` still named the
  text files the package no longer ships; netryx read the packs beside them and
  said so (`/usr/share/netryx/ip2asn-combined.tsv.gz is gone; reading
  /usr/share/netryx/ip2asn.pack, which the package ships in its place`).
- The way back was run: `apk add` of r4 brought the text files back and r4
  started on them (89.9 MB resident 15 s after start), and `apk add` of r5 again
  brought the packs back (53.4 MB, 29.1 MB anonymous, 15 s after start).
- Temperature 38.2 to 39.4 C throughout; Hermes was not restarted.

@measured on flint2 `apk add --allow-untrusted /tmp/netryx-0.1.0-r5.apk`,
`/etc/init.d/netryx restart`, `grep -E 'VmRSS|RssAnon|RssFile|VmHWM'
/proc/$(pidof netryx)/status`, the CPU from `cut -d' ' -f14,15
/proc/$(pidof netryx)/stat` 60 s and 30 s apart, `sh scripts/lab/flint2-load.sh
up|down` under `sh scripts/lab/flint2-arm.sh arm 1800`, then `apk add` of r4
and of r5, 2026-11-23 01:26-01:37 London

## 12. A day of flash writes (r6)

netryx 0.1.0-r6, pid 15911, the router up 2 days 12 hours without a reboot,
`memory_limit` 96MiB. The overlay is f2fs, mounted with `background_gc=on` and
`mode=adaptive`, 5 % full. The day is read from a start point taken at
2026-11-23 06:37:54 UTC to 2026-11-24 11:33 UTC, 28 h 55 min.

| | Written | A day |
|---|---|---|
| the whole f2fs overlay (`lifetime_write_kbytes` 2397568 to 3152968) | 755400 KB | about 627 MB |
| netryx's history (`/etc/netryx/history`, 289 to 412 KB) | 123 KB | about 100 KB |
| netryx's daily files (`/etc/netryx/daily`, 6 to 7 KB) | 1 KB | about 1 KB |

- The file system's own background garbage collection writes almost all of
  it. Over 10 minutes (11:33:37 to 11:43:37 UTC) 12508 KB were written, and
  `moved_blocks_background` rose by 2960 blocks, 11840 KB of them: 94.7 %. It
  runs in bursts; that window ran at about 1.8 GB a day. 1415 background GC
  calls since the mount.
- netryx's own share is about 100 KB a day, 0.02 % of the day: an hour file
  of about 2.4 KB each hour and one day file (20261123.gob, 42 KB) once a day,
  against the 3406 KB a day of the heavy profile of 30 busy devices
  (`scripts/memory-budget.sh`). `alerts.json` and `rules.nft` were each written
  once, on 2026-11-23 at 18:46 to 18:47 UTC. 38 files in `/overlay/upper`
  changed over the day.
- Memory: VmRSS 77560 KB, VmHWM 89056 KB, about 7 MiB under the 96MiB limit.
- r6 predates the interventions journal (TAIPANBOX/netryx#31), so the router
  has no `interventions.jsonl`; its writes are compared with the 10 KiB an
  append of `docs/journal-measured.md` once the Flint 2 runs main.

@measured on flint2 `cat /sys/fs/f2fs/*/lifetime_write_kbytes` against the
start points `/root/netryx-io-start.txt` and `/root/netryx-io-start2.txt`,
`moved_blocks_background` and `gc_background_calls` from the same directory
10 minutes apart, `du -sk /etc/netryx/history /etc/netryx/daily`,
`find /overlay/upper -mtime -1`, `grep -E 'VmRSS|VmHWM' /proc/15911/status`,
2026-11-24 11:33-11:43 London
