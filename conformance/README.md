# EKBLOOM conformance suite

A standalone, **implementation-neutral** conformance authority for the EKBLOOM
`.ekb` byte format. It exists to close a specific failure mode: two
implementations can agree on the same bug if they share fixture-generation
assumptions. Here, **neither implementation owns the truth.**

The model, in five rules:

1. **The spec is the authority.** `SPEC.md` states the byte layout, the nine
   cores, the versioning tags, the error taxonomy, the physical-accounting
   rules, and the resource bounds normatively. It is standalone; it does not
   defer to `docs/CORE-LAYOUT.md` or to either implementation. A change to a
   format rule requires a format-version bump.
2. **Fixtures are immutable and versioned.** `manifest.json` is keyed by a
   `format_version` and records the sha256 of every file in `fixtures/`. Both
   verifiers recompute those hashes, so drift is a hard failure. Regenerating or
   editing a fixture requires a `format_version` bump and a note in `SPEC.md`.
3. **Both implementations verify independently.** The Go verifier
   (`verify/go/`, a separate Go module) and the Rust verifier
   (`tools/fixturegen/src/bin/conformance.rs`) each read the *same*
   `manifest.json` and fixtures through their own public API. Neither imports
   the other implementation or the fixture generator.
4. **Expectations are `derived` or `asserted`, visibly.** `derived` means fixed
   by `SPEC.md` plus the fixture bytes and its `.src` — no decoder run (e.g.
   `logical_len = len(src)`, `blake3_of_decoded = BLAKE3(src)`,
   `worst_case_steps = N`, and most negative `error_kind`s). `asserted` means
   recorded from a reference run because it is not text-derivable (e.g.
   `physical_bytes`, and the canonical kind for torn-commit cases). The label
   lives per field in `expectation_source`.
5. **Divergences are ruled, not merely recorded.** The suite fails when the two
   verifiers disagree on a fixture that is not in `manifest.json.divergences`,
   when a ruled entry lacks a `rule`, or when a rule cites a section that does
   not exist in `SPEC.md`. The comparator (`tools/divergence`) that enforces this
   imports neither implementation.

## Layout

```text
conformance/
  SPEC.md                     the authority
  manifest.json               versioned, sha256-pinned golden index
  README.md                   this file
  fixtures/
    rust/                     copies of the Rust-written bodies (testdata/fixtures)
    go/                       Go-written bodies (roundtripgen), re-encoded by Rust here
    negative/                 synthetic corruptions (truncation, bit flips, ...)
  verify/go/                  separate Go module: the Go verifier (+ its tests)
    blake3/                   vendored pure-Go BLAKE3 (pinned by testdata/blake3_vectors.json)
    cmd/manifestgen/          authoring tool that wrote manifest.json (never run by the suite)
  tools/divergence/           separate Go module: the independent comparator (imports neither impl)
```

## Running

```sh
sh scripts/conformance.sh
```

It runs the Go verifier inside the OOM-capped sandbox
(`scripts/sandbox.sh`), runs the Rust verifier in a capped container if the
reference is staged under `.reference/`, runs the **independent comparator**
(`tools/divergence`) in the sandbox, and writes:

```json
// evidence/conformance.json
{"go":"pass","rust":"pass","fixtures":73,
 "divergences":{"ruled":21,"observed":21,"unruled":0,"resolved":0,"ruled_without_rule":0,"source":"manifest.json+SPEC.md"}}
```

Each verifier also publishes per-fixture observations to
`evidence/conformance-go.json` and `evidence/conformance-rust.json`, and the
comparator writes its machine-readable summary to `evidence/divergence.json`.

`rust` is `"skipped"` when `.reference/ekbloom-rust` is absent (a fresh clone);
in that case the comparator reports `rust: "skipped"` too and does not fail for
that reason alone. The human-readable transcript is `evidence/conformance.log`.
Nothing heavy runs on the host: `go` and `cargo` both run in capped Docker
containers.

The run **fails** (`exit 1`) if the Go verifier fails, if the Rust verifier
fails, or if the comparator finds an unruled divergence / a ruled entry without a
SPEC-citing rule / a manifest inconsistency.

Individually:

```sh
# Go (from conformance/verify/go)
go run . -root ../.. [-json out.json]

# Rust (needs .reference staged: sh scripts/setup-reference.sh)
cd .reference/fixturegen && cargo run --release --bin conformance -- <repo>/conformance [--json out.json]

# Independent comparator (from conformance/tools/divergence; imports neither impl)
go run . -manifest ../../manifest.json -spec ../../SPEC.md \
    -go <go.json> [-rust <rust.json>] [-json summary.json]
```

## What each verifier checks

| expectation | Go verifier | Rust verifier |
|---|---|---|
| file sha256 (immutability) | yes | yes |
| decoded bytes (BLAKE3 of `ReadAt(0,N)`) | yes | yes |
| `machine_id` | yes | yes |
| `logical_len` | yes | yes |
| `physical_bytes` | yes | yes |
| negative `error_kind` (canonical + accept set) | yes | yes |
| `worst_case_steps` (`WorstCaseStepsFor(0,N) == N`) | yes | **skipped** (see below) |

