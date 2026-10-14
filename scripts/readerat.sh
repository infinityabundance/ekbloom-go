#!/bin/sh
# Allocation-sensible I/O surface: an io.ReaderAt adapter, batch reads and
# cancellation-aware reads for the engine in internal/ekb.
#
# Runs the readerat tests under the race detector inside the hard-capped sandbox
# (never on the host) and writes evidence/readerat.json. Runnable from the repo
# root:
#
#   sh scripts/readerat.sh
#
# The JSON records the section-copy / batch-equality / 64-goroutine-race results
# plus the batch p50 and the cancellation return latency, e.g.:
#
#   {"readerat_section_copy":"pass","batch_equal":"pass",
#    "concurrent_64_race":"pass","cancel_latency_ns":...,"batch_p50_ns":...}
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence

echo "== readerat tests (-race, capped sandbox) ==" >&2
sh scripts/sandbox.sh go test -race -count=1 -run 'TestReaderAt' -v ./internal/ekb

if [ ! -s evidence/readerat.json ]; then
    echo "readerat: evidence/readerat.json was not written" >&2
    exit 1
fi

echo "== evidence/readerat.json ==" >&2
cat evidence/readerat.json
