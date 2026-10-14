#!/bin/sh
# Local CI: run every gate and capture its log under evidence/, so the repository
# carries the passing logs a reviewer asked for. A failing gate fails the script
# (unlike a bare `cmd | tee`, which would hide the exit status).
# Run from the repository root: sh scripts/ci.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence

# logged <log> <cmd...>: run cmd, capture combined output to <log>, echo it, and
# exit non-zero if cmd failed.
logged() {
    log=$1
    shift
    if "$@" >"$log" 2>&1; then
        cat "$log"
    else
        status=$?
        cat "$log" >&2
        echo "FAILED ($status): $*" >&2
        exit "$status"
    fi
}

echo "== go vet =="
logged evidence/vet.log go vet ./...

echo
echo "== go test -race ./... =="
logged evidence/test-race.log go test -race ./...

echo
echo "== go test -bench -benchmem =="
logged evidence/bench.txt go test -run '^$' -bench . -benchmem -benchtime 200ms ./internal/ekb

echo
echo "== cross-filesystem determinism =="
logged evidence/cross-fs.log sh scripts/cross-fs.sh

echo
echo "== skewed small-record court =="
logged evidence/courts.log sh -c 'cd courts && go run .'

if command -v cargo >/dev/null 2>&1 && [ ! -d .reference/fixturegen ]; then
    sh scripts/setup-reference.sh >/dev/null 2>&1 || true
fi

if command -v cargo >/dev/null 2>&1 && [ -d .reference/fixturegen ]; then
    echo
    echo "== bidirectional Rust round-trip =="
    logged evidence/roundtrip.log sh scripts/roundtrip.sh
else
    echo
    echo "note: Rust toolchain / .reference not present; skipping roundtrip.sh"
fi

echo
echo "== JSON summary =="
go test -json ./... 2>/dev/null > evidence/reproduce.raw.json || true
go run ./internal/tools/testsum < evidence/reproduce.raw.json | tee evidence/reproduce.json
rm -f evidence/reproduce.raw.json
