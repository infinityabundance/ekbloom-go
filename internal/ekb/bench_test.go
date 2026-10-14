package ekb

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

// benchPattern builds deterministic skewed data for benchmarks.
func benchPattern(n int) []byte {
	out := make([]byte, n)
	s := uint64(0x1234)
	for i := range out {
		s = s*6364136223846793005 + 1442695040888963407
		r := (s >> 33) % 100
		switch {
		case r < 60:
			out[i] = 'a'
		case r < 85:
			out[i] = 'b'
		case r < 95:
			out[i] = 'c'
		default:
			out[i] = 'd'
		}
	}
	return out
}

func BenchmarkMachine1Encode(b *testing.B) {
	src := benchPattern(1 << 20)
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := (nl2{}).Encode(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMachine2Encode(b *testing.B) {
	src := benchPattern(1 << 20)
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := (rans0{}).Encode(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMachine2Decode(b *testing.B) {
	src := benchPattern(1 << 20)
	core, _ := (rans0{}).Encode(src)
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := decodeAll(rans0{}, core, uint64(len(src))); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBlake3_1MiB(b *testing.B) {
	data := benchPattern(1 << 20)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = blake3Hash(data)
	}
}

func benchReader(b *testing.B, name string) *Reader {
	b.Helper()
	data, err := os.ReadFile(testdataPath("fixtures", name+".ekb"))
	if err != nil {
		b.Fatalf("read: %v", err)
	}
	r, err := Open(data)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	return r
}

func BenchmarkByteAtProbe(b *testing.B) {
	r := benchReader(b, "m2_root_skew")
	// Warm the lazily materialised decode.
	if _, err := r.ByteAt(0); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.ByteAt(uint64(i) % r.LogicalLen()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadAt4KiB(b *testing.B) {
	r := benchReader(b, "m2_root_skew")
	buf := make([]byte, 4096)
	b.SetBytes(4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := uint64(i*64) % (r.LogicalLen() - 4096)
		if err := readInto(r, off, buf); err != nil {
			b.Fatal(err)
		}
	}
}

func readInto(r *Reader, off uint64, buf []byte) error {
	got, err := r.ReadAt(off, uint64(len(buf)))
	if err != nil {
		return err
	}
	copy(buf, got)
	return nil
}

// ByteAt served by the exact path bloom (no materialisation).
func BenchmarkByteAtPathBloom(b *testing.B) {
	r := benchReader(b, "m1_path4_rand")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.ByteAt(uint64(i) % r.LogicalLen()); err != nil {
			b.Fatal(err)
		}
	}
}

// ByteAt served by the full machine-1 history (O(1) state, no materialisation).
func BenchmarkByteAtHistory(b *testing.B) {
	r := benchReader(b, "m1_full_rand")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.ByteAt(uint64(i) % r.LogicalLen()); err != nil {
			b.Fatal(err)
		}
	}
}

// ByteAt served by the nearest checkpoint restart (bounded replay).
func BenchmarkByteAtCheckpoint(b *testing.B) {
	r := benchReader(b, "m1_sparse64_text")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.ByteAt(uint64(i) % r.LogicalLen()); err != nil {
			b.Fatal(err)
		}
	}
}

// ReadAt served by the path bloom (whole range copied from the hot surface).
func BenchmarkReadAt4KiBPathBloom(b *testing.B) {
	r := benchReader(b, "m1_path4_rand")
	buf := make([]byte, 4096)
	b.SetBytes(4096)
	span := r.LogicalLen() - 4096
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var off uint64
		if span > 0 {
			off = uint64(i*64) % span
		}
		if err := readInto(r, off, buf); err != nil {
			b.Fatal(err)
		}
	}
}

// ResidentView.Get: one bounds check and a bare load, zero allocations.
func BenchmarkResidentViewGet(b *testing.B) {
	r := benchReader(b, "m1_path4_rand")
	v, err := r.Resident()
	if err != nil {
		b.Fatal(err)
	}
	n := v.Len()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = v.At(uint64(i) % n)
	}
}

// A bare slice load, as the raw-Vec parity reference point.
func BenchmarkBareSliceLoad(b *testing.B) {
	data := benchPattern(4096)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = data[i%len(data)]
	}
}

// Encode/decode/sequential benchmarks for all nine machines.
func BenchmarkMachineEncode(b *testing.B) {
	src := benchPattern(1 << 20)
	for id := uint32(1); id <= 9; id++ {
		m, _ := MachineByID(id)
		b.Run(fmt.Sprintf("m%d", id), func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := m.Encode(src); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMachineDecode(b *testing.B) {
	src := benchPattern(1 << 20)
	for id := uint32(1); id <= 9; id++ {
		m, _ := MachineByID(id)
		core, err := m.Encode(src)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("m%d", id), func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := decodeAll(m, core, uint64(len(src))); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMachineSequential(b *testing.B) {
	src := benchPattern(1 << 20)
	for id := uint32(1); id <= 9; id++ {
		eqb, err := WriteBody(id, src, rootProfile())
		if err != nil {
			b.Fatal(err)
		}
		r, err := Open(eqb)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("m%d", id), func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := r.ReadAt(0, uint64(len(src))); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkStreamBuildBounded(b *testing.B) {
	src := benchPattern(8 << 20) // 8 MiB
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var sink bytes.Buffer
		sb, err := NewStreamBuilder(&sink, StreamOptions{BlockBytes: 1 << 16, Machine: PolicyAuto})
		if err != nil {
			b.Fatal(err)
		}
		if err := sb.Write(src); err != nil {
			b.Fatal(err)
		}
		if _, err := sb.Finish(); err != nil {
			b.Fatal(err)
		}
	}
}
