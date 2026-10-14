# Standard corpora — `E_V` and read latency (Go edition)

`scripts/corpus.sh` downloads the canonical archives into the git-ignored
`corpus/` directory, then measures the Go implementation on a bounded leading
slice of every file (`EKB_CORPORA_CAP`, default 4 MiB), for machines 1 and 2 under
the Root profile:

- `E_V = physical_bytes / logical_len` (lower is smaller; `1.000` = raw size) —
  the standard-corpus **footprint** ratio. This is distinct from the reader's
  `Report().ExpansionRatio = physical_bytes / visible_bytes` (the reference's
  `EkbloomBody::expansion_ratio`), which measures the manifested expansion;
- single-byte read latency (`byte_at` p50/p99/p99.9), over 4096 deterministic
  samples.

When the Rust toolchain is present the **same files** are measured by the Rust
reference (`tools/fixturegen/src/bin/corpusbench.rs`) and the two reports are
required to agree on `physical_bytes` and `logical_len` for every row.

```sh
sh scripts/corpus.sh     # corpus/ + evidence/corpus.json + evidence/corpus-rust.json
```

## Cross-implementation result

`evidence/corpus-compare.log`: **48/48 rows identical** (physical_bytes,
logical_len, and therefore `E_V`) across Canterbury (14 files), Silesia (12 files)
and enwik8 (1 file), machines 1 and 2 — the two implementations encode the same
bytes on real corpora, not just on synthetic fixtures.

Representative rows (`evidence/corpus.json`):

| file | N | machine | physical | E_V |
| --- | ---: | ---: | ---: | ---: |
| canterbury/`alice29.txt` | 152,089 | 1 | 152,089 | 1.0000 |
| canterbury/`alice29.txt` | 152,089 | 2 | 88,685 | 0.5831 |
| canterbury/`kennedy.xls` | 1,029,744 | 2 | 462,279 | 0.4489 |
| silesia/`dickens` | 4,194,304 | 2 | 2,409,557 | 0.5745 |
| silesia/`mozilla` | 4,194,304 | 2 | 4,039,434 | 0.9631 |
| silesia/`xml` | 4,194,304 | 2 | 2,877,437 | 0.6860 |
| enwik8/`enwik8` | 4,194,304 | 2 | 2,687,412 | 0.6407 |

Machine 1 (`OrANS-NL2`) is a pure positional machine: its core is exactly the
source, so `E_V = 1.000` by construction. Machine 2 (`OrANS-RANS0`) is the base
entropy machine; its `E_V` is the honest order-0 expansion ratio on each file.

## Honest scope

- The reference's headline **`E_V` 0.988–0.998** on these corpora is the output of
  its joint **optimizer** under an equal (raw-size) budget: it deliberately spends
  up to `N` bytes on fast-read structures (machine 4 + regional blooms). That
  planner/optimizer is **not ported** here, so this port does not reproduce the
  0.998 design point — it reports the underlying machine `E_V`, which is what the
  port implements and what is proven identical to Rust.
- Latency is measured on the Go in-memory reader, which materialises one symbol
  array on first random access; the reference's `byte_at` p50/p99 come from its
  bounded engine and are not directly comparable, so they are not asserted here.
- Slices are bounded to `EKB_CORPORA_CAP` (default 4 MiB) per file, matching the
  reference's corpus cap; the full archives are never committed.
