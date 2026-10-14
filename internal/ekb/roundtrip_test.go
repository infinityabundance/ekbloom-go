package ekb

import (
	"bytes"
	"os"
	"sync"
	"testing"
)

func sampleSource(kind int, n int) []byte {
	out := make([]byte, n)
	switch kind {
	case 0:
		for i := range out {
			out[i] = byte('a' + i%4)
		}
	case 1:
		s := uint64(7)
		for i := range out {
			s = s*6364136223846793005 + 1442695040888963407
			out[i] = byte(s >> 33)
		}
	default:
		for i := range out {
			out[i] = byte(i)
		}
	}
	return out
}

// TestGoRoundTrip proves Go writing then Go reading is lossless for every machine
// and the root/full/sparse profiles.
func TestGoRoundTrip(t *testing.T) {
	src := []byte("user=alice|region=us-east-1|role=admin")
	full := sampleSource(0, 3000)
	for _, tc := range []struct {
		name      string
		machineID uint32
		profile   Profile
		source    []byte
	}{
		{"m1_root", 1, rootProfile(), src},
		{"m1_full", 1, fullProfile(), full},
		{"m1_sparse64", 1, sparseProfile(64), full},
		{"m2_root", 2, rootProfile(), full},
		{"m3_root", 3, rootProfile(), full},
		{"m4_root", 4, rootProfile(), full},
		{"m5_root", 5, rootProfile(), full},
		{"m6_root", 6, rootProfile(), full},
		{"m7_root", 7, rootProfile(), full},
		{"m8_root", 8, rootProfile(), full},
		{"m9_root", 9, rootProfile(), src},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ekb, err := WriteBody(tc.machineID, tc.source, tc.profile)
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
			if r.LogicalLen() != uint64(len(tc.source)) {
				t.Fatalf("logical len %d want %d", r.LogicalLen(), len(tc.source))
			}
			got, err := r.ReadAt(0, uint64(len(tc.source)))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, tc.source) {
				t.Fatalf("round-trip mismatch")
			}
			steps, err := r.WorstCaseStepsFor(0, uint64(len(tc.source)))
			if err != nil {
				t.Fatalf("worst case: %v", err)
			}
			if steps < uint64(len(tc.source)) {
				t.Fatalf("worst-case steps %d below output length %d", steps, len(tc.source))
			}
			if r.Estimate() < r.PhysicalBytes() {
				t.Logf("estimate %d < physical %d (allowed for cursor-divergent machines)", r.Estimate(), r.PhysicalBytes())
			}
		})
	}
}

// TestDecodeCore proves the public core encode/decode pair round-trips for every
// machine and rejects an unknown machine id with a typed error.
func TestDecodeCore(t *testing.T) {
	src := sampleSource(0, 2048)
	for id := uint32(1); id <= 9; id++ {
		m, ok := MachineByID(id)
		if !ok {
			t.Fatalf("machine %d not registered", id)
		}
		core, err := m.Encode(src)
		if err != nil {
			t.Fatalf("m%d encode: %v", id, err)
		}
		got, err := DecodeCore(id, core, uint64(len(src)))
		if err != nil {
			t.Fatalf("m%d decode: %v", id, err)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("m%d round-trip mismatch", id)
		}
	}
	if _, err := DecodeCore(99, nil, 0); err == nil {
		t.Fatal("unknown machine accepted")
	}
}

// TestConcurrentReaders runs 64 readers against one shared Reader under -race.
func TestConcurrentReaders(t *testing.T) {
	data, err := os.ReadFile(testdataPath("fixtures", "m1_root_allbytes.ekb"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	r, err := Open(data)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 128; i++ {
				off := uint64((g*17 + i*13) % 256)
				b, err := r.ByteAt(off)
				if err != nil {
					t.Errorf("ByteAt: %v", err)
					return
				}
				if b != byte(off) {
					t.Errorf("ByteAt(%d)=%d", off, b)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
