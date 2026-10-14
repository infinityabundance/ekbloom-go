#!/bin/sh
# aarch64 benchmark numbers, run inside the linux/arm64 test image (qemu).
# Writes evidence/aarch64-bench.txt.
# Run from the repository root: sh scripts/container-aarch64-bench.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence

echo "== build linux/arm64 test image =="
docker build --platform linux/arm64 --target test -t ekbloom-go:test-arm64 . >/dev/null

echo "== go test -bench on linux/arm64 (qemu) =="
docker run --rm --platform linux/arm64 --entrypoint sh ekbloom-go:test-arm64 \
    -c 'go test -run "^$" -bench "BenchmarkMachine|ByteAt|ResidentView|BareSlice|Blake3" -benchmem -benchtime 1x ./internal/ekb' \
    | tee evidence/aarch64-bench.txt
