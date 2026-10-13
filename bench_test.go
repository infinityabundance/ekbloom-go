package ekbloom

import (
	"bytes"
	"os"
	"path/filepath"
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
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", name+".ekb"))
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
