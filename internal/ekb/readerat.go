package ekb

import (
	"context"
	"io"
)

// Range is a logical read request for a batch read: the Len symbols starting at
// logical offset Off. Off is in [0, LogicalLen] and Off+Len in [0, LogicalLen].
type Range struct {
	Off uint64
	Len uint64
}

// File adapts a *Reader to the standard io.ReaderAt interface so the codec can
// back a Go storage service (a SectionReader, a databases/sql driver, an HTTP
// Range handler, ...) without a bespoke adapter.
//
// Concurrency: File embeds the *Reader it was created from. A *Reader is
// immutable after Open (the first random access materialises the symbol array
// under a sync.Once), so a *File is safe for concurrent use by any number of
// goroutines, with no locking required by the caller.
type File struct {
	*Reader
}

// io.ReaderAt is the whole point of File.
var _ io.ReaderAt = (*File)(nil)

// File returns an io.ReaderAt view of r. It is the method form of NewFile; both
// share the same underlying *Reader and therefore the same concurrency guarantee.
func (r *Reader) File() *File { return &File{Reader: r} }

// NewFile wraps r in an io.ReaderAt adapter. It is the free-function form of
// (*Reader).File, provided so a caller can name the adapter without a receiver.
func NewFile(r *Reader) *File { return &File{Reader: r} }

// ReadAt fills p with the logical symbols starting at byte offset off, per the
// io.ReaderAt contract:
//
//   - It always fills p completely or returns a non-nil error explaining why it
//     could not. At or past the logical end it returns io.EOF (n == 0 at the end,
//     or n < len(p) for a short read that reaches the end).
//   - A negative offset is a typed KindInvalidArgument error, never a panic.
//   - Any codec failure is a typed *Error.
//
// off is a logical (decompressed) offset, not a physical one. ReadAt allocates a
// temporary buffer of the requested size; a hot caller that reuses a buffer
// should use the SectionReader/ReadAt pattern or a pooled buffer.
func (f *File) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, &Error{Kind: KindInvalidArgument, Reason: "io.ReaderAt: negative offset"}
	}
	if uint64(off) >= f.logical {
		return 0, io.EOF
	}
	avail := f.logical - uint64(off)
	n := uint64(len(p))
	if n > avail {
		n = avail
	}
	if n == 0 {
		return 0, nil
	}
	out, err := f.Reader.ReadAt(uint64(off), n)
	if err != nil {
		return 0, err
	}
	c := copy(p, out)
	if c < len(p) {
		// The requested range ran past the end: a short read with io.EOF.
		return c, io.EOF
	}
	return c, nil
}

// ReadRanges decodes a batch of logical ranges in one call, returning exactly one
// slice per request (outs[i] is the Len bytes at reqs[i].Off).
//
// The batch is decoded with a single prepare of the machine model: ranges that a
// certified fast path can answer directly (the exact path bloom, a full history,
// a checkpoint restart, or a directly-addressable machine 3/5/6) are served
// individually, and every remaining range is sliced out of one shared prefix
// decode bounded by the largest requested end. Work is therefore bounded by
// max(Off+Len) and the caller's allocation cap, not by the number of requests.
//
// Safety and errors match ReadAt: a range that overflows or exceeds LogicalLen is
// a typed error (and no partial result is returned). A *Reader is safe for
// concurrent ReadRanges calls; each call builds its own decoder state.
func (r *Reader) ReadRanges(reqs []Range) ([][]byte, error) {
	outs := make([][]byte, len(reqs))
	var (
		maxEnd  uint64
		pending []int
	)
	for i, req := range reqs {
		end := req.Off + req.Len
		if end < req.Off {
			return nil, overflow("batch read range")
		}
		if end > r.logical {
			return nil, outOfBounds(req.Off, req.Len, r.logical)
		}
		if req.Len == 0 {
			outs[i] = []byte{}
			continue
		}
		if out, ok := r.fastRange(req.Off, end); ok {
			outs[i] = out
			continue
		}
		outs[i] = make([]byte, req.Len)
		pending = append(pending, i)
		if end > maxEnd {
			maxEnd = end
		}
	}
	if len(pending) == 0 {
		return outs, nil
	}
	// One prepared decode for the whole batch, materialised once and sliced.
	prefix, err := r.decodePrefix(maxEnd)
	if err != nil {
		return nil, err
	}
	for _, i := range pending {
		req := reqs[i]
		copy(outs[i], prefix[req.Off:req.Off+req.Len])
	}
	return outs, nil
}

