# Testing and trust infrastructure

## Unit, compatibility and concurrency

```sh
go test ./...                 # all packages
go test -race .               # the 64-reader path, race-detector clean
go vet ./...
```

- `compat_test.go` — byte-for-byte identity with Rust fixtures (ByteAt, ReadAt,
  PhysicalBytes, Machine.Encode).
- `fastpath_test.go` — accelerated reads (path bloom / history / checkpoints)
  match the authoritative decode and avoid materialising the symbol array.
- `auth_test.go` — verifies a Rust-written AUTH fixture; Go AUTH round-trip;
  wrong/stripped key rejected.
- `append_test.go` — additive generations, and torn-write fault injection (every
  truncation of an appended image recovers the newest valid generation).
- `corruption_test.go` — 100% single-bit-flip detection per covered structure,
  and localized `Corrupt { Section, Offset }`.
- `roundtrip_test.go` — Go write/read round-trip for every machine and profile;
  64 concurrent readers under `-race`.
- `stream_test.go` / `scale_test.go` — Rust stream golden identity; the 2 GiB
  bounded-memory build (opt-in via `EKB_RUN_SCALE=1`).
- `example_test.go` — runnable examples (open, dictionary, shared-dictionary
  write/open, streaming, corruption).
- `dictbench_test.go` — the machine-9 P5 study: one shared dictionary vs
  per-record machine 2 (83.5 vs 590.0 B/record, break-even 2 records) and the
  reference-framed `WriteBodyWithDict`/`OpenWithDict` round-trip
  (`evidence/dictionary.json`).

## Fuzzing

Native Go fuzzing targets in `fuzz_test.go`:

```sh
go test -fuzz FuzzOpenBytes       .
go test -fuzz FuzzStreamBytes     .
go test -fuzz FuzzMachineRoundTrip .
```

A short campaign (a few seconds per target, tens of thousands to millions of
executions) passes with no crashes; the seed/interesting corpus lives in
`evidence/fuzz/`. For the long campaign (the reference ran 1B+ executions per
target), run each target with a long `-fuzztime` under `scripts/fuzz.sh`.

The primary entry point **`FuzzOpenBytes` has been driven past the 1B-execution
bar**: 1,131,205,714 executions in 30 minutes with no crashes, logged verbatim at
`evidence/fuzz/FuzzOpenBytes-1b.log`. `FuzzStreamBytes`/`FuzzMachineRoundTrip`
are run for the short duration by default; extend them the same way.

## Benchmarks

```sh
go test -bench . -benchmem .
sh scripts/bench.sh               # writes bench.txt
sh scripts/courts.sh              # the skewed small-record court (separate module)
```

## Cross-filesystem / cross-architecture determinism

```sh
sh scripts/cross-fs.sh            # disk vs tmpfs fingerprints (evidence/cross-fs.log)
sh scripts/container-aarch64.sh   # full suite + fingerprints on linux/arm64 under qemu
```

Determinism is proven at runtime, not merely asserted:

- **Cross-filesystem.** `scripts/cross-fs.sh` copies the fixtures to a tmpfs and
to the repository's on-disk filesystem and confirms the fingerprint (machine id,
`physical_bytes`, and a SHA-256 over the fully decoded bytes) is identical
(`evidence/cross-fs.log`).
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
  their `expected.json`. `legacy_test.go` ports the reference's `compat_fixtures`
  test: it checks the whole-file BLAKE3, the header fields, `physical_bytes`, the
  sampled `byte_at` values and their BLAKE3, `VerifyDeep`, and byte-for-byte
  immutability; a tampered copy is refused; and `TestVersionTagsRefused` proves a
  future `format_minor` and an unknown `core_version` are typed refusals. This is
  the versioning story: **Go opens legacy files exactly.**
- `TestWorstCaseStepsBound` samples 1.2M `(offset, length)` requests and asserts
  `length <= bound <= off + length` with **0 violations** (`evidence/bound.json`).

## Fuzzing (campaign)

```sh
FUZZTIME=45s sh scripts/fuzz.sh    # logs + corpus under evidence/fuzz/
FUZZTIME=30m sh scripts/fuzz.sh    # the long campaign (the reference ran 1B/target)
```

`evidence/fuzz.json` records both runs: the 45 s smoke run (`FuzzOpenBytes` 26.0M
execs, `FuzzStreamBytes` 146k, `FuzzMachineRoundTrip` 170k; 118 corpus entries
published under `evidence/fuzz/corpus/`) and the long `FuzzOpenBytes` campaign
(1.13B execs, `PASS`). The corpus is copied out of the Go build cache so it is
reproducible and reviewable.

## Reproduce

```sh
sh scripts/ci.sh                  # every gate + captured logs under evidence/
sh scripts/reproduce.sh           # clean build + full tests + JSON summary
sh scripts/setup-reference.sh     # clone the pinned Rust reference into .reference/
sh scripts/roundtrip.sh           # Go <-> Rust bidirectional identity
sh scripts/cross-fs.sh            # cross-filesystem determinism
sh scripts/corpus.sh              # standard corpora E_V (Go vs Rust, same files)
sh scripts/container-reproduce.sh # everything inside a ~10 MB Docker image
sh scripts/container-aarch64.sh   # amd64 == arm64 runtime determinism
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
