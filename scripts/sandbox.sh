#!/bin/sh
# Run a command inside the OOM-protected minimal sandbox container.
#
# All builds/tests/benchmarks go through here so nothing runs on the host. The
# container is hard-capped (memory + swap, CPUs, pids, fds); if a command tries to
# exceed the cap it is OOM-killed inside the container and the host is untouched.
#
#   sh scripts/sandbox.sh go test ./...
#   sh scripts/sandbox.sh go test -race ./internal/ekb
#   sh scripts/sandbox.sh go test -run '^$' -bench . -benchmem -benchtime 200ms ./internal/ekb
#
# Tunables: SANDBOX_MEM (default 2g), SANDBOX_CPUS (default 2), SANDBOX_IMAGE.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

IMAGE=${SANDBOX_IMAGE:-ekbloom-go-sandbox:latest}
MEM=${SANDBOX_MEM:-2g}
CPUS=${SANDBOX_CPUS:-2}

if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    echo "== building sandbox image ($IMAGE) ==" >&2
    docker build -f Dockerfile.sandbox -t "$IMAGE" . >&2
fi

# Mark the bind-mounted repo as a safe git directory: the files are owned by the
# host uid, not the container's root, so git refuses to read them ("dubious
# ownership") and `go build`/`go test` fail while stamping VCS info.
exec docker run --rm \
    --memory="$MEM" --memory-swap="$MEM" \
    --cpus="$CPUS" \
    --pids-limit=512 \
    --ulimit nofile=4096:4096 \
    --ulimit nproc=1024:1024 \
    -e GIT_CONFIG_COUNT=1 \
    -e GIT_CONFIG_KEY_0=safe.directory \
    -e GIT_CONFIG_VALUE_0=/src \
    -v "$root":/src -w /src \
    -v ekbloom-gocache:/tmp/gocache \
    -v ekbloom-gomod:/tmp/gomod \
    "$IMAGE" "$@"
