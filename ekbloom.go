// Package ekbloom is a pure-Go implementation of the EKBLOOM
// concealed-expansion codec, wire- and byte-compatible with the Rust reference
// implementation (github.com/infinityabundance/EKBLOOM).
//
// It has no cgo, no unsafe, and no external dependencies. The same bytes decode
// identically on amd64, arm64 and wasip1, and the format is frozen and versioned
// by (format_major, format_minor, machine_id, core_version).
//
// # Quick start
//
//	data, _ := os.ReadFile("archive.ekb")
//	r, err := ekbloom.Open(data)
//	if err != nil { /* every failure is a typed *ekbloom.Error */ }
//	b, _ := r.ByteAt(0)
//	head, _ := r.ReadAt(0, 4096)
//	_ = r.PhysicalBytes()
//
// See docs/STABILITY.md for the frozen API surface and the error-handling
// contract, docs/MIGRATION.md for the format compatibility matrix, and
// docs/SCOPE.md for what is deliberately not ported.
//
// # Layout
//
// This root package is the **public API only** — a thin facade of type aliases
// and function wrappers over the engine in internal/ekb. Read the API here; read
// the implementation in internal/ekb (reader.go, writer.go, format.go,
// machine*.go, fastpath.go, stream.go, ...). The tests are the specification:
// internal/ekb/compat_test.go and legacy_test.go prove byte-for-byte identity with
// the Rust reference; the operational contracts (corruption, bound, concurrency,
// chaos, streaming) live beside them. Evidence for every claim is under evidence/.
package ekbloom

import "github.com/infinityabundance/ekbloom-go/internal/ekb"

// Wire-format and value types re-exported from the engine. Type aliases preserve
// the method sets, so no wrapper is needed for methods (e.g. Reader.ByteAt).
type (
	// Dictionary is a trained machine-9 shared order-0 model.
	Dictionary = ekb.Dictionary
	// Machine is a reproducible, resumable symbol-generating machine.
	Machine = ekb.Machine
	// Snapshot is the resumable operational state of a Machine.
	Snapshot = ekb.Snapshot

	// GenesisHeader, SectionHeader, CommitFooter and ManifestRecord are the wire
	// structures of a `.ekb` file.
	GenesisHeader     = ekb.GenesisHeader
	SectionHeader     = ekb.SectionHeader
	CommitFooter      = ekb.CommitFooter
	ManifestRecord    = ekb.ManifestRecord
	ManifestComponent = ekb.ManifestComponent

	// Scan results.
	ScannedSection = ekb.ScannedSection
	ScannedCommit  = ekb.ScannedCommit
	ScanOutcome    = ekb.ScanOutcome

	// Reporting.
	ComponentReport = ekb.ComponentReport
	Report          = ekb.Report
	ReaderMetrics   = ekb.ReaderMetrics
	ResourceMetrics = ekb.ResourceMetrics

	// Streaming.
	StreamOptions  = ekb.StreamOptions
	StreamManifest = ekb.StreamManifest
	StreamEntry    = ekb.StreamEntry
	MachineCount   = ekb.MachineCount
	StreamBuilder  = ekb.StreamBuilder
	StreamReader   = ekb.StreamReader
)
