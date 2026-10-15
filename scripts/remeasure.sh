#!/bin/sh
# One-command clean re-capture of every timing artifact, at EKB_RUNS=5, through the
# same hard-capped sandbox container (scripts/sandbox.sh: --memory=2g
# --memory-swap=2g --cpus=2 --pids-limit=512, GOMAXPROCS=2).
#
# Run this in an IDLE window to refresh evidence/ with low-contention numbers. It
# prints the captured provenance (loadavg + cpu_idle_pct) at the end, straight from
# the fresh byteat.json, so you can see what the host looked like when the run was
# taken.
#
# Nothing heavy runs on the host: every command below goes through a wrapper that
# itself runs inside the sandbox.
#
# Run from the repository root: sh scripts/remeasure.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

# The whole evidence set is regenerated at the same run count.
EKB_RUNS=${EKB_RUNS:-5}
export EKB_RUNS
echo "== remeasure: EKB_RUNS=$EKB_RUNS (median-of-runs) =="

echo
echo "== Go harnesses (byteat, sweep, concurrency, dictionary, readerat) =="
sh "$root/scripts/sandbox.sh" go test -count=1 ./internal/ekb

echo
echo "== micro-benchmarks (bench.txt) =="
sh "$root/scripts/bench.sh"

echo
echo "== performance history (bench-history.jsonl) =="
sh "$root/scripts/bench-history.sh" >/dev/null 2>&1 || true

echo
echo "== courts (courts.json, courts-point.json, ycsb.json) =="
sh "$root/scripts/courts.sh"

echo
echo "== Pebble value log (pebble-vlog.json) =="
sh "$root/scripts/pebble.sh"

echo
echo "== Pebble integration depth (pebble-integration.json) =="
sh "$root/scripts/pebble-integration.sh"

echo
echo "== Parquet dictionary court (parquet-dict.json) =="
sh "$root/scripts/parquet.sh"

echo
echo "== RocksDB value codec (rocksdb-blobdb.json) =="
sh "$root/scripts/rocksdb.sh"

echo
echo "== profiles + GC (profile.json, profile/gcstats.json) =="
sh "$root/scripts/profile.sh"

echo
echo "== captured provenance (from evidence/byteat.json) =="
awk '/^  "provenance": \{/{f=1} f{print} f&&/^  \},?$/{exit}' evidence/byteat.json

echo
echo "== done: evidence/ refreshed with provenance-stamped timings =="
