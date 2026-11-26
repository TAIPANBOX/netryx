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

## Recommendation for 512 MB

- Set `memory_limit` to 128MiB on a 512 MB router. The typical set then costs
  nothing, and every country applies on a router with nothing else large on it.
- Every country, or anything near it, is not safe on a 512 MB router that runs
  another large service. The apply should not be started there at all.

What would make that hold without the owner's care is a check before `nft`
runs: if the ruleset's expected nft peak is more than the memory available
less a margin, the apply is refused with that reason, and the OOM killer is
never reached. This is a proposal, not built. Its factor, nft's peak per byte of
ruleset, is to be measured on the Beryl itself (aarch64), since this clone is
x86-64.

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
