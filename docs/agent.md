# netryx for a router agent (Hermes through openwrt-mcp)

This is the guide the agent follows. It reads netryx with `ubus_call` on the
ubus object `netryx`, and it changes rules by writing UCI sections with
`uci_apply`. There is no other channel: every method of the object is a read
of the service's read-only socket (the same six reads `netryx ctl` makes from
a root shell), and nothing the agent does goes through the console's login.

@decided 2026-11-20 the agent writes through UCI and openwrt-mcp's rollback,
never through the socket; a section it writes is checked by netryx exactly as
a section written by hand or by the console.

## What the owner grants, once

Run on the router, as root. `hermes-main` is the client name the hermes-agent
package pairs its main profile under (`openwrt-mcp clients` lists it); a
client paired by hand goes by the name given to `openwrt-mcp pair`.

```sh
openwrt-mcp allow hermes-main uci_apply,uci_confirm 'netryx.agent_*' 30d
```

and the read grant, a policy section of its own in `/etc/config/openwrt-mcp`
(see "Reading through openwrt-mcp" below):

```
config policy 'netryx_reads'
	option client	'hermes-main'
	list tools	'ubus_call'
	list scopes	'netryx.*'
	option expires	'2026-12-22T00:00:00Z'
	option enabled	'1'
```

Then `/etc/init.d/openwrt-mcp restart`. A call of any other ubus object stays
refused unless another policy grants it.

The scope `netryx.agent_*` is the narrow grant. It limits section names, not
section types: the agent can create, change and delete only sections whose name
starts with `agent_`, so it cannot touch `netryx.main` (the listen address and
the rest of the service's settings) or the owner's own sections. Name every
section you write `agent_<something>`. What netryx then does with such a
section is its own rule:

- A `device` or `group` section named `agent_*` is ignored by netryx: it cannot
  rename a device, move it to a group or make a group. The agent does not write
  device or group sections.
- A `rule` named `agent_*` is a rule like any other. It can block, and
  `enabled 0` or `delete` removes it.
- An `allow` named `agent_*` can lift any rule except a country rule and the
  owner's DNS filters (`f_doh`, `f_dohf`), for any
  device, for at most 4 hours from now per section; a longer one is ignored. An allowance is
  active only in the last 240 minutes before its `until`, so a far `until`
  lifts nothing until then.

A wider `netryx.*` also lets the agent change `main` and the owner's sections;
the owner decides. Whether `uci_apply` needs the owner's unlock window or a held
approval is openwrt-mcp's own policy (`mfa_tools`, an approval section).

@measured 2026-11-21 in the lab VM (OpenWrt 25.12.6, openwrt-mcp 0.5.0.9):
with the write grant above, `uci_apply` of a section named `agent_t9` was
applied, and `uci_apply` of `netryx.main.listen` and of an owner's section
were denied with `no policy scope covers`.

## Reading through openwrt-mcp

@decided 2026-11-22 Hermes reads netryx's state through openwrt-mcp, and only
reads it, over ubus: netryx ships the rpcd plugin `/usr/libexec/rpcd/netryx`,
which registers the object `netryx` with six methods, each one of the reads
below and none of them a write. The grant is the policy `netryx_reads` above:
`ubus_call` with the scope `netryx.*`. It is a section of its own, not a line
in the hermes-agent package's `hermes_main_read_ubus`: the package writes its
`hermes_main_*` sections afresh at each start, and leaves other sections
alone. The console's rpcd ACL grants no call of the object, so a LuCI login
reaches it no more than the socket.

A scheduled job of the agent cannot read the object yet: in a job the
hermes-agent package lets through only the ubus methods its init lists as
reads, and `netryx.*` is not among them, so the package refuses the call
itself (`docs/flint2-measured.md` section 10). In the agent's chat the call
goes to openwrt-mcp under the grant, measured on the Flint 2 with `devices`.

An earlier form of this grant, `exec` of `netryx ctl` with `argv` lines, was
withdrawn on 2026-11-22: the hermes-agent package never offers `exec` to the
model, and its invariant is that the agent's client is never granted `exec`
(`docs/flint2-measured.md` section 9).

`scripts/lab/flint2-mcp-reads.sh add|remove|check|keep` applies the grant to
the Flint 2 under `flint2-arm.sh arm`: `add` saves `/etc/config/openwrt-mcp`
into the snapshot first, so the armed rollback puts it back and restarts
openwrt-mcp. The order is `flint2-arm.sh arm`, `add`, `check`,
`flint2-arm.sh disarm`, `keep`. Both `add` and `remove` refuse while
openwrt-mcp holds an unconfirmed apply.

## Read: `ubus_call netryx`, or `netryx ctl`

Six reads, nothing else is accepted. Each answers the JSON the console's own
page reads. Through ubus the answer is one object with the API's answer under
the method's name, and a refusal or a service that cannot answer is
`{"error": "..."}` (the call itself succeeds, so read the words). From a root
shell `netryx ctl` prints the API's answer as it is and exits 1 with the error
on stderr.

openwrt-mcp cuts what it hands you: every list in an answer over 8 KiB is cut
to its first 16 entries (it says so after the answer, `[pruned: ...]`), and
the whole answer is cut at 64 KiB. So through ubus `devices` comes keyed by
MAC (`{"devices": {"aa:bb:...": {...}}}`) and `rules` keyed by rule id
(`{"rules": {"agent_kid_night": {...}}}`): an object keeps every key, and a
house of 40 devices reaches you whole. `alerts` shows the 16 newest and `apps`
the device's 16 first apps when the answer is large. An answer over 256 KiB is
refused by netryx itself as more than ubus carries.

| ubus method, arguments | `netryx ctl` form | What it answers |
| --- | --- | --- |
| `devices` | `netryx ctl devices` | every known device: `mac`, `name`, `hostname`, `vendor`, `label`, `group`, `online`, `ipv4`, `last24h`, `topApp` |
| `rules` | `netryx ctl rules` | every rule section, with `applied`, `problem` and `error` |
| `status` | `netryx ctl status` | the overview: the WAN line, how many devices are online, the last hours |
| `apps`, `{"mac": "<mac>"}` | `netryx ctl apps <mac>` | the device's last 24 hours by app, with each app's `id`, `name`, `category` and, under `places[].cc`, the countries it went to |
| `alerts` | `netryx ctl alerts` | the owner's alerts, newest first (`kind`: `new_device`, `new_devices` (a flood, with `count`), `blocked_country`, `upload`, `quarantine_full`, `baseline_rebuilt`, `quarantine_off`; `mac`, `country`, `bytes`, `at`, `sent`, `problem`), whether Telegram is `ready` and why not, the alert and quarantine settings, and `quarantineState` (whether the quarantine holds now, when its first day ends, why it does not). Reading only: the agent cannot change alert settings, open the quarantine, approve a device or isolate one (those are the owner's, in the console) |
| `usage`, `{"mac": "<mac>"}` | `netryx ctl usage <mac>` | today's use of each time limit that covers the device, one row per rule: `rule`, `what`, `targets`, `used` (the sum of its apps' minutes, the number the block is decided on), `limit`, `spentUntil` (Unix seconds) once the minutes are used up, and `apps`, the apps used today with their minutes |

