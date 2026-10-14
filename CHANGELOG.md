# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The Go module path is
`github.com/infinityabundance/ekbloom-go`.

The wire format is **frozen** and follows the Rust reference
(`infinityabundance/EKBLOOM`, v0.5.0); see `docs/STABILITY.md` and
`docs/MIGRATION.md`.

## [Unreleased]

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

[Unreleased]: https://github.com/infinityabundance/ekbloom-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/infinityabundance/ekbloom-go/releases/tag/v0.1.0
