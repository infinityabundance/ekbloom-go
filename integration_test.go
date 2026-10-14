package ekbloom_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	ekbloom "github.com/infinityabundance/ekbloom-go"
)

// TestPublicAPIRoundTrip is the root integration gate: it exercises the public
// facade end to end on a Rust-written fixture, using only exported API. The
// engine-level gates (compat, legacy, corruption, bound, concurrency, chaos) live
// in internal/ekb beside the code they test.
func TestPublicAPIRoundTrip(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", "m1_root_text.ekb"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "fixtures", "m1_root_text.src"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := ekbloom.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadAt(0, uint64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("public-API round-trip mismatch")
	}
	if r.PhysicalBytes() != uint64(len(src)) {
		t.Fatalf("physical %d, want %d", r.PhysicalBytes(), len(src))
	}
	if err := ekbloom.VerifyDeep(data); err != nil {
		t.Fatalf("VerifyDeep: %v", err)
	}

	// The facade's sentinels work through errors.Is.
	bad := append([]byte("NOT AN EKBLOOM FILE"), make([]byte, 100)...)
	if _, err := ekbloom.Open(bad); !errors.Is(err, ekbloom.ErrBadMagic) {
		t.Fatalf("expected ErrBadMagic, got %v", err)
	}
}

// TestPublicAPICorruptionLocalized checks CorruptDetail through the facade.
func TestPublicAPICorruptionLocalized(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", "m1_root_allbytes.ekb"))
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), data...)
	bad[80] ^= 0x01 // flip one bit inside the visible core
	if _, err := ekbloom.Open(bad); err == nil {
		t.Fatal("corruption accepted")
	}
	section, _, ok := ekbloom.CorruptDetail(ekbloom.VerifyDeep(bad))
	if !ok || section == "" {
		t.Fatalf("corruption not localized: section=%q ok=%v", section, ok)
	}
}
