# WebAssembly

EKBLOOM-Go is pure Go (no cgo, no `unsafe`, no `mmap`, no `io_uring`), so it
targets WebAssembly unchanged. This is where the Go port pulls ahead of the Rust
reference, whose `mmap`/`io_uring`/SIMD subsystems do not port to wasm.

```sh
sh scripts/wasm.sh          # builds wasip1 + js/wasm; runs the WASI fingerprint
```

## What runs

- `GOOS=wasip1 GOARCH=wasm` — the whole module, including `Open`, `ByteAt`,
  `ReadAt`, `WorstCaseStepsFor`, `OpenAuthenticated`, `OpenWithDict` and the
  bounded `StreamReader` (all in-memory, no `mmap`). The fingerprint over the
  Rust-written fixtures is **byte-identical to amd64 and arm64**:
  `amd64 == arm64 == wasip1` (`evidence/wasip1-fingerprint.txt`).
- `GOOS=js GOARCH=wasm` — the CLI (`cmd/ekbloom`) and the library, for
  `verify`/`inspect` in a browser or a JS worker: pass a `Uint8Array` as `[]byte`,
  read `PhysicalBytes`, `WorstCaseStepsFor`, and `CorruptDetail` for localized
  corruption.

## Running under WASI

```sh
# Build the fingerprint tool and run it against the fixtures.
GOOS=wasip1 GOARCH=wasm go build -o /tmp/fingerprint.wasm ./internal/tools/fingerprint
wasmtime run --dir=/ /tmp/fingerprint.wasm "$PWD/testdata" | sha256sum
# -> 50675cfdfcea2e9d30d44a5fba8c9e8e42960668b4f7314c5712853ff7233720  (== amd64)
```

## Building for the browser / Node

```sh
GOOS=js GOARCH=wasm go build -o ekbloom.wasm ./cmd/ekbloom
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" .
```

In a page or worker, load `wasm_exec.js`, instantiate `ekbloom.wasm`, and call the
exported functions with the file bytes. The CLI form is compile-proof only under
`js/wasm` (its `os` file access does not exist there); for a JS host, call the
library directly through a `syscall/js` shim in your own wrapper.

## Why it matters

- **Format survivability across a third architecture.** The same bytes decode
  identically on `amd64`, `arm64` and `wasip1`, which is the strongest available
  evidence that the format depends only on integer arithmetic and BLAKE3.
- **Edge deployment.** In-browser `.ekb` verification for audit logs, and
  dictionary-encoded small records (`m9 83.5 vs 590 B/record`) at the edge with
  no native binary and no cgo toolchain.
