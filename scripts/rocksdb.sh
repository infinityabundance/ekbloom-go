#!/bin/sh
# Build and run the RocksDB value-codec integration for EKBLOOM machine-9.
#
# Everything heavy runs inside Docker: the c-shared bridge is built in the
# golang builder stage and the C++ bench is compiled and run in the Ubuntu stage.
# The run is hard-capped (memory + swap, CPUs, pids, fds).
#
# Outputs:
#   evidence/rocksdb-blobdb.json  — the benchmark's stdout (one JSON object)
#   evidence/rocksdb-blobdb.log   — build log + the run's stderr diagnostics
#
# Run from anywhere: sh scripts/rocksdb.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

image=${ROCKSDB_IMAGE:-ekbloom-go-rocksdb-blobdb:local}
mem=${ROCKSDB_MEM:-2g}
cpus=${ROCKSDB_CPUS:-2}
out="$root/evidence/rocksdb-blobdb.json"
log="$root/evidence/rocksdb-blobdb.log"
mkdir -p "$root/evidence"

# --- build (still Docker; the Go/C++ toolchains run inside the image) --------
if ! {
  echo "== docker build -f integrations/rocksdb/Dockerfile.rocksdb -t $image . =="
  docker build -f integrations/rocksdb/Dockerfile.rocksdb -t "$image" .
} >"$log" 2>&1; then
  echo "rocksdb-blobdb build failed; log follows:" >&2
  cat "$log" >&2
  exit 1
fi

# --- run (hard-capped); stdout is the JSON artifact, stderr is the log -------
image_id=$(docker image inspect --format '{{.Id}}' "$image")
echo "== docker run --memory=$mem --memory-swap=$mem --cpus=$cpus $image ==" >>"$log"
if ! docker run --rm \
      --memory="$mem" --memory-swap="$mem" \
      --cpus="$cpus" \
      -e GOMAXPROCS="$cpus" \
      -e EKB_RUNS \
      -e CI \
      -e EKB_CONTAINER_IMAGE="$image" \
      -e EKB_CONTAINER_IMAGE_ID="$image_id" \
      -e EKB_LIMIT_MEM="$mem" \
      -e EKB_LIMIT_CPUS="$cpus" \
      -e EKB_LIMIT_PIDS=512 \
      --pids-limit=512 \
      --ulimit nofile=4096:4096 \
      "$image" >"$out" 2>>"$log"; then
  echo "rocksdb-blobdb bench failed; log follows:" >&2
  cat "$log" >&2
  exit 1
fi

cat "$log"
echo
echo "== evidence/rocksdb-blobdb.json =="
cat "$out"
