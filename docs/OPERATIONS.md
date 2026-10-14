# Operations

## Metrics to export

| Metric | Source | Notes |
| --- | --- | --- |
| `physical_bytes` | `Reader.PhysicalBytes()` | equal to Rust `inspect_file`; counts the core once plus each structure |
| `visible_bytes` / expansion ratio | `Reader.VisibleBytes()`, `Report().ExpansionRatio` | `expansion_ratio = physical / visible` (the reference `EkbloomBody::expansion_ratio`). The standard-corpus footprint `E_V = physical / N` (N = raw size) is a *different* ratio — see `docs/CORPORA.md` |
| `worst_case` | `Reader.WorstCaseStepsFor(off, len)` | upper bound on positioning + output steps |
| `read_at` p50/p99 | timing `Reader.ReadAt` | in-memory decode; the first random read materialises the symbol array once |
| `bytes_fetched` | — | the in-memory reader holds the file; the bounded-work analogue is the stream reader's per-block decode |
| `resource_policy breaches` | `Breaches()` | whole-object allocations refused by the 1 GiB default cap |

`Reader.Report()` returns a full `Report` (logical/visible/physical/concealed
bytes, expansion ratio, generation, profiles, per-structure components). For a
service, `internal/ekb/metrics.go` provides ready-made exports:

