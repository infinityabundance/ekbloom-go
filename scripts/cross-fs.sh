#!/bin/sh
# Cross-filesystem (and optionally cross-architecture) determinism check.
#
# Copies the fixtures to two different filesystems (a tmpfs and the repository's
# on-disk filesystem) and confirms the fingerprint — machine id, physical bytes,
# and a SHA-256 over the fully decoded logical bytes — is identical.
# Run from the repository root: sh scripts/cross-fs.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

disk="$root/.xfstest-disk"
# Prefer a real tmpfs; fall back to /tmp.
tmpfs=/dev/shm/ekbloom-go-xfs
if [ ! -w /dev/shm ]; then tmpfs=/tmp/ekbloom-go-xfs; fi

rm -rf "$disk" "$tmpfs"
mkdir -p "$disk" "$tmpfs"

disk_fp="$disk/_fp.txt"
tmp_fp="$tmpfs/_fp.txt"

echo "== deriving fingerprint on disk FS =="
cp -r testdata "$disk/"
go run ./internal/tools/fingerprint "$disk/testdata" > "$disk_fp"

echo "== deriving fingerprint on tmpfs ($tmpfs) =="
cp -r testdata "$tmpfs/"
go run ./internal/tools/fingerprint "$tmpfs/testdata" > "$tmp_fp"

echo
if diff -u "$disk_fp" "$tmp_fp"; then
    echo "PASS: fingerprints identical across disk and tmpfs"
else
    echo "FAIL: fingerprints differ"
    exit 1
fi

echo
echo "fingerprint sample:"
head -5 "$disk_fp"

# Optional cross-architecture build check (compile only; run under qemu to compare).
if command -v qemu-aarch64-static >/dev/null 2>&1 || command -v qemu-aarch64 >/dev/null 2>&1; then
    echo
    echo "== cross-arch: building arm64 fingerprint =="
    GOOS=linux GOARCH=arm64 go build -o "$disk/fp-arm64" ./internal/tools/fingerprint
    echo "built arm64 fingerprint binary (run under qemu to compare output)"
else
    echo
    echo "note: no qemu-aarch64 found; arm64 determinism is a build-only check here"
    GOOS=linux GOARCH=arm64 go build -o "$disk/fp-arm64" ./internal/tools/fingerprint
    echo "arm64 build OK (run under qemu on an arm64 host to compare)"
fi

rm -rf "$disk" "$tmpfs"
