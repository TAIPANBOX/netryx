# Stage 2a: facts measured in the lab VM

Everything here was run on 2026-11-20 in the lab VM `netryx-lab` (OpenWrt
25.12.6 r33311, x86_64), reached with `ssh forge` and then
`ssh -i ~/.ssh/netryx_vm root@10.166.170.230`. Devices came from
`lab/fakehouse.sh` (already up: namespaces `fh-tv`, `fh-laptop`, `fh-phone`,
`fh-camera`, `fh-console`, MACs `02:4e:58:00:00:01..05`). No other router was
touched. The lab root password is `<lab password>` and lives only in the VM.

## 1. Which clock does the kernel use for `meta hour` and `meta day`?

Commands (the zone first, then a probe table with counted forward rules from
`fh-tv`, loaded with `TZ=UTC nft -f`, 25 pings from `fh-tv` over about 25 s,
then `nft list table`, then `nft delete table`):

```
uci set system.@system[0].zonename='Europe/London'
uci set system.@system[0].timezone='GMT0BST,M3.5.0/1,M10.5.0'
uci commit system; /etc/init.d/system reload
date -u; date            # Fri Nov 20 22:09:17 UTC 2026 / Fri Nov 20 22:09:17 GMT 2026
```

London is on GMT in November, so its offset today is 0 and a London run
cannot tell UTC from local time. The same probe was therefore repeated with a
zone that has a non-zero offset, as the closest equivalent:

```
uci set system.@system[0].zonename='Africa/Nairobi'
uci set system.@system[0].timezone='EAT-3'
uci commit system; /etc/init.d/system reload
date -u; date            # Fri Nov 20 22:09:42 UTC 2026 / Sat Nov 21 01:09:42 EAT 2026
```

The run order was: London (22:09:17 UTC), then EAT-3 (22:09:42 UTC), then a
London day-only repeat (22:10:26 UTC). The probe ruleset, exactly as loaded
(`/tmp/probe.nft`; the two windows and two weekdays were filled in from `date`
at run time; the EAT-3 run had `22:04`-`22:14`, `01:04`-`01:14`, `Friday`,
`Saturday`):

```
table inet stage2probe {
 chain probe { type filter hook forward priority -10; policy accept;
  iifname "br-lan" ether saddr 02:4e:58:00:00:01 meta hour "22:04"-"22:14" counter comment "utc_hour"
  iifname "br-lan" ether saddr 02:4e:58:00:00:01 meta hour "01:04"-"01:14" counter comment "shifted_hour"
  iifname "br-lan" ether saddr 02:4e:58:00:00:01 meta day "Friday" counter comment "utc_day"
  iifname "br-lan" ether saddr 02:4e:58:00:00:01 meta day "Saturday" counter comment "shifted_day"
  iifname "br-lan" ether saddr 02:4e:58:00:00:01 counter comment "all"
 }
}
```

(The chain is named `probe` because `fwd` is an nft keyword.) Traffic: 25
pings from `fh-tv` to 1.1.1.1 (and 25 to 192.168.50.1), then
`nft list table inet stage2probe`. With UTC at 22:09 on a Friday, the offset
zone reads Saturday 01:09.

EAT-3 run (offset +3 h), packets per rule:

| rule | packets |
| --- | --- |
| `utc_hour` (UTC window `22:04`-`22:14`) | 25 |
| `shifted_hour` (`01:04`-`01:14`) | 0 |
| `utc_day` (`Friday`, the UTC weekday) | 0 |
| `shifted_day` (`Saturday`, weekday of UTC plus offset) | 25 |
| `all` (no time match) | 25 |

(`nft list` prints the stored hours in the zone's local time, so under EAT the
UTC rule reads back as `01:04`-`01:14` and the shifted one as `04:04`-`04:14`.
Rules were identified by their `comment`, not by the printed window.)

The London run (offset 0) is dropped as evidence: both windows and both days
were identical there, so it could not tell the clocks apart, and every
rule including `all` showed 434 packets, more than the 25 pings sent, which I
cannot explain from my notes (fakehouse's own habits traffic from `fh-tv` is
the likely extra, not verified). The London evidence is the repeat after
restoring London (22:10:26 UTC, Friday, 5 pings): `meta day "Friday"` moved 5
of 5 and `meta day "Saturday"` 0. So the day follows the zone in force after
`system reload`, not a fixed UTC.

Findings:

- `meta hour` matched the UTC window and not the shifted one: with `nft` run
  under `TZ=UTC`, the hour is compared in UTC whatever zone the router is set to.
