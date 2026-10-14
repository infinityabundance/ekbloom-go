# Shared trained dictionaries — `OrANS-DICT` (machine 9)

Machine 2 (`OrANS-RANS0`) stores a 512-byte order-0 frequency table plus a 4-byte
coder state in **every** core, so a 100-byte record carries a header five times
its payload. Machine 9 (`OrANS-DICT`) factors the model out: one dictionary is
trained once over a corpus and stored once, and every record becomes a compact,
header-less core that **references** it — keeping machine 2's exact model
semantics.

Go API:

```go
dict, _ := ekbloom.TrainDictionary(corpus)          // order-0 model, 540-byte wire form
image, _ := ekbloom.WriteBodyWithDict(rec, root, dict) // core references dict (registry)
r, _ := ekbloom.OpenWithDict(image, dict)           // resolves the core's dict id
b, _ := r.ByteAt(0)
```

The dictionary is registered in a process-wide registry (exactly as the reference's
`dictionary::register` does), so a reader resolves a core's 16-byte dictionary id;
an **unregistered id is a typed error** — a record is never decoded against the
wrong model. For a self-contained image use the embedded writer
(`WriteBody(9, ...)`, which trains from the record and embeds the table).

## P5 break-even (`evidence/dictionary.json`)

Corpus: 4096 text-like ≈100-byte key/value records (a small, repetitive token
alphabet). Every row is asserted byte-exact before timing. `bytes/record` includes
the shared dictionary amortised over the whole corpus. Decode p50 is the median
across `EKB_RUNS` runs (default 5).

| method | total bytes | bytes/record | decode p50 |
| --- | ---: | ---: | ---: |
| **`OrANS-DICT` (machine 9)** | **341,947** | **83.5** | 461 ns |
| machine 2, per record | 2,416,529 | 590.0 | 3567 ns |
| raw records | 409,600 | 100.0 | — |

The shared dictionary is **7.1× smaller** than per-record machine 2 (83.5 vs 590.0
B/record; the 540-byte dictionary amortises to ≈0.13 B/record at this corpus
size), and the size break-even against per-record machine 2 is **2 records**: the
dictionary pays for itself as soon as it serves two records. Totals are identical
to the Rust `evidence/bench/dictionary.json` — the Go writer emits the same bytes.

Regenerate: `go test -run TestDictionaryBreakEven .`

## Honest limits

- Decode is **not** free: machine-9 order-0 rANS decode costs ~0.46 µs/record here
  with the shared reusable context (~3.6 µs for the per-record machine-2 model).
  Against Zstandard-with-dictionary it is a space-oriented choice, not a speed
  win (see `docs/COURTS.md`).
- The referenced (non-embedded) form needs the dictionary out-of-band. The
  embedded form is self-contained and needs no registry.
- Machine 9 is **not** selected by `Auto`: a single embedded record is never
  smaller than machine 2, so it is chosen explicitly.
