package ekbloom

import (
	"context"

	"github.com/infinityabundance/ekbloom-go/internal/ekb"
)

// Reader is a validated, opened `.ekb` image. Its methods (ByteAt, ReadAt,
// PhysicalBytes, Estimate, WorstCaseStepsFor, Report, Metrics, View, File,
// ReadRanges, ReadAtContext, ...) are the aliased engine methods.
type Reader = ekb.Reader

// File is an io.ReaderAt adapter over a Reader, for driving the codec from a Go
// storage service (a SectionReader, an HTTP Range handler, a SQL driver, ...).
// A *File is safe for concurrent use: it shares the immutable *Reader.
type File = ekb.File

// Range is a logical read request for Reader.ReadRanges: Len symbols at Off.
type Range = ekb.Range

// NewFile wraps r in an io.ReaderAt adapter. (The same adapter is available as
// the (*Reader).File method, which the Reader alias promotes directly.)
func NewFile(r *Reader) *File { return ekb.NewFile(r) }

// ReadRanges decodes a batch of logical ranges in one call, one slice per
// request, with a single prepare of the machine model.
func ReadRanges(r *Reader, reqs []Range) ([][]byte, error) { return r.ReadRanges(reqs) }

// ReadAtContext reads length logical symbols at off, returning the typed
// ctx.Err() promptly if ctx is cancelled or its deadline passes mid-decode.
func ReadAtContext(ctx context.Context, r *Reader, off, length uint64) ([]byte, error) {
	return r.ReadAtContext(ctx, off, length)
}

// Open validates and opens an in-memory `.ekb` file.
func Open(data []byte) (*Reader, error) { return ekb.Open(data) }

// OpenWithDict opens a `.ekb` whose machine-9 core references a shared dictionary.
func OpenWithDict(data []byte, dict Dictionary) (*Reader, error) { return ekb.OpenWithDict(data, dict) }

// OpenAuthenticated opens a `.ekb` and verifies its keyed AUTH tag.
func OpenAuthenticated(data []byte, key [32]byte) (*Reader, error) {
	return ekb.OpenAuthenticated(data, key)
}

// VerifyAuthenticated verifies the AUTH tag of an authenticated `.ekb`.
func VerifyAuthenticated(data []byte, key [32]byte) error { return ekb.VerifyAuthenticated(data, key) }

// Verify performs a structural verification of an `.ekb` image.
func Verify(data []byte) error { return ekb.Verify(data) }

// VerifyDeep performs a structural and content verification, localizing the first
// divergence to a Corrupt{Section, Offset}.
func VerifyDeep(data []byte) error { return ekb.VerifyDeep(data) }

// Scan forward-scans a `.ekb` file, returning the valid commit prefix and any
// unvalidated torn tail.
func Scan(b []byte) (ScanOutcome, error) { return ekb.Scan(b) }

// CommitManifest returns the manifest record of a scanned commit.
func CommitManifest(b []byte, commit *ScannedCommit) (ManifestRecord, error) {
	return ekb.CommitManifest(b, commit)
}

// MachineByID returns the registered machine for id (and whether it exists).
func MachineByID(id uint32) (Machine, bool) { return ekb.MachineByID(id) }

// RegisterMachine installs a machine implementation under its ID (for machine 10;
// see docs/MACHINE_CONTRACT.md).
func RegisterMachine(m Machine) { ekb.RegisterMachine(m) }

// DecodeCore decodes a machine core to its logical bytes — the inverse of
// Machine.Encode, for storing a core yourself (e.g. a value-log adapter).
func DecodeCore(machineID uint32, core []byte, logicalLen uint64) ([]byte, error) {
	return ekb.DecodeCore(machineID, core, logicalLen)
}

// MetricsText renders the resource-policy metrics plus an optional per-object
// snapshot in a Prometheus-style exposition format.
func MetricsText(r *Reader) string { return ekb.MetricsText(r) }

// MetricsJSON renders the resource-policy metrics (and, if r is non-nil, the
// per-object metrics) as a single JSON object.
func MetricsJSON(r *Reader) ([]byte, error) { return ekb.MetricsJSON(r) }

// ResourcePolicySnapshot returns the current resource-policy metrics.
func ResourcePolicySnapshot() ResourceMetrics { return ekb.ResourcePolicySnapshot() }

// MaxSingleAllocation returns the current allocation cap.
func MaxSingleAllocation() uint64 { return ekb.MaxSingleAllocation() }

// SetMaxSingleAllocation sets the cap on any single whole-object allocation.
func SetMaxSingleAllocation(limit uint64) { ekb.SetMaxSingleAllocation(limit) }

// ResetBreaches clears the resource-policy breach counter (used by tests).
func ResetBreaches() { ekb.ResetBreaches() }

// Breaches returns the number of whole-object allocations the policy has rejected.
func Breaches() uint64 { return ekb.Breaches() }
