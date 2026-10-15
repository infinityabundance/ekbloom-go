# Testing and trust infrastructure

## Unit, compatibility and concurrency

The engine (and its tests) live in `internal/ekb`; the root package is the public
API facade. Tests write evidence to the repository's `evidence/` and read
`testdata/` via helpers that resolve the repo root, so they are independent of the
working directory.

```sh
go test ./...                 # all packages
go test -race ./internal/ekb  # the 64-reader path, race-detector clean
go vet ./...
```

- `internal/ekb/compat_test.go` — byte-for-byte identity with Rust fixtures (ByteAt, ReadAt,
  PhysicalBytes, Machine.Encode).
- `internal/ekb/fastpath_test.go` — accelerated reads (path bloom / history / checkpoints)
  match the authoritative decode and avoid materialising the symbol array.
- `auth_test.go` — verifies a Rust-written AUTH fixture; Go AUTH round-trip;
  wrong/stripped key rejected.
- `append_test.go` — additive generations, torn-write fault injection (every
  truncation of an appended image recovers the newest valid generation), and
  in-place EIO (every single-bit flip in the appended region is recovered exactly
  or rejected with a typed error; a flip in a section payload falls back to the
  newest valid generation).
- `internal/ekb/corruption_test.go` — 100% single-bit-flip detection per covered structure,
  and localized `Corrupt { Section, Offset }`.
- `internal/ekb/metrics_test.go` — the per-object metrics snapshot and the Prometheus/JSON
  resource-policy exports, including the 1 GiB allocation-cap breach counter.
- `internal/ekb/sweep_test.go` — the writer-profile frontier (`Root`/`Full`/`Sparse`/`Path`) on
  one fixed source: `E_V`, `byte_at`, 4 KiB and bulk, written to
  `evidence/sweep.json` (see `docs/PERFORMANCE.md`).
- `internal/ekb/roundtrip_test.go` — Go write/read round-trip for every machine and profile;
  64 concurrent readers under `-race`.
- `internal/ekb/stream_test.go` / `internal/ekb/scale_test.go` — Rust stream golden identity; the 2 GiB
  bounded-memory build (opt-in via `EKB_RUN_SCALE=1`).
- `example_test.go` — runnable examples (open, dictionary, shared-dictionary
  write/open, streaming, corruption).
- `internal/ekb/dictbench_test.go` — the machine-9 P5 study: one shared dictionary vs
  per-record machine 2 (83.5 vs 590.0 B/record, break-even 2 records) and the
  reference-framed `WriteBodyWithDict`/`OpenWithDict` round-trip
  (`evidence/dictionary.json`).

## Fault injection, crash-safety and endurance

The append-only generation path is the crash-safety story: an appended generation
never rewrites earlier bytes, so a torn or corrupt append must never invalidate the
newest *valid* generation. Two harnesses prove it inside the sandbox:

- `internal/ekb/append_test.go` — exhaustive single-fault proofs on a two-generation
  file: every truncation of the appended region recovers the newest valid generation
  (the previous commit); every single-bit flip is recovered exactly or rejected with
  a typed `*Error`; and a flip in an appended section payload falls back to the
  newest valid generation.
- `internal/ekb/endurance_test.go` (`TestEndurance`) — a long-run fault-injection
  log. Each round appends a generation to a live chain, damages it with one of a
  deterministic taxonomy of faults, asserts `Open` returns *exactly* the newest
  surviving valid generation (its generation number, its accumulated manifest
  profile stack and the exact object bytes) or rejects with a typed error, then
  repairs with `TruncateToValid` and continues. The taxonomy (counted per kind in
  `evidence/endurance.json`) is:

  | fault | models |
  | --- | --- |
  | `trunc_body` | ENOSPC/EIO truncating at a random offset inside the newest generation |
  | `trunc_manifest` | ENOSPC mid-manifest |
  | `trunc_footer` | a torn commit (truncated footer) |
  | `flip_body` | in-place EIO in the newest non-manifest payload |
  | `flip_section_header` | a bit flip in the newest section header (Offset/Length) |
  | `flip_manifest` | a bit flip in the newest manifest payload |
  | `flip_footer` | a bit flip in the newest commit footer |
  | `old_struct_flip` | damage to an OLDER generation's manifest — the chain must fall back to the newest generation *before* it, never a stale one |
  | `old_benign_flip` | a bit flip in an older generation's reserved, unvalidated section-header `Flags` field — the NEWER valid generation must still be selected |
  | `truncate_head` | a power loss before the first durable block (header/core truncated) — the whole image is unrecoverable and must be a typed error |

  Every round also asserts the repaired prefix from `TruncateToValid` is byte-for-byte
  the true clean generation it claims to be. The concurrent-reader phase runs k
  goroutines reading (`ReadAt`/`ByteAt`, one also walks the commit chain) a live image
  while a writer appends, tears and repairs it, and requires zero torn reads under
  `-race`. `garbage` (a stale/wrong generation, a wrong payload, a non-typed error or
  a panic) must be `0`.

