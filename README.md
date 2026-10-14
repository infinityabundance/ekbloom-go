# EKBLOOM-GO

[![release](https://img.shields.io/github/v/release/infinityabundance/ekbloom-go)](https://github.com/infinityabundance/ekbloom-go/releases)
[![GHCR](https://img.shields.io/badge/ghcr.io-infinityabundance%2Fekbloom--go-blue)](https://github.com/infinityabundance/ekbloom-go/pkgs/container/ekbloom-go)
[![CI](https://github.com/infinityabundance/ekbloom-go/actions/workflows/ci.yml/badge.svg)](https://github.com/infinityabundance/ekbloom-go/actions/workflows/ci.yml)

A **pure-Go** implementation of the EKBLOOM concealed-expansion codec, wire- and
byte-compatible with the Rust reference implementation
([`infinityabundance/EKBLOOM`](https://github.com/infinityabundance/EKBLOOM)).

Container images: `ghcr.io/infinityabundance/ekbloom-go:latest` (runtime CLI) and
`:test` (full suite, no clone needed). See [`docs/INTEGRATIONS.md`](docs/INTEGRATIONS.md)
for the RocksDB and Parquet case studies.

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
| `.ekb` container (header, sections, footer, manifest) | ✅ identical | `internal/ekb/compat_test.go`, `scripts/roundtrip.sh` |
| Machines 1–9 core codecs | ✅ identical (bit-exact encode/decode) | `internal/ekb/compat_test.go` |
| Reader API (`Open`, `ByteAt`, `ReadAt`, `PhysicalBytes`, `Estimate`, `WorstCaseStepsFor`) | ✅ | `internal/ekb/reader.go`, `internal/ekb/roundtrip_test.go` |
| Accelerated reads (path bloom / history / checkpoints) | ✅ O(1) path bloom ≈2.3 ns | `internal/ekb/fastpath.go`, `internal/ekb/fastpath_test.go`, `internal/ekb/bench_test.go` |
| Writer (`WriteBody`/`AppendGeneration`: root/full/sparse/path) | ✅ Rust-readable; path bloom + certificate | `internal/ekb/writer.go`, `internal/ekb/writer_path.go`, `internal/ekb/append.go` |
| Writer parity (universal/regional blooms, tiles) | ✅ Rust-readable shells; Flat-directory TileSet | `internal/ekb/writer_bloom.go`, `internal/ekb/writer_tiles.go`, `internal/ekb/writer_test.go` |
| Legacy v0.3/v0.4 fixtures open byte-identically | ✅ frozen Rust fixtures | `testdata/legacy/`, `internal/ekb/legacy_test.go` |
| Opt-in keyed AUTH (write + verify) | ✅ | `internal/ekb/auth.go`, `auth_test.go`, Rust `open_authenticated` |
| Allocation-free `ResidentView.Get` (zero allocs/op) | ✅ ≈1.35 ns | `internal/ekb/resident.go`, `internal/ekb/resident_test.go` |
| Metrics export (physical_bytes, expansion ratio, breaches) | ✅ | `internal/ekb/metrics.go`, `internal/ekb/metrics_test.go` |
| `worst_case_steps_for` bound | ✅ 1.2M samples, 0 violations | `internal/ekb/bound_test.go`, `evidence/bound.json` |
| 64 readers, quiet vs 1 writer publish, p99.9 (`-race` clean) | ✅ tails indistinguishable (≈1 µs) | `internal/ekb/concurrency_test.go`, `evidence/concurrency.json` |
| Shared dictionaries (machine 9) | ✅ P5: 83.5 vs 590.0 B/rec, break-even 2 recs | `internal/ekb/dictionary.go`, `internal/ekb/dictbench_test.go`, `docs/DICTIONARY.md` |
| Zero-allocation dictionary decode (`DictDecoder`) | ✅ 0 allocs/op, 410 ns (was 2805) | `internal/ekb/dictionary.go`, `evidence/ycsb.json` |
| RocksDB value codec (Put/Get + MergeOperator) | ✅ containerized, honest boundary | `integrations/rocksdb/`, `evidence/rocksdb-blobdb.json` |
| Parquet dictionary-page comparison | ✅ real `RLE_DICTIONARY`, honest boundary | `integrations/parquet/`, `evidence/parquet-dict.json` |
| Pebble value-log adapter (zero-alloc decode) | ✅ 63.36 B/rec, 0 allocs/op, p50 872 ns | `courts/pebble/`, `evidence/pebble-vlog.json` |
| Prometheus exporter + Grafana dashboard | ✅ 5 metrics / 5 panels | `integrations/prometheus/`, `grafana/ekbloom-dashboard.json` |
| Implementation-neutral conformance suite | ✅ 73 fixtures, Go + Rust verifiers | `conformance/`, `evidence/conformance.json` |
| Long-run randomized roundtrip (soak) | ✅ 2.19e9 symbols, peak heap ~81 MiB, 0 mismatches | `internal/tools/soak/`, `evidence/soak.json` |
| Corruption detection (`Corrupt{Section,Offset}`) | ✅ 100% of covered bits, ≤1 structure (≤4096 B) | `internal/ekb/corruption_test.go` |
| Streaming (`StreamBuilder`/`StreamReader`, bounded RSS) | ✅ 2 GiB @ 9.5 MiB peak heap | `internal/ekb/stream_test.go`, `internal/ekb/scale_test.go` |
| Resource policy (1 GiB default cap + breach counter) | ✅ | `internal/ekb/resource.go` |
| Fault injection (torn append / ENOSPC-EIO) | ✅ newest valid generation picked | `append_test.go` |
| Per-machine `byte_at` p50/p99/p99.9 | ✅ 20 / 31 / 40 ns | `internal/ekb/latency_test.go`, `evidence/byteat.json` |
| Fuzzers (Open, Stream, machine roundtrip) | ✅ | `internal/ekb/fuzz_test.go`, `evidence/fuzz/` |
| Benchmarks (`go test -bench`, incl. native arm64) | ✅ amd64 + native arm64 (CI) | `internal/ekb/bench_test.go`, `evidence/bench.txt`, `evidence/aarch64-native-bench.txt` |
| Cross-FS determinism (ext4/xfs/btrfs/tmpfs (CI); ext4/btrfs/tmpfs locally) | ✅ identical fingerprints | `scripts/cross-fs.sh`, `evidence/cross-fs.json` |
| Cross-arch runtime determinism | ✅ native amd64 == arm64 fingerprints | `evidence/aarch64-determinism.json`, `evidence/aarch64-native-fingerprint.txt` |
| Standard corpora `E_V` (Canterbury/Silesia/enwik8/enwik9) | ✅ Go == Rust, 50/50 rows | `scripts/corpus.sh`, `evidence/corpus-compare.log` |
| Skewed small-record court vs zstd/lz4/zstd-dict | ✅ space win, decode cost, allocs/get (honest) | `courts/`, `docs/COURTS.md`, `evidence/courts.json` |
| 4 KiB point-read court vs Pebble/LZ4/Zstd | ✅ honest in-memory vs on-disk | `courts/point/`, `evidence/courts-point.json` |
| YCSB value-log court vs zstd-dict / Pebble | ✅ m9 wins vs m2, loses vs zstd-dict | `courts/ycsb/`, `evidence/ycsb.json` |
| Performance regression history | ✅ `bench-history.jsonl`, appended by CI | `scripts/bench-history.sh`, `evidence/bench-history.jsonl` |
| Fuzz campaign | ✅ **1.13B execs** (Open), corpus published | `evidence/fuzz.json`, `evidence/fuzz/FuzzOpenBytes-1b.log` |
| WebAssembly (`wasip1` == amd64 == arm64) | ✅ suite runs under wasip1 | `scripts/wasm.sh`, `evidence/wasip1-test.log` |
| Release + published container (GHCR/Docker Hub) | ✅ `v0.1.0`; `:test` refreshed on main | `CHANGELOG.md`, `.github/workflows/publish.yml` |
| CI + captured logs | ✅ | `.github/workflows/ci.yml`, `scripts/ci.sh`, `evidence/` |

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

// Authenticated open (opt-in keyed AUTH):
r, _ = ekbloom.OpenAuthenticated(data, key)

// Machine-9 shared dictionary: train once, write a referencing body, read it
// back with the dictionary out-of-band.
dict, _ := ekbloom.TrainDictionary(corpus)
img, _ := ekbloom.WriteBodyWithDict(src, ekbloom.Profile{Kind: ekbloom.ProfileRoot}, dict)
r, _ = ekbloom.OpenWithDict(img, dict)

// Append a new additive generation:
app, gen, _ := ekbloom.AppendGeneration(data, 1, src, ekbloom.Profile{Kind: ekbloom.ProfileFull})
```

The pipeline (all run inside this repository):

```sh
sh scripts/ci.sh                  # every gate + captured logs under evidence/
go test ./...                     # unit, compat, legacy, corruption, concurrency (-race), stream, auth, append
go test -bench . -benchmem ./     # micro-benchmarks (all 9 machines)
go test -fuzz FuzzOpenBytes .     # fuzzing (see docs/TESTING.md for the long campaign)
go test -race ./...               # the 64-reader path, race-detector clean
EKB_RUN_SCALE=1 go test -run 2GiB .   # the 2 GiB bounded-memory stream build (~9.5 MiB peak)
sh scripts/roundtrip.sh           # Go -> .ekb/.stream -> Rust decode, byte-for-byte
sh scripts/cross-fs.sh            # cross-filesystem determinism (ext4/btrfs/tmpfs)
sh scripts/corpus.sh              # standard corpora E_V: Go vs Rust on the same files
sh scripts/profile.sh             # CPU profiles (SVG + top) for m1/m2/m9 encode + path bloom
sh scripts/wasm.sh                # WebAssembly build + amd64 == arm64 == wasip1
sh scripts/verify-external.sh     # pull the published image, run the suite, no clone
sh scripts/sandbox.sh go test ./...   # the whole suite in a 2g-capped container (OOM-safe)
sh scripts/rocksdb.sh             # RocksDB value-codec case study (capped container)
sh scripts/parquet.sh             # Parquet dictionary-page case study (capped container)
sh scripts/pebble.sh              # Pebble value-log case study (capped container)
sh scripts/prometheus.sh          # Prometheus scrape + Grafana dashboard
sh scripts/conformance.sh         # the neutral conformance suite (Go + Rust verifiers)
sh scripts/soak.sh                # long-run randomized roundtrip (bounded memory, 2g cap)
sh scripts/container-aarch64.sh   # amd64 == arm64 runtime determinism (qemu)
sh scripts/courts.sh              # 100-byte 80/20 court + 4 KiB point-read court (Pebble)
sh scripts/container-reproduce.sh # everything inside a ~10 MB Docker image
sh scripts/reproduce.sh           # clean build + tests, emits JSON
```

Container (minimal, non-root, ~9.7 MB runtime image):

```sh
sh scripts/container-image.sh      # docker build --target runtime
sh scripts/container-test.sh       # docker build --target test (vet + full suite)
sh scripts/container-reproduce.sh  # run the suite + CLI in-container, emit JSON
sh scripts/container-publish.sh    # multi-arch build+push (needs `docker login`)
```

Or with Docker Compose (`compose.yaml`): `docker compose run --rm test` (full
suite), `bench`, `inspect`, `verify`.

### Releases and third-party reproduction

The `publish` workflow (`.github/workflows/publish.yml`) builds `linux/amd64` and
`linux/arm64` and pushes a **runtime** image and a **test** image. Tag a release to
publish: `git tag v0.1.0 && git push origin v0.1.0`.

```sh
docker run --rm ghcr.io/infinityabundance/ekbloom-go:latest version
docker run --rm ghcr.io/infinityabundance/ekbloom-go:test            # full suite, no clone
sh scripts/verify-external.sh                                       # emits evidence/external-reproduce.json
```

Images are published for every release as `:0.1.0` (runtime) and `:0.1.0-test`
(suite), plus moving `:latest`/`:test`. Verified from a clean machine: the
published `:test` image runs **379 tests, 0 failures** (`evidence/external-reproduce.json`).

The test image on GHCR is pushed unconditionally; Docker Hub
(`infinityabundance/ekbloom-go`) is used when `DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN`
secrets are set. See `CHANGELOG.md` for the release history and `docs/STABILITY.md`
for the compatibility promise.

## Layout

The **root package is the public API only** — a thin facade of type aliases and
function wrappers. The engine is `internal/` (Go enforces that it cannot be
imported by other modules), so `go list -f '{{.GoFiles}}' .` shows a handful of
files, not forty.

```
package `ekbloom` (repository root) — public API only:
  ekbloom.go       package doc + type aliases (Reader, Profile, Error, ...)
  reader.go        Open / OpenWithDict / OpenAuthenticated / Verify* / Scan / DecodeCore / metrics
  writer.go        WriteBody* / AppendGeneration* / TruncateToValid / dictionary
  stream.go        NewStreamBuilder / OpenStream
  resident.go      ResidentView
  errors.go        Error, Kind, sentinels, CorruptDetail
  options.go       Profile/Section/Policy/Machine constants, Estimate...

internal/ekb/      the engine (was package ekbloom; one cohesive package):
  reader.go writer.go format.go verify.go inspect.go estimate.go machine*.go
  dictionary.go fastpath.go resident.go stream.go metrics.go resource.go auth.go
  append.go writer_path.go writer_bloom.go writer_tiles.go errors.go
  + the tests that are the specification (compat_test.go, legacy_test.go,
    corruption_test.go, bound_test.go, concurrency_test.go, roundtrip_test.go,
    chaos_test.go, fuzz_test.go, ...)

cmd/ekbloom        minimal CLI (inspect/byte/read/verify/stream) for the container image
internal/blake3     dependency-free pure-Go BLAKE3 (unkeyed + keyed, streaming)
internal/tools      roundtripgen, fingerprint, corpusbench, corpuscompare, testsum
courts/             separate module: the competitor courts (zstd/lz4/Pebble/zstd-dict)
tools/fixturegen    committed Rust crate: fixture generator + verifygo (Rust reads Go)
testdata/           Rust-generated .ekb/.stream/auth fixtures + BLAKE3 vectors
docs/               CORE-LAYOUT.md, MIGRATION.md, STABILITY.md, SECURITY.md, THREAT_MODEL.md, OPERATIONS.md, MACHINE_CONTRACT.md, DICTIONARY.md, COURTS.md, CORPORA.md, PERFORMANCE.md, SCOPE.md, INTEGRATIONS.md, TESTING.md
integrations/       rocksdb/ (value codec), parquet/ (dictionary court) — separate modules
scripts/            roundtrip.sh, setup-reference.sh, cross-fs.sh, corpus.sh, profile.sh, wasm.sh, verify-external.sh, bench.sh, fuzz.sh, reproduce.sh, container-*.sh
examples/wasm/      WebAssembly build + run notes (wasip1 + js/wasm)
CHANGELOG.md        release history (Keep a Changelog + SemVer)
Dockerfile          minimal multi-stage image (runtime ~9.7 MB, non-root)
compose.yaml        one-command container reproduction (test/bench/inspect/verify)
.reference/         (git-ignored) the Rust reference + the fixture generator
```

The wire format and the public API are unchanged by this split: the root types are
aliases (`type Reader = ekb.Reader`), so method sets and interfaces are identical.

## Design notes

EKBLOOM is a *concealed-expansion codec*: it spends manifested physical space to
maximise access speed while preserving exact correctness. The Go port preserves
the parts that make the format interoperable — the frozen wire layout, the nine
machine semantics, the exact physical accounting, and the corruption contract —
and mirrors the reference's fastest-correct-path read selection: an exact path
bloom (O(1) `ByteAt`), a full machine-1 history, the nearest checkpoint restart,
the directly-addressable machines, and finally authoritative machine decode. See
`docs/CORE-LAYOUT.md` for the byte layout and `docs/MACHINE_CONTRACT.md` for how
to add a machine.

## Not ported (honest scope)

SIMD kernels, `io_uring`, `mmap` surfaces, the FUSE projection, OrFS, and the joint
planner/optimizer are **out of scope**. The full table — what each one is, what the
Go port does instead, and the performance the reference reports for it — is in
[`docs/SCOPE.md`](docs/SCOPE.md). In short: the port trades those systems-level
advantages for determinism (`amd64 == arm64 == wasip1`, ext4/xfs/btrfs/tmpfs) and
portability, and reads/writes the same frozen bytes in both directions. What *is*
ported and matters more — bounded streaming, crash-safe append, the certified
worst-case bound, the corruption contract, AUTH, legacy reads — is listed there
too. The codec court (`docs/COURTS.md`) is a space win at a decode cost, not a
speed win over Zstandard-with-dictionary.
