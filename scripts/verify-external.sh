#!/bin/sh
# Third-party reproduction: pull the published test image and run the full suite
# inside it, emitting the same JSON summary as evidence/reproduce.json. No clone,
# no toolchain, no Go on the host — only Docker.
#
#   sh scripts/verify-external.sh
#   IMAGE=ghcr.io/infinityabundance/ekbloom-go:test sh scripts/verify-external.sh
#   IMAGE=infinityabundance/ekbloom-go:test sh scripts/verify-external.sh
#
# The published image is built from the released tag; see .github/workflows/publish.yml.
set -eu

IMAGE=${IMAGE:-ghcr.io/infinityabundance/ekbloom-go:test}
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

echo "== pulling $IMAGE =="
docker pull "$IMAGE"

echo
echo "== running go test -json inside the image =="
raw=$(mktemp)
trap 'rm -f "$raw"' EXIT
docker run --rm --entrypoint sh "$IMAGE" -c 'go test -json ./...' > "$raw"
go run ./internal/tools/testsum < "$raw" | tee evidence/external-reproduce.json

echo
if grep -q '"failed": 0' evidence/external-reproduce.json && grep -q '"ok": true' evidence/external-reproduce.json; then
    echo "PASS: external image reports 0 failures (see evidence/external-reproduce.json)"
else
    echo "FAIL: external image reported failures"
    exit 1
fi

if [ -f evidence/reproduce.json ]; then
    echo
    echo "checked-in evidence/reproduce.json:"
    cat evidence/reproduce.json
fi
