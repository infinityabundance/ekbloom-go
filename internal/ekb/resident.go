package ekb

// ResidentView is an allocation-free, borrowed view over a contiguous resident
// surface of the logical object. Resolve it once, then Get(i) is a single
// bounds check plus a bare load (zero allocations per call) — the Go counterpart
// of the reference's `fastpath::ResidentView`, which reaches raw-`Vec` parity for
// a fully resident lookup.
//
// The surface is:
//   - the exact path-bloom `hot` array, borrowed with no copy, when a certified
//     path bloom is present; or
//   - the lazily materialised symbol array otherwise.
//
// In both cases `base` is 0 for the whole-object surfaces this port keeps; the
// field exists so a future region/tile-scoped view can rebase without changing
// the API.
type ResidentView struct {
	data []byte
	base uint64
}

// Resident resolves the best contiguous resident surface and returns a borrowed
// view. It allocates nothing when a path bloom is present.
func (r *Reader) Resident() (*ResidentView, error) {
	if r.fast.pathHot != nil && uint64(len(r.fast.pathHot)) == r.logical {
		return &ResidentView{data: r.fast.pathHot, base: 0}, nil
	}
	hot, err := r.symbols()
	if err != nil {
		return nil, err
	}
	return &ResidentView{data: hot, base: 0}, nil
}

// Len is the number of logical positions the view covers.
func (v *ResidentView) Len() uint64 { return uint64(len(v.data)) }

// Base is the first logical position covered by the view.
func (v *ResidentView) Base() uint64 { return v.base }

// Get returns the logical symbol at position i, or ok=false if i is outside the
// view. It performs one bounds check and no allocation.
func (v *ResidentView) Get(i uint64) (byte, bool) {
	if i < v.base {
		return 0, false
	}
	j := i - v.base
	if j >= uint64(len(v.data)) {
		return 0, false
	}
	return v.data[j], true
}

// At returns the symbol at i, panicking only if i is out of range (the caller has
// proven the range, as with a slice index).
func (v *ResidentView) At(i uint64) byte {
	return v.data[i-v.base]
}
