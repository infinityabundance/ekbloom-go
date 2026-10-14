# Migration and compatibility matrix

The Go port reads and writes the same frozen `.ekb`/`.stream` wire format as the
Rust reference (v0.5.x). Compatibility is not asserted from a spec reading alone:
it is proven against files the Rust implementation wrote.

## Read compatibility (Go reads Rust)

`testdata/fixtures/*.ekb` and `testdata/stream/golden.stream` are written by the
Rust reference (`fixturegen`). `compat_test.go` and `stream_test.go` assert:

| Claim | Test |
| --- | --- |
| Header/version/manifest parse identically | `TestRustFixturesAreByteIdentical` |
| `Machine.Encode(source)` reproduces the Rust core byte-for-byte | `TestRustFixturesAreByteIdentical` (`encode_matches`) |
| `ByteAt(i)` equals the source at every position | `TestRustFixturesAreByteIdentical` |
| `ReadAt(0, N)` equals the source | `TestRustFixturesAreByteIdentical` |
| `PhysicalBytes()` equals Rust `inspect_file(...).physical_bytes` | `TestRustFixturesAreByteIdentical` |
| Stream geometry, source hash, and reads match | `TestRustStreamGolden` |
| Keyed AUTH: Go verifies a Rust tag, Rust verifies a Go tag | `TestRustAuthenticatedFixture`, `scripts/roundtrip.sh` |

## Write compatibility (Rust reads Go)

`scripts/roundtrip.sh` writes `.ekb` (root/full/sparse) and `.stream` files with
the Go writer, then opens and fully decodes each with the Rust reference
(`.reference/fixturegen/src/bin/verifygo.rs`). A green run prints
`verified N file(s), 0 failure(s)`.

## Version tags

Three independent tags gate reading; an unknown value is a typed refusal, never a
guess:

| tag | where | error |
| --- | --- | --- |
| `format_major`/`format_minor` | genesis header | `ErrUnsupportedVersion` |
| `core_version` (`u16` @ offset 68) | genesis header | `ErrUnsupportedCoreVersion` |
| `manifest_version` | manifest record | `ErrUnsupportedManifestVersion` |
| stream `version` | stream header/footer | `ErrUnsupportedVersion` |

`core_version` is `0` (legacy/implicit) or `1` (current) for every machine 1–9;
the Go reader accepts both and rejects anything else for a known machine. An
unknown **machine id** is `ErrUnknownMachine` (the version check is not the
machine-lookup check).

## Behavior parity notes

- The Go reader's `PhysicalBytes()` mirrors the Rust `FileBody::inspect`
  (per-structure accounting, counted once), so it is an equal value, not a file
  length.
- `Estimate()` mirrors `estimate_physical_bytes_for_core`. It is an upper bound
  for machine-1 bodies; for cursor-divergent machines (2/4/7/8/9) the checkpoint
  estimate is exact/upper-bounded as documented in the reference.
- The Go writer emits generation 1 with `prev_generation = 0`; the Rust writer
  emits generation 1 for a fresh `create`+`write_body`. Both produce the same
  valid file; sections are the ones the chosen profile manifests.
