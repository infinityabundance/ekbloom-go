# EKBLOOM Prometheus exporter

A self-contained Go module that projects a running EKBLOOM object onto five
Prometheus metric families, plus a Grafana dashboard that binds to them. It is a
thin adapter over the **public** `ekbloom` API — every number is what the engine
reports, nothing is re-derived from the wire format.

It lives in its own module (`github.com/infinityabundance/ekbloom-go/integrations/prometheus`,
`replace github.com/infinityabundance/ekbloom-go => ../..`), so
`github.com/prometheus/client_golang` never enters the core module's dependency
graph.

```
integrations/prometheus/
├── go.mod, go.sum
├── exporter/                     # package exporter: metrics + workload + scrape
│   ├── exporter.go
│   ├── object.go
│   └── exporter_test.go
├── cmd/ekbloom-exporter/main.go  # the binary (serve / --scrape)
└── README.md
grafana/ekbloom-dashboard.json    # 5 panels bound to the metric names
scripts/prometheus.sh             # sandboxed run -> evidence/
```

## Metric names (exact)

| Metric | Type | Real API source |
| --- | --- | --- |
| `ekbloom_physical_bytes` | gauge | `Reader.Metrics().PhysicalBytes` (reader from `ekbloom.OpenWithDict`) |
| `ekbloom_expansion_ratio` | gauge | `Reader.Metrics().ExpansionRatio` — the concealed expansion ratio `E_V = physical / visible` |
| `ekbloom_breach_total` | counter | `ekbloom.Breaches()` / `ekbloom.ResourcePolicySnapshot()` (read live at each scrape) |
| `ekbloom_get_ns` | histogram `{op="byte_at\|read_at\|decode_into"}` | one observation per `Reader.ByteAt`, `Reader.ReadAt`, and `DictDecoder.DecodeInto` |
| `ekbloom_worst_case_steps_for` | gauge `{range="…"}` | `Reader.WorstCaseStepsFor(0, range)` |

`ekbloom_get_ns` is a real cumulative histogram, so p50/p99 are derivable with
`histogram_quantile(0.5|0.99, …)` over `ekbloom_get_ns_bucket`. The exporter also
supports `ekbloom.MetricsText` output for a like-for-like comparison (see below).

## What it builds

`cmd/ekbloom-exporter` builds a **representative `.ekb`** in memory by default:

1. synthesizes a small, skewed/repetitive corpus (`--records × --record-bytes`);
2. trains one shared machine-9 dictionary with `ekbloom.TrainDictionary`;
3. seals the concatenated corpus with `ekbloom.WriteBodyWithDict(..., ProfileRoot, dict)`;
4. opens it with `ekbloom.OpenWithDict` and builds a reusable decoder with
   `ekbloom.NewDictDecoder`;
5. runs `--gets` reads (`ByteAt` + `ReadAt` + `DecodeInto`), observing each into
   `ekbloom_get_ns`, and sets the gauges from `Reader.Metrics()` and
   `Reader.WorstCaseStepsFor`.

Pass `--fixture f.ekb --dict f.dict` to load a prebuilt object instead.

> Note: `WriteBodyWithDict` supports only the root profile, so a machine-9
> dictionary body has `physical == visible` and `E_V = 1` by construction — the
> same value the `courts/pebble` value-log probe reports (`ekbloom_expansion_ratio 1`).
> The gauge is still wired to the real `Reader.Metrics().ExpansionRatio`; a
> concealed (bloom/checkpoint/regional) profile is what makes `E_V > 1`.

## Commands

All heavy work runs hard-capped inside the sandbox (never on the host):

```sh
# serve :9100/metrics until SIGINT/SIGTERM
sh scripts/sandbox.sh sh -c 'cd integrations/prometheus && go run ./cmd/ekbloom-exporter'

# one deterministic scrape of the exporter's own /metrics, printed to stdout, then exit
sh scripts/sandbox.sh sh -c 'cd integrations/prometheus && go run ./cmd/ekbloom-exporter --scrape'

# load a prebuilt object
sh scripts/sandbox.sh sh -c 'cd integrations/prometheus && go run ./cmd/ekbloom-exporter --fixture f.ekb --dict f.dict --scrape'

# module tests (metric families/types, live scrape, dashboard JSON)
sh scripts/sandbox.sh sh -c 'cd integrations/prometheus && go test ./...'

# the full evidence run: scrape -> evidence/prometheus-scrape.txt, dashboard -> evidence/
sh scripts/prometheus.sh
```

Flags: `--addr` (default `:9100`), `--scrape`, `--gets`, `--records`,
`--record-bytes`, `--train-records`, `--read-len`, `--fixture`, `--dict`.

## Grafana panels ↔ evidence

| # | Panel | PromQL | Evidence field |
| --- | --- | --- | --- |
| 1 | Physical bytes | `ekbloom_physical_bytes` | `ekbloom_physical_bytes 28783` |
| 2 | Expansion ratio E_V | `ekbloom_expansion_ratio` | `ekbloom_expansion_ratio 1` |
| 3 | Allocation breach rate | `rate(ekbloom_breach_total[5m])` | `ekbloom_breach_total 0` |
| 4 | Get latency p50 / p99 | `histogram_quantile(0.5\|0.99, sum by (le, op) (rate(ekbloom_get_ns_bucket[5m])))` | `ekbloom_get_ns_count{op="byte_at"} 2000` (and `read_at`, `decode_into`) |
| 5 | Certified worst-case steps for | `ekbloom_worst_case_steps_for` | `ekbloom_worst_case_steps_for{range="4096"} 4096` |

`grafana/ekbloom-dashboard.json` targets a Prometheus datasource with `uid:
"prometheus"`. `scripts/prometheus.sh` copies it verbatim to
`evidence/grafana-dashboard.json`, and `TestDashboardValidJSON` asserts it parses
and that each panel references one of the five metric names.

## Evidence snippet

`evidence/prometheus-scrape.txt` (from `sh scripts/prometheus.sh`) contains all
five families; abridged:

```
# TYPE ekbloom_breach_total counter
ekbloom_breach_total 0
# TYPE ekbloom_expansion_ratio gauge
ekbloom_expansion_ratio 1
# TYPE ekbloom_get_ns histogram
ekbloom_get_ns_bucket{op="byte_at",le="100"} 1985
…
ekbloom_get_ns_count{op="byte_at"} 2000
ekbloom_get_ns_count{op="read_at"} 2000
ekbloom_get_ns_count{op="decode_into"} 2000
# TYPE ekbloom_physical_bytes gauge
ekbloom_physical_bytes 28783
# TYPE ekbloom_worst_case_steps_for gauge
ekbloom_worst_case_steps_for{range="1"} 1
ekbloom_worst_case_steps_for{range="4096"} 4096
```

## Compromises

- **`E_V = 1`** for the synthesized object: a machine-9 dictionary body only
  supports the root profile, so `physical == visible`. The metric is faithfully
  sourced from `Reader.Metrics().ExpansionRatio`; the value is 1 because the
  object genuinely has no concealed section.
- **`ekbloom_breach_total` is 0**: the workload never requests an allocation
  above `MaxSingleAllocation`, so no breach occurs. The counter is read live from
  `ekbloom.Breaches()` at scrape time, so it will move if a policy breach happens.
- **Private registry**: the exporter serves only the five EKBLOOM families (no
  Go/process collectors) so the CI scrape is deterministic and grep-able.
- **`--scrape` binds `--addr` (`:9100`)** for the single self-scrape. In an
  isolated sandbox this is free; override with a different address if needed.
