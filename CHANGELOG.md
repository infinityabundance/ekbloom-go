# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The Go module path is
`github.com/infinityabundance/ekbloom-go`.

The wire format is **frozen** and follows the Rust reference
(`infinityabundance/EKBLOOM`, v0.5.0); see `docs/STABILITY.md` and
`docs/MIGRATION.md`.

## [Unreleased]

## [0.5.0] - 2026-10-14

### Added

- **`io.ReaderAt` service surface** — `(*Reader).File()` / `NewFile` return an
  `io.ReaderAt` over the logical content (concurrency-safe, `io.EOF` semantics),
  `ReadRanges` decodes a batch with a single prepare of the machine model (bounded
  by `max(Off+Len)`), and `ReadAtContext` is `ReadAt` with cooperative cancellation
  returning the typed `ctx.Err()` promptly. `WorstCaseStepsFor` gives the per-range
  step bound for pre-read budgeting. `scripts/readerat.sh` → `evidence/readerat.json`.
- **CPU/heap profiling** — `scripts/profile.sh` now runs entirely inside the
  OOM-capped sandbox and emits, per hot path, a pprof call-graph SVG, an
  `-alloc_space` allocation breakdown and an `-inuse_space` retained-heap view,
  plus GC-pause quantiles and peak/retained heap. `evidence/profile.json`.
- **Pebble integration depth** — `courts/pebblebench` adds end-to-end **scan**,
  **ingest** (offline SSTable + `db.Ingest`), **compaction** (forced, with write
  amplification) and **cache-pressure** (bounded block cache, hit/miss + p99 under
  pressure), all round-trips byte-verified. `scripts/pebble-integration.sh` →
  `evidence/pebble-integration.json`.
- **Enforcing conformance authority** — both verifiers publish per-fixture JSON;
  an independent, stdlib-only comparator (`conformance/tools/divergence`, imports
  neither implementation) fails the run when an observed cross-implementation
  difference is not in the ruled `manifest.json.divergences` set. Every ruled
  divergence now cites a `SPEC.md` section (`rule`), and `SPEC.md` §7.3/§7.4/§9.4/§10
  describe the policy. `evidence/conformance.json` reports
  `{ruled, observed, unruled, resolved, ruled_without_rule}`.
- **Implementation-neutral conformance suite** (`conformance/`): an authoritative
  `SPEC.md`, an immutable format-versioned `manifest.json` of **73 golden fixtures**
  (positive + negative with expected error kinds, and each expectation labelled
  `derived` vs `asserted`), and **independent Go and Rust verifiers** that consume
  the same manifest and import neither the other implementation nor the generator.
- **Long-run randomized roundtrip soak** (`internal/tools/soak/`): random machines,
  profiles, sizes and content, round-tripped byte-exact with **no per-iteration
  retention**; reaches the 1e9-symbol target in ~25 s at ~77 MiB peak heap (a 60 s
  run reached 2.19e9 symbols at ~81 MiB, 0 mismatches) — bounded well inside the 2g
  container cap. `scripts/soak.sh` → `evidence/soak.json`. CI job `soak` (60 s).

### Notes

- The Go API remains **v0.x** and additive: `ReaderAt`, `Range`, `ReadRanges`,
  `ReadAtContext` and the profiling/CI tooling add to the frozen surface without
  changing any existing signature.

## [0.4.0] - 2026-10-14

### Added

- **Pebble value-log case study** (`courts/pebble/`) — machine-9 cores stored as
  Pebble values, decoded with a reused `DictDecoder` buffer: 63.36 B/record,
  Get p50 872 ns / p99 1,563 ns, **0 allocs/op**, 0 breaches. Evidence:
  `evidence/pebble-vlog.json`.
- **Prometheus exporter + Grafana dashboard** (`integrations/prometheus/`,
  `grafana/ekbloom-dashboard.json`) — `ekbloom_physical_bytes`,
  `ekbloom_expansion_ratio`, `ekbloom_breach_total`, `ekbloom_get_ns`,
  `ekbloom_worst_case_steps_for`, with five dashboard panels. Evidence:
  `evidence/prometheus-scrape.txt`.
- **CI jobs** `pebble` and `prometheus` (OOM-capped via the scripts).
- `docs/INTEGRATIONS.md` sections for Pebble and Prometheus/Grafana.

## [0.3.0] - 2026-10-14

### Added

- **`DictDecoder`** — a reusable, zero-allocation dictionary decoder
  (`NewDictDecoder`, `DecodeInto`, `Decode`). The dictionary model is fixed, so the
  prepared rANS context is built once and reused; `DecodeInto` writes into a caller
  buffer with **0 allocs/op** (machine-9 read p50 2,805 ns → **410 ns**).
- **`integrations/rocksdb`** — RocksDB value-codec case study: a separate Go module
  built `-buildmode=c-shared` (`ekbloom_train` / `ekbloom_compress` /
  `ekbloom_decompress`) and a C++ program that stores EKBLOOM-encoded values in
  RocksDB (`Put`/`Get`) and registers a custom `MergeOperator` through the Object
  Registry. Evidence: `evidence/rocksdb-blobdb.json`.
