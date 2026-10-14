# EKBLOOM conformance specification

**This document is the authority for the EKBLOOM `.ekb` byte format and for the
conformance suite that both implementations are judged against.** It is
deliberately *standalone*: the rules below are stated normatively here, not
delegated to `docs/CORE-LAYOUT.md`, to the Go module, or to the Rust crate. Where
a rule names a fixture, that fixture (and its recorded expectations in
`manifest.json`) is a concrete instance of the rule.

Neither implementation owns this document. A Go reader, a Rust reader, and a
third-party reader are all judged by the same text and the same immutable golden
files in `fixtures/`. Changing the byte format, the error taxonomy, the
accounting rules, or the resource bounds **requires a format-version bump and a
note in this file** (§10). Changing a fixture likewise requires a
`format_version` bump in `manifest.json` (§9), because both implementations read
that manifest and neither regenerates it at test time.

The keywords MUST, MUST NOT, SHOULD, and MAY are used as in RFC 2119.

---

## 1. Scope

This specification covers:

1. the top-level `.ekb` container layout (genesis header, core, commit blocks,
   sections, manifest, footer, optional AUTH);
2. the nine **cores** (machines 1–9) and their decode semantics;
3. the **versioning tags** that freeze a wire dialect;
4. the **error taxonomy** — the closed set of typed error kinds and which
   malformation maps to which kind;
5. the **physical-accounting** rules that define an object's manifested byte
   total;
6. the **resource bounds** an implementation MUST satisfy;
7. the **conformance-suite contract** — entry points, the manifest schema, and
   the `derived` / `asserted` distinction.

It does not cover the separate `.stream` container, the trained-dictionary
*registry* lifetime, fused filesystems, or measurement courts; those are out of
scope for conformance and are not part of the golden set.

**Conventions.** Every integer is unsigned little-endian. Widths are `u8` (1
byte), `u16` (2), `u32` (4), `u64` (8), `[u8; N]` a fixed `N`-byte array.
`align8(n) = (n + 7) & ~7`. Offsets are absolute from byte 0 unless stated. There
is no implicit struct padding. A conforming reader MUST reject any unknown
version, bad magic, out-of-range field, checksum mismatch, or truncation; it MUST
NOT guess.

---

## 2. Top-level layout

```text
offset 0                       GenesisHeader        (fixed 80 bytes)
offset 80                      core                 (core_len = V bytes)
align8(80 + V)                 commit block 0
align8(end of block 0)         commit block 1       (appended generations)
...                            ...
EOF
```

A commit block is a sequence of `[SectionHeader(40) ++ payload]`, each region
8-aligned, ending with a `MANIFEST` section, an optional `AUTH` section, and a
fixed 64-byte `CommitFooter`. An implementation MUST select the newest
structurally valid commit (the last element of the valid prefix; §7.2).

---

## 3. Fixed wire structures

### 3.1 GenesisHeader — 80 bytes at offset 0

| off | size | field | rule |
|---:|---:|---|---|
| 0 | 8 | `magic` | MUST equal ASCII `EKBLOOM\0` |
| 8 | 2 | `format_major` | MUST equal `0` |
| 10 | 2 | `format_minor` | MUST be `<= 3` |
| 12 | 4 | `header_len` | MUST equal `80` |
| 16 | 8 | `feature_flags` | MUST equal `0` |
| 24 | 16 | `object_id` | `blake3("ekbloom.object-id.v1" ++ core_hash)[0..16]` |
| 40 | 8 | `logical_symbols` | `N` |
| 48 | 8 | `logical_bytes` | `N` (equals `logical_symbols` for byte machines) |
| 56 | 8 | `core_len` | visible core length `V` |
| 64 | 4 | `core_machine_id` | machine id 1–9 |
| 68 | 2 | `core_version` | `0` legacy / `1` current (§5) |
| 70 | 10 | `reserved` | zero |

Validation order is normative and short-circuits: length ≥ 80
(`ErrTruncated`); magic (`ErrBadMagic`); `header_len == 80` (`ErrBadSection`);
`format_major == 0 && format_minor <= 3` (`ErrUnsupportedVersion`);
`feature_flags == 0` (`ErrBadSection`); if `core_machine_id` names a registered
machine, `core_version` MUST be supported for it (`ErrUnsupportedCoreVersion`);
`core_len` MUST fit the file (`ErrTruncated`).