**Finding a device by name.** Read `devices` and match the name the person
used against `name` first (what the console shows, which is the owner's own
name for the device when there is one), then `label`, then `hostname`. The
`mac` of the match is what a rule takes. If two devices match, ask; do not
guess. A MAC is accepted in any case with `:` or `-` between the bytes in
the `apps` and `usage` reads; in a rule write it as `aa:bb:cc:dd:ee:ff`.

**App ids.** An app id in a rule must be one the catalog knows. The ids you
can be sure of are the ones the `apps` read shows for that device.

**Did my rule take effect?** The `rules` read is the answer. Every rule section
appears there:

- `applied: true` and no `error` or `problem`: it is in force (or waiting for
  its time window; `activeNow` says which).
- `error` set: netryx rejected the section and it is inactive. The text names
  the reason (an unknown country, a time that is not `HH:MM`, a device that is
  not a MAC, a name already used). Fix the section and apply again.
- `problem` set: the section is valid but the kernel did not take it; the text
  says why (it starts with `not applied:`).

Every text netryx returns (`error`, `problem`, an alert's `problem`,
`quarantineState.problem`, the API's `error`) is plain English, the same words
the console shows.

A section that is not a valid rule name never appears as a rule: see the name
rule below.

## Write: `uci_apply`, then `uci_confirm`

A rule is applied with `uci_apply` and kept with `uci_confirm`. `uci_apply`
writes the sections, commits, and reloads (the router sends netryx a SIGHUP,
which reads the rules again within a few seconds without restarting it), and
it arms a rollback timer. Read `rules`, then call `uci_confirm` with the
token. If you do not confirm before the timer ends, openwrt-mcp restores the
config and netryx reloads again: the rule is gone. Use this on purpose for
anything you are not sure of.

A section is created by a change with `type` and no `option`, then filled by
changes with an `option`. Each value is one string.

**Lists go in one value.** `uci_apply` sets an option to one string; it cannot
write a UCI list. So `member`, `target` and `days` of a rule take several
entries in that one string, separated by spaces: `"tiktok youtube"`,
`"sat sun"`, `"aa:bb:cc:dd:ee:ff 11:22:33:44:55:66"`. (A real UCI list written
by hand reads the same.) The `member` of an `allow` is one MAC.

### Rule section names

A rule section name is `agent_` followed by lower-case letters, digits and
`_`, at most 32 characters in all, starting with a letter, and never `all`.
(netryx accepts `^[a-z][a-z0-9_]{0,31}$` and refuses `all`; the `agent_`
prefix is the grant above.) An anonymous section, such as `@rule[3]`, is
inactive. A name that is already used makes the second section an error.

### A `rule`

| Option | Values |
| --- | --- |
| `action` | `block` or `limit` (required). `limit` takes `minutes` and works only with `what app` or `what category`, without `from` and `to` |
| `minutes` | with `limit`: minutes a day, 1 to 1440. A minute counts when the app moved at least 64 KB for the device in it; the count restarts at local midnight |
| `what` | `all`, `country`, `app` or `category` |
| `target` | with `country`: two-letter upper-case codes such as `RU`; with `app`: app ids; with `category`: category ids; several separated by spaces. Required unless `what` is `all` |
| `who` | `all`, `device` or `group` |
| `member` | with `device`: one or more MACs, `aa:bb:cc:dd:ee:ff`, separated by spaces |
| `group` | with `group`: the id of a group that exists and has devices |
| `days` | `mon` ... `sun`, several separated by spaces; absent means every day |
| `from`, `to` | `HH:MM`, both or neither, not equal; a window that runs past midnight belongs to the day it starts. Both absent means all day. `days` without `from` and `to` means the whole of those days |
| `until` | Unix seconds, or RFC 3339; the rule ends itself then. A time in the past is an error |
| `enabled` | `0`, `false`, `off` or `no` keeps the rule but switches it off; anything else, or absent, is on |

Times are in the router's own time zone (`system.@system[0].zonename`).

Example: block everything for one device from 21:00 to 07:00 on Fridays and
Saturdays (the `devices` read gave the MAC):

```
uci_apply {"timeout": 120, "changes": [
  {"config":"netryx","section":"agent_kid_night","type":"rule"},
  {"config":"netryx","section":"agent_kid_night","option":"action","value":"block"},
  {"config":"netryx","section":"agent_kid_night","option":"what","value":"all"},
  {"config":"netryx","section":"agent_kid_night","option":"who","value":"device"},
  {"config":"netryx","section":"agent_kid_night","option":"member","value":"02:4e:58:00:00:03"},
  {"config":"netryx","section":"agent_kid_night","option":"days","value":"fri sat"},
  {"config":"netryx","section":"agent_kid_night","option":"from","value":"21:00"},
  {"config":"netryx","section":"agent_kid_night","option":"to","value":"07:00"}]}
```

then read `rules`, then `uci_confirm {"token": "..."}`.

To pause a device for a while, write a rule with `what all`, `who device`, and
an `until`.

A time limit, "an hour of TikTok a day on this phone": `action limit`,
`minutes 60`, `what app`, `target tiktok`, `who device`, `member <mac>`. Once
the hour is used the app is blocked for that device until local midnight;
the `usage` read shows where it stands. An `allow` for that rule lets the
device through again for a while, like any other rule. To remove a rule, `uci_apply` a change with `delete: true`, a
`section` and no `option`.

### An `allow`

An allowance lets one device through one rule (or every rule) for a short
while, and it is how "30 more minutes" is written.

| Option | Values |
| --- | --- |
| `rule` | the id of the rule it lifts. The option must be present: an empty value means every rule. An allowance cannot lift a country rule |
| `member` | the device's MAC |
| `until` | Unix seconds or RFC 3339, in the future and at most 240 minutes from now. A time that has passed, or is further away, makes netryx ignore the allowance |

Example, lifting the rule above for this device until a given time:

```
uci_apply {"timeout": 90, "changes": [
  {"config":"netryx","section":"agent_kid_more","type":"allow"},
  {"config":"netryx","section":"agent_kid_more","option":"rule","value":"agent_kid_night"},
  {"config":"netryx","section":"agent_kid_more","option":"member","value":"02:4e:58:00:00:03"},
  {"config":"netryx","section":"agent_kid_more","option":"until","value":"1795200000"}]}
```

An `allow` section is not a rule of its own in the `rules` read: it shows under the
rules it lifts, in each rule's `allows` list (`id`, `member`, `until` in Unix
seconds, and `by`: `agent` for an `agent_*` section, `owner` for one the
console wrote). Check an allowance there, not by `activeNow`: `activeNow` says
the rule's window covers now, and stays true while a device is let through.
An allowance that ended, or one for a device the rule does not hold, is not
listed.

## What not to do

- Do not write `device`, `group` or `main` sections. Names and groups belong to
  the owner and the console (netryx ignores `agent_*` ones); `main` is the
  service's own settings.
- Do not try to change the DNS filters (forced DNS, DoT and DoH blocking, safe
  search, YouTube's restricted mode). They are the owner's: netryx reads them
  only from the section named `filters`, which the grant above does not reach,
  and ignores any other `filters` section.
- Do not retry a refusal. `denied: no policy scope covers ...` means the owner
  has not granted it.
- Do not leave a change unconfirmed that you meant to keep: it is undone.
