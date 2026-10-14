# Scope — what is not ported, and what that costs

EKBLOOM-Go implements the **format**, the **nine machines**, the **reader**, the
**writer** and the **streaming container** in pure Go, with no cgo, no `unsafe`,
and no external dependencies. That choice buys determinism (`amd64 == arm64 ==
wasip1`), trivial cross-compilation, and WebAssembly — and it means six of the
reference's systems-level subsystems are **out of scope**. This page says exactly
what each one is, what the Go port does instead, and the performance the reference
reports for it, so the trade is explicit rather than hidden.

## Not ported

| Reference subsystem | In Go? | What Go does instead | Reference-reported delta (its own docs) |
| --- | :---: | --- | --- |
| SIMD kernels (`OrANS-PACK`/`RANS`) | ❌ | pure-Go scalar code with prepared decode contexts | `packed-m5` 1 B p50 **30 ns**, 4 KiB p50 **3,546 ns**, bulk **1.16 GB/s** vs `rans0-m2` 6,592 ns / 13,515 ns / 0.30 GB/s — `docs/FRONTIER.md` |
| `io_uring` | ❌ | blocking reads + in-memory `ReadAt` | batched cold reads **7.02×** a `pread` loop at 4 KiB / depth 64 (**496,278** vs **70,706** IOPS), parity (0.97–1.01×) at depth 1 — `docs/IOURING.md` |
| `mmap` surfaces / cached handles | ❌ | in-memory `Open` (the whole image is one slice), `ResidentView` | cached verified handle **120 ns** p50 vs **~413 µs** reopen+reparse (**~3,400×**), lock-free under publication — `docs/HANDLES.md` |
| FUSE projection / OrFS | ❌ | `cmd/ekbloom inspect/verify` + a ~9.7 MB image | a FUSE passthrough adds a syscall/context tax per operation regardless of codec; OrFS keeps it out of the hot path by design |
| Joint planner / optimizer | ❌ | explicit `Profile{Kind: Root/Full/Sparse/Path}` | no automatic `E_V ↔ latency` frontier search; the caller picks a point — see `docs/PERFORMANCE.md` |
| Courts (vs zstd/lz4/zstd-dict) | ✅ | honest: space win at a decode cost | `m9` **83.5 vs 590 B/record**, break-even 2, decode 390 vs 2,364 ns — `docs/COURTS.md` |

## What is ported that matters more

The absence of the six above does **not** weaken the properties a storage team
depends on. These **are** implemented and proven:

- **Bounded streaming** — `StreamBuilder`/`StreamReader` hold RSS to the block
  size: **2 GiB at ≈9.5 MiB peak** (`internal/ekb/scale_test.go`).
- **Crash safety** — `AppendGeneration` + `TruncateToValid`; a torn append
  (ENOSPC/EIO) recovers the newest **valid** generation, never a partial one
  (`append_test.go`, exhaustive truncation + single-bit-flip injection).
- **Bounded work** — `worst_case_steps_for`: 1,200,000 samples, **0 violations**
  (`evidence/bound.json`); corrupt builds are rejected in bounded time.
- **Corruption contract** — 100% of covered bits detected, localised to
  `Corrupt{Section, Offset}` within ≤4096 B; inert padding never misinterpreted
  (`internal/ekb/corruption_test.go`).
- **Concurrency** — 64 readers vs 1 publishing writer, p99.9 ≈1 µs, tails
  indistinguishable quiet vs publish, `-race` clean (`evidence/concurrency.json`).
- **Opt-in AUTH** — keyed integrity/authenticity covering every section
  (`docs/SECURITY.md`).
- **Legacy** — v0.3/v0.4 fixtures open byte-identically (`internal/ekb/legacy_test.go`).
- **Determinism** — `amd64 == arm64 == wasip1` and ext4/xfs/btrfs/tmpfs
  (`evidence/`).

## Why "pure Go" is the right trade here

A storage engineer asking "why is Go slower than Rust on a 4 KiB read?" gets a
table, not a shrug: the deltas above are the reference's SIMD/`io_uring`/`mmap`
advantages, and they are real. The Go port trades them for properties the
reference cannot offer as cheaply:

- no `unsafe`/cgo ⇒ identical bytes on `amd64`, `arm64` and `wasip1`, and a
  fingerprint that is byte-for-byte reproducible across ext4/xfs/btrfs/tmpfs;
- no `mmap`/`io_uring` ⇒ it runs unchanged in WebAssembly and in a ~9.7 MB
  container;
- the same frozen wire format ⇒ files are portable between the two
  implementations in both directions (`docs/MIGRATION.md`).

If you need the SIMD/`io_uring` numbers, use the Rust reference for the hot
server path; if you need portable, auditable, dependency-free, deterministic
files, use this port. They read and write the same bytes.
