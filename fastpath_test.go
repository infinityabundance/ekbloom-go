package ekbloom

import (
	"os"
	"path/filepath"
	"testing"
)

func openFixture(t *testing.T, name string) *Reader {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", name+".ekb"))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	r, err := Open(data)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	return r
}

// TestFastPathSelected confirms each manifested structure is parsed and chosen.
func TestFastPathSelected(t *testing.T) {
	if r := openFixture(t, "m1_path4_rand"); r.fast.pathHot == nil {
		t.Error("path bloom not selected")
	}
	if r := openFixture(t, "m1_full_rand"); r.fast.histStates == nil {
		t.Error("history not selected")
	}
	if r := openFixture(t, "m1_sparse64_text"); r.fast.checkpoints == nil {
		t.Error("checkpoints not selected")
	}
	// m2_root_skew has no accelerating structure.
	if r := openFixture(t, "m2_root_skew"); r.fast.pathHot != nil || r.fast.histStates != nil || r.fast.checkpoints != nil {
		t.Error("unexpected fast path for a root machine-2 body")
	}
}

// TestFastPathAvoidsMaterialisation proves a path-bloom read does not build the
// whole symbol array.
func TestFastPathAvoidsMaterialisation(t *testing.T) {
	r := openFixture(t, "m1_path4_rand")
	if _, err := r.ByteAt(1234); err != nil {
		t.Fatalf("ByteAt: %v", err)
	}
	if r.hot != nil {
		t.Fatal("path-bloom read materialised the whole symbol array")
	}
	if _, err := r.ReadAt(0, 100); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if r.hot != nil {
		t.Fatal("path-bloom range read materialised the whole symbol array")
	}
}

// TestAcceleratedEqualsAuthoritative cross-checks every accelerated path against
// the authoritative machine decode.
func TestAcceleratedEqualsAuthoritative(t *testing.T) {
	for _, name := range []string{"m1_path4_rand", "m1_full_rand", "m1_sparse64_text"} {
		r := openFixture(t, name)
		want, err := decodeAll(r.machine, r.core, r.logical)
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		// ByteAt at every position.
		for i := uint64(0); i < r.logical; i++ {
			got, err := r.ByteAt(i)
			if err != nil {
				t.Fatalf("%s: ByteAt(%d): %v", name, i, err)
			}
			if got != want[i] {
				t.Fatalf("%s: ByteAt(%d) mismatch", name, i)
			}
		}
		// A few ranges.
		for _, rng := range [][2]uint64{{0, 10}, {5, 100}, {r.logical - 3, 3}} {
			got, err := r.ReadAt(rng[0], rng[1])
			if err != nil {
				t.Fatalf("%s: ReadAt: %v", name, err)
			}
			for k := range got {
				if got[k] != want[rng[0]+uint64(k)] {
					t.Fatalf("%s: range mismatch at %d", name, rng[0]+uint64(k))
				}
			}
		}
	}
}
