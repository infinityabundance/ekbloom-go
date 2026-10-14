# The skewed small-record court

This court lives in a **separate Go module** (`courts/`) so the core `ekbloom`
module stays dependency-free. It measures the RocksDB-style workload the reference
documents: 100-byte values, 2000 records, an 80/20 key skew, 50000 gets, with the
shared dictionary trained on the same 500-record set for both Zstandard and
EKBLOOM machine 9.

```sh
sh scripts/courts.sh        # writes evidence/courts.json
```

## Result (this host, Go 1.23, amd64)

| codec | B/record | vs raw | decode p50 | decode p99.9 | allocs/get |
| --- | ---: | ---: | ---: | ---: | ---: |
| raw | 100.0 | 1.000 | 30 ns | 551 ns | 1.00 |
| lz4 (block) | 101.9 | 1.019 | 20 ns | 321 ns | 0.00 |
| zstd | 113.0 | 1.130 | 131 ns | 1933 ns | 1.00 |
| zstd-dict | 47.4 | 0.474 | 261 ns | 3798 ns | 1.87 |
| ekbloom-m9 | **59.8** | **0.598** | 2735 ns | 84187 ns | 3.00 |

## Reading it honestly

- **Space win, decode cost.** EKBLOOM machine 9 shrinks 100-byte records to
  `0.598× raw` (the shared dictionary is stored once), but its order-0 rANS decode
  is **≈10× slower** than Zstandard-with-dictionary (median 2,735 ns vs 261 ns) and
  its tail is heavier (p99.9 84 µs vs 3.8 µs). This mirrors the reference's honest
  positioning: the dictionary machine buys bytes at a per-record decode cost.
- **Allocations.** `ekbloom-m9` performs **3.0 allocs/get** — the decoded output
  buffer plus the rANS scratch state — against `zstd-dict`'s 1.87 and `lz4`'s 0.0.
  Closing that gap (a reusable decode buffer) is the obvious next optimization;
  it is reported here rather than hidden.
- **Block LZ4 cannot compress a 100-byte record** (`1.019×`): the block header
  dominates. That is the expected non-result.
- **Zstandard without a dictionary expands** a 100-byte record (`1.130×`): the
  frame header dominates, which is exactly why a shared dictionary matters.

Numbers are single-host, median-of-runs (`EKB_RUNS`, default 5) with deterministic
seeds; they are a decision aid, not a portable benchmark. The Go port's `m9`
column uses `Dictionary.EncodeRecord` (the header-less referenced core), matching the
reference's per-record framing (the dictionary is counted once, separately).

## 4 KiB point-read court (vs an embedded LSM)

`courts/point` measures one random **4 KiB point read** when the same payloads are
held in different representations, with an 80/20 skew, and reports the footprint
too. EKBLOOM is an in-memory codec; Pebble is an on-disk LSM with a warmed block
cache — the comparison is "what does a point read cost, and what does a value
occupy", not a claim that a codec beats a store.

```sh
sh scripts/courts.sh        # writes evidence/courts.json and courts-point.json
```

| codec | B/value | Get p50 | Get p99 | Get p99.9 |
| --- | ---: | ---: | ---: | ---: |
| raw-slab | 4096.0 | 210 ns | 781 ns | 2,715 ns |
| lz4-block | 1,067.1 | 982 ns | 1,253 ns | 2,264 ns |
| zstd | 669.7 | 4,508 ns | 6,322 ns | 16,761 ns |
| **pebble** | 4096.0 | **4,900 ns** | **5,891 ns** | **7,974 ns** |
| ekbloom-m1-root | 4096.0 | 370 ns | 1,032 ns | 3,406 ns |
| ekbloom-m1-path4 | 12,288.1 | 400 ns | 1,433 ns | 4,339 ns |
| **ekbloom-m2-root** | **2,398.8** | **311 ns** | **1,272 ns** | **6,893 ns** |

Read it honestly:

- **EKBLOOM m2 root holds ≈0.59× raw** (2,398.8 B/value) and serves a point read
  from memory in ≈310 ns p50 — because the in-memory reader materialises one
  symbol array on first random access; it is not a cold random-access decoder.
- **Pebble's `Get` is ≈4.9 µs p50** for the same values at full size — an on-disk
  LSM point lookup, not a slab read. Raw is the latency floor for an in-memory
  slab; the codecs trade bytes for decode work.
- **Numbers are in-memory vs on-disk.** EKBLOOM pays no I/O here; a real service
  would add `pread`, which is where the reference's `io_uring`/`mmap` wins live
  (see `docs/SCOPE.md`).

## YCSB value-log court

`courts/ycsb` replays YCSB-style traces (zipfian `theta=0.99`, 20,000 records ×
100 B, 50,000 ops) over the same payloads held in the different representations —
including the same codec placed **inside** a Pebble embedded store (a value-log
adapter: values are codec-encoded and decoded on `Get`).

Workload C (read-only):

| store | B/rec | ops/s | read p50 | read p99 |
| --- | ---: | ---: | ---: | ---: |
| raw | 100.0 | 16,158,133 | 20 ns | 110 ns |
| machine2-per-record | 590.0 | 149,276 | 6,212 ns | 11,411 ns |
| zstd-dict | 45.7 | 2,401,861 | 340 ns | 771 ns |
| **ekbloom-m9** | **63.4** | 2,237,137 | 401 ns | 681 ns |
| pebble+raw | 100.0 | 1,907,024 | 470 ns | 881 ns |
| pebble+zstd-dict | 45.7 | 1,049,284 | 872 ns | 1,984 ns |
| pebble+ekbloom-m9 | 63.4 | 1,089,694 | 871 ns | 1,372 ns |

Read it honestly:

- **Where the dictionary matters — versus the per-record model.** Machine 9 is
  **9.3× smaller** than per-record machine 2 (63.4 vs 590 B/record) and **≈15×
  faster** to read (401 vs 6,212 ns). That is the “83.5 vs 590, break-even 2”
  story, and it is a space *and* speed win over the naive per-record model.
- **Where it does not — versus Zstandard-with-dictionary.** zstd-dict is both
  smaller (45.7 vs 63.4 B/record) and faster (340 vs 401 ns), though with the
  reusable `DictDecoder` the read gap narrows to ≈1.2× (the per-record court above
  measures the allocating decode path, where the gap is ≈11×). Machine 9 is
  **not** a speed win over zstd-dict; this trace shows exactly that, which is what
  the space-at-a-decode-cost position means for a small-record workload.
- Workloads A/B (updates) widen the gap: machine 9 must re-encode on update.
- Raw is the latency floor; the codecs trade decode work for bytes.
