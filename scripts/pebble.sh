#!/bin/sh
# Run the EKBLOOM machine-9 Pebble value-log case study inside the OOM-protected
# sandbox (courts/pebble, a package of the separate courts module).
#
# Hard-capped: memory + swap, CPUs, pids, fds. Nothing heavy runs on the host.
#
# Outputs:
#   evidence/pebble-vlog.json  — the program's stdout (one JSON object)
#   evidence/pebble-vlog.log   — the container's stderr table + build log
#   evidence/pebble-vlog.prom  — the program's Prometheus-style exposition
#
# Run from anywhere: sh scripts/pebble.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

image=${PEBBLE_IMAGE:-ekbloom-go-sandbox:latest}
mem=${PEBBLE_MEM:-2g}
cpus=${PEBBLE_CPUS:-2}
out="$root/evidence/pebble-vlog.json"
log="$root/evidence/pebble-vlog.log"
mkdir -p "$root/evidence"

# Reuse the existing sandbox image (it is the capped Go runner); build it if absent.
if ! docker image inspect "$image" >/dev/null 2>&1; then
    echo "== building sandbox image ($image) ==" >&2
    docker build -f Dockerfile.sandbox -t "$image" . >&2
fi

# --- run (hard-capped); stdout is the JSON artifact, stderr is the log -------
echo "== docker run --memory=$mem --memory-swap=$mem --cpus=$cpus $image ==" >"$log"
echo "== sh -c 'cd courts && go run ./pebble' ==" >>"$log"
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
      sh -c 'cd courts && go run ./pebble' \
      >"$out" 2>>"$log"; then
    echo "courts/pebble failed; log follows:" >&2
    cat "$log" >&2
    exit 1
fi

cat "$log" >&2
echo
echo "== evidence/pebble-vlog.json =="
cat "$out"
