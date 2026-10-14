# `courts/pebble` — machine-9 as a Pebble value-log adapter

A focused case study that puts the EKBLOOM **machine-9 shared dictionary** behind
a real embedded LSM store and measures the read path end to end. It is a package
of the `courts` module (which already depends on
`github.com/cockroachdb/pebble`), separate from the root module.

## What it does

1. Builds **20,000 records × 100 B** from the same skewed/repetitive token
   alphabet as `courts/ycsb` (`id=`, `region=`, `status=`, …), so the byte
   distribution matches the YCSB court.
2. Trains **one** shared order-0 dictionary from a slice of the corpus:

   ```go
   dict, _ := ekbloom.TrainDictionary(corpus) // 540 B on the wire
   ```

3. Encodes every value to its machine-9 **core** and stores it as the Pebble
   value:

   ```go
   core, _ := dict.EncodeRecord(v)
   db.Set(key, core, pebble.NoSync)
   ```

4. Replays **50,000 Gets** with an **80/20 key-access skew**. Each Get fetches the
   core and decodes it with a **single decoder built once** into a
   **caller-reused** buffer, then verifies the round trip:

   ```go
   dec, _ := ekbloom.NewDictDecoder(dict)   // built once
   ...
   core, closer, _ := db.Get(key)
   n, _ := dec.DecodeInto(dst, core)        // zero-alloc decode into reused dst
   closer.Close()
   // assert dst[:n] == original value
   ```

5. Also exercises the **sealed-body path** on one representative value and
   reports its certified step bound and manifest metrics:

   ```go
   img, _ := ekbloom.WriteBodyWithDict(v, ekbloom.Profile{Kind: ekbloom.ProfileRoot}, dict)
   r, _ := ekbloom.OpenWithDict(img, dict)
   steps, _ := r.WorstCaseStepsFor(0, uint64(len(v)))
   m := r.Metrics() // PhysicalBytes, ExpansionRatio, …
   ```

## Exact command

Everything heavy runs inside the 2 GiB-capped sandbox container (never on the
host):

```sh
sh scripts/pebble.sh
```

which is equivalent to:

```sh
sh scripts/sandbox.sh sh -c 'cd courts && go run ./pebble'
```

Outputs:

| Path | Contents |
| --- | --- |
| `evidence/pebble-vlog.json` | the program's **stdout** — exactly one JSON object |
| `evidence/pebble-vlog.log` | the program's **stderr** — the human table + diagnostics |
| `evidence/pebble-vlog.prom` | a Prometheus-style exposition (`ekbloom.MetricsText(r)` plus court gauges) |

## What it proves

- **The machine-9 dictionary is a drop-in Pebble value-log adapter.** Values are
  plain `[8 B header + rANS payload]` cores; the store does not need to know the
  codec. A single 540 B shared dictionary serves all 20,000 values.
- **Decode is genuinely zero-allocation.** `decode_allocs_per_op` is measured with
  `testing.AllocsPerRun` over `DictDecoder.DecodeInto` into a reused buffer, and
  it is **0**. The whole `Get + decode` path (`get+decode allocs/op`) is also
  **0** on this working set.
- **The codec payload is small and uniform.** `bytes_per_record` is the pure
  codec payload (the 8 B header + rANS body), `≈ 63.4 B` for 100 B of input.
- **The sealed path is certified.** The root-profile body reports
  `worst_case_steps = 100` (output reveals are exactly `length`; the root profile
  carries no positioning structure) and `physical_bytes = 80`,
  `expansion_ratio = 1` (root adds no side structures).

## Honest notes / compromises

- **Pebble adds its own LSM overhead.** These numbers describe the **codec
  payload only** — `bytes_per_record` does **not** include Pebble's keys,
  sequence numbers, block checksums, Bloom filters, WAL, or the LSM tree. The
  on-disk footprint of this store is materially larger than `63.4 B/record`.
- **The dictionary is amortised here.** `bytes_per_record` excludes the 540 B
  dictionary (reported separately as `dict_bytes`). At 20,000 records that is
  `0.027 B/record`; at small record counts it dominates.
- **Latency is a single-host, warm-cache, `NoSync` figure.** `get_p50_ns` /
  `get_p99_ns` cover `db.Get` + decode + `closer.Close()` with the working set
  resident; they are not a portable benchmark, and Pebble compaction could
  occasionally perturb them.
- **`breaches`** is the EKBLOOM resource-policy allocation-breach counter
  (`ekbloom.Breaches()`), which `MetricsText` also exports as
  `ekbloom_resource_allocation_breaches` — not a data-integrity counter. Round-trip
  integrity is tracked separately and reported as `roundtrip_mismatches` in the
  log and `.prom`; a nonzero value fails the run.

## Representative output

```json
{
  "records": 20000,
  "record_bytes": 100,
  "gets": 50000,
  "bytes_per_record": 63.35745,
  "get_p50_ns": 882,
  "get_p99_ns": 1322,
  "decode_allocs_per_op": 0,
  "breaches": 0,
  "worst_case_steps": 100,
  "expansion_ratio": 1,
  "physical_bytes": 80,
  "dict_bytes": 540
}
```
