# Integrations — EKBLOOM inside real storage systems

These are **case studies**, not a claim that EKBLOOM replaces Zstandard. They put
the machine-9 shared dictionary into two real systems and measure what it buys and
what it costs. Everything below builds and runs **inside a memory-capped
container** (`--memory=2g --memory-swap=2g --cpus=2 --pids-limit=512`), never on
the host — see `Dockerfile.sandbox` and `scripts/sandbox.sh`.

Reproduce:

```sh
sh scripts/rocksdb.sh     # -> evidence/rocksdb-blobdb.json
sh scripts/parquet.sh     # -> evidence/parquet-dict.json
sh scripts/sandbox.sh go test ./...   # the main module suite, capped
```

## 1. RocksDB — value codec (`integrations/rocksdb/`)

A separate Go module (`bridge/`) is built with `-buildmode=c-shared` and exports a
C ABI (`ekbloom_train` / `ekbloom_compress` / `ekbloom_decompress`); a C++ program
(`rocksdb_blob.cc`) stores **EKBLOOM-encoded values** in RocksDB (`Put` =
compress, `Get` = decompress) and registers a custom `rocksdb::MergeOperator`
through the Object Registry (decode both operands, concatenate, re-encode).

`evidence/rocksdb-blobdb.json` (20,000 records × 100 B, 80/20 skew, 50,000 ops):

| metric | value |
| --- | ---: |
| bytes/record | 63.38 |
| Get p50 | 4,218 ns |
| Get p99 | 16,471 ns |
| E_V (physical / N) | 0.634 |
| allocation breaches | 0 |

**Honest boundary.** RocksDB has **no runtime `CompressionType` plugin** — its own
Object Registry wiki says dynamically loadable plugins are not supported, and the
registry instantiates customizable classes (comparators, `MergeOperator`, table
factories, …), **not compressors**. So a transparent codec would require building
RocksDB from source with the codec compiled in. The real, runnable integration is
what this does: value-level encoding at the application boundary plus a registered
`MergeOperator`. That is the same shape MyRocks/UDB-style value handling uses, and
it is honest about the boundary rather than faking the enum.

## 2. Parquet — dictionary page (`integrations/parquet/`)

A separate Go module writes a **real Parquet file** (a string column encoded
`RLE_DICTIONARY`, ZSTD-framed) and compares, on the identical records, the Parquet
dictionary page against the machine-9 shared dictionary.

`evidence/parquet-dict.json` (20,000 records × 100 B, 512 distinct values):

| representation | bytes/record |
| --- | ---: |
| Parquet `RLE_DICTIONARY` (dict page 11,829 B) | **1.74** |
| EKBLOOM machine-9 (540 B shared model, amortised) | 60.95 |
| Zstandard + trained dictionary | 34.32 |
| raw | 100.00 |

Read cost: EKBLOOM `DecodeRecord` p50 ≈ 3.2 µs, **0 allocs/op** on the reusable
`DictDecoder` path (below); Parquet per-value ≈ 96 ns (after loading the column).

**Honest boundary.** The column is deliberately Parquet-favourable: a dictionary
page *dedupes identical values*, so with only 512 distinct values it collapses
brilliantly — and Parquet's dictionary falls back to `PLAIN` once a chunk has too
many unique values, while EKBLOOM's model is a fixed 540 bytes that never falls
back. The two are different structures: value-dedup vs a shared byte-level order-0
model. See `docs/DICTIONARY.md` for where the m9 dictionary actually wins — against
**per-record machine 2** (63 B vs 590 B/record, break-even 2), not against
Zstandard-with-dictionary.

## 2b. Pebble — value-log adapter (`courts/pebble/`)

The same machine-9 dictionary as a **Pebble value-log adapter**: each value is
stored as its header-less core (`Dictionary.EncodeRecord`) in a Pebble DB; a
`Get` reads the core and decodes it with a decoder built **once**
(`NewDictDecoder`) into a reused caller buffer — **zero allocations**.

