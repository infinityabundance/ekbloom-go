#!/bin/sh
# Bidirectional format proof: write .ekb files with the Go implementation, then
# open and fully decode each one with the Rust reference implementation.
#
# Requires the Rust toolchain and network access on first run (to build the
# reference crate in .reference/fixturegen). Run from the repository root:
#   sh scripts/roundtrip.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT

echo "== Go -> .ekb =="
( cd "$root" && go run ./internal/tools/roundtripgen "$out" )

echo
echo "== Rust reads the Go-written .ekb files =="
( cd "$root/.reference/fixturegen" && cargo run --quiet --release --bin verifygo -- "$out" )
