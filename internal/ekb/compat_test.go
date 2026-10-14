package ekb

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

type fixtureMeta struct {
	Name          string `json:"name"`
	MachineID     uint32 `json:"machine_id"`
	Profile       string `json:"profile"`
	LogicalLen    uint64 `json:"logical_len"`
	CoreLen       uint64 `json:"core_len"`
	VisibleBytes  uint64 `json:"visible_bytes"`
	PhysicalBytes uint64 `json:"physical_bytes"`
	Generation    uint64 `json:"generation"`
	EncodeMatches bool   `json:"encode_matches"`
	Dict          string `json:"dict"`
}

type fixturesFile struct {
	Fixtures []fixtureMeta `json:"fixtures"`
}

func loadFixtures(t *testing.T) fixturesFile {
	t.Helper()
	raw, err := os.ReadFile(testdataPath("fixtures.json"))
	if err != nil {
		t.Fatalf("read fixtures.json: %v", err)
	}
	var f fixturesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse fixtures.json: %v", err)
	}
	if len(f.Fixtures) == 0 {
		t.Fatal("no fixtures")
	}
	return f
}

// TestRustFixturesAreByteIdentical proves, for every fixture written by the Rust
// reference implementation, that:
//
//  1. Go's Open accepts it and agrees on machine id, logical length, visible and
//     physical bytes, and generation;
//  2. Go's ByteAt returns exactly the source symbol at every position;
//  3. Go's ReadAt over the whole object equals the source;
//  4. Go's machine Encode reproduces the Rust core byte-for-byte.
func TestRustFixturesAreByteIdentical(t *testing.T) {
	fix := loadFixtures(t)
	for _, fx := range fix.Fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			if fx.Dict != "" {
				db, err := os.ReadFile(testdataPath(fx.Dict))
				if err != nil {
					t.Fatalf("read dict: %v", err)
				}
				d, err := DictionaryFromBytes(db)
				if err != nil {
					t.Fatalf("parse dict: %v", err)
				}
				RegisterDictionary(d)
			}
			ekb, err := os.ReadFile(testdataPath("fixtures", fx.Name+".ekb"))
			if err != nil {
				t.Fatalf("read ekb: %v", err)
			}
			src, err := os.ReadFile(testdataPath("fixtures", fx.Name+".src"))
			if err != nil {
				t.Fatalf("read src: %v", err)
			}
			r, err := Open(ekb)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if r.MachineID() != fx.MachineID {
				t.Errorf("machine id: got %d want %d", r.MachineID(), fx.MachineID)
			}
			if r.LogicalLen() != fx.LogicalLen {
				t.Errorf("logical len: got %d want %d", r.LogicalLen(), fx.LogicalLen)
			}
			if r.VisibleBytes() != fx.VisibleBytes {
				t.Errorf("visible bytes: got %d want %d", r.VisibleBytes(), fx.VisibleBytes)
			}
			if r.PhysicalBytes() != fx.PhysicalBytes {
				t.Errorf("physical bytes: got %d want %d", r.PhysicalBytes(), fx.PhysicalBytes)
			}
			if r.Generation() != fx.Generation {
				t.Errorf("generation: got %d want %d", r.Generation(), fx.Generation)
			}

			// (2) ByteAt at every position.
			for i, want := range src {
				got, err := r.ByteAt(uint64(i))
				if err != nil {
					t.Fatalf("ByteAt(%d): %v", i, err)
				}
				if got != want {
					t.Fatalf("ByteAt(%d): got %d want %d", i, got, want)
				}
			}
			// (3) ReadAt over the whole object.
			all, err := r.ReadAt(0, uint64(len(src)))
			if err != nil {
				t.Fatalf("ReadAt: %v", err)
			}
			if !bytes.Equal(all, src) {
				t.Fatalf("ReadAt whole object mismatch")
			}
			// (4) Re-encode the source and compare to the Rust core, when the fixture
			// core was produced by the machine's own Encode (a supplied-dictionary
			// fixture uses a different framing, so the comparison does not apply).
			if fx.EncodeMatches {
				m, ok := MachineByID(fx.MachineID)
				if !ok {
					t.Fatalf("no machine %d", fx.MachineID)
				}
				core, err := m.Encode(src)
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				if !bytes.Equal(core, r.CoreBytes()) {
					t.Fatalf("re-encoded core differs from Rust core (%d vs %d bytes)", len(core), len(r.CoreBytes()))
				}
			}
			if uint64(len(r.CoreBytes())) != fx.CoreLen {
				t.Errorf("core len: got %d want %d", len(r.CoreBytes()), fx.CoreLen)
			}
		})
	}
}
