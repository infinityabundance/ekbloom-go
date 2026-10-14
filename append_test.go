package ekbloom

import (
	"bytes"
	"errors"
	"testing"
)

// TestAppendGenerationManifestsAndReads appends a full history to a root body and
// proves the new generation reads byte-exactly and selects the accelered path.
func TestAppendGenerationManifestsAndReads(t *testing.T) {
	src := sampleSource(0, 2000)
	root, err := WriteBody(1, src, rootProfile())
	if err != nil {
		t.Fatal(err)
	}
	app, gen, err := AppendGeneration(root, 1, src, fullProfile())
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if gen != 2 {
		t.Fatalf("generation %d want 2", gen)
	}
	r, err := Open(app)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if r.Generation() != 2 {
		t.Fatalf("generation %d want 2", r.Generation())
	}
	got, err := r.ReadAt(0, uint64(len(src)))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("append changed the logical content")
	}
	if r.PhysicalBytes() <= uint64(len(src)) {
		t.Fatal("appended history not accounted")
	}
	if r.fast.histStates == nil {
		t.Fatal("appended history not selected as a fast path")
	}
	// Appending again is additive and monotone.
	app2, gen2, err := AppendGeneration(app, 1, src, sparseProfile(64))
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if gen2 != 3 {
		t.Fatalf("generation %d want 3", gen2)
	}
	r2, err := Open(app2)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	if r2.fast.checkpoints == nil || r2.fast.histStates == nil {
		t.Fatal("second generation lost an earlier structure")
	}
	got2, _ := r2.ReadAt(0, uint64(len(src)))
	if !bytes.Equal(got2, src) {
		t.Fatal("second append changed the logical content")
	}
}

// TestAppendFaultInjection models a torn write (ENOSPC/EIO mid append): every
// truncation of the appended image must still recover the newest *valid*
// generation — the previous committed one — and never a corrupt or partial
// success. This is the "newest valid generation is picked" property.
func TestAppendFaultInjection(t *testing.T) {
	src := sampleSource(1, 1500)
	root, err := WriteBody(1, src, rootProfile())
	if err != nil {
		t.Fatal(err)
	}
	app, _, err := AppendGeneration(root, 1, src, fullProfile())
	if err != nil {
		t.Fatal(err)
	}
	if len(app) <= len(root) {
		t.Fatal("append produced no new bytes")
	}

	bad := 0
	for cut := len(root); cut < len(app); cut++ {
		prefix := app[:cut]
		outcome, err := Scan(prefix)
		if err != nil {
			// Only the header/core region could error, and that is intact here.
			t.Fatalf("cut=%d: scan error %v", cut, err)
		}
		if len(outcome.Commits) == 0 {
			t.Fatalf("cut=%d: no valid commit recovered", cut)
		}
		// The recovered prefix must be exactly generation 1 (torn) or 2 (whole).
		last := outcome.Commits[len(outcome.Commits)-1]
		if last.Generation != 1 && last.Generation != 2 {
			t.Fatalf("cut=%d: recovered generation %d", cut, last.Generation)
		}
		if len(outcome.Commits) == 1 && last.Generation != 1 {
			t.Fatalf("cut=%d: torn append recovered a non-previous generation", cut)
		}
		// Open must succeed and return the recovered generation's bytes.
		r, err := Open(prefix)
		if err != nil {
			t.Fatalf("cut=%d: open: %v", cut, err)
		}
		got, err := r.ReadAt(0, uint64(len(src)))
		if err != nil {
			t.Fatalf("cut=%d: read: %v", cut, err)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("cut=%d: recovered bytes differ from source", cut)
		}
		if outcome.TornTailBytes == 0 && last.Generation != 2 && cut > len(root) {
			bad++
		}
	}
	if bad != 0 {
		t.Fatalf("%d truncations reported no torn tail but were not generation 2", bad)
	}

	// TruncateToValid returns the previous generation for a torn tail.
	truncated, gen, err := TruncateToValid(app[:len(app)-1])
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if gen != 1 {
		t.Fatalf("truncate recovered generation %d want 1", gen)
	}
	if len(truncated) != len(root) {
		t.Fatalf("truncate length %d want %d", len(truncated), len(root))
	}
}

// TestAppendInPlaceCorruption models an EIO in the appended region. It proves two
// properties:
//
//   - fallback: a bit flip anywhere in generation 2's section *payload* fails the
//     section CRC, so the corrupt commit is dropped and the newest *valid*
//     generation (1) is recovered with the correct bytes;
//   - robustness: no single-bit flip anywhere at or after gen 1's end can ever
//     yield wrong bytes or a panic — it yields either the correct bytes or a typed
//     *Error.
func TestAppendInPlaceCorruption(t *testing.T) {
	src := sampleSource(2, 1500)
	root, err := WriteBody(1, src, rootProfile())
	if err != nil {
		t.Fatal(err)
	}
	app, _, err := AppendGeneration(root, 1, src, fullProfile())
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := Scan(app)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Commits) != 2 {
		t.Fatalf("want 2 commits, got %d", len(outcome.Commits))
	}
	gen2 := outcome.Commits[1]
	var hist *ScannedSection
	for i := range gen2.Sections {
		if gen2.Sections[i].Kind == SectionHistoryFull {
			hist = &gen2.Sections[i]
		}
	}
	if hist == nil {
		t.Fatal("generation 2 has no history section")
	}

	// Fallback: corrupt one byte of gen 2's section payload -> CRC fails -> the
	// newest valid generation (1) is selected, byte-exact.
	for _, off := range []uint64{hist.PayloadOffset, hist.PayloadOffset + hist.PayloadLen/2, hist.PayloadOffset + hist.PayloadLen - 1} {
		mutated := append([]byte(nil), app...)
		mutated[off] ^= 0x80
		r, err := Open(mutated)
		if err != nil {
			t.Fatalf("payload flip at %d: open: %v", off, err)
		}
		if r.Generation() != 1 {
			t.Fatalf("payload flip at %d: generation %d, want fallback to 1", off, r.Generation())
		}
		if got, _ := r.ReadAt(0, uint64(len(src))); !bytes.Equal(got, src) {
			t.Fatalf("payload flip at %d: recovered wrong bytes", off)
		}
	}

	// Robustness: every flip in the appended region is either recovered exactly or
	// rejected with a typed error — never wrong bytes, never a panic.
	for off := len(root); off < len(app); off++ {
		mutated := append([]byte(nil), app...)
		mutated[off] ^= 0x01
		r, err := Open(mutated)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("flip at %d: non-typed error %v", off, err)
			}
			continue
		}
		got, err := r.ReadAt(0, uint64(len(src)))
		if err != nil {
			t.Fatalf("flip at %d: read: %v", off, err)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("flip at %d: recovered wrong bytes", off)
		}
	}
}

// TestAppendRejectsMismatchedObject confirms a body that does not match the
// committed object is refused rather than silently appended.
func TestAppendRejectsMismatchedObject(t *testing.T) {
	root, err := WriteBody(1, []byte("aaaa"), rootProfile())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := AppendGeneration(root, 1, []byte("bbbb"), fullProfile()); err == nil {
		t.Fatal("append accepted a mismatched object")
	}
	if _, _, err := AppendGeneration(root, 2, []byte("aaaa"), fullProfile()); err == nil {
		t.Fatal("append accepted a mismatched machine")
	}
}
