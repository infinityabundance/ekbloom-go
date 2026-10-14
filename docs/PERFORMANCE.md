# Performance and tuning

Everything here is measured on one host (AMD Ryzen 7 9800X3D, Go 1.27, scalar, no
SIMD) unless marked native arm64, and is a decision aid, not a portable
benchmark. The raw logs are `evidence/sweep.json`, `evidence/bench.txt`,
`evidence/aarch64-native-bench.txt`, `evidence/byteat.json`, `evidence/courts.json`
and `evidence/corpus-compare.log`.

## Profile frontier (`evidence/sweep.json`)

One fixed 1 MiB compressible source (4-symbol 60/25/10/5 alphabet), all profiles:

| profile | machine | physical B | `E_V` | `ByteAt` p50 | 4 KiB read p50 | bulk seq |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| `m1_root` | 1 | 1,048,576 | **1.000** | 40 ns | **280 ns** | **14,126 MB/s** |
| `m2_root` | 2 | 208,084 | **0.198** | 110 ns | 351 ns | 11,455 MB/s |
| `m1_full` | 1 | 5,242,884 | 5.000 | 30 ns | 2,725 ns | 1,502 MB/s |
| `m1_sparse64` | 1 | 1,081,346 | 1.031 | 100 ns | 9,067 ns | 457 MB/s |
| `m1_path4` | 1 | 3,145,963 | 3.000 | 120 ns | 1,142 ns | 6,267 MB/s |
| `m2_full` | 2 | 4,402,392 | 4.198 | 30 ns | 421 ns | 5,726 MB/s |

`ByteAt` here includes a `time.Now` per sample (~30 ns floor); the true single-op
cost is in `evidence/bench.txt` (path bloom ≈2.3 ns, history ≈3.8 ns,
materialised ≈5.3 ns).

## Choosing a machine

| workload | machine | why | measured |
| --- | --- | --- | --- |
| any bytes, never expand | 2 (`RANS0`) → 3 fallback | order-0 rANS; falls back to store when it would expand | `m2_root E_V 0.198` on skew; `auto` keeps `V ≤ N` |
| skewed, many small records | 9 (`OrANS-DICT`) | one shared trained dictionary, header-less records | **83.5 vs 590 B/record**, break-even 2 (`docs/DICTIONARY.md`) |
| O(1) random access, fixed alphabet | 5 (`PACK`) | direct `bits`-wide addressing, no index, no replay | reference `packed-m5` 1 B 30 ns / 4 KiB 3,546 ns / 1.16 GB/s (`docs/FRONTIER.md`) |
| heterogeneous objects | 6 (`SEG`) | per-segment constant/raw/coded choice | `BenchmarkMachineEncode/m6` |
| speed at ~raw bytes | 1 (`NL2`) | O(1) step, `V = N` | `E_V 1.000`, 4 KiB 280 ns |

## Choosing a profile

The profile is a point on the `E_V ↔ latency` frontier; there is no auto-search
(the joint optimizer is out of scope — `docs/SCOPE.md`).

- **`Root`** — `E_V = 1.000` for machine 1, `≈0.2` for machine 2 on compressible
  data. Best bytes-per-speed for streaming; the default.
- **`Sparse{K}`** — a checkpoint every `K` symbols. Cheap (`E_V 1.031` at K=64)
  and bounds machine-1 random-access replay, but a `ByteAt` still replays from the
  nearest checkpoint (`4 KiB 9,067 ns` at K=64).
- **`Path{D}`** — an exact path bloom + certificate. `E_V 3.000` at D=4 buys fast
  bulk (`6,267 MB/s`) and indexed ranges; the strongest general accelerator.
- **`Full`** — a machine-1 history. Best random-access on machine 1 (`ByteAt`
  30 ns — O(1) state) at the cost of `E_V 5.000`; on machine 2 (`m2_full`) it is
  `E_V 4.198` and gives O(1) 4 KiB reads (421 ns vs 2,725 ns).

Rule of thumb: **`Root` unless random reads dominate; then `Path` (general) or
`Full` (machine 1).** `Sparse` is for bounded replay with a tiny byte cost.

## Honest losses

- **Versus raw bytes.** A positional slab reads a 4 KiB block in ~70 ns; EKBLOOM's
  best indexed read is ≈280 ns–1.1 µs and machine-2 decode is O(offset) work. The
  codec is not faster than raw on hot reads — it is the same bytes-or-less with
  bounded work and a corruption contract.
- **Versus LZ4/Zstd.** In the court (`docs/COURTS.md`), machine 9 is a **space**
  win (0.598× raw) at a **decode** cost (~2.4 µs/record); block LZ4 cannot compress
  a 100-byte record and plain zstd expands one. It is not a speed win over
  Zstandard-with-dictionary.
- **Versus the Rust reference.** SIMD decode, `io_uring` batch reads and cached
  handles are faster there, as tabulated in `docs/SCOPE.md`. The port trades those
  for determinism and portability.

## Native arm64 (`evidence/aarch64-native-bench.txt`, 4 vCPU)

| benchmark | amd64 | native arm64 |
| --- | ---: | ---: |
| machine 1 encode, 1 MiB | ≈1.60 GB/s | ≈0.96 GB/s |
| BLAKE3, 1 MiB | ≈324 MB/s | ≈170 MB/s |
| `ByteAt` path bloom | ≈2.3 ns | ≈13.3 ns |
| `ByteAt` history | ≈3.8 ns | ≈7.4 ns |
| `ResidentView.Get` | ≈1.35 ns | ≈1.77 ns |
| `ReadAt` 4 KiB path bloom | ≈350 ns | ≈881 ns |

Rerun locally with `sh scripts/profile.sh` (CPU profiles) and
`sh scripts/bench.sh`.
