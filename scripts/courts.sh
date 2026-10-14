#!/bin/sh
# Run the RocksDB-style skewed small-record court (separate module with the
# zstd/lz4 competitors). Writes evidence/courts.json.
# Run from the repository root: sh scripts/courts.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root/courts"

go mod tidy >/dev/null 2>&1 || true
go run .
echo
echo "== 4 KiB point-read court (Pebble) =="
go run ./point