- `ResourcePolicySnapshot()` — `{max_single_allocation, allocation_breaches}`
  (the reference's `resource_breaches` counter).
- `Reader.Metrics()` — `{machine_id, logical_len, visible_bytes, physical_bytes,
  concealed_bytes, expansion_ratio, generation}` (`expansion_ratio = physical /
  visible`, the reference's `EkbloomBody::expansion_ratio`; distinct from the
  standard-corpus `E_V = physical / N`, see `docs/CORPORA.md`).
- `Reader.WorstCaseMetric(off, length)` — the certified step bound.
- `MetricsText(r)` / `MetricsJSON(r)` — Prometheus-style / JSON rendering.

Measured latency (this host): `ByteAt` via path bloom ≈2.3 ns (≈1.35 ns via the
borrowed `ResidentView.Get`, zero allocs/op) single-threaded. Under 64 concurrent
readers the end-to-end `ByteAt` (including the timing harness) is reported in two
phases in `evidence/concurrency.json`: **quiet** (readers only) p50 ≈200 ns /
p99 ≈560 ns / p99.9 ≈1.5 µs, and **publish** (a live writer RCU-republishing
generations) p50 ≈210 ns / p99 ≈550 ns / p99.9 ≈1.1 µs. The tail is dominated by
cross-core cache traffic on the shared published pointer and the per-read clock
read, not by the codec; the quiet and publish distributions are statistically
indistinguishable, which is the property the RCU publication exists to provide.

## Publishing the container

The minimal runtime image (~9.7 MB, non-root, `scratch`-free Alpine) is the whole
deployment artifact; the codec has no runtime dependencies and no configuration.

```sh
sh scripts/container-image.sh                 # build locally
TAG=v0.1.0 sh scripts/container-publish.sh    # multi-arch build + push to Docker Hub
```

`scripts/container-publish.sh` fails fast with a clear message when no registry
login is present, so it is safe to run in any environment. The `publish` CI
workflow pushes to GHCR unconditionally and to Docker Hub when
`DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN` are configured.

## Capacity planning

For a file of logical length `N`:

| quantity | accessor | meaning |
| --- | --- | --- |
| `N` | `LogicalLen()` | the source length (logical symbols) |
| `V` | `VisibleBytes()` | the visible core length |
| `P` | `PhysicalBytes()` | total manifested bytes (core + every structure), equal to Rust `inspect_file` |
| concealed | `P - V` | manifested-but-not-visible bytes |
| expansion ratio | `Report().ExpansionRatio` | `P / V` (the reference `expansion_ratio`) |
| footprint `E_V` | — | `P / N`; what the standard-corpus study reports (`docs/CORPORA.md`) |

**Budgeting a write.** `EstimatePhysicalBytesForCore(coreLen, N, profile)` is the
static upper bound for a profile; use it to reject a profile up front and
`Estimate()` on an open `Reader` for the committed profile. For machine-1 bodies it
is an exact/upper bound; for cursor-divergent machines it is documented as loose
(`docs/MIGRATION.md`). Profiling a workload is `docs/PERFORMANCE.md`.

**Limits and counters.** Any whole-object allocation driven by a declared length is
capped by `ResourcePolicy` (default 1 GiB); a rejection is `ErrAllocationTooLarge`
and increments the breach counter — watch it as a hostile-input signal:

```go
snap := ekbloom.ResourcePolicySnapshot() // {max_single_allocation, allocation_breaches}
ekbloom.SetMaxSingleAllocation(256 << 20)  // tighten the cap for a strict service
```

**Scrape example** (`MetricsText` is Prometheus-style, one `name value` per line):

```
# on each read, with the open *Reader r
curl -s localhost:9100/metrics | grep ekbloom_
ekbloom_resource_max_single_allocation 1073741824
ekbloom_resource_allocation_breaches 0
ekbloom_physical_bytes 4194304
ekbloom_visible_bytes 4194304
ekbloom_expansion_ratio 1
ekbloom_logical_len 4194304
ekbloom_generation 1
```

## Recovery runbook

```sh
sh scripts/reproduce.sh    # clean build + full tests -> evidence/reproduce.json
sh scripts/ci.sh           # every gate + captured logs under evidence/
```

The reader exposes the valid commit prefix directly:

- `Scan(data)` returns the fully-validated commits (oldest first) and any torn
  tail (`TornTailBytes`, `TornNote`). Parsing stops at the first structural
  failure, so the returned prefix is the recoverable state.
- To truncate to the newest valid commit, keep `data[: commits[len-1].End)` and
  discard the tail — the same `truncate_to_valid` operation as the reference.

A crash may leave a torn final block; a torn block never invalidates an earlier
committed generation.

## Expected numbers (this host, scalar, no SIMD)

From `go test -bench` (`internal/ekb/bench_test.go`), AMD Ryzen 7 9800X3D, Go 1.27:

| benchmark | result |
| --- | --- |
| machine 1 encode, 1 MiB | ≈1.60 GB/s |
| machine 2 encode, 1 MiB | ≈216 MB/s |
| machine 2 decode, 1 MiB | ≈133 MB/s |
| BLAKE3, 1 MiB (pure Go) | ≈324 MB/s |
| `ByteAt` via exact path bloom | ≈2.3 ns/op |
| `ByteAt` via full history | ≈3.8 ns/op |
| `ByteAt` via nearest checkpoint (K=64) | ≈67 ns/op |
| `ByteAt` materialised fallback | ≈5.3 ns/op |
| `ResidentView.Get` (borrowed, zero allocs) | ≈1.35 ns/op |
| `ReadAt` 4 KiB via path bloom | ≈350 ns (≈11.7 GB/s) |
| stream build, 2 GiB, 64 KiB blocks | ≈16 s at **9.46 MiB peak heap** (≈125 MiB/s) |

Captured verbatim in `evidence/bench.txt` (`go test -bench . -benchmem -benchtime 200ms`).

**Native arm64** (`ubuntu-24.04-arm`, 4 vCPU; `evidence/aarch64-native-bench.txt`):

| benchmark | amd64 (9800X3D) | native arm64 |
| --- | ---: | ---: |
| machine 1 encode, 1 MiB | ≈1.60 GB/s | ≈0.96 GB/s |
| BLAKE3, 1 MiB | ≈324 MB/s | ≈170 MB/s |
| `ByteAt` via exact path bloom | ≈2.3 ns/op | ≈13.3 ns/op |
| `ByteAt` via full history | ≈3.8 ns/op | ≈7.4 ns/op |
| `ResidentView.Get` | ≈1.35 ns/op | ≈1.77 ns/op |
| `ReadAt` 4 KiB via path bloom | ≈350 ns | ≈881 ns |

The arm64 fingerprint is byte-identical to amd64
(`evidence/aarch64-native-fingerprint.txt`).

These are correctness-first scalar numbers. The Rust reference wins on raw encode
throughput with SIMD kernels; the Go port prioritizes a dependency-free,
panic-free, byte-exact implementation with the same fastest-correct-path read
selection. SIMD kernels are a possible future addition (see
`docs/MACHINE_CONTRACT.md` for the extension bar).

## Choosing a representation

`EstimatePhysicalBytesForCore(coreLen, logicalLen, profile)` is the static upper
bound used to pre-filter a budget. To pick the smallest machine, encode the source
with each candidate and compare `len(core)`:

- `auto` (machine 2, falling back to machine 3 when it does not shrink the
  source) never expands the base past raw — see `MachineByID` + the encode
  compare used in the tests.
- Machine 5 trade size for O(1) positioning; machine 6 for heterogeneous
  objects; machine 9 (dictionary) for many small records.

## Operational limits (honest)

- Reads are served by the fastest correct path available: an exact path bloom
  (O(1)), a full machine-1 history (O(1) state), the nearest checkpoint restart
  (bounded replay), the directly-addressable machines 3/5/6 (O(1)), and finally
  authoritative machine decode. When none of these structures is present, the
  in-memory reader materialises one symbol array on first random access.
- The universal/regional bloom families are parsed and accounted but are not used
  as read accelerators: on arbitrary bytes no certified state-keyed table exists,
  exactly as the reference documents; the exact path bloom is the speed ceiling.
- SIMD, `io_uring`, `mmap`, FUSE, OrFS, and the joint planner/optimizer from the
  reference are out of scope for this port.
- Benchmarks are single-host, single-run, in-memory.
