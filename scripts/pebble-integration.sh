#!/bin/sh
# Run the EKBLOOM machine-9 Pebble *integration-depth* study inside the
# OOM-protected sandbox (courts/pebblebench, a package of the separate courts
# module). It extends scripts/pebble.sh with scan / ingest / compaction /
# cache-pressure scenarios on the same value-log DB.
#
# Hard-capped: memory + swap, CPUs, pids, fds. Nothing heavy runs on the host.
#
# Outputs:
#   evidence/pebble-integration.json  — the program's stdout (one JSON object)
#   evidence/pebble-integration.log   — the container's stderr table + build log
#   evidence/pebble-integration.prom  — the program's Prometheus-style exposition
#
# Run from anywhere: sh scripts/pebble-integration.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

image=${PEBBLE_IMAGE:-ekbloom-go-sandbox:latest}
mem=${PEBBLE_MEM:-2g}
cpus=${PEBBLE_CPUS:-2}
out="$root/evidence/pebble-integration.json"
log="$root/evidence/pebble-integration.log"
mkdir -p "$root/evidence"

# Reuse the existing sandbox image (it is the capped Go runner); build it if absent.
if ! docker image inspect "$image" >/dev/null 2>&1; then
    echo "== building sandbox image ($image) ==" >&2
    docker build -f Dockerfile.sandbox -t "$image" . >&2
fi

# --- run (hard-capped); stdout is the JSON artifact, stderr is the log -------
echo "== docker run --memory=$mem --memory-swap=$mem --cpus=$cpus $image ==" >"$log"
echo "== sh -c 'cd courts && go run ./pebblebench' ==" >>"$log"
if ! docker run --rm \
      --memory="$mem" --memory-swap="$mem" \
      --cpus="$cpus" \
      -e GOMAXPROCS="$cpus" \
      --pids-limit=512 \
      --ulimit nofile=4096:4096 \
      --ulimit nproc=1024:1024 \
      -e GOFLAGS="-mod=mod -buildvcs=false" \
      -v "$root":/src -w /src \
      -v ekbloom-gocache:/tmp/gocache \
      -v ekbloom-gomod:/tmp/gomod \
      "$image" \
      sh -c 'cd courts && go run ./pebblebench' \
      >"$out" 2>>"$log"; then
    echo "courts/pebblebench failed; log follows:" >&2
    cat "$log" >&2
    exit 1
fi

cat "$log" >&2
echo
echo "== evidence/pebble-integration.json =="
cat "$out"
