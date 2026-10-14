#!/bin/sh
# Run the micro-benchmarks and save a median-of-runs summary under evidence/.
#
# Nothing heavy runs on the host: `go test -bench` executes INSIDE the OOM-capped
# sandbox (scripts/sandbox.sh: --memory=2g --memory-swap=2g --cpus=2
# --pids-limit=512, GOMAXPROCS=2). The benchmarks run `-count=R` and
# internal/tools/benchagg reduces the R runs of each benchmark to the median
# ns/op, B/op and allocs/op (with min/max), so a single noisy run cannot move the
# recorded numbers. R = EKB_RUNS (default 5; 3 in CI).
#
# Run from the repository root: sh scripts/bench.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence

echo "== go test -bench . -benchmem -count=R (R = EKB_RUNS) =="
sh "$root/scripts/sandbox.sh" sh -c '
set -eu
runs=$(go run ./internal/tools/benchagg -print-runs)
echo "runs=$runs"
go test -run "^$" -bench . -benchmem -benchtime 200ms -count="$runs" ./... > /tmp/bench.raw 2>&1 || { cat /tmp/bench.raw; exit 1; }
go run ./internal/tools/benchagg < /tmp/bench.raw > evidence/bench.txt
'

echo
echo "== evidence/bench.txt =="
cat evidence/bench.txt
