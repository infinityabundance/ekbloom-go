# EKBLOOM `.ekb` core layout — Go edition

This is the implementable byte layout of a `.ekb` file and of every machine's
core payload (machines 1–9), as implemented by this Go package. It is a native
port of the reference `docs/CORE-LAYOUT.md` and is sufficient to reimplement a
decoder from the text alone. Byte-for-byte identity with the Rust reference is
proven by `testdata/` fixtures written by the Rust implementation.

**Conventions.** Every integer is unsigned little-endian. Widths are `u8`
(1 byte), `u16` (2), `u32` (4), `u64` (8). `[u8; N]` is a fixed `N`-byte array.
There is no implicit struct padding. `align8(n) = (n + 7) & ~7`. Offsets are
absolute from byte 0 unless stated otherwise. A conforming reader rejects any
unknown version, bad magic, out-of-range field, checksum mismatch, or trailing
byte; it never guesses.

## 1. Top-level layout

```text
offset 0                       GenesisHeader        (fixed 80 bytes)
offset 80                      core                 (core_len bytes)
align8(80 + core_len)          commit block 0
align8(end of block 0)         commit block 1       (appended generations)
...                            ...
EOF
```

Each commit block is a sequence of `[SectionHeader(40) ++ payload]`, 8-aligned,
ending with a `MANIFEST` section (optionally followed by an `AUTH` section) and a
fixed 64-byte `CommitFooter`.

### 1.1 GenesisHeader (80 bytes at offset 0)

| off | size | field | meaning |
|---:|---:|---|---|
| 0 | 8 | `magic` | ASCII `EKBLOOM\0` |
| 8 | 2 | `format_major` | `0` |
| 10 | 2 | `format_minor` | `3` |
| 12 | 4 | `header_len` | must equal `80` |
| 16 | 8 | `feature_flags` | must be `0` |
| 24 | 16 | `object_id` | `blake3("ekbloom.object-id.v1" ++ core_hash)[0..16]` |
| 40 | 8 | `logical_symbols` | `N` |
| 48 | 8 | `logical_bytes` | `N` (equals `logical_symbols` for byte machines) |
| 56 | 8 | `core_len` | visible core length `V` |
| 64 | 4 | `core_machine_id` | machine id (1–9) |
| 68 | 2 | `core_version` | `0` legacy / `1` current (`ErrUnsupportedCoreVersion` otherwise) |
| 70 | 10 | `reserved` | zero |

Validation order: length ≥ 80; magic; `header_len == 80`; `format_major == 0 &&
format_minor <= 3`; `feature_flags == 0`; `core_version` supported for the
machine; `core_len` against the file length.

### 1.2 SectionHeader (40 bytes)

| off | size | field |
|---:|---:|---|
| 0 | 4 | `kind` (wire code, §1.3) |
| 4 | 4 | `flags` (must be `0`) |
| 8 | 8 | `offset` = `header_offset + 40` |
| 16 | 8 | `length` |
| 24 | 8 | `generation` |
| 32 | 4 | `checksum` = CRC-32/IEEE of the payload |
| 36 | 4 | `reserved` |

### 1.3 Section kinds

| code | kind | code | kind |
|---:|---|---:|---|
| 1 | core-machine (reserved) | 8 | certificate |
| 2 | history-full | 9 | manifest |
| 3 | checkpoints | 10 | auth |
| 4 | bloom-global | 11 | history-segmented |
| 5 | bloom-context | 12 | tiles |
| 6 | bloom-region | 13 | blocks |
| 7 | bloom-path | | |

### 1.4 ManifestRecord (payload of `MANIFEST`)

`manifest_version u32 = 1`, `generation u64`, `logical_len u64`,
`object_id [16]`, `machine_id u32`, `core_hash [32]`,
`profile_count u32` ++ per profile `len u32 ++ UTF-8 bytes`,
`component_count u32` ++ per component `name_len u32 ++ name ++ kind_len u32 ++
kind ++ bytes u64`. Any version other than `1` is
`ErrUnsupportedManifestVersion`; trailing bytes are rejected.

### 1.5 CommitFooter (64 bytes)

