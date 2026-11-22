# Measured

Numbers taken from a run, each with the command that produced them. A number
here is true about the moment it was taken; repeat the command to refresh it.

## 2026-11-20: country and ASN table footprint (forge)

Command, run on forge in the remote copy of the tree (where `data/` exists):

```
scripts/forge.sh go test -tags realdata -run TestRealFileFootprint -v ./internal/enrich/
```

File: `data/ip2asn-combined.tsv.gz` (iptoasn.com, public domain), 719 149 lines.

Output:

```
ranges=579453 v4=459655 v6=119798 distinct_answers=86901
load_time=516.478051ms
heap_retained_bytes=22250456 (21.2 MB)
total_allocated_during_load_bytes=203694696
```

Re-run after the review fixes (same command, same file), with the skip count
added to the output:

```
ranges=579453 v4=459655 v6=119798 distinct_answers=86901
skipped=139696
load_time=552.812578ms
heap_retained_bytes=22332408 (21.3 MB)
total_allocated_during_load_bytes=204278352
```

`skipped` counts every line the loader did not use. On this file it equals the
AS 0 count exactly, so no line was malformed, overlapping or too long.

Reading the numbers:

- The 719 149 lines minus the 139 696 lines with AS 0 (space that is not
  routed, which `LoadIP2ASN` drops) leaves the 579 453 ranges loaded.
- The table keeps 86 901 distinct country, ASN and organisation answers; ranges
  point at them by index.
- The table retains about 21 MB on the heap after a full GC. The loader
  allocates about 194 MB in total while it runs, which the garbage collector
  reclaims.
