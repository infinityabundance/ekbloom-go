package ekb

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

// TestWriteBloomProfiles proves the universal and regional writers emit valid,
// verifiable shells that the reader accepts and accounts exactly (universal) or
// under the upper bound (regional).
func TestWriteBloomProfiles(t *testing.T) {
	src := sampleSource(0, 400)

	univ, err := WriteBody(1, src, Profile{Kind: ProfileBloom, Depth: 4})
	if err != nil {
		t.Fatalf("universal write: %v", err)
	}
	if err := VerifyDeep(univ); err != nil {
		t.Fatalf("universal verify: %v", err)
	}
	ru, err := Open(univ)
	if err != nil {
		t.Fatalf("universal open: %v", err)
	}
	if got, _ := ru.ReadAt(0, uint64(len(src))); !bytes.Equal(got, src) {
		t.Fatal("universal round-trip mismatch")
	}
	if ru.PhysicalBytes() != ru.Estimate() {
		t.Fatalf("universal physical %d != estimate %d", ru.PhysicalBytes(), ru.Estimate())
	}

	reg, err := WriteBody(1, src, Profile{Kind: ProfileRegional, Depth: 4, RegionBytes: 256})
	if err != nil {
		t.Fatalf("regional write: %v", err)
	}
	if err := VerifyDeep(reg); err != nil {
		t.Fatalf("regional verify: %v", err)
	}
	rr, err := Open(reg)
	if err != nil {
		t.Fatalf("regional open: %v", err)
	}
	if got, _ := rr.ReadAt(0, uint64(len(src))); !bytes.Equal(got, src) {
		t.Fatal("regional round-trip mismatch")
	}
	if rr.PhysicalBytes() > rr.Estimate() {
		t.Fatalf("regional physical %d exceeds upper-bound estimate %d", rr.PhysicalBytes(), rr.Estimate())
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

// TestWriteTiles proves the tiles writer emits a valid, verifiable TileSet.
func TestWriteTiles(t *testing.T) {
	src := sampleSource(0, 4096)
	ekb, err := WriteBodyTiles(1, src, 12, []uint64{0})
	if err != nil {
		t.Fatalf("write tiles: %v", err)
	}
	if err := VerifyDeep(ekb); err != nil {
		t.Fatalf("verify: %v", err)
	}
	r, err := Open(ekb)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got, _ := r.ReadAt(0, uint64(len(src))); !bytes.Equal(got, src) {
		t.Fatal("tiles round-trip mismatch")
	}
	// physical = core + (data + directory)
	if r.PhysicalBytes() != uint64(len(src))+4096+4 {
		t.Fatalf("physical %d unexpected", r.PhysicalBytes())
	}
}
