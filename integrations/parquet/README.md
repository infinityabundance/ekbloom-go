# EKBLOOM × Parquet — dictionary-page integration

This directory is a **separate Go module** (`integrations/parquet/go.mod`, with
`replace github.com/infinityabundance/ekbloom-go => ../../`) that measures the
EKBLOOM **machine-9 shared-dictionary codec** against a **real Parquet dictionary
page** for the *same* column of skewed, repetitive strings. It exists so the
external Parquet dependency never enters the core `ekbloom` module.

## What it does

- Builds **20,000 records × 100 B** structured config/session strings
  (`user=alice|region=us-east-1|role=writer|status=active|…`), drawn with an 80/20
  skew from a pool of **512 distinct values** (so the column is genuinely
  repetitive but not a constant).
- Writes a real Parquet file with
  [`github.com/parquet-go/parquet-go`](https://github.com/parquet-go/parquet-go)
  (ZSTD compression, `RLE_DICTIONARY` data pages, `PLAIN` dictionary page) to a
  temp path, then reads the file metadata back and **asserts a dictionary page
  exists** before reporting.
- Measures, on the *identical* records:

  | Codec | What is measured |
  | --- | --- |
  | **Parquet** | dictionary page + data pages, whole column read back into `[]string` (`ReadFile`) |
  | **EKBLOOM m9** | one 540 B shared trained dictionary per file (`TrainDictionary`) + one header-less core per value (`EncodeRecord`); the 540 B is amortised over the records |
  | **Zstandard (trained dict)** | `zstd.BuildDict` trained on the same records; dictionary bytes amortised |
  | **raw** | uncompressed |

- Read cost: EKBLOOM `DecodeRecord` per value (p50 over **8192** samples) vs.
  reading the Parquet column into values (p50 over 8192 samples).
- Emits **one JSON object on stdout**; human-readable diagnostics go to stderr.

## Run

```sh
sh scripts/parquet.sh
```

Runs hard-capped in the `ekbloom-go-sandbox` image
(`--memory=2g --memory-swap=2g --cpus=2 --pids-limit=512 --ulimit nofile=4096:4096`).

- stdout → `evidence/parquet-dict.json`
- stderr → `evidence/parquet-dict.log`

Requires Docker only; **nothing is built on the host**.

## What it proves

For the same 20,000 × 100 B column, the real dictionary-encoded Parquet file is
**34,834 B (1.74 B/record)** — its `RLE_DICTIONARY` page stores the 512 distinct
values once (an 11,829 B dictionary page) and the rows as dictionary indices. On
the identical records, EKBLOOM machine 9 is **60.95 B/record** and Zstandard with
a trained dictionary is **34.32 B/record**. The honest reading is that a Parquet
dictionary page is *value-level deduplication*, which is the ideal case for a
low-cardinality column; EKBLOOM m9 is a shared *byte-level order-0 model* that
compresses inside every record and generalises to values it never saw in
training.

Field definitions (see `evidence/parquet-dict.json`):

- `parquet_bytes_total` / `parquet_bytes_per_record` — the written file size and
  that size divided by `records`.
- `parquet_dict_page_bytes` — `DataPageOffset − DictionaryPageOffset`, i.e. the
  serialized dictionary page (including its page header).
- `parquet_column_encodings` — the encodings recorded in the column chunk;
  `[PLAIN RLE_DICTIONARY]` (the dictionary page is `PLAIN`, the data pages are
  `RLE_DICTIONARY`).
- `ekbloom_m9_bytes_per_record` — `(Σ core bytes + 540 B dictionary) / records`
  (the dictionary is amortised).
- `zstd_dict_bytes_per_record` — `(Σ compressed bytes + dictionary bytes) / records`.
- `ekbloom_read_p50_ns` — p50 of `Dictionary.DecodeRecord` for a **single value**.
- `parquet_read_p50_ns` — p50 of reading the **whole column** (20,000 values) back
  into `[]string`, including opening the file. `parquet_read_ns_per_value` divides
  this by `records` (~96 ns/value here) for a rough per-value figure.
- `ekbloom_read_allocations` — total heap allocations (`runtime.MemStats.Mallocs`
  delta) across the decode loop; `ekbloom_read_allocations_per_op` ≈ 3.0 (the
  `DecodeRecord` output slice, the per-call model header, and the rANS context).

## Honest limitations

- **Not the same structure.** A Parquet dictionary is a **per-column-chunk**
  value table: it deduplicates *identical* values within a chunk and falls back
  to `PLAIN`/`DELTA_BYTE_ARRAY` once the dictionary grows too large. EKBLOOM
  machine 9 is a **single global trained order-0 byte model** (540 B here),
  referenced by every record; it never stores whole values.
- **The column is chosen in Parquet's favour (and that is the point).** With only
  512 distinct values, whole-value dedup is near-optimal, so Parquet wins on
  bytes/record. On a column of near-unique values (e.g. genuinely random session
  IDs) a Parquet dictionary grows until it falls back to `PLAIN`, while the
  540 B EKBLOOM dictionary stays fixed and still compresses each record.
- **Read cost is apples-to-oranges by construction.** EKBLOOM's p50 is one
  single-value decode; Parquet's p50 is a whole-column read (20,000 values plus
  file open). Use `parquet_read_ns_per_value` for the per-value figure.
- **Timings are single-host, scalar (no SIMD)** and depend on CPU and cache; the
  *byte* numbers are deterministic (fixed PRNG seed), the *latencies* are not.
- The Zstandard dictionary is built with `zstd.BuildDict` (entropy tables trained
  on 2,000 samples; 25,687 B of raw content from 256 of the distinct values) and
  amortised over the column. A different dictionary size would shift that row; it
  is a representative point, not a tuned optimum.

## Where `ResidentView.Get` applies

`ResidentView.Get` is the zero-allocation, borrowed random-access path over a
**fully materialised `.ekb` image** (`Reader.Resident()` → `ResidentView`, then
`Get(i)` is a bounds check plus a bare load, ≈1.35 ns/op, 0 allocs — see
`docs/OPERATIONS.md`, `docs/PERFORMANCE.md`, and
`internal/ekb/resident_test.go`). It applies when the whole logical byte surface
is resident and the caller wants repeated O(1) byte lookups by position.

It does **not** apply to this integration's measurements: reading a Parquet
dictionary page or an EKBLOOM machine-9 core is a **decompress/decode** path
(a dictionary-driven reconstruction), not a borrowed in-memory view. `ResidentView`
is the right tool once an image is already open and materialised — not for the
write-side codecs compared here.
