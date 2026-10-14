#!/bin/sh
# Build the minimal runtime image.
# Run from the repository root: sh scripts/container-image.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

docker build --target runtime -t ekbloom-go:latest -t ekbloom-go:runtime .
echo
echo "built ekbloom-go:latest"
docker image ls ekbloom-go
echo
docker run --rm ekbloom-go:latest version
