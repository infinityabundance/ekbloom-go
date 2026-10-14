package ekbloom

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// These fixtures are the Rust reference's frozen wire-format compatibility set
// (`.reference/ekbloom-rust/tests/fixtures/`), written by older releases
// (`writer` 0.3.0 / 0.4.0) with `core_version = 0`. Porting the companion test
// proves the Go reader opens legacy files byte-identically and enforces the same
// tamper refusal.

type legacyCase struct {
	Name          string     `json:"name"`
	File          string     `json:"file"`
	Writer        string     `json:"writer"`
	MachineID     uint32     `json:"machine_id"`
	FormatMajor   uint16     `json:"format_major"`
	FormatMinor   uint16     `json:"format_minor"`
	CoreVersion   uint16     `json:"core_version"`
	CoreLen       uint64     `json:"core_len"`
	LogicalLen    uint64     `json:"logical_len"`
	PhysicalBytes uint64     `json:"physical_bytes"`
	FileBytes     uint64     `json:"file_bytes"`
	Samples       [][2]int64 `json:"samples"`
	SampleHash    string     `json:"sample_hash"`
	FileBlake3    string     `json:"file_blake3"`
}

type legacyManifest struct {
	Fixtures []legacyCase `json:"fixtures"`
}

func loadLegacy(t *testing.T) []legacyCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "legacy", "expected.json"))
	if err != nil {
		t.Fatalf("read expected.json: %v", err)
	}
	var m legacyManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse expected.json: %v", err)
	}
	if len(m.Fixtures) < 12 {
		t.Fatalf("expected >=12 frozen fixtures, found %d", len(m.Fixtures))
	}
	return m.Fixtures
}

func u16le(b []byte, off int) uint16 { return uint16(b[off]) | uint16(b[off+1])<<8 }
func u32le(b []byte, off int) uint32 {
	return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24
}
func u64le(b []byte, off int) uint64 {
	var v uint64
	for i := 0; i < 8; i++ {
		v |= uint64(b[off+i]) << (8 * i)
	}
	return v
}

// TestLegacyFixturesOpenByteIdentically ports the Rust `compat_fixtures` test.
func TestLegacyFixturesOpenByteIdentically(t *testing.T) {
	cases := loadLegacy(t)
	writers := map[string]bool{}
	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			writers[c.Writer] = true
			before, err := os.ReadFile(filepath.Join("testdata", "legacy", c.File))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if uint64(len(before)) != c.FileBytes {
				t.Fatalf("file length %d want %d", len(before), c.FileBytes)
			}
			if string(before[0:8]) != "EKBLOOM\x00" {
				t.Fatal("bad magic")
			}
			// The frozen bytes themselves.
			if h := blake3Hash(before); hex.EncodeToString(h[:]) != c.FileBlake3 {
				t.Fatalf("file blake3 mismatch: %x want %s", h, c.FileBlake3)
			}
			if u16le(before, 8) != c.FormatMajor || u16le(before, 10) != c.FormatMinor {
				t.Fatalf("format version %d.%d want %d.%d", u16le(before, 8), u16le(before, 10), c.FormatMajor, c.FormatMinor)
			}
			if u32le(before, 64) != c.MachineID {
				t.Fatalf("machine id %d want %d", u32le(before, 64), c.MachineID)
			}
			if u16le(before, 68) != c.CoreVersion {
				t.Fatalf("core_version %d want %d", u16le(before, 68), c.CoreVersion)
			}
			if u64le(before, 56) != c.CoreLen {
				t.Fatalf("core_len %d want %d", u64le(before, 56), c.CoreLen)
			}

			r, err := Open(before)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if r.LogicalLen() != c.LogicalLen {
				t.Fatalf("logical_len %d want %d", r.LogicalLen(), c.LogicalLen)
			}
			if r.MachineID() != c.MachineID {
				t.Fatalf("machine_id %d want %d", r.MachineID(), c.MachineID)
			}
			if r.PhysicalBytes() != c.PhysicalBytes {
				t.Fatalf("physical_bytes %d want %d", r.PhysicalBytes(), c.PhysicalBytes)
			}

			sampled := make([]byte, 0, len(c.Samples))
			for _, s := range c.Samples {
				got, err := r.ByteAt(uint64(s[0]))
				if err != nil {
					t.Fatalf("ByteAt(%d): %v", s[0], err)
				}
				if got != byte(s[1]) {
					t.Fatalf("ByteAt(%d)=%d want %d", s[0], got, s[1])
				}
				sampled = append(sampled, got)
			}
			if h := blake3Hash(sampled); hex.EncodeToString(h[:]) != c.SampleHash {
				t.Fatalf("sample hash mismatch: %x want %s", h, c.SampleHash)
			}
			if err := VerifyDeep(before); err != nil {
				t.Fatalf("VerifyDeep: %v", err)
			}
			// FROZEN: reading must not rewrite the artifact.
			after, _ := os.ReadFile(filepath.Join("testdata", "legacy", c.File))
			if string(before) != string(after) {
				t.Fatal("fixture bytes changed after opening")
			}
		})
	}
	if !writers["0.3.0"] || !writers["0.4.0"] {
		t.Fatalf("fixture set must cover writers 0.3.0 and 0.4.0; got %v", writers)
	}
}

// TestLegacyFixtureTamperRefused proves a single flipped byte is refused.
func TestLegacyFixtureTamperRefused(t *testing.T) {
	cases := loadLegacy(t)
	var c legacyCase
	for _, x := range cases {
		if x.CoreLen > 8 {
			c = x
			break
		}
	}
	good, err := os.ReadFile(filepath.Join("testdata", "legacy", c.File))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(good); err != nil {
		t.Fatalf("untampered fixture must open: %v", err)
	}
	core := append([]byte(nil), good...)
	core[80+3] ^= 0x01
	if _, err := Open(core); err == nil {
		t.Fatal("tampered core byte accepted")
	}
	footer := append([]byte(nil), good...)
	footer[len(footer)-1] ^= 0x01
	if _, err := Open(footer); err == nil {
		t.Fatal("tampered footer byte accepted")
	}
}

// TestVersionTagsRefused proves unknown version tags are typed refusals, never a
// guess: a future format version and an unknown core version are both rejected.
func TestVersionTagsRefused(t *testing.T) {
	cases := loadLegacy(t)
	good, err := os.ReadFile(filepath.Join("testdata", "legacy", cases[0].File))
	if err != nil {
		t.Fatal(err)
	}
	// Future format_minor = 4.
	future := append([]byte(nil), good...)
	future[10] = 4
	if _, err := Open(future); err == nil {
		t.Fatal("future format version accepted")
	}
	// Unknown core_version = 2.
	badCore := append([]byte(nil), good...)
	badCore[68] = 2
	_, err = Open(badCore)
	if err == nil {
		t.Fatal("unknown core_version accepted")
	}
	if e, ok := err.(*Error); !ok || e.Kind != KindUnsupportedCoreVersion {
		t.Fatalf("expected ErrUnsupportedCoreVersion, got %v", err)
	}
}
