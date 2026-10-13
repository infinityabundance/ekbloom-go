#!/bin/sh
# Run the fuzz targets for a bounded time each. Override the per-target duration
# with FUZZTIME (Go duration, e.g. 30s, 5m, 1h).
# Run from the repository root: sh scripts/fuzz.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence/fuzz

FUZZTIME=${FUZZTIME:-30s}

for target in FuzzOpenBytes FuzzStreamBytes FuzzMachineRoundTrip; do
    echo "== $target ($FUZZTIME) =="
    go test -run '^$' -fuzz "^${target}$" -fuzztime "$FUZZTIME" .
done

echo
echo "corpus:"; ls -R testdata/fuzz 2>/dev/null || true
