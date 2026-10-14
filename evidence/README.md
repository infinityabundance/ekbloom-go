# Evidence

Every claim in the README and docs points at an artifact here or at an executable
proof in `testdata/` and the test suite. Artifacts marked **(CI)** were produced by
the GitHub Actions jobs (`.github/workflows/ci.yml`) and downloaded verbatim,
because they need hardware or privileges a laptop may not have.

**Timing correction.** Every latency/throughput artifact here was re-measured after
`scripts/sandbox.sh` began pinning `GOMAXPROCS` to the container CPU cap
(`--cpus=2`). Go 1.23 does not read the cgroup CPU quota, so without the pin the
sandbox scheduler was sized for the host's cores, CFS throttling inflated every
measured latency, and the micro-benchmarks ran as `-16` instead of `-2`; the pin
keeps the scheduler honest. The sandbox toolchain is Go 1.23.12. **Sizes and
identities are unaffected** — `physical_bytes`, `E_V`, `bytes_per_record`,
`bytes_per_value`, `expansion_ratio`, `dict_bytes`, `worst_case_steps`,
`write_amplification`, fixture sha256s and decoded hashes are unchanged; only
`*_ns`, `ns/op`, `MB/s` and `ops/s` fields moved.

**Median-of-runs.** Every latency/throughput field is now the **median across
`EKB_RUNS` runs** (default 5; 3 when `CI` is set) rather than a single shot. Each
affected artifact records a top-level `"runs"` and `"statistic": "median-of-runs"`,
and most metrics also carry `_min`/`_max` so the run-to-run spread is visible. The
timing harnesses are run **without `-race`**: the race detector adds a memory access
to every read and inflates every latency (~5–25× here), so latency is only ever
measured on the non-race build, and `go test -race ./...` is the correctness gate
that never overwrites the timing evidence (`writeTimingEvidence`). Set `EKB_RUNS`
to change the run count (odd values are preferred so the median is an observed run).

