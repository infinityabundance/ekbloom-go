#!/bin/sh
# Run the micro-benchmarks and save the raw output under evidence/.
# Run from the repository root: sh scripts/bench.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence

echo "== go test -bench =="
go test -run '^$' -bench . -benchmem ./... 2>&1 | tee evidence/bench.txt
