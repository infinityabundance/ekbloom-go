# EKBLOOM-GO

A **pure-Go** implementation of the EKBLOOM concealed-expansion codec, wire- and
byte-compatible with the Rust reference implementation
([`infinityabundance/EKBLOOM`](https://github.com/infinityabundance/EKBLOOM)).

- **No cgo. No `unsafe`.** The format, all nine machines, the reader, the writer,
  the dictionary and the streaming container are written in portable Go. The only
  low-level primitive is a self-contained pure-Go BLAKE3 (`internal/blake3`),
  pinned to the Rust `blake3` crate by generated vectors.
- **Proven the same format.** Every `.ekb` and `.stream` file in `testdata/` was
  written by the Rust reference; `go test` proves `ByteAt`, `ReadAt`,
  `PhysicalBytes` and `Machine.Encode` are byte-for-byte identical. The reverse
  direction is proven by `scripts/roundtrip.sh`: Go-written files are opened and
  fully decoded by the Rust reference.
- **Typed errors, never a panic.** Every failure is an `*ekbloom.Error`; use
  `errors.Is` with the exported sentinels or `CorruptDetail` to read a corruption's
  section and offset.

## Status at a glance

| Area | State | Evidence |
| --- | --- | --- |
| `.ekb` container (header, sections, footer, manifest) | ✅ identical | `compat_test.go`, `scripts/roundtrip.sh` |
| Machines 1–9 core codecs | ✅ identical (bit-exact encode/decode) | `compat_test.go` |
| Reader API (`Open`, `ByteAt`, `ReadAt`, `PhysicalBytes`, `Estimate`, `WorstCaseStepsFor`) | ✅ | `reader.go`, `roundtrip_test.go` |
| Writer (`WriteBody`: root/full/sparse) | ✅ Rust-readable | `writer.go`, `scripts/roundtrip.sh` |
| Shared dictionaries (machine 9) | ✅ | `dictionary.go`, `compat_test.go` |
| Corruption detection (`Corrupt{Section,Offset}`) | ✅ 100% of covered bits, ≤1 structure (≤4096 B) | `corruption_test.go` |
| Streaming (`StreamBuilder`/`StreamReader`, bounded RSS) | ✅ identical | `stream_test.go`, `scripts/roundtrip.sh` |
| Resource policy (1 GiB default cap + breach counter) | ✅ | `resource.go` |
| Fuzzers (Open, Stream, machine roundtrip) | ✅ | `fuzz_test.go`, `evidence/fuzz/` |
| Benchmarks (`go test -bench`) | ✅ | `bench_test.go` |
| Cross-arch / cross-FS determinism | ⏭ see `docs/TESTING.md` | — |

## Quick start

```go
data, _ := os.ReadFile("archive.ekb")
r, err := ekbloom.Open(data)
if err != nil { /* typed *ekbloom.Error */ }

b, _ := r.ByteAt(0)              // logical symbol 0
head, _ := r.ReadAt(0, 4096)     // a range
fmt.Println(r.PhysicalBytes())   // manifested bytes (== Rust inspect_file)
fmt.Println(r.Estimate())        // static upper bound
steps, _ := r.WorstCaseStepsFor(0, 4096)
```

The pipeline (all run inside this repository):

```sh
go test ./...                     # unit, compat, corruption, concurrency (-race), stream
go test -bench . -benchmem ./     # micro-benchmarks
go test -fuzz FuzzOpenBytes .     # fuzzing (see docs/TESTING.md for the long campaign)
sh scripts/roundtrip.sh           # Go -> .ekb -> Rust decode, byte-for-byte
sh scripts/reproduce.sh           # clean build + tests, emits JSON
```

## Layout

```
ekbloom.go-free package `ekbloom` at the repository root:
  errors.go        typed errors (Kind, sentinels, CorruptDetail)
  format.go        GenesisHeader, SectionHeader, CommitFooter, ManifestRecord, Scan
  machine*.go      the nine machines (encode/decode/check, prepared contexts)
  dictionary.go    machine-9 shared order-0 dictionaries + registry
  estimate.go      profile labels and the upper-bound estimator
  inspect.go       physical accounting (mirrors the Rust FileBody::inspect)
  reader.go        Open / ByteAt / ReadAt / PhysicalBytes / Estimate / WorstCaseStepsFor
  writer.go        WriteBody (Rust-readable .ekb images)
  verify.go        structural + deep verification with localized Corrupt errors
  stream.go        bounded-memory stream container builder/reader
  resource.go      ResourcePolicy (default 1 GiB) + breach counter
internal/blake3     dependency-free pure-Go BLAKE3 (unkeyed + keyed, streaming)
internal/tools      roundtripgen (writes Go-side files for Rust to verify)
testdata/           Rust-generated .ekb / .stream fixtures + BLAKE3 vectors
docs/               CORE-LAYOUT.md, MIGRATION.md, SECURITY.md, OPERATIONS.md, MACHINE_CONTRACT.md, TESTING.md
scripts/            roundtrip.sh, reproduce.sh, bench.sh, fuzz.sh
.reference/         (git-ignored) the Rust reference + the fixture generator
```

## Design notes

EKBLOOM is a *concealed-expansion codec*: it spends manifested physical space to
maximise access speed while preserving exact correctness. The Go port preserves
the parts that make the format interoperable — the frozen wire layout, the nine
machine semantics, the exact physical accounting, and the corruption contract —
and mirrors the reference's correctness-first read path (authoritative machine
decode; the manifested structures are additive accelerations). See
`docs/CORE-LAYOUT.md` for the byte layout and `docs/MACHINE_CONTRACT.md` for how
to add a machine.