```sh
sh scripts/endurance.sh                            # short bounded run (default 2000 rounds)
ENDURANCE_ROUNDS=200000 sh scripts/endurance.sh    # longer run
```

`evidence/endurance.json` records the per-kind counts, `recovered_exact`,
`rejected_typed`, `garbage`, the peak heap and the concurrent-reader result;
`evidence/endurance.log` is the human-readable fault-injection log.

### Boundary: what an unprivileged sandbox cannot test

A true power-fail endurance test — `dm-flakey`/`dm-error` fault layers, `fsstress`
under a real journaling/reflink filesystem (`xfs`/`zfs`/`bcachefs`), and device-level
flush/reorder fault injection — needs privileges the sandbox does not have
(`CAP_SYS_ADMIN`, device-mapper, module loading, real block devices). The sandbox
runs as an unprivileged, OOM-capped container, so this harness is the strongest
*unprivileged* equivalent: it injects the same byte-level damage those mechanisms
produce (mid-write truncation and bit flips, mid-manifest and torn-footer cuts,
older-generation corruption) directly into the in-memory image and proves the
recovery / typed-rejection contract. What it does **not** exercise: the kernel's
block layer, the filesystem's own atomicity/flush ordering, `fsync` loss, and
device-level reordering or latent sector corruption. Those are covered (partially)
by the CI `fsmatrix` job, which uses the runner's passwordless sudo to exercise real
ext4/xfs/btrfs/tmpfs filesystems — filesystem-level, on loopback images, never a
dedicated device.

## Fuzzing

Native Go fuzzing targets in `internal/ekb/fuzz_test.go`:

- `FuzzOpenBytes` — `Open` over arbitrary bytes.
- `FuzzOpenAuthenticatedBytes` — `OpenAuthenticated` over arbitrary bytes under a
  **fixed** 32-byte key, plus a second input-derived (wrong) key, so both the
  authenticated-valid and the AUTH-mismatch paths run. Invariant: it never panics
  and only ever returns a typed `*Error` (never garbage bytes).
- `FuzzStreamBytes` — builds *and* reads a whole stream per execution.
- `FuzzMachineRoundTrip` — nine machines' encode/decode invertibility per execution.

Every fuzz run goes through the OOM-capped sandbox — **no `go` runs on the host**:

```sh
FUZZTIME=30s sh scripts/fuzz.sh                                           # all targets, 30s each
FUZZTIME=60s FUZZTIME_FuzzOpenAuthenticatedBytes=30m sh scripts/fuzz.sh    # the long campaign
FUZZTARGETS=FuzzStreamBytes FUZZTIME=20m sh scripts/fuzz.sh                # one target
```

`scripts/fuzz.sh` runs each `go test -fuzz` inside `scripts/sandbox.sh`
(`--memory=2g --memory-swap=2g --cpus=2 --pids-limit=512`, `GOMAXPROCS=2`), so a
runaway fuzzer is OOM-killed inside the container and can never take down the
host. The per-target budget defaults to `FUZZTIME`; `FUZZTIME_<target>` overrides
it for a single target. The accumulated corpus is published from the **sandbox's**
`GOCACHE` volume (the `ekbloom-gocache` named volume), never a host cache, to
`evidence/fuzz/corpus/`, and `evidence/fuzz.json` records per-target execs and
PASS/FAIL.

