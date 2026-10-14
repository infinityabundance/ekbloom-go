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
// # Where things live
//
// Everything is package ekbloom in the repository root. The reading order for a
// newcomer is roughly:
//
//   - Open / ByteAt / ReadAt ......... reader.go
//   - typed errors ................... errors.go
//   - the wire format ................ format.go, verify.go, inspect.go
//   - the nine machines .............. machine*.go, dictionary.go
//   - the writer ..................... writer.go, writer_path.go,
//     writer_bloom.go, writer_tiles.go, append.go
//   - accelerated reads .............. fastpath.go, resident.go
//   - streaming ...................... stream.go
//   - operations ..................... metrics.go, resource.go, auth.go
//
// The tests are the specification: compat_test.go and legacy_test.go prove
// byte-for-byte identity with the Rust reference, and roundtrip_test.go,
// corruption_test.go, bound_test.go, concurrency_test.go and chaos_test.go prove
// the operational contracts. Evidence for every claim is under evidence/.
package ekbloom
