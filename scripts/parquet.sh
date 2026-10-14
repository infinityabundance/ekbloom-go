#!/bin/sh
# Run the Parquet dictionary-page integration inside the OOM-protected sandbox.
#
# It builds the EKBLOOM machine-9 comparison program in integrations/parquet (a
# separate module, so the Parquet dependency never enters the core module) and
# runs it hard-capped (memory + swap, CPUs, pids, fds). The container mounts the
# repository at /src; nothing heavy runs on the host.
#
# Outputs:
#   evidence/parquet-dict.json — the program's stdout (one JSON object)
#   evidence/parquet-dict.log  — the container's stderr diagnostics + build log
#
# Run from anywhere: sh scripts/parquet.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

image=${PARQUET_IMAGE:-ekbloom-go-sandbox:latest}
mem=${PARQUET_MEM:-2g}
cpus=${PARQUET_CPUS:-2}
out="$root/evidence/parquet-dict.json"
log="$root/evidence/parquet-dict.log"
mkdir -p "$root/evidence"

# Reuse the existing sandbox image (it is the capped Go runner); build it if absent.
if ! docker image inspect "$image" >/dev/null 2>&1; then
    echo "== building sandbox image ($image) ==" >&2
    docker build -f Dockerfile.sandbox -t "$image" . >&2
fi

# --- run (hard-capped); stdout is the JSON artifact, stderr is the log --------
echo "== docker run --memory=$mem --memory-swap=$mem --cpus=$cpus $image ==" >"$log"
echo "== go -C integrations/parquet mod tidy && go -C integrations/parquet run . ==" >>"$log"
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
      sh -c 'go -C integrations/parquet mod tidy && go -C integrations/parquet run .' \
      >"$out" 2>>"$log"; then
    echo "parquet integration failed; log follows:" >&2
    cat "$log" >&2
    exit 1
fi

cat "$log" >&2
echo
echo "== evidence/parquet-dict.json =="
cat "$out"
