#!/bin/sh
# WebAssembly build + determinism.
#
# EKBLOOM-Go is pure Go with no cgo, no unsafe, no mmap, so it targets WebAssembly
# unchanged. This script:
#   - builds the whole module for wasip1 and js/wasm (compile proof);
#   - builds the WASI fingerprint tool and, if a WASI runtime is available
#     (wasmtime or wasmer), runs it and requires the fingerprint to equal the
#     recorded amd64/arm64 fingerprint (amd64 == arm64 == wasip1).
#
# WASI needs the fixtures visible to the sandbox, so the runtime is invoked with a
# preopened directory; absolute paths are used so the guest cwd does not matter.
#
# Run from the repository root: sh scripts/wasm.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "== build wasip1 =="
GOOS=wasip1 GOARCH=wasm go build ./...
GOOS=wasip1 GOARCH=wasm go build -o "$work/fingerprint.wasm" ./internal/tools/fingerprint

echo "== build js/wasm =="
GOOS=js GOARCH=wasm go build ./...
GOOS=js GOARCH=wasm go build -o "$work/ekbloom.wasm" ./cmd/ekbloom

runtime=""
if command -v wasmtime >/dev/null 2>&1; then
    runtime=wasmtime
elif [ -x "$HOME/.wasmtime/bin/wasmtime" ]; then
    runtime="$HOME/.wasmtime/bin/wasmtime"
elif command -v wasmer >/dev/null 2>&1; then
    runtime=wasmer
fi

if [ -z "$runtime" ]; then
    echo
    echo "note: no WASI runtime (wasmtime/wasmer) found; wasip1 is build-only here"
    exit 0
fi

echo
echo "== WASI fingerprint ($runtime) =="
case "$(basename "$runtime")" in
    wasmtime) "$runtime" run --dir=/ "$work/fingerprint.wasm" "$root/testdata" > evidence/wasip1-fingerprint.txt ;;
    wasmer)   "$runtime" run --dir "$root" "$work/fingerprint.wasm" "$root/testdata" > evidence/wasip1-fingerprint.txt ;;
    *)        echo "note: unknown runtime $runtime"; exit 0 ;;
esac

got=$(sha256sum evidence/wasip1-fingerprint.txt | cut -d' ' -f1)
want=$(grep -o '"amd64_fingerprint_sha256": "[0-9a-f]*"' evidence/aarch64-determinism.json | grep -o '[0-9a-f]\{64\}')
echo "amd64/arm64: $want"
echo "wasip1:      $got"
if [ "$want" = "$got" ]; then
    echo "PASS: amd64 == arm64 == wasip1"
else
    echo "FAIL: wasip1 fingerprint differs"
    exit 1
fi
