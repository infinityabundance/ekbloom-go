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
- `example_test.go` — runnable examples (open, dictionary, streaming, corruption).

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

## Benchmarks

```sh
go test -bench . -benchmem .
sh scripts/bench.sh               # writes bench.txt
sh scripts/courts.sh              # the skewed small-record court (separate module)
```

## Cross-filesystem / cross-architecture determinism

```sh
sh scripts/cross-fs.sh    # fingerprints identical on disk vs tmpfs; arm64 cross-build
sh scripts/container-aarch64.sh  # full suite + fingerprints on linux/arm64 under qemu
```

The format depends only on BLAKE3, CRC-32 and integer arithmetic, all pure
functions with no host dependence. `scripts/cross-fs.sh` copies the fixtures to a
tmpfs and to the repository's on-disk filesystem and confirms the fingerprint
(machine id, `physical_bytes`, and a SHA-256 over the fully decoded bytes) is
identical; it also cross-builds the fingerprint tool for arm64 (run it under qemu
on an arm64 host to compare outputs).

## Reproduce

```sh
sh scripts/reproduce.sh           # clean build + full tests + JSON summary
sh scripts/roundtrip.sh           # Go <-> Rust bidirectional identity
sh scripts/cross-fs.sh            # cross-filesystem determinism
sh scripts/container-reproduce.sh # everything inside a ~10 MB Docker image
```

`scripts/roundtrip.sh` requires the Rust toolchain (and network on first run) to
build the reference in `.reference/fixturegen`. Everything else is Go-only.