- `meta day` matched the weekday of the local time (UTC plus the zone's
  offset), not the UTC weekday. With the zone set to `Europe/London` it equals
  the UTC weekday only while London is on GMT.
- `/proc/sys/kernel/tz` does not exist on this kernel; the kernel's offset is
  visible only through behaviour (and `date`).
- A summer (BST) offset could not be measured, because the VM's date is in
  November.
- Inference, not measured: the kernel's offset (`sys_tz`) is set when
  `system reload` runs, so after a DST change it can be stale until the next
  reload. Only "the day follows the zone after `system reload`" was measured.

@measured `TZ=UTC nft -f /tmp/probe.nft` (probe table above, 25 pings from fh-tv, `nft list table inet stage2probe`) 2026-11-20: `meta hour` is matched in UTC when loaded under `TZ=UTC` (outcome A for hour); `meta day` is shifted by the kernel timezone offset (outcome B for day).

Decision for Task 4 (the rule compiler), ruled by the controller:

- Hours are compiled in UTC and loaded under `TZ=UTC`.
- Days are compiled in the kernel's frame, that is UTC minus
  `sys_tz.tz_minuteswest` (the kernel's `meta day` uses it). netryx reads that
  offset with the raw `gettimeofday` syscall, whose `struct timezone` returns
  `sys_tz`, and recompiles the rules when it changes.
- Every schedule interval is split at both UTC midnight and kernel midnight, so
  each piece has one hour window in UTC and one weekday in the kernel frame.

Reason: enforcement stays in the kernel while netryx is down, and stays exact
even if the kernel offset is stale.

## 2. rpcd sessions through uhttpd

Setup:

```
apk update && apk add luci          # 29 packages: luci, uhttpd, uhttpd-mod-ubus, rpcd (+mod-file, iwinfo, luci, ucode, rrdns)
passwd root                         # a lab-only password, shown here as <lab password>
/etc/init.d/rpcd restart; /etc/init.d/uhttpd enable; /etc/init.d/uhttpd restart
```

Relevant config as installed: `uhttpd.main.ubus_prefix='/ubus'`, listening on
`0.0.0.0:80`; `rpcd.@rpcd[0].timeout='30'`; `rpcd.@login[0]` has
`username='root'`, `password='$p$root'`, `read='*'`, `write='*'`.

Requests, all `POST http://127.0.0.1/ubus` (loopback, body JSON). The null
session id is `00000000000000000000000000000000`.

Login, request:

```
{"jsonrpc":"2.0","id":2,"method":"call","params":["00000000000000000000000000000000","session","login",{"username":"root","password":"<lab password>"}]}
```

Response (shortened; `acls` is about 7 KB of the LuCI groups):

```
{"jsonrpc":"2.0","id":2,"result":[0,{"ubus_rpc_session":"7410463e0fdcad9a8050daf02d2a2c3f","timeout":300,"expires":299,"acls":{...},"data":{"username":"root"}}]}
```

- Default session timeout: `"timeout":300` seconds (`expires` counts down from
  it). Rpcd's own `rpcd.@rpcd[0].timeout='30'` is not what login returns.
- `login` accepts an optional `"timeout"` number: `"timeout":3` returned
  `"timeout":3,"expires":2`.
- Wrong password: HTTP 200, `{"jsonrpc":"2.0","id":1,"result":[6]}`. The
  failure is the ubus status in `result[0]`: 6 is permission denied; there is
  no `error` object and no session.
- Missing `username`: `{"jsonrpc":"2.0","id":7,"result":[2]}` (2 is invalid argument).
- Unknown session id (`11111111111111111111111111111111`) and an expired
  session (login with `timeout` 3, then a call after 5 s): HTTP 200,
  `{"jsonrpc":"2.0","id":3,"error":{"code":-32002,"message":"Access denied"}}`.
  The same answer comes for any call with an expired session, for example
  `system board`, and for `system board` with the null session.
  An unknown and an expired session are indistinguishable.
- A good call returns `result:[0, {...}]`; `result[0]` is the ubus status.

`session access`, request:

```
{"jsonrpc":"2.0","id":3,"method":"call","params":["<sid>","session","access",{"scope":"netryx","object":"control","function":"write"}]}
```

Answer shape: `{"jsonrpc":"2.0","id":3,"result":[0,{"access":false}]}` or
`{"access":true}`. With `{}` as arguments it returns the session's whole ACL
tree instead (the `acls` object, minus `data`).

| step | `netryx`/`control`/`write` | `netryx`/`view`/`read` |
| --- | --- | --- |
| root session, before the ACL file | `{"access":false}` (the `*` grants do not cover a custom scope) | `{"access":false}` |
| unknown / expired session | error -32002 | error -32002 |
| ACL file installed, session opened before it, no reload | `{"access":false}` | not asked |
| ACL file installed, a fresh login, no rpcd reload | `{"access":true}` | not asked |
| after `/etc/init.d/rpcd reload`, the old session | `{"access":true}` | not asked |
| after reload, a fresh login | `{"access":true}` | `{"access":true}` |
| scope `netryx`, object `other`, function `write` | `{"access":false}` | |

The test ACL file `/usr/share/rpcd/acl.d/netryx.json`:

```
{ "netryx": { "description": "netryx console",
  "read":  { "netryx": [ "view" ] },
  "write": { "netryx": [ "control" ] } } }
```

A fresh login then lists `"netryx":{"control":["write"],"view":["read"]}` in
`acls`, and the access-group `"netryx":["read","write"]`. The ACL files are
read when a session is created: a new ACL file needs no reload for new
sessions, and `rpcd reload` is what makes an already-open session pick it up.

The ACL file was left in the VM (it is harmless); `luci` and `uhttpd` stay
installed and running for Task 11.

@measured `curl -s http://127.0.0.1/ubus -d '{"jsonrpc":"2.0","id":2,"method":"call","params":["00000000000000000000000000000000","session","login",{"username":"root","password":"<lab password>"}]}'`, then `curl -s http://127.0.0.1/ubus -d '{"jsonrpc":"2.0","id":3,"method":"call","params":["<sid>","session","access",{"scope":"netryx","object":"control","function":"write"}]}'` before and after installing `/usr/share/rpcd/acl.d/netryx.json` (and again after `/etc/init.d/rpcd reload`) 2026-11-20: login result `[0,{"ubus_rpc_session":...,"timeout":300,"expires":299,"acls":...,"data":{"username":"root"}}]`, bad password `result:[6]` with HTTP 200, unknown or expired session `error.code -32002 "Access denied"`, `session access` for scope netryx / object control / function write false before the ACL file and true after, with no reload needed for new sessions.

Decision for Task 7 (LuCI login in netryx): call `session login` and
`session access` over `http://127.0.0.1/ubus` with the shapes above; treat
`result[0] != 0` on login (6 for a bad password, 2 for a missing field) as
failure, and `error.code == -32002` on any later call as "session gone"
(expired and unknown cannot be told apart). Authorise with `session access`
scope `netryx`, object `control`, function `write` for changes and object
`view`, function `read` for reading. Do not cache an answer past the 300 s
the login states. The package must ship
`/usr/share/rpcd/acl.d/netryx.json` and run `/etc/init.d/rpcd reload` in
post-install and post-upgrade so already-open sessions get it.

## 3. The agent's reload path

The stage 1b init script already has `procd_add_reload_trigger "netryx"` (and
`"network"`) and no `reload_service`. Source reading, not measured: serve
handles only SIGINT and SIGTERM (`cmd/netryx/main.go`), so SIGHUP is not
handled; the pid change below is the measurement. Procedure:

1. Scratch build on forge (not committed): the same tree plus a goroutine that
   logs `SCRATCH: got SIGHUP` on `signal.Notify(hup, syscall.SIGHUP)`,
   `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/netryx`, copied with
   `scp -O`. The original binary, init script and `/etc/config/netryx` were
   saved first and restored afterwards (checked: same size and date, no
   `reload_service` in the init).
2. In the VM copy of the init script appended
   `reload_service() { procd_send_signal netryx '*' HUP; }`.
3. `uci set netryx.main.capture=1; uci commit netryx; ubus call uci reload_config '{}'`.

Log, with the same pid before and after (no restart):

```
22:12:02 netryx[4710]: listening to group 7 on br-lan (ingress)   (the log line, then in Ukrainian)
22:12:07 netryx[4710]: SCRATCH: got SIGHUP
```

More runs, same scratch build (counted from `logread` timestamps):

- a commit that changed a value (`fetch_icons` 1 to 0, then back): one SIGHUP
  each time, at 22:12:23 and 22:12:26, pid unchanged;
- a commit that rewrote the same value: no SIGHUP measured. Source reading,
  not measured: `/sbin/reload_config` compares an md5 of `uci show` per
  package and only emits `config.change` for a package that differs, and its
  very first call after a boot sees everything as changed;
- `ubus call uci reload_config` is what sends the event; `uci commit` alone
  does not.

Without a handler (the stage 1b binary under the same `reload_service`): the
process id changed from 5637 to 5963 across the reload runs, that is, the
default action of SIGHUP ended serve and procd respawned it. A default
`reload_service` (a restart) would also work but costs a restart on every
settings change; the SIGHUP path needs a handler.

@measured `uci set netryx.main.capture=1; uci commit netryx; ubus call uci reload_config '{}'` in netryx-lab with `reload_service() { procd_send_signal netryx '*' HUP; }` appended to /etc/init.d/netryx and a scratch binary logging SIGHUP, 2026-11-20: `logread` shows `netryx[4710]: SCRATCH: got SIGHUP` five seconds after start, pid unchanged.

Decision for Task 9 (reload): serve must `signal.Notify` SIGHUP and reread
its rules (and the settings it can) in place, never exit on it; the init
script gets `reload_service() { procd_send_signal netryx '*' HUP; }` so a
changed `/etc/config/netryx` reaches the running process without a restart;
keep `procd_add_reload_trigger "netryx"`. Note that with a `reload_service`
defined, the existing `procd_add_reload_trigger "network"` also sends HUP on a
network config change (inference from how procd triggers call
`reload_service`, not measured), so the handler must tolerate a HUP with
nothing of netryx's own changed. The agent path (a config edit plus
`ubus call uci reload_config '{}'`, or `/etc/init.d/netryx reload`) then
delivers SIGHUP only when the package's config actually differs.

