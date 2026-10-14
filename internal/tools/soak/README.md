# soak — randomized round-trip soak harness

`internal/tools/soak` is a long-running soak harness that repeatedly round-trips
randomized objects through the EKBLOOM Go implementation while accumulating a
cumulative **logical-symbol** counter. Its job is to show that the implementation
can process billions of symbols across varied objects with **bounded memory** —
it must survive the 2 GiB cap of the sandbox container.

## What each iteration does

1. Builds a random object:
   - a random machine id in `1..9`;
   - a random manifestation profile: `root`, `full`, `sparse(K)` or `path(D)`;
   - a random size in `1 B .. 4 MiB` (log-uniform);
   - random but varied content: incompressible, skewed, repetitive or text-like.
2. Round-trips it through the public API and asserts byte-exact identity:
   `WriteBody` → `Open` → `ReadAt(0,N)` → a random `ByteAt(off)`.
3. Occasionally exercises an additive generation and torn-tail recovery:
   `AppendGeneration` + `TruncateToValid`.
4. Occasionally exercises the machine-9 shared-dictionary path:
   `TrainDictionary` → `EncodeRecord` → `NewDictDecoder().DecodeInto`.

Two implementation constraints shape the generator: the `sparse(K)` profile is a
machine-1-only structure (so sparse iterations pin the machine to 1, with `K`
drawn from the supported interval set), and `path(D)` uses the supported depths
`{1,2,4,8,16,32}`.

## Memory control (the point)

The harness keeps **no per-iteration data**. Each iteration allocates its source,
image and decoded copies, verifies them, and lets them become garbage; only
running counters (`symbols`, `iterations`, `mismatches`, peak memory) survive the
loop. Peak `HeapAlloc` is therefore a function of the largest *single* object, not
of how many objects have been processed.

It samples `runtime.ReadMemStats` periodically and records the peak `HeapAlloc`
and `HeapSys`, plus `runtime.NumGC()` and the cumulative `PauseTotalNs`. The
recorded peak is the evidence that memory stayed bounded — a run that leaked
per-iteration data would climb toward (and be OOM-killed at) the 2 GiB cap.

The practical consequence for sizing: if a run ever OOMs under `--memory=2g`,
shrink the per-iteration object size (`maxObjectBits` in `main.go`) — that is the
knob the harness exists to expose.

## Running it

Everything runs capped inside the sandbox; nothing runs on the host. From the
repo root:

```sh
# default: stop after 1e9 symbols or 120 seconds, whichever comes first
sh scripts/soak.sh
```

A long soak — here, run for up to 24 hours (or 1e9 symbols, whichever first):

```sh
SOAK_SECONDS=86400 sh scripts/soak.sh
```

To accumulate far more than the default symbol target across a long run, raise it
too:

```sh
SOAK_SYMBOLS=100000000000 SOAK_SECONDS=86400 sh scripts/soak.sh
```

A fixed seed makes a run reproducible:

```sh
SOAK_SEED=4242 SOAK_SECONDS=60 sh scripts/soak.sh
```

### Environment

| Variable       | Default      | Meaning                                        |
| -------------- | ------------ | ---------------------------------------------- |
| `SOAK_SYMBOLS` | `1000000000` | stop once this many logical symbols are verified |
| `SOAK_SECONDS` | `120`        | stop after this many seconds (whichever first) |
| `SOAK_SEED`    | `1`          | deterministic PRNG seed                        |

The run stops at whichever of the symbol target or wall-clock budget is reached
first. **On any mismatch it prints the details and exits nonzero.**

## Output

- `evidence/soak.json` — the final report (program stdout, captured by the script):

  ```json
  {"target_symbols":1000000000,"symbols":...,"iterations":...,"seconds":...,
   "peak_heap_bytes":...,"heap_sys_bytes":...,"gc_runs":...,"gc_pause_total_ns":...,"mismatches":0}
  ```

- `evidence/soak.log` — progress lines (about every 10s) and diagnostics.

Exit status is `0` only when `mismatches` is `0`.

## In CI

The `soak` job in `.github/workflows/ci.yml` runs a short bounded soak
(`SOAK_SECONDS=60 sh scripts/soak.sh`) and uploads `evidence/soak.json`.