`evidence/pebble-vlog.json` (20,000 records × 100 B, 80/20 skew, 50,000 Gets):

| metric | value |
| --- | ---: |
| bytes/record (codec payload) | 63.36 |
| Get p50 | 872 ns |
| Get p99 | 1,563 ns |
| decode allocs/op | **0** |
| allocation breaches | 0 |
| `worst_case_steps_for` | 100 |
| shared dictionary | 540 B |

**Honest boundary.** `bytes_per_record` is the **codec payload only** — it excludes
Pebble's keys, sequence numbers, checksums, Bloom filters, WAL and LSM overhead,
so the on-disk footprint is larger. Latency is single-host, warm-cache, `NoSync`.
One 540 B dictionary serves all 20,000 values; it never falls back.

```sh
sh scripts/pebble.sh    # -> evidence/pebble-vlog.json (+ .prom exposition)
```

## 3. Zero-allocation decode (`DictDecoder`)

The dictionary's order-0 model is fixed, so the prepared rANS context is built
**once** and reused for every record; only the per-record `initial_state` changes.
`ekbloom.NewDictDecoder(dict)` + `DecodeInto(dst, core)` decode straight into a
caller buffer with **no allocation** on the steady path.

`evidence/ycsb.json` (workload C, read-only):

| | before | after |
| --- | ---: | ---: |
| `ekbloom-m9` read p50 | 2,805 ns | **410 ns** |
| `ekbloom-m9` allocs/op | 3.0 | **0** |

For a fully materialised `.ekb` image, `Reader.View().Get(i)` is the zero-allocation
borrowed read (≈1.35 ns, 0 allocs/op). See `docs/COURTS.md` for the space/decode
trade against zstd/lz4/Pebble.

## 4. Metrics, Prometheus and Grafana

`integrations/prometheus/` exposes the five families a storage reviewer asks for,
sourced from the real API (`Reader.Metrics()`, `Breaches()`, `WorstCaseStepsFor`,
`NewDictDecoder`):

| metric | type | source |
| --- | --- | --- |
| `ekbloom_physical_bytes` | gauge | `Reader.Metrics().PhysicalBytes` |
| `ekbloom_expansion_ratio` | gauge | `Reader.Metrics().ExpansionRatio` |
| `ekbloom_breach_total` | counter | `ekbloom.Breaches()` |
| `ekbloom_get_ns` | histogram | observed per `ByteAt` / `ReadAt` / `DecodeInto` |
| `ekbloom_worst_case_steps_for` | gauge | `Reader.WorstCaseStepsFor(off, len)` |

`grafana/ekbloom-dashboard.json` has five panels bound to those names (physical
bytes, E_V, `rate(ekbloom_breach_total[5m])`, Get
`histogram_quantile(0.5|0.99, …)`, `worst_case_steps_for`).

```sh
sh scripts/prometheus.sh    # one scrape -> evidence/prometheus-scrape.txt
                            # dashboard    -> evidence/grafana-dashboard.json
```

Panel values come from `evidence/`: physical `63.38 B/rec`
(`rocksdb-blobdb.json`), `E_V 0.634`, breach rate `0`, Get p50 `410 ns` /
p99 `16,471 ns`, `worst_case_steps_for` bounded by `bound.json` (1.2M samples,
0 violations). `Reader.Metrics()` and `MetricsText` remain available directly for a
custom exposition (`docs/OPERATIONS.md`).

## 5. Why "pure Go" matters here

No cgo, no `unsafe`, no `mmap` in the codec means the same bytes decode on
`amd64 == arm64 == wasip1`, and the codec drops into any of these systems without a
native toolchain. The Rust reference is faster on raw 4 KiB reads via SIMD/io_uring
(`docs/SCOPE.md`); this port trades that for determinism and portability, and reads
and writes the same frozen files. The integrations above are where a storage team
would actually embed it.
