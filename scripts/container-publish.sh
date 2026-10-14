#!/bin/sh
# Build and push the minimal runtime image to a registry (Docker Hub by default),
# multi-arch. Requires an authenticated `docker login` and a buildx builder that
# supports multiple platforms.
#
# Usage:
#   sh scripts/container-publish.sh                 # local tag, dry build
#   TAG=v0.1.0 sh scripts/container-publish.sh      # tagged
#   IMAGE=ghcr.io/infinityabundance/ekbloom-go TAG=v0.1.0 sh scripts/container-publish.sh
#
# Environment:
#   IMAGE      target repository         (default infinityabundance/ekbloom-go)
#   TAG        primary tag               (default a UTC date stamp)
#   PLATFORMS  buildx platforms          (default linux/amd64,linux/arm64)
#   PUSH       set to 0 to build only    (default 1)
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

IMAGE=${IMAGE:-infinityabundance/ekbloom-go}
TAG=${TAG:-$(date -u +%Y%m%d)}
PLATFORMS=${PLATFORMS:-linux/amd64,linux/arm64}
PUSH=${PUSH:-1}

if ! docker buildx version >/dev/null 2>&1; then
    echo "error: docker buildx is required" >&2
    exit 1
fi

if [ "$PUSH" = 0 ]; then
    # `--load` cannot export a multi-arch manifest list; build the host arch only.
    case "$(uname -m)" in
        x86_64 | amd64) PLATFORMS=linux/amd64 ;;
        aarch64 | arm64) PLATFORMS=linux/arm64 ;;
    esac
fi

if [ "$PUSH" = 1 ]; then
    # Fail early with a clear message rather than after a long multi-arch build.
    if ! docker info >/dev/null 2>&1; then
        echo "error: docker daemon not reachable" >&2
        exit 1
    fi
    if ! grep -q '"auths"' "${HOME}/.docker/config.json" 2>/dev/null; then
        echo "error: not logged in to a registry; run 'docker login' first" >&2
        exit 1
    fi
fi

echo "== buildx ($PLATFORMS) -> $IMAGE:$TAG and $IMAGE:latest (push=$PUSH) =="
set -- buildx build \
    --platform "$PLATFORMS" \
    --target runtime \
    -t "$IMAGE:$TAG" \
    -t "$IMAGE:latest" \
    --label "org.opencontainers.image.version=$TAG" \
    --label "org.opencontainers.image.revision=$(git rev-parse HEAD 2>/dev/null || echo unknown)"
if [ "$PUSH" = 1 ]; then
    set -- "$@" --push .
else
    set -- "$@" --load .
fi
docker "$@"

echo
if [ "$PUSH" = 1 ]; then
    echo "pushed $IMAGE:$TAG and $IMAGE:latest"
else
    echo "built $IMAGE:$TAG (not pushed)"
fi
docker buildx imagetools inspect "$IMAGE:$TAG" 2>/dev/null || true
