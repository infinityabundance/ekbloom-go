# The skewed small-record court

This court lives in a **separate Go module** (`courts/`) so the core `ekbloom`
module stays dependency-free. It measures the RocksDB-style workload the reference
documents: 100-byte values, 2000 records, an 80/20 key skew, 50000 gets, with the
shared dictionary trained on the same 500-record set for both Zstandard and
EKBLOOM machine 9.

```sh
sh scripts/courts.sh        # writes evidence/courts.json
```

## Result (this host, Go 1.27, amd64)

| codec | B/record | vs raw | decode p50 |
| --- | ---: | ---: | ---: |
| raw | 100.0 | 1.000 | 30 ns |
| lz4 (block) | 101.9 | 1.019 | 20 ns |
| zstd | 113.0 | 1.130 | 100 ns |
| zstd-dict | 47.4 | 0.474 | 251 ns |
| ekbloom-m9 | **59.8** | **0.598** | 2144 ns |

## Reading it honestly

- **Space win, decode cost.** EKBLOOM machine 9 shrinks 100-byte records to
  `0.598× raw` (the shared dictionary is stored once), but its order-0 rANS decode
  is **≈8.5× slower** than Zstandard-with-dictionary (2144 ns vs 251 ns). This
  mirrors the reference's honest positioning: the dictionary machine buys bytes at
  a per-record decode cost.
- **Block LZ4 cannot compress a 100-byte record** (`1.019×`): the block header
  dominates. That is the expected non-result.
- **Zstandard without a dictionary expands** a 100-byte record (`1.130×`): the
  frame header dominates, which is exactly why a shared dictionary matters.

Numbers are single-host, single-run, deterministic seeds; they are a
decision aid, not a portable benchmark. The Go port's `m9` column uses
`Dictionary.EncodeRecord` (the header-less referenced core), matching the
reference's per-record framing (the dictionary is counted once, separately).
