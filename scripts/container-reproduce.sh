#!/bin/sh
# Reproduce everything inside a minimal container and emit a JSON summary:
#   - go vet + the full test suite (JSON), summarized
#   - the runtime CLI inspecting a Rust-written fixture
# Run from the repository root: sh scripts/container-reproduce.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

echo "== build test image =="
docker build --target test -t ekbloom-go:test . >/dev/null

echo
echo "== go test -json (inside container) =="
docker run --rm --entrypoint sh ekbloom-go:test -c 'go test -json ./...' > evidence/container-test.json
go run ./internal/tools/testsum < evidence/container-test.json | tee evidence/container-reproduce.json

echo
echo "== runtime image + CLI on a Rust fixture =="
docker build --target runtime -t ekbloom-go:latest . >/dev/null
docker run --rm -v "$root/testdata":/data:ro ekbloom-go:latest inspect /data/fixtures/m2_root_skew.ekb
echo
docker run --rm -v "$root/testdata":/data:ro ekbloom-go:latest verify /data/fixtures/m2_root_skew.ekb
echo
docker run --rm -v "$root/testdata":/data:ro ekbloom-go:latest byte /data/fixtures/m1_root_text.ekb 0

echo
echo "done. summary: evidence/container-reproduce.json"
