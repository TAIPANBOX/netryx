# What the interventions journal writes to flash

Measured 2026-11-23 in netryx-lab (x86 OpenWrt VM) with
`scripts/lab/journal-load`, which writes made-up interventions through the
journal package into a file of its own (never the journal netryx keeps) and
reads what the disk took. The interventions are spread 35 s apart, longer
than the kernel's dirty expiry (30 s), so each reaches the disk on its own,
as a day's spread-out interventions do.

## f2fs, as on the Flint 2

An f2fs image on tmpfs, mounted through a loop device with the options
OpenWrt gives its overlay (`mode=adaptive`, `checkpoint_merge`,
`fsync_mode=posix`, `inline_data`, `flush_merge`), and f2fs's own
`/sys/fs/f2fs/loop0/lifetime_write_kbytes` read before and after. 20
interventions a run (21 appends with the journal's start line), and a
baseline of the same length with none.

| Run | Written | Per intervention | A day at 50 |
|---|---|---|---|
| baseline, nothing written | 0 KiB | | |
| as shipped (no fsync), 35 s apart | 200 KiB | about 9.5 KiB | about 476 KB |
| fsync after each, 35 s apart | 220 KiB | about 10.5 KiB | about 524 KB |
| as shipped, 5 s apart (one writeback) | 40 KiB | about 1.9 KiB | |
| kept in memory, one write at the end | 24 KiB for two writes | | at most 24 writes a day |

Command for each run:
`journal-load -path /mnt/jload/interventions-load.jsonl -n 20 -every 35s -disk loop0 -f2fs loop0`
(with `-fsync`, `-every 5s` or `-batch`).

## ext4

The VM's own root (ext4 on sda), `/sys/block/sda/stat` sectors written:
21 appends 35 s apart took 496 KiB, about 23.6 KiB each (`-n 20 -every 35s
-disk sda`), and 50 took 1080 KiB in a window that also held one hourly
history save (about 7 KB). A 30-minute baseline with nothing written took 0.

## What was decided from it

The journal stays as shipped: appended, no fsync, no buffer, so a power cut
loses at most the seconds before it (@decided 2026-11-23). The gate
(`scripts/journal-budget.sh`) counts each append at 10 KiB, the f2fs figure,
and holds a busy house's 50 interventions a day to 640 KB a day; the model
gives 500.7 KB.
