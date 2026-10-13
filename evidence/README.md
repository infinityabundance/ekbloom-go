# Evidence

| Artifact | What it proves |
| --- | --- |
| `environment.txt` | the toolchain, determinism, and cross-compile results for the local run |
| `bench.txt` | raw `go test -bench` output (`scripts/bench.sh`) |
| `fuzz/` | seed corpus for the fuzzers (the interesting corpus accumulates in the Go build cache) |

The **primary** evidence is executable and lives in `testdata/` and the test
suite:

- `testdata/fixtures/*.ekb` — `.ekb` files written by the Rust reference
  implementation (`fixturegen`), with sibling `.src` sources and `.dict`
  dictionaries.
- `testdata/stream/golden.stream` — a Rust-written stream container.
- `testdata/blake3_vectors.json` — BLAKE3 digests (unkeyed and keyed) produced by
  the Rust `blake3` crate.
- `testdata/fixtures.json`, `testdata/stream.json` — expected metadata
  (`physical_bytes`, geometry, hashes) asserted by `compat_test.go` /
  `stream_test.go`.
- `testdata/reproduce.json` — the JSON summary emitted by `scripts/reproduce.sh`.

Regenerate the Rust-side fixtures with:

```sh
cd .reference/fixturegen && cargo run --release --bin fixturegen
```

Re-run the Rust verifier against freshly written Go files with:

```sh
sh scripts/roundtrip.sh
```