| Artifact | What it proves |
| --- | --- |
| `environment.txt` | toolchain, determinism and cross-compile results for the local run |
| `test-race.log`, `vet.log` | `go test -race ./...` and `go vet ./...` (local CI) |
| `bench.txt` | amd64 `go test -bench . -benchmem -benchtime 200ms` |
| `aarch64-bench.txt` | arm64 benchmarks under qemu (slow, indicative only) |
| `aarch64-native-bench.txt` **(CI)** | **native** arm64 benchmarks (`ubuntu-24.04-arm`, 4 vCPU) |
| `aarch64-determinism.json` | amd64 == arm64 fingerprint under qemu (`container-aarch64.sh`) |
| `aarch64-native-fingerprint.txt` **(CI)** | native arm64 fingerprint, byte-identical to amd64 |
| `cross-fs.json` **(CI)** | fingerprint identical across **ext4, xfs, btrfs, tmpfs** |
| `cross-fs.log` | the local run (rootless: ext4, btrfs, tmpfs) |
| `corpus.json` / `corpus-rust.json` / `corpus-compare.log` | standard-corpus `E_V`: Go == Rust, 50/50 rows |
| `dictionary.json` | machine-9 P5 break-even (83.5 vs 590.0 B/record; break-even 2) |
| `courts.json` / `courts.log` | skewed 80/20 court vs zstd/lz4/zstd-dict (+ allocs/get) |
| `bound.json` | `worst_case_steps_for`: 1,200,000 samples, 0 violations |
| `byteat.json` | per-machine `byte_at` p50/p99/p99.9 |
| `concurrency.json` | 64 readers quiet vs 1 writer publish, p50/p99/p99.9 |
| `profile/` | per hot path (m1/m2/m9 encode + path bloom): CPU profile (`*.prof`), heap profile (`*.mem.prof`), flat-CPU/allocation/retained tables (`*.top`, `*.alloc.top`, `*.inuse.top`), and CPU + heap SVG call graphs (`*.svg`, `*.heap.svg`), all produced in the sandbox (`scripts/profile.sh`) |
| `profile.json` | consolidated profile report: per hot path top allocation sites, flat CPU shares and retained (inuse) bytes, plus GC-pause count/total/quantiles and peak heap from a soak-style workload (`scripts/profile.sh`) |
| `sweep.json` | writer-profile frontier: `E_V` / `byte_at` / 4 KiB / bulk per profile |
| `courts-point.json` | 4 KiB point read vs raw/LZ4/zstd/Pebble/EKBLOOM |
| `ycsb.json` | YCSB value-log court (zipfian) vs zstd-dict / Pebble / m2 |
| `bench-history.jsonl` | append-only performance regression history (commit + ns/op) |
| `wasip1-fingerprint.txt` / `wasip1-test.log` | WebAssembly (wasip1) fingerprint + full suite, identical to amd64/arm64 |
| `external-reproduce.json` | the suite run from the **published** `ghcr.io/…:test` image (no clone): pass/fail counts plus provenance — the resolved `reference`, immutable `digest` (`sha256:…`), `tag`, and `pulled_anonymously` (`scripts/verify-external.sh`, re-enforced by `.github/workflows/external-reproduce.yml`) |
| `chaos.json` | 400 append/damage/repair rounds (truncate + bit flips): 400/400 recovered exact |
| `endurance.json` / `endurance.log` | long-run fault-injection endurance: 10 fault kinds (truncate / bit flip / older-generation / header-core), newest-valid generation selected or typed rejection, `garbage: 0`, concurrent readers race-clean (`scripts/endurance.sh`) |
| `fuzz.json`, `fuzz/` | fuzz campaign (OOM-capped sandbox, `scripts/fuzz.sh`): `FuzzOpenBytes` 1.13B historical execs + sandboxed `FuzzOpenAuthenticatedBytes` 132.4M/30m (typed-error-only invariant), with per-target logs and the published corpus |
| `roundtrip.log` | Go-written `.ekb`/`.stream` opened and decoded by the Rust reference (22 files) |
| `parquet-dict.json` / `parquet-dict.log` | EKBLOOM machine-9 shared dictionary vs a **real Parquet `RLE_DICTIONARY` page** on the same 20,000 × 100 B column (`scripts/parquet.sh`) |
| `rocksdb-blobdb.json` / `rocksdb-blobdb.log` | EKBLOOM-encoded values inside RocksDB (`Put`/`Get` + a registered `MergeOperator`), containerized (`scripts/rocksdb.sh`) |
| `pebble-vlog.json` / `pebble-vlog.prom` | machine-9 cores as Pebble values; zero-alloc `DecodeInto` (`scripts/pebble.sh`) |
| `prometheus-scrape.txt` / `grafana-dashboard.json` | the exporter's five metric families and the Grafana dashboard (`scripts/prometheus.sh`) |
| `conformance.json` / `conformance.log` | the implementation-neutral conformance suite: 73 immutable fixtures verified independently by Go and Rust, plus the ruling `divergences` summary (`scripts/conformance.sh`) |
| `conformance-go.json` / `conformance-rust.json` | per-fixture observations published by each verifier (`-json` / `--json`) for the independent comparator |
| `divergence.json` | the independent comparator's machine-readable ruling: ruled/observed/unruled/resolved counts, each with its cited SPEC `rule` (`conformance/tools/divergence`) |
| `soak.json` / `soak.log` | long-run randomized roundtrip: cumulative symbols, peak heap, GC pause, 0 mismatches (`scripts/soak.sh`) |
| `readerat.json` | `io.ReaderAt` section-copy, batch equality, 64-reader race, cancellation latency (`scripts/readerat.sh`) |
| `pebble-vlog.json` / `pebble-vlog.log` / `pebble-vlog.prom` | machine-9 as a **Pebble value-log adapter**: 20,000 × 100 B cores via `db.Set`, 50,000 80/20 Gets with a single reusable `DictDecoder.DecodeInto` (0 allocs/op), plus a sealed-body `worst_case_steps`/`E_V` probe (`scripts/pebble.sh`) |
| `pebble-integration.json` / `pebble-integration.log` / `pebble-integration.prom` | machine-9 Pebble **integration depth**: full range **scan** (verified), offline-SSTable **ingest** (`sstable.NewWriter` + `db.Ingest`), forced **compaction** (write amplification from Pebble metrics), and 80/20 point reads under a 128 KiB cache **vs** the 8 MiB warm case — every round-trip verified, 0 mismatches (`scripts/pebble-integration.sh`) |
| `container-test.json`, `container-reproduce.json` | the suite and CLI inside the ~9.7 MB image |
| `reproduce.json` | JSON summary from `scripts/reproduce.sh` |

Regenerate the Rust-side fixtures with:

```sh
sh scripts/setup-reference.sh
cd .reference/fixturegen && cargo run --release --bin fixturegen
```

Re-run the Rust verifier against freshly written Go files with:

```sh
sh scripts/roundtrip.sh
```

The **primary** evidence is executable and lives in `testdata/` and the test
suite:

- `testdata/fixtures/*.ekb` — `.ekb` files written by the Rust reference
  implementation (`fixturegen`), with sibling `.src` sources and `.dict`
  dictionaries.
- `testdata/legacy/` — the reference's frozen v0.3/v0.4 fixtures.
- `testdata/stream/golden.stream` — a Rust-written stream container.
- `testdata/blake3_vectors.json` — BLAKE3 digests (unkeyed and keyed) produced by
  the Rust `blake3` crate.
- `testdata/fixtures.json`, `testdata/stream.json` — expected metadata
  (`physical_bytes`, geometry, hashes) asserted by `compat_test.go` /
  `stream_test.go`.