## 4. Does a cut keep-alive connection come back?

The question (final review, ruling R40): an app rule cuts a connection by
deleting its conntrack entry. Instagram's names are shared fronts, so its
addresses never enter the app set, and the rule's own lines do not reject
them. With `nf_conntrack_tcp_loose=1` (the VM's value, and OpenWrt's default),
does the next packet of the cut connection simply create a new entry?

Setup: the fakehouse phone (02:4e:58:00:00:03) holds one keep-alive connection
to `www.instagram.com` with `curl --rate 12/m` and 18 to 30 requests for
`/robots.txt` on it; a rule `what app, target instagram, who device, member
<phone>` is added by uci and `/etc/init.d/netryx reload` while it runs; a loop
reads the connection's line in `/proc/net/nf_conntrack` every second.

Before the fix (build of 77cb198): the entry disappeared at the reload, came
back four to five seconds later as `[UNREPLIED]` with the counters starting
again at `packets=2`, then `[ASSURED]`, and carried 30 KB more; the engine cut
it again at every tick (03:50:19, 03:50:34, 03:50:49) and it came back every
time, on the same NAT port. All twelve requests after the rule got 200 on the
same connection (`num_connects=0`, same local port 55440).

@measured `/tmp/ka.sh` (curl keep-alive plus a conntrack poll) in netryx-lab, build 77cb198, 2026-11-21 03:48-03:50 London: the connection survived every cut.

