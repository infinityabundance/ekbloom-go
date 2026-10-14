#!/bin/sh
# Append one JSON performance record to evidence/bench-history.jsonl, so
# regressions (and improvements) are visible over time. Each line records the
# commit, Go version, architecture, and the median ns/op — across R = EKB_RUNS
# runs — of the hot benchmarks, with the observed min/max retained.
#
# Nothing heavy runs on the host: the benchmarks run INSIDE the OOM-capped sandbox
# (scripts/sandbox.sh) and internal/tools/benchagg reduces the R runs to medians.
#
# Run from the repository root: sh scripts/bench-history.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence
out=evidence/bench-history.jsonl
tbl=evidence/.bench-history.agg
trap 'rm -f "$tbl"' EXIT

# Run the hot benchmarks R times in the sandbox and aggregate to medians; the
# aggregated table (host-visible, in evidence/) also carries the provenance
# header (runs / Go version / arch) that benchagg captured inside the container.
sh "$root/scripts/sandbox.sh" sh -c '
set -eu
out="$1"
runs=$(go run ./internal/tools/benchagg -print-runs)
go test -run "^$" -bench "BenchmarkByteAtPathBloom|BenchmarkByteAtHistory|BenchmarkByteAtProbe|BenchmarkResidentViewGet|BenchmarkMachine1Encode|BenchmarkMachine2Decode" -benchmem -benchtime 200ms -count="$runs" ./internal/ekb > /tmp/bh.raw 2>&1 || { cat /tmp/bh.raw; exit 1; }
go run ./internal/tools/benchagg < /tmp/bh.raw > "$out"
' sh "$tbl"

# ns/op statistics of a named benchmark, read from the aggregated table
# ("<name>\t<iters>\t<median ns/op>\t<min ns/op>\t<max ns/op> ..."); ns/op is always
# field 3 and its min/max immediately follow as fields 4 and 5.
ns()    { awk -F'\t' -v b="$1" '$1 ~ "^"b"-" {print $3; exit}' "$tbl"; }
nsmin() { awk -F'\t' -v b="$1" '$1 ~ "^"b"-" {print $4; exit}' "$tbl"; }
nsmax() { awk -F'\t' -v b="$1" '$1 ~ "^"b"-" {print $5; exit}' "$tbl"; }
hdr() { awk -F= -v k="$1" '$1=="# "k {print $2; exit}' "$tbl"; }

runs=$(hdr runs)
gov=$(hdr go)
arch=$(hdr goarch)
commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)

printf '{"ts":"%s","commit":"%s","go":"%s","arch":"%s","runs":%s,"statistic":"median-of-runs","byteat_pathbloom_ns":%s,"byteat_pathbloom_ns_min":%s,"byteat_pathbloom_ns_max":%s,"byteat_history_ns":%s,"byteat_history_ns_min":%s,"byteat_history_ns_max":%s,"byteat_probe_ns":%s,"byteat_probe_ns_min":%s,"byteat_probe_ns_max":%s,"resident_get_ns":%s,"resident_get_ns_min":%s,"resident_get_ns_max":%s,"m1_encode_ns":%s,"m1_encode_ns_min":%s,"m1_encode_ns_max":%s,"m2_decode_ns":%s,"m2_decode_ns_min":%s,"m2_decode_ns_max":%s}\n' \
    "$ts" "$commit" "$gov" "$arch" "$runs" \
    "$(ns BenchmarkByteAtPathBloom)" "$(nsmin BenchmarkByteAtPathBloom)" "$(nsmax BenchmarkByteAtPathBloom)" \
    "$(ns BenchmarkByteAtHistory)" "$(nsmin BenchmarkByteAtHistory)" "$(nsmax BenchmarkByteAtHistory)" \
    "$(ns BenchmarkByteAtProbe)" "$(nsmin BenchmarkByteAtProbe)" "$(nsmax BenchmarkByteAtProbe)" \
    "$(ns BenchmarkResidentViewGet)" "$(nsmin BenchmarkResidentViewGet)" "$(nsmax BenchmarkResidentViewGet)" \
    "$(ns BenchmarkMachine1Encode)" "$(nsmin BenchmarkMachine1Encode)" "$(nsmax BenchmarkMachine1Encode)" \
    "$(ns BenchmarkMachine2Decode)" "$(nsmin BenchmarkMachine2Decode)" "$(nsmax BenchmarkMachine2Decode)" >> "$out"

echo "appended to $out:"
tail -n 1 "$out"
echo "history:"
cat "$out"