`generation u64`, `manifest_offset u64`, `manifest_len u64`,
`prev_generation u64`, `manifest_hash [16]` (= BLAKE3(manifest payload)[0..16]),
`commit_marker` ASCII `EKBCOMM\0`, `footer_checksum u32` = CRC-32 of bytes
`[0,56)` ++ `[60,64)`, `reserved u32`.

### 1.6 AUTH (optional)

Payload is exactly 32 bytes:
`blake3::keyed_hash(key, blake3(file[0..auth_header_offset]))`. Written after the
manifest and immediately before the footer; at most one per commit.

### 1.7 HistorySegmented (wire 11)

Fixed 104-byte header: `segment_bytes u32`, `checkpoint_interval u32`,
`logical_len u64`, `state_count u64`, `segment_count u64`, `index_offset u64`,
`states_offset u64`, `checkpoints_offset u64`, `checkpoints_len u64`,
`core_hash [32]`, `generation u64`; then offsets, states, and the sparse
checkpoint bytes.

## 2. Machines

Each `encode(source)` produces the core placed at offset 80. `Omega_0` and the
per-step reveal are below.

### 2.0 Core byte diagrams (header-bearing machines)

```text
machine 2  OrANS-RANS0    [freq: 256*u16 LE = 512] [initial_state u32] [renorm bytes, reversed]
machine 4  OrANS-RANS0I   [freq 512] [K u32] [initial_states: K*u32] [interleaved renorm, reversed]
machine 5  OrANS-PACK     [bits u8][k u8][alphabet: k][logical_len u64][packed payload ceil(N*bits/8)]
machine 6  OrANS-SEG      hdr[ESEG |ver u16|flags u16|count u32|entry_bytes u32|seg_shift u8|window_shift u8|resv 6|logical_len u64|dir_bytes u64|payload_off u64|payload_bytes u64|dir_hash 32|resv 8] dir[count*(56-entry)] [win entries] [payloads]
machine 7  OrANS-RANS0W   [freq 512] [initial_state u32] [renorm words: u16 LE, reversed]
machine 8  OrANS-RANS0IL  [freq 512] [K u32] [lane_len: K*u32] [states: K*u32] [lane payloads, each reversed]
machine 9  OrANS-DICT     [dict_id 16][dict_len u32][dict_bytes: dict_len][logical_len u32][initial_state u32][renorm bytes, reversed]
machine 1/3              no header: core is N bytes
```

### 2.1 Machine 1 — `OrANS-NL2` (V = N)

No header; `core` is `N` bytes. `S(v) = v ^ (v<<1) ^ (v<<3)` (8-bit), an
invertible GF(2) map on `0..255` (`TestSboxIsBijection` proves the bijection).
Step at
cursor `p` with `h = x>>8`, `l = x&0xff`: `s = S[h] ^ l ^ core[p]`,
`x' = (l<<8)|s`, `p' = p+1`. Genesis `(0,0)`. Encode is the inverse.

### 2.2 Machine 2 — `OrANS-RANS0` (order-0 rANS, bytes)

`[freq: 256 × u16 LE = 512 B][initial_state: u32 LE][renorm bytes, reversed]`.
`freq[s] >= 1`, `Σ freq = 4096`. Decode: `slot = x & 4095`; find `s` with
`cum[s] <= slot < cum[s]+freq[s]`; `x = freq[s]*(x>>12) + slot - cum[s]`; while
`x < 2^23`, `x = (x<<8) | next_byte()`. `HEADER_BYTES = 516`.

### 2.3 Machine 3 — `OrANS-Store` (identity)

No header; `core == source`; reveal `core[cursor]`, `state = symbol`, `cursor++`.
Genesis `(0,0)`.

### 2.4 Machine 4 — `OrANS-RANS0I` (interleaved order-0 rANS)

`[freq 512][K u32][initial_states: K × u32][interleaved renorm bytes, reversed]`.
`1 <= K <= 8`, each state in `[2^23, 2^31)`. Symbol `i` is coded in lane `i % K`;
genesis `(states, cursor = 516+4K, lane 0)`.

### 2.5 Machine 5 — `OrANS-PACK` (directly addressable fixed-width)

