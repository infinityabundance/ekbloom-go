# Testing and trust infrastructure

## Unit, compatibility and concurrency

```sh
go test ./...                 # all packages
go test -race .               # the 64-reader path, race-detector clean
go vet ./...
```

- `compat_test.go` — byte-for-byte identity with Rust fixtures (ByteAt, ReadAt,
  PhysicalBytes, Machine.Encode).
- `corruption_test.go` — 100% single-bit-flip detection per covered structure,
  and localized `Corrupt { Section, Offset }`.
- `roundtrip_test.go` — Go write/read round-trip for every machine and profile;
  64 concurrent readers under `-race`.
- `stream_test.go` — Rust stream golden identity + Go stream round-trip.
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
sh scripts/bench.sh               # writes bench.json
```

## Cross-filesystem / cross-architecture determinism

The format is defined little-endian and depends only on BLAKE3 and CRC-32, both
of which are pure functions with no host dependence. To confirm on your hosts:

```sh
# identical physical_bytes and identical decode across filesystems/architectures
GOARCH=amd64 go test -run TestGoRoundTrip .
GOARCH=arm64 go test -run TestGoRoundTrip .   # (with an arm64 runner or qemu)
```

Determinism is a design property (no floating point, no locale, no map iteration
ordering on any wire path); add a CI matrix job per architecture to enforce it.

## Reproduce

```sh
sh scripts/reproduce.sh           # clean build + full tests + JSON summary
sh scripts/roundtrip.sh           # Go <-> Rust bidirectional identity
```

`scripts/roundtrip.sh` requires the Rust toolchain (and network on first run) to
build the reference in `.reference/fixturegen`. Everything else is Go-only.
