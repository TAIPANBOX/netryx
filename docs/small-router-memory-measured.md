# netryx on 512 MB: idle, a typical rule set, every country

netryx 0.1.0-r7 (main 6bda59c) on a throwaway clone of the lab template
`nxlab-golden` on forge, with `limits.memory=512MiB`, the RAM of the Beryl AX
(GL-MT3000). The kernel saw 474.2 MB (MemTotal), and 333.0 MB was available
after boot with netryx running. The clone is OpenWrt 25.12.6 x86-64 with 2
vCPU, so it has the Beryl's memory, not its CPU or its Wi-Fi driver's share.

The question: what is a safe envelope for a 512 MB router, and does anything
reach the OOM killer. Three phases, at `memory_limit` 160MiB (the default),
128MiB and none, each on a fresh restart of netryx:

1. Idle: 150 s with the five fakehouse devices and their habits only.
2. Under load: 150 s more with 12 loops of HTTPS `HEAD` requests from two of
   the devices, as in `docs/apply-memory-measured.md`, at 9 900 to 11 200
   conntrack entries.
3. A typical household rule set, written through the console, then deleted:
   - a group `kids` of three devices;
   - five countries (CN RU KP IR BY) blocked for everyone;
   - the ads category blocked for everyone;
   - four apps blocked for the group;
   - gaming, video and social blocked for the group from 21:00 to 07:00;
   - a 60-minute daily limit on YouTube for the group;
   - one device paused for an hour.

   That is 6 rules and 124 sets.
4. Every country: the 242 codes as four rules for a device not in the house,
   then deleted, as in `docs/flint2-measured.md` section 6.

`/proc` was sampled every second. Every `nft` run's own peak was read by a
wrapper put in place of `/usr/sbin/nft` on the clone, which polls the child's
VmHWM until it exits. The image has no `usleep`, so it polls once a second,
and its figure is a lower bound of nft's peak. Units as in
`docs/flint2-measured.md` section 6: kB from `/proc` over 1000.

## Results

| | 160MiB (default) | 128MiB | none |
|---|---|---|---|
| idle, VmRSS / RssAnon | 50.9 / 25.0 MB | 71.2 / 46.2 MB | 73.2 / 48.1 MB |
| under load, VmRSS / RssAnon | 101.5 / 75.5 MB | 102.2 / 77.0 MB | 111.5 / 86.2 MB |
| MemAvailable under load | 254.7 MB | 262.2 MB | 247.3 MB |
| **typical set**: netryx VmHWM | 126.3 MB | 124.4 MB | 123.5 MB |
| typical set: MemAvailable, lowest | 193.8 MB | 203.6 MB | 194.1 MB |
| typical set: SUnreclaim, highest | 61.8 MB | 62.1 MB | 62.5 MB |
| typical set: nft, highest | 7.1 MB | 5.4 MB | 5.1 MB |
| typical set: OOM kills | 0 | 0 | 0 |
| **every country**: netryx VmHWM | 179.2 MB | 151.2 MB | 195.5 MB |
| every country: MemAvailable, lowest | **9.4 MB** | **42.2 MB** | **17.7 MB** |
| every country: SUnreclaim, highest | 88.6 MB | 87.7 MB | 85.1 MB |
| every country: nft, highest seen | 139.7 MB, then killed | 169.8 MB, finished | 130.8 MB, then killed |
| every country: OOM kills | **4** (the fourth write, then three deletes) | **0** | **1** (the fourth write) |
| sets in the kernel after the four writes | 393 of about 490 | 494 | 393 |

The first phase's idle figure is of a process with an empty history. The
later phases restarted a process that had history to load, most likely why
their idle RSS is about 20 MB higher.

Each OOM kill took `nft` (`Killed process ... (nft.real) ... anon-rss:161676kB`
to `168248kB`), the largest process at that moment. netryx kept its pid
through every phase.

## When the OOM killer takes nft

A separate run on another 512MiB clone, at no limit, repeated the four country
writes and kept every answer:

