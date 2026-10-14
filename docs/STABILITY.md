# API and format stability

The module is `github.com/infinityabundance/ekbloom-go`. The public API is the
root package `ekbloom`, a thin facade over the engine in `internal/ekb` (Go
forbids importing `internal/` from other modules). This document is the
compatibility promise a storage team can plan against.

## Two independent guarantees

1. **Wire format** — frozen. An `.ekb`/`.stream` file is portable forever: it is
   identified by `(format_major, format_minor, machine_id, core_version)` and is
   byte-compatible with the Rust reference. A change to any transition or `S(v)`
   map requires a new machine id or a bumped `core_version` with the payload
   rewritten; an old file is never reinterpreted. See `docs/MIGRATION.md`.
2. **Go API** — semver. This release is **v0.x**, so the API may still change,
   but the surface below is intended to be stable and any break is called out in
   `CHANGELOG.md`.

## Frozen surface (v0.1.0)

These signatures are the contract; they will not change without a major version
and a changelog entry.

```go
// Construction
func Open(data []byte) (*Reader, error)
func OpenAuthenticated(data []byte, key [32]byte) (*Reader, error)
func OpenWithDict(data []byte, dict Dictionary) (*Reader, error)
func OpenStream(data []byte) (*StreamReader, error)

// Reading
func (r *Reader) ByteAt(off uint64) (byte, error)
func (r *Reader) ReadAt(off, length uint64) ([]byte, error)
func (r *Reader) PhysicalBytes() uint64
func (r *Reader) VisibleBytes() uint64
func (r *Reader) LogicalLen() uint64
func (r *Reader) MachineID() uint32
func (r *Reader) Generation() uint64
func (r *Reader) Estimate() uint64
func (r *Reader) EstimateForProfile(p Profile) uint64
func (r *Reader) WorstCaseStepsFor(off, length uint64) (uint64, error)
func (r *Reader) Report() Report
func (r *Reader) Metrics() ReaderMetrics

// I/O / ReaderAt
func (r *Reader) File() *File
type File struct{ *Reader }              // implements io.ReaderAt
func NewFile(r *Reader) *File
func (f *File) ReadAt(p []byte, off int64) (int, error)
type Range struct{ Off, Len uint64 }
func (r *Reader) ReadRanges(reqs []Range) ([][]byte, error)
func (r *Reader) ReadAtContext(ctx context.Context, off, length uint64) ([]byte, error)
func ReadRanges(r *Reader, reqs []Range) ([][]byte, error)
func ReadAtContext(ctx context.Context, r *Reader, off, length uint64) ([]byte, error)

// Zero-allocation borrowed view
func (r *Reader) View() (*ResidentView, error)
func (v *ResidentView) Get(i uint64) (byte, error)

// Writing / appending
func WriteBody(machineID uint32, source []byte, profile Profile) ([]byte, error)
func WriteBodyAuthenticated(machineID uint32, source []byte, profile Profile, key [32]byte) ([]byte, error)
func WriteBodyWithDict(source []byte, profile Profile, dict Dictionary) ([]byte, error)
func AppendGeneration(data []byte, machineID uint32, source []byte, profile Profile) ([]byte, uint64, error)
func TruncateToValid(data []byte) ([]byte, uint64, error)

// Dictionaries (machine 9)
func TrainDictionary(records [][]byte) (Dictionary, error)
func RegisterDictionary(d Dictionary)
func (d Dictionary) Bytes() []byte
func DictionaryFromBytes(b []byte) (Dictionary, error)
func (d Dictionary) EncodeRecord(record []byte) ([]byte, error)
func (d Dictionary) DecodeRecord(core []byte) ([]byte, error)
func NewDictDecoder(d Dictionary) (*DictDecoder, error)
func (dec *DictDecoder) DecodeInto(dst, core []byte) (int, error)
func (dec *DictDecoder) Decode(core []byte) ([]byte, error)

// Core encode/decode (for storing cores yourself, e.g. a value-log adapter)
func MachineByID(id uint32) (Machine, bool)
func DecodeCore(machineID uint32, core []byte, logicalLen uint64) ([]byte, error)

// Errors
func CorruptDetail(err error) (section string, offset uint64, ok bool)
var (ErrUnsupportedVersion, ErrUnsupportedCoreVersion, ErrUnsupportedManifestVersion, ...)
```

`*ekbloom.Error` carries a `Kind` and optional `Section`/`Offset`; use
`errors.Is` against the sentinels, or `CorruptDetail` for a corruption's
location. `go doc github.com/infinityabundance/ekbloom-go` renders the full
surface.

### I/O / ReaderAt

`Open` returns a `*Reader` that is **safe for concurrent reads** by any number of
goroutines with no external locking: it is immutable after `Open`, and the first
random access materialises the symbol array under a `sync.Once`. `*File` (the
`io.ReaderAt` adapter), `ReadRanges` and `ReadAtContext` inherit that guarantee.

- `(*Reader).File()` / `NewFile` return an `io.ReaderAt` over the logical
  (decompressed) content: `off` is a logical byte offset, a negative offset is a
  typed `KindInvalidArgument` error, and a read reaching the end returns `io.EOF`
  exactly as the contract requires.
- `ReadRanges` decodes a batch with a **single prepare** of the machine model,
  returning one slice per request; ranges covered by a certified fast path are
  served directly and the rest are sliced from one shared prefix decode bounded
  by `max(Off+Len)`. Use `WorstCaseStepsFor` for the per-range step bound.
- `ReadAtContext` is `ReadAt` with cooperative cancellation. It checks the
  context before any work and at least every few thousand symbols inside any
  decode or replay, so it returns the typed `ctx.Err()` (`context.Canceled` /
  `context.DeadlineExceeded`) promptly — well below a full decode — rather than a
  codec error. Validation errors remain typed codec errors.

## Error-handling contract

- No function panics on untrusted input; malformed bytes are typed errors.
- A corruption that prevents a read is `KindCorrupt` (or a more specific kind)
  with `{Section, Offset}` localised to the containing structure (≤4096 bytes).
- A torn append recovers the newest **valid** generation, never a partial one.

## Non-guarantees (explicit)

- Only `.ekb`/`.stream` versions `format_major = 0` are read; unknown versions are
  typed refusals (`ErrUnsupportedVersion`), never guesses.
- The reference's SIMD/io_uring/mmap/FUSE/OrFS/optimizer subsystems are **not**
  part of this module (see `docs/SCOPE.md`); their absence does not affect the
  format guarantees above.
- `Estimate()` is an upper bound for the profiles documented in
  `docs/OPERATIONS.md`; it is not a byte-exact predictor for cursor-divergent
  machines.
