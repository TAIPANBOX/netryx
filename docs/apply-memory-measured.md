# The apply's peak memory at five GOMEMLIMIT values

netryx 0.1.0-r7 (main 6bda59c) on a throwaway clone of the lab template
`nxlab-golden` on forge: OpenWrt 25.12.6 x86-64, 2 vCPU, 1 GiB, the same RAM
as the Flint 2. The question is whether the default `memory_limit '160MiB'`
should stay, so the apply of a big rule set (the peak of
`docs/flint2-measured.md` sections 6 and 8) was run at no limit, 160MiB,
128MiB, 96MiB and 64MiB.

Each value had its own phase:

1. `memory_limit` set, `/etc/init.d/netryx restart` with `GODEBUG=gctrace=1`
   added to the service's environment, so every collection of the Go heap
   is logged.
2. 3 minutes under the house's load.
3. Through the console (`POST /api/rules`), the 242 country codes of the address
   table as four rules (64, 64, 64 and 50 codes) for a device not in the house,
   as in section 6, and five app and category rules for the five fakehouse
   devices (YouTube and Netflix; social and video; gaming; ads; seven social and
   gaming apps). 9 rules, 607 sets, `rules.nft` 10 523 191 bytes, every write
   200 in every phase.
4. 20 s, then the nine rules deleted, then 20 s.

`/proc` was sampled every second from step 3 to the end of step 4. The load: the
five fakehouse devices with their habits, plus 12 loops of HTTPS `HEAD` requests
(4 s at most) to random names of `flint2-hosts.txt` from two of them, through the
router's resolver and NAT: 10 000 to 10 900 conntrack entries, more than the
Flint 2's 4 000 to 7 400.

Units as in `docs/flint2-measured.md` section 6: `/proc` counts kB of 1024
bytes, and every MB from `/proc` here is that figure over 1000, so 160MiB is
163.8 MB. The heap figures from gctrace are Go's MB of 2^20 bytes (MiB). CPU
is utime + stime of netryx from `/proc/<pid>/stat` over steps 3 and 4, in
seconds and as a share of one core.

## Results

| `GOMEMLIMIT` | none | 160MiB (default) | 128MiB | 96MiB | 64MiB |
|---|---|---|---|---|---|
| plateau before the writes, VmRSS / RssAnon | 100.0 / 74.0 MB | 112.6 / 87.2 MB | 110.5 / 85.2 MB | 109.4 / 83.9 MB | 76.6 / 51.2 MB |
| **netryx VmHWM** | **226.0 MB** | **184.4 MB** | **153.5 MB** | **129.6 MB** | **121.2 MB** |
| netryx RssAnon, highest | 190.8 MB | 155.4 MB | 122.7 MB | 90.2 MB | 89.0 MB |
| Go heap at a collection's start, highest | 174 MiB | 133 MiB | 111 MiB | 90 MiB | 85 MiB |
| Go live heap after marking, highest | 102 MiB | 90 MiB | 90 MiB | 86 MiB | 81 MiB |
| collections during the writes and deletes | 42 | 48 | 63 | 147 | 2 897 |
| netryx CPU over the writes and deletes | 10.4 s in 96.5 s, 10.8 % | 11.5 s in 101.2 s, 11.3 % | 11.8 s in 105.2 s, 11.2 % | 14.4 s in 103.3 s, 14.0 % | 88.2 s in 134.1 s, 65.8 % |
| the nine writes, answered in | 39.0 s, the longest 7.8 s | 36.7 s, 7.2 s | 41.7 s, 7.8 s | 41.2 s, 7.9 s | 59.8 s, 13.3 s |
| the nine deletes, answered in | 12.3 s | 19.2 s | 18.2 s | 16.8 s | 28.8 s |
| `nft` process RSS, highest | 171.0 MB | 171.0 MB | 176.3 MB | 171.0 MB | 171.0 MB |
| SUnreclaim, before and highest | 46.0, 106.2 MB | 46.7, 104.9 MB | 46.9, 102.8 MB | 46.9, 103.2 MB | 46.9, 104.9 MB |
| MemAvailable, before and lowest | 761.1, 424.3 MB | 750.4, 465.6 MB | 754.0, 464.8 MB | 751.1, 522.2 MB | 783.9, 545.3 MB |

An earlier run of the same phases on another clone gave VmHWM of 218.6,
183.3, 150.8, 128.3 and 123.2 MB. In that run the sampler had no pause, since
busybox `sleep` takes no fraction: it held one of the two vCPUs, so its CPU and
timing figures were not kept. The peaks agree within 7 MB.

## What the figures say

- **The apply holds about 90 MiB of live heap.** That is the floor under any
  limit. At 96MiB the limit sits on it: the collector runs three times as often
  as at 160MiB and netryx takes 26 % more CPU than at 160MiB, for 55 MB off the
  peak. At 64MiB it runs without pause: 2 897 collections, two thirds of a core
  for two minutes, writes 63 % slower than at 160MiB, and the peak only 8 MB under 96MiB's.
- **160MiB takes 41.6 MB off the peak** (226.0 to 184.4 MB VmHWM) for 1.1 s
  more CPU over the whole apply, with no write slower. Its heap stayed 27 MiB
  under the limit, and the live heap 70 MiB under it. VmHWM sits above the
  163.8 MB limit because it also counts the binary's and the mapped tables'
  file pages, about 29 MB here. GOMEMLIMIT bounds the Go runtime's own memory,
  which RssAnon follows (155.4 MB, under the limit).
- **128MiB takes another 30.9 MB** (184.4 to 153.5 MB) at about the same CPU
  as 160MiB (11.8 s against 11.5 s) and writes 5 s slower in all. Its heap
  reached 111 MiB of the 128, and the live heap stayed 38 MiB under the limit.
- **The apply's largest single process is `nft`, not netryx.** `nft -f` of the
  10.5 MB ruleset reached 171 to 176 MB of RSS in every phase, and the kernel's
  sets added about 58 MB of SUnreclaim. Neither depends on GOMEMLIMIT. They are
  why MemAvailable falls by 230 to 340 MB during an all-country apply even at
  the lowest limit.

## Recommendation

Keep `memory_limit '160MiB'` as the default for a 1 GB router. It removes the
largest part of the peak at almost no cost, and its 70 MiB above the apply's
live heap leaves room for a bigger house before the collector is pushed.
128MiB is a measured option for a router where those 31 MB matter, at a third
of the headroom. A limit at or below 96MiB buys little more peak and costs CPU
(at 64MiB a great deal), so it is not one to set. A 256 or 512 MB router is
bounded first by the `nft` process and the kernel's sets, about 230 MB together
for an all-country rule set, which no setting of netryx's heap changes.

@measured `scripts/forge.sh sh scripts/lab/forge-apply-memory.sh` (it runs
`scripts/lab/apply-memory.sh` on the clone, as committed; that run used the
same two scripts from `build/mem`, with the clone named `nxt5`), the log,
samples and gctrace lines in `build/mem/out` on forge; forge-lab clone of
`nxlab-golden` (68a5bd944112), 2026-11-26 14:35-14:59 London; the earlier run
14:07-14:33, the same day. The clone was stopped after each run, which deletes it.