// ReadAtContext is ReadAt with cooperative cancellation: it returns
// length logical symbols starting at off, but aborts promptly with the typed
// ctx.Err() (context.Canceled or context.DeadlineExceeded) if ctx is cancelled.
//
// The context is checked before any work and at least every ctxCheckInterval
// symbols inside any decode or replay loop, so cancellation latency is bounded by
// a few thousand reveals regardless of the requested length — well below the cost
// of a full decode. A cancelled or expired context is reported as ctx.Err(), not
// a codec error, so a caller can distinguish "you cancelled me" from "the file is
// bad". Validation errors (overflow, out of bounds) are still typed codec errors.
func (r *Reader) ReadAtContext(ctx context.Context, off, length uint64) ([]byte, error) {
	if ctx == nil {
		return nil, &Error{Kind: KindInvalidArgument, Reason: "nil context"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	end := off + length
	if end < off {
		return nil, overflow("read range")
	}
	if end > r.logical {
		return nil, outOfBounds(off, length, r.logical)
	}
	if length == 0 {
		return []byte{}, nil
	}

	if r.directlyAddressable() {
		if r.machine.ID() == MachineStore {
			out := make([]byte, length)
			if err := copyCtx(ctx, out, r.core[off:end]); err != nil {
				return nil, err
			}
			return out, nil
		}
		out := make([]byte, length)
		for i := uint64(0); i < length; i++ {
			if i%ctxCheckInterval == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			b, err := r.symbolAtDirect(off + i)
			if err != nil {
				return nil, err
			}
			out[i] = b
		}
		return out, nil
	}

	if out, ok, err := r.readPathBloomContext(ctx, off, end); err != nil {
		return nil, err
	} else if ok {
		return out, nil
	}
	if out, ok, err := r.readHistoryContext(ctx, off, end); err != nil {
		return nil, err
	} else if ok {
		return out, nil
	}
	if out, ok, err := r.readCheckpointContext(ctx, off, end); err != nil {
		return nil, err
	} else if ok {
		return out, nil
	}

	prefix, err := r.decodePrefixContext(ctx, end)
	if err != nil {
		return nil, err
	}
	out := make([]byte, length)
	copy(out, prefix[off:end])
	return out, nil
}

// ctxCheckInterval is the number of decode/replay steps between context checks.
// A few thousand keeps cancellation latency bounded (tens of microseconds on a
// modern core) while adding nothing measurable to the decode hot loop.
const ctxCheckInterval = 4096

// fastRange serves [off,end) from an O(1) or certified fast path, exactly as the
// ReadAt path selection does, and reports whether it could. It never mutates
// shared state beyond the once-guarded direct contexts.
func (r *Reader) fastRange(off, end uint64) ([]byte, bool) {
	if r.directlyAddressable() {
		out := make([]byte, end-off)
		if r.machine.ID() == MachineStore {
			copy(out, r.core[off:end])
			return out, true
		}
		for i := uint64(0); i < end-off; i++ {
			b, err := r.symbolAtDirect(off + i)
			if err != nil {
				return nil, false
			}
			out[i] = b
		}
		return out, true
	}
	if out, ok := r.readPathBloom(off, end); ok {
		return out, true
	}
	if out, ok := r.readHistory(off, end); ok {
		return out, true
	}
	if out, ok := r.readCheckpoint(off, end); ok {
		return out, true
	}
	return nil, false
}

// decodePrefix reveals the first n logical symbols with a single prepare of the
// machine model, bounded by the resource policy's allocation cap. It is the
// shared decode used by ReadRanges; ReadAtContext uses the cancellable variant.
func (r *Reader) decodePrefix(n uint64) ([]byte, error) {
	return r.decodePrefixContext(context.Background(), n)
}

// decodePrefixContext is decodePrefix with cooperative cancellation, checking ctx
// every ctxCheckInterval symbols.
func (r *Reader) decodePrefixContext(ctx context.Context, n uint64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkDefaultAllocation(n); err != nil {
		return nil, err
	}
	dctx, err := prepare(r.machine, r.core)
	if err != nil {
		return nil, err
	}
	s, err := r.machine.Init(r.core)
	if err != nil {
		return nil, err
	}
	out := make([]byte, n)
	for i := uint64(0); i < n; i++ {
		if i%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		b, err := dctx.reveal(r.core, &s)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

// copyCtx is copy with a periodic context check, so a large path-bloom surface
// copy (or the machine-3 direct core copy) is still cancellable.
func copyCtx(ctx context.Context, dst, src []byte) error {
	const chunk = 1 << 16
	for len(src) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := len(src)
		if n > chunk {
			n = chunk
		}
		copy(dst, src[:n])
		dst = dst[n:]
		src = src[n:]
	}
	return nil
}

// readPathBloomContext is readPathBloom with a cancellable copy.
func (r *Reader) readPathBloomContext(ctx context.Context, off, end uint64) ([]byte, bool, error) {
	if r.fast.pathHot == nil || end > uint64(len(r.fast.pathHot)) {
		return nil, false, nil
	}
	out := make([]byte, end-off)
	if err := copyCtx(ctx, out, r.fast.pathHot[off:end]); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// readHistoryContext is readHistory with a periodic context check.
func (r *Reader) readHistoryContext(ctx context.Context, off, end uint64) ([]byte, bool, error) {
	if r.machine.ID() != MachineNL2 || r.fast.histStates == nil || end > uint64(len(r.fast.histStates)) {
		return nil, false, nil
	}
	out := make([]byte, end-off)
	for i := off; i < end; i++ {
		if (i-off)%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
		}
		out[i-off] = nl2Reveal(r.fast.histStates[i], r.core[i])
	}
	return out, true, nil
}

// readCheckpointContext is readCheckpoint with a periodic context check on the
// bounded replay, so a slow replay is cancellable like a full decode.
func (r *Reader) readCheckpointContext(ctx context.Context, off, end uint64) ([]byte, bool, error) {
	cs := r.fast.checkpoints
	if cs == nil || cs.interval == 0 || r.machine.ID() != MachineNL2 || end > r.logical {
		return nil, false, nil
	}
	j := off / cs.interval
	if j >= cs.count {
		return nil, false, nil
	}
	x := uint32(cs.states[j])
	cursor := j * cs.interval
	out := make([]byte, end-off)
	var work uint64
	for i := off; i < end; i++ {
		for cursor < i {
			if work%ctxCheckInterval == 0 {
				if err := ctx.Err(); err != nil {
					return nil, false, err
				}
			}
			s := nl2Reveal(x, r.core[cursor])
			x = uint32(byte(x))<<8 | uint32(s)
			cursor++
			work++
		}
		out[i-off] = nl2Reveal(x, r.core[i])
	}
	return out, true, nil
}
