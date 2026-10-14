#!/bin/sh
# Build the test image (vet + full test suite) and run it.
# Run from the repository root: sh scripts/container-test.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

docker build --target test -t ekbloom-go:test .
echo
echo "== running the suite inside the container =="
docker run --rm --entrypoint sh ekbloom-go:test -c 'go vet ./... && go test ./...'
