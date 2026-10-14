# Operations

## Metrics to export

| Metric | Source | Notes |
| --- | --- | --- |
| `physical_bytes` | `Reader.PhysicalBytes()` | equal to Rust `inspect_file`; counts the core once plus each structure |
| `visible_bytes` / `E_V` | `Reader.VisibleBytes()` | `E_V = physical / visible` (`Reader.Report().ExpansionRatio`) |
| `worst_case` | `Reader.WorstCaseStepsFor(off, len)` | upper bound on positioning + output steps |
| `read_at` p50/p99 | timing `Reader.ReadAt` | in-memory decode; the first random read materialises the symbol array once |
| `bytes_fetched` | — | the in-memory reader holds the file; the bounded-work analogue is the stream reader's per-block decode |
| `resource_policy breaches` | `Breaches()` | whole-object allocations refused by the 1 GiB default cap |

`Reader.Report()` returns a full `Report` (logical/visible/physical/concealed
bytes, expansion ratio, generation, profiles, per-structure components) that a
service can expose directly, e.g. as JSON.

## Recovery runbook

The reader exposes the valid commit prefix directly:

- `Scan(data)` returns the fully-validated commits (oldest first) and any torn
  tail (`TornTailBytes`, `TornNote`). Parsing stops at the first structural
  failure, so the returned prefix is the recoverable state.
- To truncate to the newest valid commit, keep `data[: commits[len-1].End)` and
  discard the tail — the same `truncate_to_valid` operation as the reference.

A crash may leave a torn final block; a torn block never invalidates an earlier
committed generation.

## Expected numbers (this host, scalar, no SIMD)

From `go test -bench` (`bench_test.go`), AMD Ryzen 7 9800X3D, Go 1.27:

| benchmark | result |
| --- | --- |
| machine 1 encode, 1 MiB | ≈1.6 GB/s |
| machine 2 encode, 1 MiB | ≈216 MB/s |
| machine 2 decode, 1 MiB | ≈133 MB/s |
| BLAKE3, 1 MiB (pure Go) | ≈320 MB/s |
| `ByteAt` via exact path bloom | ≈2.5 ns/op |
| `ByteAt` via full history | ≈3.6 ns/op |
| `ByteAt` via nearest checkpoint (K=64) | ≈67 ns/op |
| `ByteAt` materialised fallback | ≈5.7 ns/op |
| `ReadAt` 4 KiB via path bloom | ≈320 ns (≈12.8 GB/s) |
| stream build, 2 GiB, 64 KiB blocks | ≈16 s at **9.46 MiB peak heap** (≈125 MiB/s) |

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
