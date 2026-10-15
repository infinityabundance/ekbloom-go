#!/bin/sh
# Run the RocksDB-style skewed small-record court (separate module with the
# zstd/lz4 competitors). Writes evidence/courts.json, evidence/courts-point.json
# and evidence/ycsb.json.
#
# Nothing heavy runs on the host: every court program runs INSIDE the OOM-capped
# sandbox via scripts/sandbox.sh (--memory=2g --memory-swap=2g --cpus=2
# --pids-limit=512, GOMAXPROCS=2), which also injects the provenance environment
# (image/tag/digest + caps) that each JSON artifact is stamped with.
#
# Run from the repository root: sh scripts/courts.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

echo "== skewed small-record court (sandbox) =="
sh "$root/scripts/sandbox.sh" sh -c 'cd courts && go mod tidy >/dev/null 2>&1 || true; go run .'
echo
echo "== 4 KiB point-read court (Pebble) =="
sh "$root/scripts/sandbox.sh" sh -c 'cd courts && go run ./point'
echo
echo "== YCSB value-log court (zipfian, vs zstd-dict) =="
sh "$root/scripts/sandbox.sh" sh -c 'cd courts && go run ./ycsb'

# Append to the performance history (bench-history.sh sandboxes its own run).
sh "$root/scripts/bench-history.sh" >/dev/null 2>&1 || true
