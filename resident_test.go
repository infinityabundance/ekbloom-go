package ekbloom

import "testing"

// TestResidentViewMatchesByteAt proves the borrowed view returns the same bytes
// as the reader for every position on every surface kind.
func TestResidentViewMatchesByteAt(t *testing.T) {
	for _, name := range []string{"m1_path4_rand", "m1_full_rand", "m2_root_skew"} {
		r := openFixture(t, name)
		v, err := r.Resident()
		if err != nil {
			t.Fatalf("%s: Resident: %v", name, err)
		}
		if v.Len() != r.LogicalLen() {
			t.Fatalf("%s: view length %d want %d", name, v.Len(), r.LogicalLen())
		}
		for i := uint64(0); i < r.LogicalLen(); i++ {
			b, ok := v.Get(i)
			if !ok {
				t.Fatalf("%s: Get(%d) out of range", name, i)
			}
			want, err := r.ByteAt(i)
			if err != nil {
				t.Fatalf("%s: ByteAt(%d): %v", name, i, err)
			}
			if b != want {
				t.Fatalf("%s: Get(%d)=%d ByteAt=%d", name, i, b, want)
			}
		}
		if _, ok := v.Get(r.LogicalLen()); ok {
			t.Fatalf("%s: Get past the end must fail", name)
		}
	}
}

// TestResidentViewBorrowsPathBloom proves the path-bloom surface is borrowed (no
// copy), so resolving it allocates nothing.
func TestResidentViewBorrowsPathBloom(t *testing.T) {
	r := openFixture(t, "m1_path4_rand")
	v, err := r.Resident()
	if err != nil {
		t.Fatal(err)
	}
	if len(v.data) == 0 || &v.data[0] != &r.fast.pathHot[0] {
		t.Fatal("ResidentView did not borrow the path-bloom surface")
	}
}

// TestResidentViewGetIsAllocationFree confirms per-call Get allocates nothing.
func TestResidentViewGetIsAllocationFree(t *testing.T) {
	r := openFixture(t, "m1_path4_rand")
	v, err := r.Resident()
	if err != nil {
		t.Fatal(err)
	}
	n := v.Len()
	allocs := testing.AllocsPerRun(1000, func() {
		for i := uint64(0); i < n; i++ {
			_ = v.At(i)
		}
	})
	if allocs != 0 {
		t.Fatalf("ResidentView.At allocated %.1f/run, want 0", allocs)
	}
}