`[bits u8][k u8][alphabet: k bytes (k>=2)][logical_len u64][packed payload]`.
`header_len = 10 + k`. Identity: `bits=8, k=0`, payload `= source`. Constant:
`k=1`, zero-length payload, one alphabet byte. Position `i`: `bit = i*bits`,
index = `(payload[bit>>3] >> (bit&7))` OR next byte across the boundary, then
`alphabet[index]` (or `index` when `k==0`).

### 2.6 Machine 6 — `OrANS-SEG` (segmented container)

Fixed 96-byte header `ESEG`, `version=1`, `flags` bit 0 = variable directory,
`segment_count u32`, `entry_bytes = 56`, `seg_shift u8`, `window_shift u8`,
`logical_len u64`, `directory_bytes u64`, `payload_offset u64 (= 96 +
directory_bytes)`, `payload_bytes u64`, `directory_hash [32]` = BLAKE3 of the
directory ++ payloads. Each 56-byte entry: `logical_start u64`, `logical_len
u64`, `physical_offset u64`, `physical_len u64`, `machine_id u32` (`0`
constant, `3` raw, `5` packed), `model_ref u32 (0xFFFFFFFF)`, `content_id [16]`.

### 2.7 Machine 7 — `OrANS-RANS0W` (16-bit word renorm)

As machine 2 but the renorm loop shifts a 16-bit LE word in at most one
iteration: while `x < 2^15`, `x = (x<<16) | next_u16()`.

### 2.8 Machine 8 — `OrANS-RANS0IL` (independent-lane streams)

`[freq 512][K u32][lane_len: K × u32][states: K × u32][lane payloads, each
reversed]`. `header = 516 + 8K`; `Σ lane_len == core_len - header`. Lane `t`'s
payload starts after the header plus the preceding lanes' lengths.

### 2.9 Machine 9 — `OrANS-DICT` (shared dictionary records)

`[dict_id: 16][dict_len u32][dict_bytes: dict_len (if > 0)][logical_len u32]
[initial_state u32][renorm bytes, reversed]`. `dict_len == 0` references a
registered dictionary by `dict_id`; `dict_len > 0` embeds one (validated against
`dict_id`). `header_bytes = 20 + dict_len`. The serialized dictionary is 540
bytes: `"EKBDICT\0" [8] ++ version u16 ++ reserved u16 ++ id [16] ++ freq [512]`,
with `id = blake3("ekbloom.dictionary-id.v1" ++ version ++ freq)[0..16]`.

### 2.10 Core size summary

| id | machine | header bytes | V |
|---:|---|---:|---|
| 1 | NL2 | 0 | `N` |
| 2 | RANS0 | 516 | `516 + R` |
| 3 | Store | 0 | `N` |
| 4 | RANS0I | `516 + 4K` | `516 + 4K + R` |
| 5 | PACK | `10 + k` | `10 + k + ceil(N·bits/8)` |
| 6 | SEG | `96` (+ directory) | `96 + directory + payload` |
| 7 | RANS0W | 516 | `516 + 2W` |
| 8 | RANS0IL | `516 + 8K` | `516 + 8K + Σ lane_len` |
| 9 | DICT | `20 + dict_len` | `20 + dict_len + R` |

## 3. Streaming container (`.stream`)

A distinct, downstream-seekable container (`EKSTREAM`), independent of `.ekb`.

```text
[header 64 B][block payloads][directory: block_count × 56 B][footer 160 B]
```

Header: `magic EKSTREAM [8]`, `version u16 = 1`, `header_len u16 = 64`,
`flags u32 = 0`, `block_bytes u32`, `policy u32`, `crc u32` (CRC-32 of the first
52 bytes). Each 56-byte directory entry is a `StreamEntry` (`logical_start`,
`logical_len`, `physical_offset`, `physical_len`, `machine_id`, `model_ref`,
`content_id [16]`). Footer (160 B): magic `EKSFTRM\0`, version, `footer_len =
160`, flags, block_bytes, policy, block_count, logical_len, directory_offset/len,
payload_offset/bytes, `source_hash [32]`, `directory_hash [32]`, `header_crc u32`,
`crc u32`. Each block is an independently decodable core (store / packed /
segmented / …) chosen by the policy; the builder holds at most one block plus an
`O(blocks)` directory.