- The first three answered `applied: true`.
- The fourth answered `200` with `applied: false` and
  `"problem": "not applied: exit status 137"`.

The kernel kept the ruleset it had, with the first three rules in it (393
sets), and `rules.nft` was not rewritten, as the invariant "one transaction or
none" says. Afterwards, `netryx ctl rules` and the console listed all four rules
as not applied with that problem, the three that the kernel was enforcing
included.

At 160MiB the deletes went through the same: deleting the first rule meant an
apply of the other three, which was killed in turn. Three deletes were refused
by the kernel that way before the remaining rules were small enough to apply.
Until then a deleted rule stayed in force. Every phase ended with the kernel
back at its six base sets.

## What the figures say

- **A typical household's rules fit with room to spare at any limit.** netryx
  peaks at about 125 MB, nft at about 7 MB, the sets add 16 MB, and the
  lowest MemAvailable stays above 190 MB.
- **Every country does not fit at the default.** The apply needs an `nft`
  process of about 170 MB beside netryx's own peak and about 40 MB of sets in
  the kernel. At 160MiB and with no limit the OOM killer took nft.
- **At 128MiB every country applied**, with 42 MB available at the lowest
  sample. netryx's peak came down by 28 MB against 160MiB, which was what nft
  lacked. The margin is thin: a bigger house's heap, a Wi-Fi driver's share or
  another service would take it.
- **The OOM killer takes the largest process.** On this clone that was nft, so
  the apply failed cleanly and was reported. On a router where another service
  is larger than nft's 170 MB, that service would be taken instead. Hermes
  holds about 213 MB on the Flint 2 (`docs/flint2-measured.md` section 6).

@measured `scripts/forge.sh sh -c 'MEM=512MiB sh scripts/lab/forge-small-router-memory.sh'`
(it runs `scripts/lab/small-router-memory.sh` on the clone), the log, samples,
gctrace lines, nft peaks and OOM lines in `build/small/out` on forge; clone
`nxsmall` of `nxlab-golden` (68a5bd944112), 2026-11-26 15:46-16:12 London. The
answers to the four writes: its `answers` part (`MODE=answers`), run on clone
`nxoom` from the same setup code, 16:13-16:17. Both clones were
stopped after the run, which deletes them. An earlier attempt at 15:37 was
discarded: its nft wrapper passed nft no input (a background command's stdin is
/dev/null under busybox), so nothing reached the kernel. The script now
checks that a set made through the wrapper exists before it measures.

## On the Beryl AX itself (beryl1)

The same questions on the router: a GL-MT3000 (aarch64, OpenWrt 25.12.6
r33311), 496.7 MB MemTotal.

**The router beforehand:**
- 334.5 MB available, two days up.
- Resident processes: tailscaled (37 MB), rollcalld (16 MB) and two openwrt-mcp
  processes (11 MB each). hermes-agent is installed and was not running.
- 59.3 C idle. Its trip points are 60 C (fan), 85 C, 120 C (hot) and 125 C
  (critical). The run's guard was the start plus 12 C.

**The run:**
- netryx 0.1.0-r7 for aarch64_cortex-a53 (sha256 7c828b4e...2c8), installed
  with `apk add`, which brought three kernel modules with it.
- `memory_limit '128MiB'`, the quarantine off.
- Rules written as UCI sections and applied with a reload, the agent's path.
  Every rule names devices that are not on the router, so no client's traffic
  changed.
- No synthetic load, only the router's own clients.
- The typical set of the clone (a group, five countries, ads, apps, the night
  window, the time limit, a pause), then ten countries (US CN DE GB RU JP FR NL
  BR IN), as in `docs/flint2-measured.md` section 6.
- Every country was not run on the router. The ten-country figure below shows
  why.

