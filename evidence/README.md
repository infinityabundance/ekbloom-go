# Evidence

Every claim in the README and docs points at an artifact here or at an executable
proof in `testdata/` and the test suite. Artifacts marked **(CI)** were produced by
the GitHub Actions jobs (`.github/workflows/ci.yml`) and downloaded verbatim,
because they need hardware or privileges a laptop may not have.

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
| `profile/` | CPU profiles (`*.prof`), SVG call graphs, and top functions for m1/m2/m9 + path bloom |
| `sweep.json` | writer-profile frontier: `E_V` / `byte_at` / 4 KiB / bulk per profile |
| `courts-point.json` | 4 KiB point read vs raw/LZ4/zstd/Pebble/EKBLOOM |
| `ycsb.json` | YCSB value-log court (zipfian) vs zstd-dict / Pebble / m2 |
| `bench-history.jsonl` | append-only performance regression history (commit + ns/op) |
| `wasip1-fingerprint.txt` / `wasip1-test.log` | WebAssembly (wasip1) fingerprint + full suite, identical to amd64/arm64 |
| `external-reproduce.json` | the suite run from the **published** `ghcr.io/…:test` image (no clone) |
| `chaos.json` | 400 append/damage/repair rounds (truncate + bit flips): 400/400 recovered exact |
| `fuzz.json`, `fuzz/` | fuzz campaign logs and the published corpus (`FuzzOpenBytes` 1.13B execs) |
| `roundtrip.log` | Go-written `.ekb`/`.stream` opened and decoded by the Rust reference (22 files) |
| `parquet-dict.json` / `parquet-dict.log` | EKBLOOM machine-9 shared dictionary vs a **real Parquet `RLE_DICTIONARY` page** on the same 20,000 × 100 B column (`scripts/parquet.sh`) |
| `rocksdb-blobdb.json` / `rocksdb-blobdb.log` | EKBLOOM-encoded values inside RocksDB (`Put`/`Get` + a registered `MergeOperator`), containerized (`scripts/rocksdb.sh`) |
| `pebble-vlog.json` / `pebble-vlog.prom` | machine-9 cores as Pebble values; zero-alloc `DecodeInto` (`scripts/pebble.sh`) |
| `prometheus-scrape.txt` / `grafana-dashboard.json` | the exporter's five metric families and the Grafana dashboard (`scripts/prometheus.sh`) |
| `conformance.json` / `conformance.log` | the implementation-neutral conformance suite: 73 immutable fixtures verified independently by Go and Rust (`scripts/conformance.sh`) |
| `soak.json` / `soak.log` | long-run randomized roundtrip: cumulative symbols, peak heap, GC pause, 0 mismatches (`scripts/soak.sh`) |
| `readerat.json` | `io.ReaderAt` section-copy, batch equality, 64-reader race, cancellation latency (`scripts/readerat.sh`) |
| `pebble-vlog.json` / `pebble-vlog.log` / `pebble-vlog.prom` | machine-9 as a **Pebble value-log adapter**: 20,000 × 100 B cores via `db.Set`, 50,000 80/20 Gets with a single reusable `DictDecoder.DecodeInto` (0 allocs/op), plus a sealed-body `worst_case_steps`/`E_V` probe (`scripts/pebble.sh`) |
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
