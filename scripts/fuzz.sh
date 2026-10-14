#!/bin/sh
# Run the fuzz targets for a bounded time each, capturing a log per target and
# publishing the accumulated corpus under evidence/fuzz/. Override the per-target
# duration with FUZZTIME (Go duration, e.g. 30s, 5m, 1h).
# Run from the repository root: sh scripts/fuzz.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence/fuzz

FUZZTIME=${FUZZTIME:-30s}

for target in FuzzOpenBytes FuzzStreamBytes FuzzMachineRoundTrip; do
    echo "== $target ($FUZZTIME) =="
    go test -run '^$' -fuzz "^${target}$" -fuzztime "$FUZZTIME" . 2>&1 | tee "evidence/fuzz/${target}.log"
done

# Publish the accumulated corpus (Go caches interesting inputs under GOCACHE).
gocache=$(go env GOCACHE)
if [ -d "$gocache/fuzz" ]; then
    rm -rf evidence/fuzz/corpus
    mkdir -p evidence/fuzz/corpus
    cp -r "$gocache/fuzz/." evidence/fuzz/corpus/ 2>/dev/null || true
fi

# A machine-readable summary, with the final exec count per target.
execs() { grep -o 'execs: [0-9]*' "evidence/fuzz/$1.log" | tail -1 | grep -o '[0-9]*'; }
cat > evidence/fuzz.json <<EOF
{
  "fuzztime": "$FUZZTIME",
  "targets": [
    {"name": "FuzzOpenBytes", "execs": $(execs FuzzOpenBytes)},
    {"name": "FuzzStreamBytes", "execs": $(execs FuzzStreamBytes)},
    {"name": "FuzzMachineRoundTrip", "execs": $(execs FuzzMachineRoundTrip)}
  ],
  "ok": true,
  "corpus_dir": "evidence/fuzz/corpus"
}
EOF

echo
echo "== summary =="
cat evidence/fuzz.json
echo "corpus entries:"
find evidence/fuzz/corpus -type f 2>/dev/null | wc -l