### 3.2 SectionHeader — 40 bytes

| off | size | field | rule |
|---:|---:|---|---|
| 0 | 4 | `kind` | wire code in §3.3, else `ErrBadSection` |
| 4 | 4 | `flags` | MUST equal `0` |
| 8 | 8 | `offset` | MUST equal `header_offset + 40` |
| 16 | 8 | `length` | payload length |
| 24 | 8 | `generation` | owning generation |
| 32 | 4 | `checksum` | CRC-32/IEEE of the payload |
| 36 | 4 | `reserved` | zero |

A payload whose CRC-32/IEEE does not equal `checksum` is a **section checksum
mismatch** (§7).

### 3.3 Section kinds (wire codes)

| code | kind | code | kind |
|---:|---|---:|---|
| 1 | core-machine (reserved) | 8 | certificate |
| 2 | history-full | 9 | manifest |
| 3 | checkpoints | 10 | auth |
| 4 | bloom-global | 11 | history-segmented |
| 5 | bloom-context | 12 | tiles |
| 6 | bloom-region | 13 | blocks |
| 7 | bloom-path | | |

### 3.4 ManifestRecord — payload of kind 9

`manifest_version u32` (MUST be `1`, else `ErrUnsupportedManifestVersion`),
`generation u64`, `logical_len u64`, `object_id [16]`, `machine_id u32`,
`core_hash [32]`, `profile_count u32` ++ per profile `len u32 ++ UTF-8 bytes`,
`component_count u32` ++ per component `name_len u32 ++ name ++ kind_len u32 ++
kind ++ bytes u64`. Trailing bytes are rejected. The manifest's `object_id`,
`machine_id` and `logical_len` MUST agree with the genesis header (a
disagreement is a structural rejection, §7.3).

### 3.5 CommitFooter — 64 bytes

`generation u64`, `manifest_offset u64`, `manifest_len u64`, `prev_generation
u64`, `manifest_hash [16]` (= `BLAKE3(manifest payload)[0..16]`), `commit_marker`
ASCII `EKBCOMM\0`, `footer_checksum u32` = CRC-32/IEEE of footer bytes
`[0,56) ++ [60,64)`, `reserved u32`. `manifest_offset`/`manifest_len` MUST point
at the MANIFEST section; `prev_generation` MUST link to the predecessor; the
manifest's `generation` MUST equal the footer's.

### 3.6 AUTH — payload of kind 10 (optional)

Exactly 32 bytes: `blake3::keyed_hash(key, blake3(file[0..auth_header_offset]))`.
Written after the manifest and immediately before the footer; at most one per
commit. A body opened in authenticated mode MUST carry an AUTH whose MAC
verifies.

---

## 4. The nine cores

Each core is placed at offset 80 with `core_len = V`. `Omega_0` is the initial
executable state; a step reveals one logical symbol and advances the state.

| id | machine | header bytes | V |
|---:|---|---:|---|
| 1 | `OrANS-NL2` | 0 | `N` |
| 2 | `OrANS-RANS0` | 516 | `516 + R` |
| 3 | `OrANS-Store` | 0 | `N` |
| 4 | `OrANS-RANS0I` | `516 + 4K` | `516 + 4K + R` |
| 5 | `OrANS-PACK` | `10 + k` | `10 + k + ceil(N·bits/8)` |
| 6 | `OrANS-SEG` | `96` (+ directory) | `96 + directory + payload` |
| 7 | `OrANS-RANS0W` | 516 | `516 + 2W` |
| 8 | `OrANS-RANS0IL` | `516 + 8K` | `516 + 8K + Σ lane_len` |
| 9 | `OrANS-DICT` | `20 + dict_len` | `20 + dict_len + R` |

**Machine 1 — NL2.** No header; `core` is `N` bytes. `S(v) = v ^ (v<<1) ^ (v<<3)`
is a fixed bijection on `0..255`. At cursor `p` with `h = x>>8`, `l = x&0xff`:
`s = S[h] ^ l ^ core[p]`, `x' = (l<<8)|s`, `p' = p+1`. Genesis `(x,p) = (0,0)`.