| | idle | typical set | ten countries |
|---|---|---|---|
| netryx VmRSS, or VmHWM over the apply | 48.4 MB | 70.1 MB | 117.2 MB |
| netryx RssAnon, highest | 23.2 MB | 44.9 MB | 97.7 MB |
| `rules.nft` | 3 267 bytes | 704 068 bytes | 5 699 956 bytes |
| sets in the kernel | 13 | 133 | 34 |
| nft, highest seen | | 23.7 MB | 125.7 MB |
| SUnreclaim, before and highest | 23.8 MB | 41.1 MB | 63.1 MB |
| MemAvailable, lowest | 290.4 MB | 222.7 MB | **81.0 MB** |
| applied | | all 6 | yes |
| OOM kills | 0 | 0 | 0 |

The temperature stayed between 57.8 and 60.2 C, 24.8 C under the 85 C trip
point, with a load average under 1.

**nft costs more on aarch64.** Its peak per byte of ruleset was 22.0 for the
ten countries (125.7 MB for 5.7 MB), against 16.1 on the x86-64 clone (169.8 MB
for 10.5 MB). At 22 bytes a byte, every country's 10.5 MB ruleset would need
about 230 MB for nft alone, estimated from the ten-country figure. Beside netryx's peak and the
sets, that does not fit in the Beryl's 334 MB, whatever the limit. Ten
countries fit, with 81 MB left at the lowest sample.

**The router afterwards:**
- The rules deleted, then `apk del netryx`, which purged the three modules.
- `/etc/config/netryx`, `/etc/netryx` and `/tmp/netryx` removed.
- The rollback timer disarmed.

The router was then compared with a snapshot taken before the install, and was
the same in:
- the 222 installed packages;
- the sha256 of the 18 files in `/etc/config`;
- the stateless nft ruleset;
- the pids of rollcalld, tailscaled, openwrt-mcp, hostapd and dnsmasq;
- `/etc/rc.local`, `/etc/init.d`, the crontab and the mounts of the extroot;
- the loaded modules.

