#!/bin/sh
# Cross-architecture runtime determinism: build the test image for linux/arm64
# (running the full suite under emulation) and compare the decode fingerprints of
# the amd64 and arm64 builds. Writes evidence/aarch64-determinism.json.
# Requires qemu/binfmt (Docker Desktop, `docker run --privileged
# tonistiigi/binfmt --install arm64`, or docker/setup-qemu-action in CI).
# Run from the repository root: sh scripts/container-aarch64.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence

echo "== build + run the full suite on linux/arm64 (qemu) =="
docker build --platform linux/arm64 --target test -t ekbloom-go:test-arm64 .

echo
echo "== native (amd64) fingerprint =="
go run ./internal/tools/fingerprint ./testdata > /tmp/fp-amd64.txt
head -3 /tmp/fp-amd64.txt

echo
echo "== arm64 fingerprint (qemu) =="
docker run --rm --platform linux/arm64 --entrypoint sh ekbloom-go:test-arm64 \
    -c 'go run ./internal/tools/fingerprint /src/testdata' > /tmp/fp-arm64.txt
head -3 /tmp/fp-arm64.txt

sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
amd=$(sha /tmp/fp-amd64.txt)
arm=$(sha /tmp/fp-arm64.txt)
files=$(wc -l < /tmp/fp-amd64.txt | tr -d ' ')

identical=true
if ! diff -u /tmp/fp-amd64.txt /tmp/fp-arm64.txt >/dev/null; then
    identical=false
fi

cat > evidence/aarch64-determinism.json <<EOF
{
  "platforms": ["linux/amd64", "linux/arm64"],
  "identical": $identical,
  "files": $files,
  "amd64_fingerprint_sha256": "$amd",
  "arm64_fingerprint_sha256": "$arm"
}
EOF

echo
cat evidence/aarch64-determinism.json
if [ "$identical" = "true" ]; then
    echo "PASS: identical fingerprints on amd64 and arm64"
else
    echo "FAIL: fingerprints differ across architectures"
    exit 1
fi
