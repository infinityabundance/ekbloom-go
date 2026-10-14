# Performance and tuning

Everything here is measured on one host (AMD Ryzen 7 9800X3D, Go 1.23, scalar, no
SIMD) unless marked native arm64, and is a decision aid, not a portable
benchmark. Every latency/throughput value is a **median of `EKB_RUNS` runs**
(default 5, 3 in CI), measured without `-race`; deterministic sizes (`physical_bytes`,
`E_V`) are unaffected. The raw logs are `evidence/sweep.json`, `evidence/bench.txt`,
`evidence/aarch64-native-bench.txt`, `evidence/byteat.json`, `evidence/courts.json`
and `evidence/corpus-compare.log`.

## Profile frontier (`evidence/sweep.json`)

One fixed 1 MiB compressible source (4-symbol 60/25/10/5 alphabet), all profiles:

| profile | machine | physical B | `E_V` | `ByteAt` p50 | 4 KiB read p50 | bulk seq |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| `m1_root` | 1 | 1,048,576 | **1.000** | 30 ns | 521 ns | 13,111 MB/s |
| `m2_root` | 2 | 208,084 | **0.198** | 30 ns | 270 ns | 12,066 MB/s |
| `m1_full` | 1 | 5,242,884 | 5.000 | 30 ns | 3,577 ns | 1,237 MB/s |
| `m1_sparse64` | 1 | 1,081,346 | 1.031 | 110 ns | 9,527 ns | 464 MB/s |
| `m1_path4` | 1 | 3,145,963 | 3.000 | 30 ns | 261 ns | 14,398 MB/s |
| `m2_full` | 2 | 4,402,392 | 4.198 | 20 ns | 230 ns | 13,680 MB/s |

`ByteAt` here includes a `time.Now` per sample (~30 ns floor); the true single-op
cost is in `evidence/bench.txt` (median path bloom ≈7.1 ns, history ≈4.1 ns,
materialised ≈5.5 ns).

## Choosing a machine

| workload | machine | why | measured |
| --- | --- | --- | --- |
| any bytes, never expand | 2 (`RANS0`) → 3 fallback | order-0 rANS; falls back to store when it would expand | `m2_root E_V 0.198` on skew; `auto` keeps `V ≤ N` |
| skewed, many small records | 9 (`OrANS-DICT`) | one shared trained dictionary, header-less records | **83.5 vs 590 B/record**, break-even 2 (`docs/DICTIONARY.md`) |
| O(1) random access, fixed alphabet | 5 (`PACK`) | direct `bits`-wide addressing, no index, no replay | reference `packed-m5` 1 B 30 ns / 4 KiB 3,546 ns / 1.16 GB/s (`docs/FRONTIER.md`) |
| heterogeneous objects | 6 (`SEG`) | per-segment constant/raw/coded choice | `BenchmarkMachineEncode/m6` |
| speed at ~raw bytes | 1 (`NL2`) | O(1) step, `V = N` | `E_V 1.000`, 4 KiB 521 ns |

## Choosing a profile

The profile is a point on the `E_V ↔ latency` frontier; there is no auto-search
(the joint optimizer is out of scope — `docs/SCOPE.md`).

- **`Root`** — `E_V = 1.000` for machine 1, `≈0.2` for machine 2 on compressible
  data. Best bytes-per-speed for streaming; the default.
- **`Sparse{K}`** — a checkpoint every `K` symbols. Cheap (`E_V 1.031` at K=64)
  and bounds machine-1 random-access replay, but a `ByteAt` still replays from the
  nearest checkpoint (`4 KiB 9,527 ns` at K=64).
- **`Path{D}`** — an exact path bloom + certificate. `E_V 3.000` at D=4 buys fast
  bulk (`14,398 MB/s`) and indexed ranges; the strongest general accelerator.
- **`Full`** — a machine-1 history. Best random-access on machine 1 (`ByteAt`
  30 ns — O(1) state) at the cost of `E_V 5.000`; on machine 2 (`m2_full`) it is
  `E_V 4.198` and gives O(1) 4 KiB reads (230 ns vs 3,577 ns).