After the fix (a cut puts `<MAC> . <address>` into the rule's `cut_<id>_v4/_v6`
set for ten minutes, matched by the rule's lines): the entry disappeared at
04:10:26 and never came back; the request in flight and every later one got
`000`; the set held `02:4e:58:00:00:03 . 57.144.62.34 expires 8m44s`; the
laptop, not in the rule, got 200 from the same address; a new connection from
the phone got `000`; after the rule was removed its cut sets were pruned (0
left) and the phone got 200 again.

@measured `/tmp/ka2.sh` in netryx-lab, build sha256 3aca133c... of this branch, 2026-11-21 04:10-04:12 London: the cut connection stayed cut.

Residual round (ruling R43): the set is keyed by the device's hardware
address, the remote address and the device's own port (`type ether_addr .
ipv4_addr . inet_service`, matched as `ether saddr . ip daddr . th sport`), so
the reborn flow, which keeps its local port, is refused, and a new connection
of the device to the same shared front address is not. Re-run with the same
setup: the cut connection's entry (port 58714) was gone at 04:35:02 and never
came back, and its next request got `000`; each new Instagram connection the
same curl opened answered its first request and was then cut by its own
handshake (the ordinary path for a blocked app, `200` then `000`, ports 49498,
40692, 47644, 36464); a new connection of the phone to `www.facebook.com` on
the same address (57.144.238.34) got `404` from the front, not a reject; the
laptop got 200; after the rule was removed no cut set was left and the phone
got 200.

@measured `/tmp/ka3.sh` in netryx-lab, build sha256 44184bf9... of this branch, 2026-11-21 04:35 London: the cut flow stayed dead and the shared front stayed open to new connections.

Ruling R45: `th sport` is a port only for TCP and UDP (an ICMPv6 echo's type
and code read as 32768), so every cut line also carries `meta l4proto { tcp,
udp }` and the protocol is part of the key: `type ether_addr . ipv4_addr .
inet_proto . inet_service`, matched as `ether saddr . ip daddr . meta l4proto .
th sport`, filled as `<MAC> . <address> . tcp|udp . <device port>`. The
script below is the check, run on the router as root with the fakehouse up;
earlier runs (`ka.sh`, `ka2.sh`, `ka3.sh`) were the same loop with more
requests and, before R43, without the new-connection lines.

```sh
#!/bin/sh
# Keep-alive check for a cut connection (stage2-measured section 4). Run on
# the router with the fakehouse up (lab/fakehouse.sh): the phone holds one
# keep-alive connection to www.instagram.com, a rule blocking instagram for
# the phone is added while it runs, and the connection's conntrack line is
# polled every second. Then new connections of the phone to the same front.
set -u
ARGS=""
for i in $(seq 1 8); do ARGS="$ARGS -o /dev/null https://www.instagram.com/robots.txt"; done
uci -q delete netryx.igphone; uci commit netryx; /etc/init.d/netryx reload; sleep 3
ip netns exec fh-phone curl -sN --rate 12/m -w '%{http_code} conn=%{num_connects} port=%{local_port} ip=%{remote_ip}\n' $ARGS > /tmp/ka.log 2>&1 &
CURL=$!
sleep 7
PORT=$(awk -F'port=' '{split($2,a," "); print a[1]; exit}' /tmp/ka.log)
IP=$(awk -F'ip=' '{print $2; exit}' /tmp/ka.log)
echo "phone port $PORT to $IP"
( end=$(( $(date +%s) + 30 )); last=""; while [ "$(date +%s)" -lt "$end" ]; do
	l=$(grep "sport=$PORT " /proc/net/nf_conntrack | grep -o 'tcp .*dport=443 ' | head -1)
	[ -z "$l" ] && l="(no entry)"
	[ "$l" != "$last" ] && echo "$(date +%T) $l" && last="$l"
	sleep 1
done ) > /tmp/ct.log &
MON=$!
sleep 4
uci set netryx.igphone=rule; uci set netryx.igphone.action=block; uci set netryx.igphone.what=app
uci set netryx.igphone.target=instagram; uci set netryx.igphone.who=device; uci set netryx.igphone.member=02:4e:58:00:00:03
uci commit netryx; /etc/init.d/netryx reload
echo "== rule added $(date +%T)"
wait $CURL; wait $MON
echo "== curl (one line per request)"; cat /tmp/ka.log
echo "== conntrack line of port $PORT"; cat /tmp/ct.log
echo "== cut set"; nft list set inet netryx_rules cut_igphone_v4 | grep -E 'type|elements'
echo "facebook on the same front: $(ip netns exec fh-phone curl -s -o /dev/null -w '%{http_code}' --max-time 8 --resolve www.facebook.com:443:$IP https://www.facebook.com/robots.txt)"
echo "laptop instagram: $(ip netns exec fh-laptop curl -s -o /dev/null -w '%{http_code}' --max-time 8 https://www.instagram.com/robots.txt)"
uci delete netryx.igphone; uci commit netryx; /etc/init.d/netryx reload; sleep 2
echo "== removed: cut sets $(nft list table inet netryx_rules | grep -c 'set cut_'), phone instagram $(ip netns exec fh-phone curl -s -o /dev/null -w '%{http_code}' --max-time 8 https://www.instagram.com/robots.txt)"
rm -f /tmp/ka.log /tmp/ct.log
```

Result: the conntrack line of port 45258 was gone at 04:49:44 and never came
back, and its next request got `000`; each new Instagram connection answered
once and was cut by its own handshake (ports 43590, 44770); the set held
`02:4e:58:00:00:03 . 57.144.62.34 . tcp . 43590 expires 9m44s`; facebook on the
same address got `404` from the front; the laptop got 200; after the rule was
removed no cut set was left and the phone got 200.

@measured the script above as `/tmp/ka4.sh` in netryx-lab, build sha256 66aa7961... of this branch, 2026-11-21 04:49 London: the cut flow stayed dead, the shared front stayed open to new connections.
