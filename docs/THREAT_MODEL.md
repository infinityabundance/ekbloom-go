# Threat model

Scope: an application embeds `ekbloom` and reads/writes `.ekb`/`.stream` files
that may come from an untrusted source. This document states what the format
protects, what it does not, and what the caller must own.

## Assets

| Asset | Owner | Protected by |
| --- | --- | --- |
| Logical bytes (the source) | caller | CRC-32 per section, BLAKE3 `core_hash`, manifest hash |
| File structure (header/sections/footer) | caller | structural validation, footer CRC, version typing |
| Authenticity (who wrote it) | caller's key | opt-in keyed `AUTH` (`blake3::keyed_hash`) |
| Freshness (which generation) | caller | **not protected** — see non-goals |

## Adversaries

1. **Corruption** (bit rot, torn write, partial flush). Detected, always, and
   localised to `Corrupt{Section, Offset}` within one structure (≤4096 B).
   Measured: 100% of covered bits in every structure (`corruption_test.go`).
2. **Malicious bytes** (structured fuzz input). Never panics; every failure is a
   typed `*Error`; declared lengths are capped by the resource policy.
3. **A writer without the key** (forgery). `AUTH` is fail-closed: a missing,
   stripped or mismatching tag is a typed error, never a fallback to an unkeyed
   read.
4. **A writer *with* the key** (malicious insider). Out of scope beyond AUTH: the
   MAC proves the bytes came from a key holder.
5. **A rollback attacker** (replay an older valid file). **Not defended** — see
   below.

## AUTH: what it covers, exactly

`AUTH` = `blake3::keyed_hash(K, blake3(file[0..auth_header_offset]))` — a 32-byte
tag covering the genesis header, the core, alignment padding, and every section up
to and including the manifest.

**In coverage:** header, core, padding, all sections, manifest.
**Out of coverage (validated structurally, not MAC-covered):** the commit footer
and the AUTH tag itself; they are cross-checked against the manifest.

## Non-goals (explicit)

- **No anti-rollback / freshness.** An attacker with write access can truncate to
  an earlier, still-valid generation and it will verify. AUTH is integrity +
  authenticity of the covered bytes, *not* freshness. If the caller needs
  freshness, pin the expected `object_id` / generation out of band (e.g. in a
  signed log) and reject anything older.
- **No confidentiality.** The format does not encrypt; the logical bytes are
  reconstructible from the file by anyone who can read it. Confidentiality is the
  caller's layer.
- **No protection against a hostile key holder.** A key holder can produce any
  authenticated file.

## Key management

- `K` is a 32-byte caller-owned secret; it is never stored in the file and never
  printed. There is no key material in this repository.
- Rotation is a re-write under the new key; see the runbook in
  `docs/SECURITY.md` (rotate, do not mutate; re-write from a trusted plaintext copy
  on suspected compromise).
- A reader must treat a verification failure as fatal and must never silently fall
  back to an unkeyed read (the implementation does not).

## Denial of service

- Whole-object allocations driven by a declared length are capped by the resource
  policy (default **1 GiB**); a rejected allocation is `ErrAllocationTooLarge` and
  increments the breach counter (`metrics.go`). A hostile header cannot force a
  transient gigabyte allocation.
- Parsing is bounded and non-recursive; `worst_case_steps_for` is a certified
  bound (1.2M samples, 0 violations).

## Residual risks (caller-visible)

| Risk | Mitigation |
| --- | --- |
| Rollback to an older valid generation | pin expected `object_id`/generation out of band |
| Hostile key holder | out of scope; use HSMs / separate trust domains |
| Confidentiality of stored bytes | encrypt at the caller's layer |
| Footer/AUTH tag not MAC-covered | structural validation + footer↔manifest cross-check |
