package ekb

import (
	"bytes"
	"testing"
)

// FuzzOpenBytes asserts that Open never panics on arbitrary bytes, and that a
// successfully opened image serves every byte without panicking.
func FuzzOpenBytes(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		[]byte("EKBLOOM\x00"),
		[]byte("not an ekbloom file at all"),
		append([]byte("EKBLOOM\x00"), make([]byte, 200)...),
	} {
		f.Add(seed)
	}
	if data, err := readTestdata("fixtures/m1_root_allbytes.ekb"); err == nil {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := Open(data)
		if err != nil {
			return
		}
		n := r.LogicalLen()
		if n > 1<<20 {
			return
		}
		for i := uint64(0); i < n; i++ {
			if _, err := r.ByteAt(i); err != nil {
				return
			}
		}
		if all, err := r.ReadAt(0, n); err == nil && len(all) != int(n) {
			t.Fatalf("ReadAt returned %d bytes, want %d", len(all), n)
		}
	})
}

// FuzzStreamBytes asserts that OpenStream never panics on arbitrary bytes.
func FuzzStreamBytes(f *testing.F) {
	f.Add([]byte("EKSTREAM"))
	f.Add(make([]byte, 224))
	if data, err := readTestdata("stream/golden.stream"); err == nil {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := OpenStream(data)
		if err != nil {
			return
		}
		if r.Len() > 1<<20 {
			return
		}
		buf := make([]byte, r.Len())
		if err := r.ReadAt(0, buf); err != nil {
			return
		}
		seq, err := OpenStream(data)
		if err != nil {
			return
		}
		tmp := make([]byte, 1024)
		for {
			n, err := seq.ReadSequential(tmp)
			if err != nil || n == 0 {
				break
			}
		}
	})
}

// FuzzMachineRoundTrip asserts every machine's encode/decode is exactly
// invertible and never panics.
func FuzzMachineRoundTrip(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("hello, world"))
	f.Add(bytes.Repeat([]byte("abcd"), 300))
	for i := 0; i < 256; i++ {
		f.Add([]byte{byte(i)})
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		for id := uint32(1); id <= 9; id++ {
			m, ok := MachineByID(id)
			if !ok {
				continue
			}
			core, err := m.Encode(data)
			if err != nil {
				continue
			}
			out, err := decodeAll(m, core, uint64(len(data)))
			if err != nil {
				t.Fatalf("machine %d: decode of own encode failed: %v", id, err)
			}
			if !bytes.Equal(out, data) {
				t.Fatalf("machine %d: round-trip mismatch", id)
			}
		}
	})
}
