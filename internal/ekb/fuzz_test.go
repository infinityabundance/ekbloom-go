package ekb

import (
	"bytes"
	"errors"
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

// FuzzOpenAuthenticatedBytes asserts that OpenAuthenticated over arbitrary bytes
// with a fixed 32-byte key never panics and only ever returns a typed *Error: a
// successfully authenticated reader serves every byte, and every failure
// (bad magic, corruption, a missing/stripped AUTH section, or a key that does
// not match the stored tag) is a typed *Error, never garbage or a panic. The
// same bytes are also driven through a second, input-derived (wrong) key so the
// AUTH-mismatch rejection path is covered as well as the authenticated-valid
// one.
func FuzzOpenAuthenticatedBytes(f *testing.F) {
	key := testKey()

	for _, seed := range [][]byte{
		{},
		[]byte("EKBLOOM\x00"),
		[]byte("not an ekbloom file at all"),
		append([]byte("EKBLOOM\x00"), make([]byte, 200)...),
	} {
		f.Add(seed)
	}
	if data, err := readTestdata("auth/golden_auth.ekb"); err == nil {
		f.Add(data)
	}

	// A genuine authenticated image under the fixed key, plus mutated variants
	// that exercise the integrity/AUTH-failure paths rather than the valid one.
	flip := func(b []byte, i int) []byte {
		c := append([]byte(nil), b...)
		c[i] ^= 0x01
		return c
	}
	if auth, err := WriteBodyAuthenticated(1, []byte("authenticated fuzz seed payload"), rootProfile(), key); err == nil {
		f.Add(auth)
		f.Add(flip(auth, 0))
		f.Add(flip(auth, len(auth)/2))
		f.Add(flip(auth, len(auth)-1))
		f.Add(append([]byte(nil), auth[:len(auth)/2]...))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// check opens under k and, if it authenticates, walks every logical byte,
		// requiring a typed error from any failure (never a panic or a raw error).
		check := func(k [32]byte, which string) {
			r, err := OpenAuthenticated(data, k)
			if err != nil {
				var typed *Error
				if !errors.As(err, &typed) {
					t.Fatalf("%s: non-typed error %T: %v", which, err, err)
				}
				return
			}
			if !r.Authenticated() {
				t.Fatalf("%s: OpenAuthenticated returned an unauthenticated reader", which)
			}
			n := r.LogicalLen()
			if n > 1<<20 {
				return
			}
			for i := uint64(0); i < n; i++ {
				if _, err := r.ByteAt(i); err != nil {
					var typed *Error
					if !errors.As(err, &typed) {
						t.Fatalf("%s: ByteAt non-typed error %T: %v", which, err, err)
					}
					return
				}
			}
		}

		// Fixed-key path: only a genuine image authenticates.
		check(key, "fixed-key")

		// Wrong-key path: a second key derived from the input. A genuine image (or
		// one mutated anywhere the tag covers) must be rejected as a typed error.
		var wrong [32]byte
		if len(data) >= 32 {
			copy(wrong[:], data[:32])
		} else {
			copy(wrong[:], data)
		}
		if wrong != key {
			check(wrong, "wrong-key")
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