The Go verifier's tests (`go test ./...` in `verify/go`) also assert that the
manifest itself is well-formed, that every fixture hash matches, and that the
vendored BLAKE3 matches the Rust-`blake3`-generated vectors in
`testdata/blake3_vectors.json` — so the decoded-bytes check cannot be circular
through a bad hash primitive.

## Coverage

73 fixtures: **41 positive** (20 written by the Rust reference, 21 by the Go
implementation) and **32 negative**.

* **Cores.** Machines 1–9, across `root`, `full`, `sparse(64)`, `sparse(256)`,
  `path(4)`, universal bloom and regional bloom profiles, plus machine-9
  referenced- and embedded-dictionary bodies, an authenticated body, and a
  tiles body.
* **Negative fixtures by canonical error kind:**

  | kind | count | examples |
  |---|---:|---|
  | `ErrRecoveryFailed` | 15 | torn first commit (section/footer corruption) |
  | `ErrBadSection` | 6 | `header_len`, `feature_flags`, footer marker, unknown section kind |
  | `ErrChecksumMismatch` | 3 | core bit flip (machines 3/6/9) |
  | `ErrTruncated` | 3 | empty file, short header, core truncation |
  | `ErrUnsupportedVersion` | 2 | `format_major=1`, `format_minor=4` |
  | `ErrUnsupportedCoreVersion` | 1 | `core_version=9` |
  | `ErrBadMagic` | 2 | magic corrupted |
  | `ErrUnknownMachine`, `ErrUnsupportedManifestVersion` | in accept sets | |

* **Negative fixtures by section kind mutated:** history-full (2), checkpoints
  (3), bloom-global (4), bloom-region (6), bloom-path (7), certificate (8),
  manifest (9), auth (10), tiles (12), plus the genesis header, the core, the
  footer, and the section table. (The corpus contains no bloom-context,
  history-segmented, blocks, or core-machine sections, so those are not
  mutated.)

## Accepted cross-implementation differences

Both implementations reject every negative fixture, but for some malformations
they surface a *different typed error*, with the same rejection. `manifest.json`
records each such difference in `divergences`; each fixture's `error_kind_accept`
set permits both. Because membership is a *ruled set* rather than a fixed list,
the corpus may contain any number of these: the current families are

* **Torn first commit.** Go's `Open` wraps the torn error as `ErrRecoveryFailed`;
  Rust's `open_bytes` surfaces the raw underlying kind (`ErrChecksumMismatch`,
  `ErrBadSection`, `ErrTruncated`, `ErrUnsupportedManifestVersion`). `SPEC.md`
  §7.2 fixes the torn-tail rule and §7.3 permits either wording. Ruled as
  `SPEC §7.2 (torn-first-commit)`.
* **Header/manifest cross-field disagreement.** Go returns `ErrBadSection`;
  Rust returns its localized `Corrupt`. Ruled as `SPEC §7.4 (cross-field
  disagreement)`.
* **Core hash and other localizable corruptions.** Go returns
  `ErrChecksumMismatch`; Rust returns its localized `Corrupt`/`CorruptGeneration`.
  Ruled as `SPEC §7.3 (structural rejection vs localization)`.

The canonical `error_kind` is never relaxed: it is always a member of the
taxonomy, and the accept set only widens *which* taxonomy kind is conforming.
Every ruled entry carries a `rule` citing the enabling SPEC section, and the
independent comparator (`tools/divergence`) fails the suite if the two verifiers
disagree on a fixture that is not ruled — so a new difference cannot be absorbed
silently by widening an accept set.

## Compromises and limits

* **`worst_case_steps` is Go-only.** `SPEC.md` §8 fixes
  `WorstCaseStepsFor(0, N) == N`. The Rust crate exposes a *different* quantity
  (`AccessPlan::worst_case_steps_for`: positioning-only, summed over engine
  regions, and only obtainable from a planned `Engine`), so it is not the same
  denominator and the Rust verifier skips that one field (reported as
  `skipped`). Every other expectation is checked by both.
* **`physical_bytes` is `asserted`, not `derived`.** It was not re-derived from
  `SPEC.md` §6 by hand (that would re-implement the accounting walk); it is
  recorded from a reference run and then *cross-checked* by both implementations
  reading the same manifest. For Rust-written fixtures the value comes from the
  Rust `inspect` run; for Go-written fixtures from a Go run. This is exactly the
  kind of expectation the `asserted` label exists to make visible.
* **The negative fixtures are synthetic and derived from positive bases.** They
  are generated once (deterministically) by `verify/go/cmd/manifestgen` and then
  frozen with recorded sha256s. The generator is an authoring tool: the suite
  never runs it, and the Go verifier does not import it.
* **The Go verifier vendors BLAKE3.** SHA is needed for the decoded-bytes
  expectation; the verifier does not use the implementation's own hash. A
  known-answer test against Rust-generated vectors pins the primitive.
* **`.stream` is out of scope.** The golden set is `.ekb` only.