Two steps beyond the package's own were needed to get there:
- **fw4 still held the package's DNS accept line** (`th dport 553 ct status
  dnat accept`). The removal took the include file away but did not reload fw4,
  so the line stayed until `fw4 -q reload`. This is a defect of the package's
  removal scripts.
- **The five modules the install loaded stayed loaded** (nf_conntrack_bridge,
  nf_conntrack_netlink, nfnetlink_log, nft_meta_bridge, nft_reject_bridge),
  with their files gone. They were unloaded with `rmmod`.

@measured `sh scripts/lab/beryl-memory.sh 128MiB` on beryl1 under
`HOST=beryl1 scripts/lab/flint2-arm.sh arm 900`, its log and samples kept
from `/tmp/nxm`, the snapshot in `/root/netryx-pre.2026-11-26` on the router
(`apk list --installed`, `sha256sum /etc/config/*`, `nft -s list ruleset`, the
pids, `lsmod`); the package built on forge with
`scripts/forge.sh sh -c 'sh package/netryx/build.sh aarch64_cortex-a53 && PKGREL=7 sh scripts/lab/forge-mkapk.sh aarch64_cortex-a53 && sh scripts/package-sane.sh build/aarch64_cortex-a53/tree build/aarch64_cortex-a53/netryx-0.1.0-r7.apk && sh scripts/elf-arch.sh build/aarch64_cortex-a53/tree/usr/sbin/netryx arm64'`,
2026-11-26 16:20-16:33 London. In seven one-second samples during the
ten-country apply `pidof netryx` gave no single pid, so the sampler wrote
those lines without netryx's three fields. Their MemAvailable (106.8 MB at the
lowest) was read from its shifted place, and netryx's peak in those seconds is
in its VmHWM.

## Recommendation for 512 MB

- **A typical household's rules fit at any limit**, on the clone and on the
  Beryl: netryx at 70 to 126 MB, nft at 5 to 24 MB, over 190 MB left.
- **128MiB on a 512 MB router, by default.** It costs the typical set nothing,
  and on the clone it was what let every country apply. The init now takes
  128MiB when MemTotal is at most 768 MiB and 160MiB above, unless the config
  names a limit. An install configured before keeps its `memory_limit '160MiB'`
  until that line is removed or set to `auto`.
- **Ten countries fit on the Beryl**, with 81 MB left at the lowest sample.
- **Every country does not fit on a Beryl at any limit.** On aarch64, nft alone
  would need about 230 MB.

Left to itself, the OOM killer settles an apply that does not fit, and it takes
the largest process: nft here, another service on a router that runs a larger
one. The check below refuses such an apply first.

## The check before nft (r9)

netryx now counts an apply that loads new country sets before nft runs:
- nft over the ruleset the kernel holds and the script, at 22 bytes a byte
  (16 on x86-64);
- the kernel's new sets at 7 bytes a byte;
- against MemAvailable, with 64 MiB kept free.

The two factors are the ones measured above. The margin covers the 18 MB by
which that count fell short of the ten-country apply on the Beryl, and the
9 MB available at which the OOM killer was reached on the clone.

**When the apply would not fit:**
- nft is not run;
- the kernel keeps its ruleset and rules.nft is not rewritten;
- the rule says `not applied: too little memory to apply: ...` with the
  figures, in the console and in the API's answer.

**What is not checked:**
- A change that loads no country set (a delete, an app rule) is never held
  back. nft then holds no more than the router already held, and a starved
  router can always shed rules.
- A router whose memory cannot be read applies as before, and says so in the
  log.
- A restarted process loads every country set again (they may come from an
  older address table), which needs nft over the kernel's ruleset twice. When
  that does not fit, the country sets the kernel holds are kept as they are:
  the rules stay enforced with the ranges they had, the rest applies, and the
  log names the countries kept. Each such rule is shown applied with a note
  that its country ranges were kept from the kernel. The sets are loaded again
  at the next retry (5 minutes) once the memory is there, and the note goes.

The same four every-country writes on a 512MiB clone, with r9 built from this
tree, at no limit:

| write | r7 (above) | r9 |
|---|---|---|
| 1 and 2 | applied | applied |
| 3 | applied | refused: needs about 132 MB, 171 MB available, 64 MiB kept free |
| 4 | nft killed by the OOM killer, `exit status 137` | refused: needs about 207 MB, 178 MB available |
| OOM kills | 1 | **0** |

The kernel kept the two rules it held (264 sets). The third write would have
fitted with 39 MB to spare, which is under the margin. The check errs on the
side of the router.

@measured the `answers` part of `scripts/lab/small-router-memory.sh` on a
512MiB clone `nxguard` of `nxlab-golden` upgraded to
`netryx-0.1.0-r9.apk` (`scripts/forge.sh sh -c 'sh package/netryx/build.sh x86_64 && PKGREL=9 sh scripts/lab/forge-mkapk.sh x86_64'`),
2026-11-26 16:46-16:50 London. The figures in its answers were then printed in
MB of 10^6 bytes; the console now prints MiB.

**A restart on the clone with kept sets (r10):**
- Two rules of 64 countries each were applied (264 sets, 230.9 MB available).
- Then `/etc/init.d/netryx restart`.
- The new process could not load the sets again within the memory, so it kept the kernel's. The kernel still held 264 sets.
- `netryx ctl rules` showed both rules applied, with the note `country ranges kept from the kernel: too little memory to load them again`.
- The log named the countries kept. No OOM kill.

@measured on a 512MiB clone `nxkept` of `nxlab-golden` upgraded to
`netryx-0.1.0-r10.apk` built from this tree, with the script inlined in the
pull request that added the note, 2026-11-26 17:18-17:19 London.

**The limit by RAM on the clone (r11):**
- r7 ran with its config's `memory_limit '160MiB'` (GOMEMLIMIT=160MiB).
- `apk add` of r11 replaced the untouched config with the new one, which names no limit.
- netryx came up with GOMEMLIMIT=128MiB on 474 248 kB of MemTotal.

@measured on a 512MiB clone `nxram` of `nxlab-golden`,
`tr '\0' '\n' < /proc/$(pidof netryx)/environ | grep GOMEMLIMIT` before and
after `apk add --allow-untrusted netryx-0.1.0-r11.apk` built from this tree,
2026-11-26 17:33 London.

