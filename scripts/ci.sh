#!/bin/sh
# Local CI: run every gate and capture its log under evidence/, so the repository
# carries the passing logs the reviewer asked for.
# Run from the repository root: sh scripts/ci.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence

echo "== go vet =="
go vet ./... 2>&1 | tee evidence/vet.log

echo
echo "== go test -race ./... =="
go test -race ./... 2>&1 | tee evidence/test-race.log

echo
echo "== go test -bench -benchmem =="
go test -run '^$' -bench . -benchmem -benchtime 200ms . 2>&1 | tee evidence/bench.txt

echo
echo "== cross-filesystem determinism =="
sh scripts/cross-fs.sh 2>&1 | tee evidence/cross-fs.log

echo
echo "== skewed small-record court =="
( cd courts && go run . ) 2>&1 | tee evidence/courts.log

if command -v cargo >/dev/null 2>&1 && [ -d .reference/fixturegen ]; then
    echo
    echo "== bidirectional Rust round-trip =="
    sh scripts/roundtrip.sh 2>&1 | tee evidence/roundtrip.log
else
    echo
    echo "note: Rust toolchain / .reference not present; skipping roundtrip.sh"
fi

echo
echo "== JSON summary =="
go test -json ./... 2>/dev/null | go run ./internal/tools/testsum | tee evidence/reproduce.json