- **`integrations/parquet`** — a real Parquet file (`RLE_DICTIONARY`, ZSTD) compared
  against the machine-9 shared dictionary on identical records. Evidence:
  `evidence/parquet-dict.json`.
- **`Dockerfile.sandbox` + `scripts/sandbox.sh`** — run every build/test/bench in a
  container hard-capped at `--memory=2g --memory-swap=2g --cpus=2 --pids-limit=512`,
  so nothing runs on the host.
- **CI jobs** `rocksdb` and `parquet` (both OOM-capped), and the `court` job now
  also runs the 4 KiB point-read and YCSB courts.
- `docs/INTEGRATIONS.md`.

## [0.2.0] - 2026-10-14

### Added

- `DecodeCore(machineID, core, logicalLen)` (and `MachineByID`) — decode a machine
  core directly, e.g. for a value-log adapter that stores codec-encoded values.

### Changed

- **Layout.** The engine moved to `internal/ekb`; the root package is now the
  public API only (type aliases + wrappers over the engine). The wire format and
  the public API are unchanged — root types are aliases (`type Reader = ekb.Reader`),
  so method sets and interfaces are identical. `go list -f '{{.GoFiles}}' .` now
  lists 7 files instead of 42.

### Notes

- Continuous publishing: the suite image (`ghcr.io/…:test`) is republished on
  every push to `main`; releases also publish `:<version>` and `:<version>-test`
  (runtime and suite), plus moving `:latest`.

## [0.1.0] - 2026-10-14

First public release: a pure-Go (no cgo, no `unsafe`) implementation of the
EKBLOOM concealed-expansion codec that is wire- and byte-compatible with the Rust
reference.

### Added

- **Format** — the `.ekb` container (genesis header, section framing, commit
  footer, manifest), `Scan`/`Open`/`OpenAuthenticated`, and the frozen
  `(machine_id, core_version)` typing (`ErrUnsupportedCoreVersion`).
- **Machines 1–9** — bit-exact encode/decode for `OrANS-NL2`, `OrANS-RANS0`,
  `OrANS-Store`, `OrANS-RANS0I`, `OrANS-PACK`, `OrANS-SEG`, `OrANS-RANS0W`,
  `OrANS-RANS0IL` and `OrANS-DICT`, plus the machine-9 shared dictionary
  (`TrainDictionary`, `WriteBodyWithDict`, `OpenWithDict`).
- **Reader** — `Open`, `ByteAt`, `ReadAt`, `PhysicalBytes`, `Estimate`,
  `WorstCaseStepsFor`, `ResidentView` (zero-allocation borrowed view), `Report`.
- **Writer** — `WriteBody`/`WriteBodyAuthenticated` (root/full/sparse/path),
  `WriteBodyWithDict`, `WriteBodyTiles`, and `AppendGeneration`
  (+ `AppendGenerationAuthenticated`) with `TruncateToValid`.
- **Streaming** — bounded-RSS `StreamBuilder`/`StreamReader`.
- **AUTH** — opt-in keyed integrity/authenticity (BLAKE3 keyed MAC), write and
  verify halves.
- **Errors** — every failure is a typed `*ekbloom.Error`; `CorruptDetail` exposes
  `{Section, Offset}`. No panics on untrusted input.
- **Ops** — `ResourcePolicy` (default 1 GiB cap + breach counter) and
  `metrics.go` (Prometheus-style text / JSON export).
- **Docs** — `CORE-LAYOUT.md`, `MIGRATION.md`, `SECURITY.md`, `THREAT_MODEL.md`,
  `OPERATIONS.md`, `MACHINE_CONTRACT.md`, `DICTIONARY.md`, `COURTS.md`,
  `CORPORA.md`, `PERFORMANCE.md`, `SCOPE.md`, `STABILITY.md`, `TESTING.md`.
- **Container** — a ~9.7 MB non-root runtime image, a `test` image, and
  `compose.yaml`.

### Proven

- Byte-for-byte identity with Rust fixtures (`compat_test.go`) and legacy
  v0.3/v0.4 fixtures (`legacy_test.go`); Rust re-reads Go-written files
  (`scripts/roundtrip.sh`, 22 files).
- Standard corpora `E_V`: Go == Rust, 50/50 rows (`scripts/corpus.sh`).
- Determinism across `amd64 == arm64 (native) == wasip1` and across
  ext4/xfs/btrfs/tmpfs (`evidence/`).
- `worst_case_steps_for`: 1.2M samples, 0 violations; corruption 100% ≤4096 B;
  64 readers p99.9 ≈1 µs under `-race`; 2 GiB bounded stream at ≈9.5 MiB peak.
- Fuzzing: `FuzzOpenBytes` 1.13B executions, no crashes.

[Unreleased]: https://github.com/infinityabundance/ekbloom-go/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/infinityabundance/ekbloom-go/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/infinityabundance/ekbloom-go/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/infinityabundance/ekbloom-go/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/infinityabundance/ekbloom-go/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/infinityabundance/ekbloom-go/releases/tag/v0.1.0
