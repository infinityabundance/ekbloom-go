package ekbloom

import (
	"bytes"
	"testing"
)

// TestWritePathProfile proves the writer emits a certified exact path bloom that
// the reader selects as the fastest path, with accounting that matches the
// static estimate (core + path payload + certificate).
func TestWritePathProfile(t *testing.T) {
	src := sampleSource(0, 4000)
	ekb, err := WriteBody(1, src, pathProfile(4))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := VerifyDeep(ekb); err != nil {
		t.Fatalf("verify: %v", err)
	}
	r, err := Open(ekb)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if r.fast.pathHot == nil {
		t.Fatal("path bloom not selected")
	}
	got, err := r.ReadAt(0, uint64(len(src)))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("path-profile round-trip mismatch")
	}
	if r.PhysicalBytes() != r.Estimate() {
		t.Fatalf("physical %d != estimate %d (path accounting)", r.PhysicalBytes(), r.Estimate())
	}
	if r.Metrics().ExpansionRatio <= 1 {
		t.Fatalf("path profile should expand: E_V=%v", r.Metrics().ExpansionRatio)
	}
}

// TestVersionRefusals proves bad magic and unsupported versions are typed.
func TestVersionRefusals(t *testing.T) {
	ekb, _ := WriteBody(1, []byte("hello"), rootProfile())
	if _, err := Open([]byte("NOTANEBK")); err == nil {
		t.Fatal("bad magic accepted")
	}
	future := append([]byte(nil), ekb...)
	future[10] = 4 // format_minor
	if _, err := Open(future); err == nil {
		t.Fatal("future format accepted")
	}
	badcore := append([]byte(nil), ekb...)
	badcore[68] = 2 // core_version
	if _, err := Open(badcore); err == nil {
		t.Fatal("unsupported core version accepted")
	}
}
