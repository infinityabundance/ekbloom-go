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
| Accelerated reads (path bloom / history / checkpoints) | ✅ O(1) path bloom ≈2.3 ns | `fastpath.go`, `fastpath_test.go`, `bench_test.go` |
| Writer (`WriteBody`/`AppendGeneration`: root/full/sparse/path) | ✅ Rust-readable; path bloom + certificate | `writer.go`, `writer_path.go`, `append.go` |
| Writer parity (universal/regional blooms, tiles) | ✅ Rust-readable shells; Flat-directory TileSet | `writer_bloom.go`, `writer_tiles.go`, `writer_test.go` |
| Legacy v0.3/v0.4 fixtures open byte-identically | ✅ frozen Rust fixtures | `testdata/legacy/`, `legacy_test.go` |
| Opt-in keyed AUTH (write + verify) | ✅ | `auth.go`, `auth_test.go`, Rust `open_authenticated` |
| Allocation-free `ResidentView.Get` (zero allocs/op) | ✅ ≈1.35 ns | `resident.go`, `resident_test.go` |
| Metrics export (physical_bytes, E_V, breaches) | ✅ | `metrics.go` |
| `worst_case_steps_for` bound | ✅ 1.2M samples, 0 violations | `bound_test.go`, `evidence/bound.json` |
| 64 readers, quiet vs 1 writer publish, p99.9 (`-race` clean) | ✅ tails indistinguishable (≈1 µs) | `concurrency_test.go`, `evidence/concurrency.json` |
| Shared dictionaries (machine 9) | ✅ P5: 83.5 vs 590.0 B/rec, break-even 2 recs | `dictionary.go`, `dictbench_test.go`, `docs/DICTIONARY.md` |
| Corruption detection (`Corrupt{Section,Offset}`) | ✅ 100% of covered bits, ≤1 structure (≤4096 B) | `corruption_test.go` |
| Streaming (`StreamBuilder`/`StreamReader`, bounded RSS) | ✅ 2 GiB @ 9.5 MiB peak heap | `stream_test.go`, `scale_test.go` |
| Resource policy (1 GiB default cap + breach counter) | ✅ | `resource.go` |
| Fault injection (torn append / ENOSPC-EIO) | ✅ newest valid generation picked | `append_test.go` |
| Per-machine `byte_at` p50/p99/p99.9 | ✅ 20 / 31 / 40 ns | `latency_test.go`, `evidence/byteat.json` |
| Fuzzers (Open, Stream, machine roundtrip) | ✅ | `fuzz_test.go`, `evidence/fuzz/` |
| Benchmarks (`go test -bench`, incl. arm64) | ✅ amd64 + arm64/qemu | `bench_test.go`, `evidence/bench.txt`, `evidence/aarch64-bench.txt` |
| Cross-FS determinism (disk vs tmpfs) | ✅ identical fingerprints | `scripts/cross-fs.sh` |
| Cross-arch runtime determinism | ✅ amd64 == arm64 fingerprints | `evidence/aarch64-determinism.json` |
| Standard corpora `E_V` (Canterbury/Silesia/enwik8) | ✅ Go == Rust, 48/48 rows | `scripts/corpus.sh`, `evidence/corpus-compare.log` |
| Skewed small-record court vs zstd/lz4/zstd-dict | ✅ space win, decode cost, allocs/get (honest) | `courts/`, `docs/COURTS.md`, `evidence/courts.json` |
| Fuzz campaign | ✅ **1.13B execs** (Open), corpus published | `evidence/fuzz.json`, `evidence/fuzz/FuzzOpenBytes-1b.log` |
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
sh scripts/cross-fs.sh            # cross-filesystem determinism (disk vs tmpfs)
sh scripts/corpus.sh              # standard corpora E_V: Go vs Rust on the same files
sh scripts/container-aarch64.sh   # amd64 == arm64 runtime determinism (qemu)
sh scripts/courts.sh              # 100-byte, 80/20 court vs zstd/lz4/zstd-dict
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

The `publish` workflow (`.github/workflows/publish.yml`) builds `linux/amd64` and
`linux/arm64` and pushes to GHCR, and to Docker Hub
(`infinityabundance/ekbloom-go`) when `DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN`
secrets are set. Tag a release to publish: `git tag v0.1.0 && git push origin v0.1.0`.

## Layout

```
package `ekbloom` at the repository root:
  errors.go        typed errors (Kind, sentinels, CorruptDetail)
  format.go        GenesisHeader, SectionHeader, CommitFooter, ManifestRecord, Scan
  machine*.go      the nine machines (encode/decode/check, prepared contexts)
  dictionary.go    machine-9 shared order-0 dictionaries + registry
  fastpath.go      accelerated reads: path bloom / history / checkpoints
  resident.go      allocation-free borrowed ResidentView (Get(i), zero allocs/op)
  estimate.go      profile labels and the upper-bound estimator
  inspect.go       physical accounting (mirrors the Rust FileBody::inspect)
  reader.go        Open / ByteAt / ReadAt / PhysicalBytes / Estimate / WorstCaseStepsFor
  writer.go        WriteBody / WriteBodyAuthenticated (Rust-readable .ekb images)
  writer_path.go   exact path-bloom writer (P_D[j] + certificate)
  append.go        AppendGeneration (additive generations) + TruncateToValid
  auth.go          OpenAuthenticated / VerifyAuthenticated (keyed AUTH)
  metrics.go       Metrics / MetricsText / MetricsJSON / ResourcePolicySnapshot
  verify.go        structural + deep verification with localized Corrupt errors
  stream.go        bounded-memory stream container builder/reader
  resource.go      ResourcePolicy (default 1 GiB) + breach counter
cmd/ekbloom        minimal CLI (inspect/byte/read/verify/stream) for the container image
internal/blake3     dependency-free pure-Go BLAKE3 (unkeyed + keyed, streaming)
internal/tools      roundtripgen, fingerprint, testsum (reproducibility helpers)
tools/fixturegen    committed Rust crate: fixture generator + verifygo (Rust reads Go)
testdata/           Rust-generated .ekb/.stream/auth fixtures + BLAKE3 vectors
docs/               CORE-LAYOUT.md, MIGRATION.md, SECURITY.md, OPERATIONS.md, MACHINE_CONTRACT.md, DICTIONARY.md, COURTS.md, CORPORA.md, TESTING.md
scripts/            roundtrip.sh, setup-reference.sh, cross-fs.sh, corpus.sh, bench.sh, fuzz.sh, reproduce.sh, container-*.sh
Dockerfile          minimal multi-stage image (runtime ~9.7 MB, non-root)
.reference/         (git-ignored) the Rust reference + the fixture generator
```

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

The following reference subsystems are **out of scope** for this port and are
listed so their absence is explicit, not implied: SIMD kernels, `io_uring`,
`mmap` surfaces, the FUSE projection, OrFS, and the joint planner/optimizer. The
codec court (`docs/COURTS.md`) is a space win at a decode cost, matching the
reference's honest position; it is not a speed win over Zstandard-with-dictionary.