**Machine 2 — RANS0.** `[freq: 256 × u16 LE = 512 B][initial_state u32]
[renorm bytes, reversed]`. `freq[s] >= 1`, `Σ freq = 4096`. Decode:
`slot = x & 4095`; find `s` with `cum[s] <= slot < cum[s]+freq[s]`;
`x = freq[s]*(x>>12) + slot - cum[s]`; while `x < 2^23`, `x = (x<<8) | next_byte()`.

**Machine 3 — Store.** `core == source`; reveal `core[cursor]`, `state = symbol`,
`cursor++`. Genesis `(0,0)`.

**Machine 4 — RANS0I.** `[freq 512][K u32][initial_states: K × u32][interleaved
renorm bytes, reversed]`. `1 <= K <= 8`. Symbol `i` is coded in lane `i % K`;
genesis `(states, cursor = 516+4K, lane 0)`.

**Machine 5 — PACK.** `[bits u8][k u8][alphabet: k bytes (k>=2)][logical_len u64]
[packed payload]`. `header_len = 10 + k`. Identity: `bits=8, k=0`, payload
`= source`. Constant: `k=1`. Position `i` reads `bits` starting at bit `i*bits`
(low bit first, spanning the byte boundary) and maps through `alphabet` (or
identity when `k==0`).

**Machine 6 — SEG.** Fixed 96-byte header `ESEG`, version `1`, flags bit 0 =
variable directory, `segment_count u32`, `entry_bytes = 56`, `seg_shift u8`,
`window_shift u8`, `logical_len u64`, `directory_bytes u64`, `payload_offset u64
(= 96 + directory_bytes)`, `payload_bytes u64`, `directory_hash [32]` =
BLAKE3 of directory ++ payloads. Each 56-byte entry: `logical_start u64`,
`logical_len u64`, `physical_offset u64`, `physical_len u64`, `machine_id u32`
(`0` constant, `3` raw, `5` packed), `model_ref u32 (0xFFFFFFFF)`, `content_id
[16]`.

**Machine 7 — RANS0W.** As machine 2 but `while x < 2^15: x = (x<<16) |
next_u16()` (one 16-bit LE word per iteration).

**Machine 8 — RANS0IL.** `[freq 512][K u32][lane_len: K × u32][states: K × u32]
[lane payloads, each reversed]`. `header = 516 + 8K`; `Σ lane_len = core_len -
header`; lane `t`'s payload starts after the header plus preceding lane lengths.

**Machine 9 — DICT.** `[dict_id: 16][dict_len u32][dict_bytes: dict_len][logical_len
u32][initial_state u32][renorm bytes, reversed]`. `dict_len == 0` references a
registered dictionary by `dict_id`; `dict_len > 0` embeds one (MUST validate
against `dict_id`). `header_bytes = 20 + dict_len`. The serialized dictionary is
540 bytes: `"EKBDICT\0"[8] ++ version u16 ++ reserved u16 ++ id [16] ++ freq
[512]`, with `id = blake3("ekbloom.dictionary-id.v1" ++ version ++ freq)[0..16]`.

A core that cannot produce its declared `N` symbols is `ErrCoreTooShort`; a core
whose structure is invalid is a structural rejection (§7.3). The simplest
integrity rule is that `BLAKE3(core)` MUST equal the newest manifest's
`core_hash` (§7.4).

---

## 5. Versioning tags

A wire dialect is frozen by the tuple `(format_major, format_minor,
core_machine_id, core_version)`. `format_major = 0`, `format_minor = 3`,
`core_version ∈ {0 (legacy), 1 (current)}` are the supported values. An
implementation MUST reject `format_major > 0` or `format_minor > 3` with
`ErrUnsupportedVersion`, and an unsupported `(machine, core_version)` with
`ErrUnsupportedCoreVersion`. A new machine id, a changed core transition, or a
changed `S(v)` requires a new machine id or a bumped `core_version` (with the
payload rewritten), and a `format_minor` bump if the container changes.

---

## 6. Physical-accounting rules

`physical_bytes` is the sum of the manifested components; a structure MUST
contribute exactly its serialized length, and the core owns any shared model
header exactly once.

