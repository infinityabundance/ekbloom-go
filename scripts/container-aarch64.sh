#!/bin/sh
# Cross-architecture runtime determinism: build the test image for linux/arm64
# (running the full suite under emulation) and compare the decode fingerprints of
# the amd64 and arm64 builds. Requires qemu/binfmt (Docker Desktop or
# `docker run --privileged tonistiigi/binfmt --install arm64`).
# Run from the repository root: sh scripts/container-aarch64.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

echo "== build + run the full suite on linux/arm64 (qemu) =="
docker build --platform linux/arm64 --target test -t ekbloom-go:test-arm64 .

echo
echo "== native (amd64) fingerprint =="
go run ./internal/tools/fingerprint ./testdata > /tmp/fp-amd64.txt
head -3 /tmp/fp-amd64.txt

echo
echo "== arm64 fingerprint (qemu) =="
docker run --rm --platform linux/arm64 --entrypoint sh ekbloom-go:test-arm64 \
    -c 'go run ./internal/tools/fingerprint /src/testdata' > /tmp/fp-arm64.txt
head -3 /tmp/fp-arm64.txt

echo
if diff -u /tmp/fp-amd64.txt /tmp/fp-arm64.txt; then
    echo "PASS: identical fingerprints on amd64 and arm64"
else
    echo "FAIL: fingerprints differ across architectures"
    exit 1
fi