The primary entry point **`FuzzOpenBytes` has been driven past the 1B-execution
bar**: 1,131,205,714 executions in 30 minutes with no crashes, logged verbatim at
`evidence/fuzz/FuzzOpenBytes-1b.log` (that historical run predates the
sandbox-mandatory change, so it used the host's workers). The mandatory sandbox
caps a target to 2 CPUs, so a 30-minute sandboxed run of one target reaches ~1e8
executions, not 1e9: in the sandboxed campaign `FuzzOpenAuthenticatedBytes`
reached ~132M executions in 30 minutes. `FuzzStreamBytes` (which builds *and*
reads a whole stream per execution) and `FuzzMachineRoundTrip` (nine machines per
execution) are structurally far heavier per execution and are bounded-fuzzed with
`FUZZTIME`/`FUZZTARGETS` rather than driven to 1B.

## Benchmarks

```sh
go test -bench . -benchmem .
sh scripts/bench.sh               # writes bench.txt (median-of-runs, sandboxed)
sh scripts/bench-history.sh       # append a median-of-runs record to bench-history.jsonl
sh scripts/profile.sh             # CPU profiles: .prof + SVG call graph + top
sh scripts/courts.sh              # 100-byte 80/20 court + 4 KiB point read + YCSB (separate module)
sh scripts/remeasure.sh           # re-capture the whole timing set at EKB_RUNS=5, prints loadavg/cpu_idle_pct
```

Every timing artifact is **provenance-stamped** — a top-level `"provenance"`
object with the toolchain, caps, image digest, `loadavg`, and `cpu_idle_pct` seen
at measure time (`internal/tools/provenance`) — so a reviewer can tell whether a
number came from an idle or a contended host. Read `loadavg` alongside
`cpu_idle_pct`, since a container's `/proc/loadavg` is the host's and counts
I/O-wait; `_min` is the least-contended estimate. `evidence/environment.txt` is
*generated* by `scripts/environment.sh` (inside the sandbox), so it cannot go
stale.

## Cross-filesystem / cross-architecture determinism

```sh
sh scripts/cross-fs.sh            # ext4/btrfs/tmpfs fingerprints (evidence/cross-fs.json)
sh scripts/profile.sh             # CPU profiles (SVG + top) for m1/m2/m9 + path bloom
sh scripts/container-aarch64.sh   # full suite + fingerprints on linux/arm64 under qemu
```

Determinism is proven at runtime, not merely asserted:

- **Cross-filesystem.** `scripts/cross-fs.sh` copies the fixtures to every
filesystem type it can reach and confirms the fingerprint (machine id,
`physical_bytes`, and a SHA-256 over the fully decoded bytes) is identical
(`evidence/cross-fs.json`, `evidence/cross-fs.log`). A rootless host reaches the
repository's own **ext4** plus **tmpfs** (and a real **btrfs** mount where one is
mounted); when privileged (root or passwordless sudo) it additionally creates
loopback **ext4/xfs/btrfs** images. The CI `fsmatrix` job uses the runner's
passwordless sudo to exercise **all four (ext4, xfs, btrfs, tmpfs)**. A type that
cannot be reached is reported as "not exercised", never silently skipped. `zfs`
and `bcachefs` are not exercised (no module/mount is available on a hosted runner,
and creating them needs a kernel module); a dedicated device partition is likewise
unavailable, so xfs is exercised on a loopback image — a real xfs filesystem,
filesystem-level, without a separate block device.
- **Cross-architecture (native).** The CI `arm64-native` job runs the suite and
benchmarks on real arm64 hardware (`ubuntu-24.04-arm`) and requires its
fingerprint to equal the recorded amd64 fingerprint. `scripts/container-aarch64.sh`
additionally checks amd64 == arm64 under qemu.
- **WebAssembly.** The CI `wasm` job builds `wasip1` and `js/wasm`, runs the
**entire test suite under wasip1** (`evidence/wasip1-test.log`), and requires the
WASI fingerprint to equal amd64/arm64 — `amd64 == arm64 == wasip1`
(`evidence/wasip1-fingerprint.txt`).
- **Cross-architecture.** `scripts/container-aarch64.sh` runs the full suite on
`linux/arm64` under qemu and compares fingerprints; `evidence/aarch64-determinism.json`
records `identical: true` with **equal** amd64/arm64 fingerprint SHA-256s over the
same 34 fixtures — the Go analogue of the reference `aarch64-determinism.json`.

