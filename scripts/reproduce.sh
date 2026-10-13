#!/bin/sh
# Clean build + full test suite, emitting a JSON summary of the local run.
# Run from the repository root: sh scripts/reproduce.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

echo "== go build =="
go build ./...

echo
echo "== go vet =="
go vet ./...

echo
echo "== go test (race) =="
go test -race ./... 2>&1 | tee /tmp/ekbloom-go-test.txt

echo
echo "== summary JSON =="
go test ./... -json 2>/dev/null | go run ./internal/tools/testsum > testdata/reproduce.json
cat testdata/reproduce.json