| component | contributes |
|---|---|
| core | `core_len` |
| history-full (kind 2) | `(logical_len + 1) × 4` (the state projection) |
| checkpoints (kind 3) | `count × entry_len`, `entry_len ∈ {2 packed, 12 normal, 45 wide}` |
| bloom-\* (kinds 4–7) | `payload_len - 74` |
| certificate (kind 8) | `payload_len` |
| history-segmented (kind 11) | `payload_len` |
| tiles (kind 12) | `directory_entries × 4 + data_len` |
| blocks (kind 13) | `prefix_len(104) + count × entry_len` |

The core is counted once; a block index or segmented history MUST NOT repeat the
machine's shared model header. `logical_len` is `N`; `visible_bytes` is `V`.
These rules are the reason a `physical_bytes` expectation can be cross-checked by
both implementations independently of who wrote the file.

---

## 7. Error taxonomy

Every rejection is a single **typed** error whose kind is drawn from this closed
set (the names are the Go sentinels and the Rust `EkbloomError` variants, which
share spelling):

`ErrBadMagic`, `ErrUnsupportedVersion`, `ErrUnsupportedCoreVersion`,
`ErrUnsupportedManifestVersion`, `ErrTruncated`, `ErrChecksumMismatch`,
`ErrCorrupt`, `ErrBadSection`, `ErrOverflow`, `ErrUnknownMachine`,
`ErrCoreTooShort`, `ErrOutOfBounds`, `ErrAllocationTooLarge`,
`ErrNonTermination`, `ErrInvalidArgument`, `ErrRecoveryFailed`,
`ErrNotCertified`, `ErrBadInterval`, `ErrBadDepth`.

### 7.1 Classification by malformation

| malformation | kind |
|---|---|
| magic ≠ `EKBLOOM\0` | `ErrBadMagic` |
| `format_major > 0` or `format_minor > 3` | `ErrUnsupportedVersion` |
| unsupported `(machine, core_version)` | `ErrUnsupportedCoreVersion` |
| manifest_version ≠ 1 | `ErrUnsupportedManifestVersion` |
| declared length/offset exceeds available bytes | `ErrTruncated` |
| region CRC-32 / footer checksum wrong | `ErrChecksumMismatch` |
| core does not match the manifest `core_hash` | `ErrChecksumMismatch` |
| localized corruption (deep verifier) | `ErrCorrupt { section, offset }` |
| structurally invalid header/section/manifest | `ErrBadSection` |
| unregistered machine id | `ErrUnknownMachine` |

### 7.2 The torn-commit rule

Parsing commits stops at the **first** structural failure. The valid prefix of
successfully parsed commits is retained; the remainder is an unvalidated **torn
tail**. Selecting the newest commit therefore selects the last element of the
valid prefix. If the *first* commit is torn, there is no valid generation.

Consequences, both of which conform:

* any malformation inside the first (only) commit leaves **no valid commit**;
* a malformation inside a *later* commit is silently a torn tail and the earlier,
  valid commit is still selected.

### 7.3 Structural rejection vs. localization

The taxonomy intentionally has two "corruption" kinds. A *plain* reader (Go
`Open`, Rust `open_bytes`) reports the container-level kind it hits
(`ErrChecksumMismatch`, `ErrBadSection`, `ErrTruncated`, …) or, if the only
commit is torn, may report `ErrRecoveryFailed`. A *deep* verifier localizes the
first divergence to `ErrCorrupt { section, offset }`. An implementation MAY
choose either wording for the torn-first-commit case as long as the kind is in
the fixture's accept set (§9). Two specific accepted differences are recorded in
`manifest.json.divergences`.

### 7.4 Cross-field disagreement

When the manifest and the genesis header disagree on `object_id`, `machine_id`,
or `logical_len`, the rejection is a structural one. The Go reader surfaces
`ErrBadSection`; the Rust reader surfaces its localized `Corrupt`. Both kinds
MUST be accepted (fixtures `neg_object_id_mismatch`, `neg_machine_mismatch`,
`neg_unknown_machine`, `neg_logical_mismatch`).

---

## 8. Resource bounds

