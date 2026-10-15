# Archived timing capture — "load30" (pre-provenance)

This directory is a **byte-for-byte** copy of the timing evidence that was
committed *before* provenance stamping was added. It is kept as the reference
point for the comparison described in `evidence/README.md`.

## What this capture is

- It was taken while the host's `loadavg` was **≈29** (1-minute average). The load
  came from **unrelated, I/O-bound containers** running on the same host, not from
  the EKBLOOM harnesses.
- At capture time the CPU was in fact largely **idle** (the host was ~87% idle):
  the high average is dominated by processes blocked on I/O (I/O-wait), which
  Linux counts toward load average. So a high `loadavg` here does **not** by itself
  mean the CPU was busy.
- These files **predate** the `"provenance"` object. They therefore do not carry
  the in-container `loadavg` / `cpu_idle_pct` that would let a reader see the
  contention directly. That is exactly the gap provenance closes.

## The main evidence is the later capture

The `evidence/*.json` files (one level up) are the **later, provenance-stamped**
capture. Every timing artifact there now carries a `"provenance"` object recording
the toolchain, limits, container image digest, `loadavg`, and `cpu_idle_pct`
observed at measure time. Read those first; use this archive only to compare.

## How to read the numbers here

- Each metric is the **median across `EKB_RUNS` runs** (default 5), with the
  observed `_min` and `_max` retained.
- **`_min` is the least-contended estimate.** A run that happened to catch the
  quietest window yields the smallest latency; the median sits below the worst runs
  but above the best.
- The **`_min`-vs-median gap is the contention signal.** A wide gap (e.g. median
  well above `_min`, and a much larger `_max`) means the capture was noisy — some
  runs were slowed by co-tenant I/O or scheduling. A narrow gap means the runs
  agreed and the median is trustworthy. The provenance-stamped captures make this
  explicit by also recording `cpu_idle_pct`.

## Files

`byteat.json`, `sweep.json`, `concurrency.json`, `dictionary.json`, `readerat.json`,
`courts.json`, `courts-point.json`, `ycsb.json`, `pebble-vlog.json`,
`pebble-integration.json`, `rocksdb-blobdb.json`, `parquet-dict.json`,
`profile.json`, `gcstats.json`, `bench.txt`, `bench-history.jsonl`.

Sizes and identities (physical bytes, `E_V`, fixture hashes) are unaffected by
contention; only `*_ns`, `ns/op`, `MB/s` and `ops/s` fields move between captures.
