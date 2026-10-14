#!/bin/sh
# Third-party reproduction from the published image.
#
# Pulls the published test image ANONYMOUSLY (its GHCR packages are public),
# resolves the immutable digest of exactly what was pulled, then runs the whole
# thing — `go test -json ./...` and the testsum summary — INSIDE that image.
# Nothing runs Go on the host: only Docker. No clone and no toolchain are needed
# on the host; the only host file used is the tiny, versioned testsum helper,
# mounted read-only at /testsum.
#
#   sh scripts/verify-external.sh
#   IMAGE=ghcr.io/infinityabundance/ekbloom-go:test       sh scripts/verify-external.sh
#   IMAGE=ghcr.io/infinityabundance/ekbloom-go:0.5.0-test sh scripts/verify-external.sh
#
# The published image is built by .github/workflows/publish.yml and verified in
# CI by .github/workflows/external-reproduce.yml.
set -eu

IMAGE=${IMAGE:-ghcr.io/infinityabundance/ekbloom-go:test}
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

# An empty DOCKER_CONFIG means the pull cannot use cached credentials, so success
# proves the image is publicly (anonymously) pullable.
anon=$(mktemp -d)
trap 'rm -rf "$anon"' EXIT

echo "== pulling $IMAGE anonymously =="
DOCKER_CONFIG="$anon" docker pull "$IMAGE" >/dev/null

# Prefer the RepoDigest for the repo we actually pulled (an image present under
# several registries has one per repo), so `reference` names the same registry
# as $IMAGE; fall back to the first entry.
repo=${IMAGE%:*}
reference=$(docker inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$IMAGE" \
    | grep -F "${repo}@" | head -n1 || true)
[ -n "$reference" ] || reference=$(docker inspect --format '{{index .RepoDigests 0}}' "$IMAGE")
digest=${reference##*@}
case $IMAGE in
    *:*) tag=${IMAGE##*:} ;;
    *)   tag=latest ;;
esac
echo "reference: $reference"
echo "digest:    $digest"

echo
echo "== running the suite + summary inside $IMAGE (no host Go) =="
# Memory is capped so a runaway image cannot OOM the host, but the CPU is *not*
# quota-limited: the suite contains timing assertions, and a CFS cpu.max quota
# (e.g. --cpus=2) throttles runnable threads for up to the quota period, which
# makes those tests flap. No quota => the image runs at host speed.
docker run --rm \
    --memory=2g --memory-swap=2g --pids-limit=512 \
    -v "$root/internal/tools/testsum":/testsum:ro \
    --entrypoint sh "$IMAGE" -c '
        go test -json ./... > /tmp/raw.json 2>/tmp/test.err
        ec=$?
        if [ "$ec" -ne 0 ]; then
            echo "note: the suite reported failures (go test exit $ec); summarizing anyway" >&2
            tail -n 40 /tmp/test.err >&2
        fi
        GOFLAGS= go run /testsum/main.go "$@" < /tmp/raw.json
    ' sh \
    -image "$IMAGE" -tag "$tag" -reference "$reference" -digest "$digest" -anonymous=true \
    > evidence/external-reproduce.json

cat evidence/external-reproduce.json

echo
if grep -q '"failed": 0' evidence/external-reproduce.json &&
   grep -q '"ok": true' evidence/external-reproduce.json; then
    echo "PASS: $reference reports 0 failures (see evidence/external-reproduce.json)"
else
    echo "FAIL: external image reported failures" >&2
    exit 1
fi

if [ -f evidence/reproduce.json ]; then
    echo
    echo "checked-in evidence/reproduce.json:"
    cat evidence/reproduce.json
fi