For a random-access reader, `WorstCaseStepsFor(off, length)` is an upper bound on
the machine positioning steps plus output reveals a read of `length` symbols at
`off` may perform. The suite fixes the probe at `off = 0`, `length = N`; for that
probe the positioning is zero for every path (genesis, or a certified bloom, or
a checkpoint whose interval divides `0`) and the reveals are exactly `N`.
Therefore:

> **`WorstCaseStepsFor(0, N) == N`** for every well-formed object.

This is `derived` from the rule above and MUST hold for both implementations.
The manifest records it as `expected.worst_case_steps`. (The Rust reference
exposes a *different* quantity — an engine-planned, positioning-only,
region-summed bound — so the Rust verifier does not check this field; see
`README.md`.)

---

## 9. The conformance-suite contract

### 9.1 Entry points

A verifier judges an implementation through its public read API only:

* **open**: `Open` (Go) / `open_bytes` (Rust); with a referenced machine-9
  dictionary registered, or `OpenAuthenticated` / `open_bytes_authenticated`
  when the fixture is authenticated.
* **decode**: read `N` logical symbols from offset 0 and hash the result with
  BLAKE3.
* **account**: `PhysicalBytes` / `physical_bytes`, `LogicalLen` / `logical_len`,
  `MachineID` / `machine_id`.
* **bound**: `WorstCaseStepsFor(0, N)` (Go).

Neither verifier may import the other implementation or the fixture generator;
both read only `manifest.json` and the files it names.

### 9.2 The golden set is immutable and versioned

`manifest.json` is keyed by `format_version`. Every fixture records the sha256 of
its bytes. Regenerating or editing any fixture — including the synthetic negative
fixtures — requires a `format_version` bump and a note in this file. The verifiers
check the sha256 of every file, so drift is a hard failure.

### 9.3 `derived` vs `asserted`

Each expectation is labelled in `expectation_source`:

* **derived** — fixed by this specification plus the fixture bytes and its
  `.src` companion, *without running a decoder*. Examples: `sha256`; `logical_len`
  (`= len(src)`); `blake3_of_decoded` (`= BLAKE3(src)`); `worst_case_steps` (`= N`
  by §8); the negative `error_kind` when §7.1 fixes it; `machine_id` (header
  bytes at offset 64).
* **asserted** — recorded from a run of a reference implementation because it is
  not independently derivable from this text alone. Examples: `physical_bytes`
  (the accounting walk), and the negative `error_kind` for torn-commit and
  cross-field cases, whose *accept set* is derived from §7 but whose canonical
  member is observed.

`expected.error_kind` is the canonical kind; `expected.error_kind_accept` lists
additional kinds that are also a conforming rejection for that fixture (§7.3).
A negative fixture passes when the implementation rejects it with a kind in the
accept set and does not open it.

### 9.4 Manifest schema

```json
{
  "format_version": "ekb-format-0.3-conformance-1",
  "authority": "conformance/SPEC.md",
  "immutable": true,
  "fixtures": [
    {
      "name": "rust_m3_root_rand",
      "file": "fixtures/rust/m3_root_rand.ekb",
      "sha256": "<64 hex>",
      "origin": "rust | go | negative",
      "polarity": "positive | negative",
      "src_file": "fixtures/rust/m3_root_rand.src",
      "dict_file": null,
      "auth_key_hex": null,
      "mutation": null,
      "expected": {
        "machine_id": 3,
        "logical_len": 4096,
        "physical_bytes": 4096,
        "blake3_of_decoded": "<64 hex>",
        "error_kind": null,
        "error_kind_accept": [],
        "worst_case_steps": 4096
      },
      "expectation_source": { "...": "derived | asserted" }
    }
  ],
  "divergences": []
}
```

---

## 10. Change control

1. A change to any normative rule in §1–§8 requires a **format-version bump**
   (`format_minor`, or `format_major`) and a dated note here.
2. A change to any fixture requires a **`format_version` bump** in
   `manifest.json` and a note here recording why the golden set moved.
3. A new accepted cross-implementation difference MUST be added to
   `manifest.json.divergences` and MAY widen a fixture's accept set; it MUST NOT
   silently relax the canonical `error_kind`.
4. The suite is green only when `evidence/conformance.json` reports
   `"go":"pass"` (and, when the Rust reference is staged, `"rust":"pass"`).
