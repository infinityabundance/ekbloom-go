# Machine contract — adding machine 10

This Go port supports machines 1–9. An external contributor adds machine 10 by
implementing a single interface and registering it; no core file changes are
required.

## Required API

See `docs/CORE-LAYOUT.md` §2 for the exact core byte layout a machine 10 must
produce, and §2.0 for the header-bearing-machines diagram.

```go
type Machine interface {
    ID() uint32
    Init(core []byte) (Snapshot, error)
    Reveal(core []byte, s *Snapshot) (byte, error)
    Peek(core []byte, s *Snapshot) (byte, error)
    Encode(source []byte) ([]byte, error)
    CheckCore(core []byte, logicalLen uint64) error
}
```

Register it in an `init()` (or from a test) with
`ekbloom.RegisterMachine(myMachine{})`. `Init`/`Reveal`/`Peek` must be pure
functions of `(core, Snapshot)`; `Reveal` advances the snapshot by exactly one
logical symbol.

## Obligations (all must hold, each with a test)

1. **Byte-exact round-trip.** For every input `src`, `Encode(src)` then decode
   yields `src`. Covered by `FuzzMachineRoundTrip`.
1a. **`S(v)` is a bijection.** If the machine mixes symbols through the fixed
   permutation `S(v) = v ^ (v<<1) ^ (v<<3)`, it relies on `S` being a bijection on
   `0..255`; `TestSboxIsBijection` proves this exhaustively. A new mixing function
   must carry the same proof.
2. **Bounded `CheckCore`.** `CheckCore` must terminate in bounded time and never
   loop on a crafted core (use a step cap and return `ErrNonTermination`).
3. **`Estimate >= actual`.** If the machine declares a header/length, the static
   estimate must never under-state a built body (see `internal/ekb/estimate.go`).
4. **Corruption entry.** A single bit flip in the core must be detected (the
   manifest `core_hash` covers it) and, for a declared structure, localized to a
   `Corrupt { Section, Offset }`.
5. **Exact accounting.** A structure's contributed bytes must equal its
   serialized length; the core counts a shared model header once.
6. **No panic on untrusted input.** Every field read from `core` is bounds
   checked; a truncated or crafted core is a typed error.
7. **Wire identity.** The core layout is frozen and versioned by
   `(machine_id, core_version)`; changing any transition or `S(v)` map requires a
   new machine id or a bumped `core_version` with the payload rewritten.

## Review bar

- Cross-check the machine against the reference semantics (a Rust equivalent, or
  a byte-for-byte fixture written by the reference).
- Add a fixture to `testdata/fixtures/` (via `.reference/fixturegen`) so
  `internal/ekb/compat_test.go` exercises it, and extend `roundtripgen` so Rust verifies the
  Go-written form.
- Run `go test -race ./...` and `go test -fuzz FuzzMachineRoundTrip`.

## Accept / reject rule

Accept only when every obligation has a passing, committed test. A machine that
cannot satisfy bounded `CheckCore` or exact accounting is rejected, not shipped
behind a flag.
