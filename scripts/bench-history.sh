#!/bin/sh
# Append one JSON performance record to evidence/bench-history.jsonl, so
# regressions (and improvements) are visible over time. Each line records the
# commit, Go version, architecture, and the ns/op of the hot benchmarks.
#
# Run from the repository root: sh scripts/bench-history.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence
out=evidence/bench-history.jsonl
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

go test -run '^$' -bench 'BenchmarkByteAtPathBloom|BenchmarkByteAtHistory|BenchmarkByteAtProbe|BenchmarkResidentViewGet|BenchmarkMachine1Encode|BenchmarkMachine2Decode' -benchmem -benchtime 200ms . > "$tmp" 2>&1

# ns/op of a named benchmark (the output line is "BenchmarkX-N  <iters>  <ns> ns/op ...").
ns() { awk -v b="$1" '$1 ~ "^"b"-" {print $3; exit}' "$tmp"; }

commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
gov=$(go version | awk '{print $3}')
arch=$(go env GOARCH)
ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)

printf '{"ts":"%s","commit":"%s","go":"%s","arch":"%s","byteat_pathbloom_ns":%s,"byteat_history_ns":%s,"byteat_probe_ns":%s,"resident_get_ns":%s,"m1_encode_ns":%s,"m2_decode_ns":%s}\n' \
    "$ts" "$commit" "$gov" "$arch" \
    "$(ns BenchmarkByteAtPathBloom)" \
    "$(ns BenchmarkByteAtHistory)" \
    "$(ns BenchmarkByteAtProbe)" \
    "$(ns BenchmarkResidentViewGet)" \
    "$(ns BenchmarkMachine1Encode)" \
    "$(ns BenchmarkMachine2Decode)" >> "$out"

echo "appended to $out:"
tail -n 1 "$out"
echo "history:"
cat "$out"