The format depends only on BLAKE3, CRC-32 and integer arithmetic, all pure
functions with no host dependence (no floating point, no locale, no map-iteration
ordering on any wire path).

## Compatibility, legacy and versioning

- `testdata/legacy/` are the reference's **frozen** v0.3/v0.4 fixtures
  (`core_version = 0`), ported from `.reference/ekbloom-rust/tests/fixtures/` with
  their `expected.json`. `internal/ekb/legacy_test.go` ports the reference's `compat_fixtures`
  test: it checks the whole-file BLAKE3, the header fields, `physical_bytes`, the
  sampled `byte_at` values and their BLAKE3, `VerifyDeep`, and byte-for-byte
  immutability; a tampered copy is refused; and `TestVersionTagsRefused` proves a
  future `format_minor` and an unknown `core_version` are typed refusals. This is
  the versioning story: **Go opens legacy files exactly.**
- `TestWorstCaseStepsBound` samples 1.2M `(offset, length)` requests and asserts
  `length <= bound <= off + length` with **0 violations** (`evidence/bound.json`).

## Fuzzing (campaign)

```sh
FUZZTIME=30s sh scripts/fuzz.sh    # logs + corpus under evidence/fuzz/
FUZZTIME=60s FUZZTIME_FuzzOpenAuthenticatedBytes=30m sh scripts/fuzz.sh    # the long campaign
```

`evidence/fuzz.json` records the sandboxed campaign: per-target execs and `ok`,
the corpus entry count, and the long-campaign block. In the campaign
`FuzzOpenBytes` reached 15.8M executions (3m), `FuzzOpenAuthenticatedBytes`
132.4M (30m), `FuzzStreamBytes` 27.5k and `FuzzMachineRoundTrip` 255.4k (both
heavy per execution) — all `PASS`, no crashes; 135 corpus entries were published
under `evidence/fuzz/corpus/`, copied out of the sandbox `GOCACHE` volume so it is
reproducible and reviewable. `FuzzOpenBytes`'s historical 1.13B executions remain
recorded in the `campaign` block (`evidence/fuzz/FuzzOpenBytes-1b.log`).

## Reproduce

```sh
sh scripts/ci.sh                  # every gate + captured logs under evidence/
sh scripts/reproduce.sh           # clean build + full tests + JSON summary
sh scripts/setup-reference.sh     # clone the pinned Rust reference into .reference/
sh scripts/roundtrip.sh           # Go <-> Rust bidirectional identity
sh scripts/cross-fs.sh            # cross-filesystem determinism
sh scripts/corpus.sh              # standard corpora E_V (Go vs Rust, same files)
sh scripts/container-reproduce.sh # everything inside a ~10 MB Docker image
sh scripts/verify-external.sh     # pull the published image, run the suite, no clone
sh scripts/wasm.sh                # WebAssembly build + amd64 == arm64 == wasip1
sh scripts/container-aarch64.sh   # amd64 == arm64 runtime determinism
sh scripts/endurance.sh           # long-run append/damage/repair endurance + concurrent readers
sh scripts/wasm.sh                # WebAssembly build + amd64 == arm64 == wasip1
sh scripts/courts.sh              # skewed small-record court
```

`scripts/roundtrip.sh` requires the Rust toolchain; from a fresh clone it
bootstraps itself by calling `scripts/setup-reference.sh`, which clones the Rust
reference at a pinned commit and stages the committed `tools/fixturegen` crate.
Everything else is Go-only.

`scripts/corpus.sh` downloads the standard corpora (Canterbury, Silesia, enwik8)
into the git-ignored `corpus/` and measures `E_V = physical_bytes / N` and
single-byte read latency for machines 1–2 on identical files. When the Rust
toolchain is present it also runs the Rust reference on the **same** files and
requires `physical_bytes` to be identical (`evidence/corpus.json`,
`evidence/corpus-rust.json`, `evidence/corpus-compare.log`).