Rule of thumb: **`Root` unless random reads dominate; then `Path` (general) or
`Full` (machine 1).** `Sparse` is for bounded replay with a tiny byte cost.

## Honest losses

- **Versus raw bytes.** A positional slab reads a 4 KiB block in ~70 ns; EKBLOOM's
  best indexed read is ≈230 ns–9.5 µs and machine-2 decode is O(offset) work. The
  codec is not faster than raw on hot reads — it is the same bytes-or-less with
  bounded work and a corruption contract.
- **Versus LZ4/Zstd.** In the court (`docs/COURTS.md`), machine 9 is a **space**
  win (0.598× raw) at a **decode** cost (~3.5 µs/record); block LZ4 cannot compress
  a 100-byte record and plain zstd expands one. It is not a speed win over
  Zstandard-with-dictionary.
- **Versus the Rust reference.** SIMD decode, `io_uring` batch reads and cached
  handles are faster there, as tabulated in `docs/SCOPE.md`. The port trades those
  for determinism and portability.

## Regression history

Performance is tracked over time, not asserted once. `scripts/bench-history.sh`
appends one JSON line — commit, Go version, arch, `runs`, `statistic`, and the
**median `ns/op` across `EKB_RUNS` runs** (with min/max) of the hot benchmarks — to
`evidence/bench-history.jsonl`, and CI appends a record on every run (uploaded as
an artifact). Diff the last line against a previous one to see a regression; because
each value is already a run-to-run median, the remaining line-to-line spread is the
reproducibility of the median, not of a single run.

```sh
sh scripts/bench-history.sh     # appends and prints the full history
tail -n 5 evidence/bench-history.jsonl
```

## Profiling

`scripts/profile.sh` produces storage-review-grade profiles with everything running
inside the OOM-capped sandbox (`scripts/sandbox.sh`, 2 GiB): it builds a test binary,
runs the m1/m2/m9 encode and path-bloom read benchmarks with `-test.cpuprofile` and
`-test.memprofile`, and renders the artifacts with `go tool pprof` (Graphviz `dot`
comes from the sandbox image).

```sh
sh scripts/profile.sh
```

Artifacts, per hot path `<name>` under `evidence/profile/`:

| artifact | what it is | pprof view |
| --- | --- | --- |
| `<name>.prof` / `<name>.mem.prof` | raw CPU / heap profile | `go tool pprof -http=:0 evidence/profile/<name>.prof` |
| `<name>.top` | flat CPU shares | `-top` |
| `<name>.alloc.top` | allocation breakdown | `-top -alloc_space` |
| `<name>.inuse.top` | retained heap | `-top -inuse_space` |
| `<name>.svg` / `<name>.heap.svg` | CPU / heap call graphs | `-svg` |

`evidence/profile.json` consolidates it: per hot path the top allocation sites,
flat CPU shares and retained (inuse) bytes, plus GC-pause count/total/quantiles and
peak heap from a soak-style roundtrip + machine-9 decode workload
(`internal/tools/profstats`, sampling `runtime/metrics` `/gc/pauses:seconds` and
`runtime.MemStats`). `evidence/profile/gcstats.json` is the GC measurement alone.

Rerun the raw micro-benchmarks with `sh scripts/bench.sh`.

## Native arm64 (`evidence/aarch64-native-bench.txt`, 4 vCPU)

| benchmark | amd64 | native arm64 |
| --- | ---: | ---: |
| machine 1 encode, 1 MiB | ≈1.59 GB/s | ≈0.96 GB/s |
| BLAKE3, 1 MiB | ≈316 MB/s | ≈170 MB/s |
| `ByteAt` path bloom | ≈7 ns | ≈13.3 ns |
| `ByteAt` history | ≈4.0 ns | ≈7.4 ns |
| `ResidentView.Get` | ≈1.35 ns | ≈1.77 ns |
| `ReadAt` 4 KiB path bloom | ≈321 ns | ≈881 ns |

Rerun locally with `sh scripts/profile.sh` (CPU + heap + GC profiles) and
`sh scripts/bench.sh`.
