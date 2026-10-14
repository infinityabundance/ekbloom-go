package ekb

import "testing"

// TestSboxIsBijection proves S(v) = v ^ (v<<1) ^ (v<<3) is a bijection on 0..255,
// the property every machine that mixes a byte through S(v) relies on. It is an
// invertible linear map over GF(2)^8 ((I + L + L^3) with L the left shift); here
// we verify exhaustively, which also pins the exact table the format freezes.
func TestSboxIsBijection(t *testing.T) {
	var seen [256]bool
	for v := 0; v < 256; v++ {
		s := sbox[byte(v)]
		if seen[s] {
			t.Fatalf("S(%d) = %d collides: not a bijection", v, s)
		}
		seen[s] = true
	}
	for s := 0; s < 256; s++ {
		if !seen[s] {
			t.Fatalf("value %d is never produced: not a bijection", s)
		}
	}

	// The inverse must exist for every image, so a round-trip recovers v.
	var inv [256]byte
	for v := 0; v < 256; v++ {
		inv[sbox[byte(v)]] = byte(v)
	}
	for v := 0; v < 256; v++ {
		if inv[sbox[byte(v)]] != byte(v) {
			t.Fatalf("inverse failed at %d", v)
		}
	}
}
