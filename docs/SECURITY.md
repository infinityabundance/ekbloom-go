# Security

## Threat model

Untrusted `.ekb`/`.stream` bytes may be supplied by an attacker. The Go port
treats every byte as untrusted: no native-struct reinterpretation, no unchecked
lengths, no `unsafe`, no panics. Every malformed input is a typed `*Error`.

## Integrity and corruption detection

- Every section payload carries a CRC-32/IEEE checksum in its header.
- The `MANIFEST` payload is committed by a BLAKE3 hash in the 64-byte footer, and
  the footer has its own CRC-32.
- The visible core is bound to the manifest by BLAKE3 `core_hash`.
- Every machine-6 segment carries an independent BLAKE3 `content_id`.
- `Verify` / `VerifyDeep` localize the first divergence to a structure and an
  absolute offset: `Corrupt { Section, Offset }`.

**Measured.** `corruption_test.go` flips up to 1024 single bits in every covered
structure of every fixture (core and every section payload) and requires 100%
detection. Detection granularity is the containing structure, bounded to ≤4096
bytes for every structure the writer emits. **Inert alignment padding and reserved
header metadata are never interpreted**, so a flip confined to them is not (and
must not be) reported — matching the reference contract.

## Authenticity (opt-in AUTH)

The default format is unkeyed and detects accidental corruption, not forgery. The
opt-in `AUTH` section (32 bytes) is
`blake3::keyed_hash(K, blake3(file[0..auth_header_offset]))`, covering the header,
core, padding and every section up to and including the manifest. It is written
after the manifest and immediately before the footer.

- **Key lifecycle.** The key `K` is a 32-byte secret owned by the caller; it is
  never stored in the file and never printed. Rotation is a re-write: produce a
  new authenticated image under the new key. There is no key material in this
  repository.
- **Coverage gap (honest).** The commit footer and the AUTH tag itself lie outside
  the literal MAC range `[0, auth_header_offset)`; they are validated structurally
  and by the footer↔manifest cross-check, not MAC-covered.
- **Anti-rollback (honest limit).** There is **no** anti-rollback: an attacker
  with write access can truncate the file to an earlier, still-valid generation.
  `AUTH` provides integrity and authenticity of the covered bytes, not freshness.

## Resource policy

Any whole-object allocation driven by a declared length is capped by the resource
policy (default 1 GiB). A rejected allocation is `ErrAllocationTooLarge` and
increments a breach counter (`Breaches()`), so a hostile declared length cannot
force a transient gigabyte allocation. `SetMaxSingleAllocation` adjusts the cap;
`ResetBreaches` clears the counter.

## `unsafe` and cgo

There is **no** `unsafe` and **no** cgo anywhere in this module. The only
low-level primitive is the pure-Go BLAKE3 in `internal/blake3`, which is pinned to
the Rust `blake3` crate by generated vectors (`testdata/blake3_vectors.json`).
