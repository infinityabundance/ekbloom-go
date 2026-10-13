package blake3

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// vectorFile is the ground truth produced by the Rust `blake3` crate (see
// .reference/fixturegen). It pins the Go implementation to the exact reference.
type vectorFile struct {
	Pattern string `json:"pattern"`
	KeyHex  string `json:"key_hex"`
	Vectors []struct {
		Len   int    `json:"len"`
		Hash  string `json:"hash"`
		Keyed string `json:"keyed"`
	} `json:"vectors"`
}

func patternBytes(len int) []byte {
	b := make([]byte, len)
	for i := range b {
		b[i] = byte((i*7 + 11) & 0xff)
	}
	return b
}

func TestBlake3AgainstRustVectors(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "blake3_vectors.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vf vectorFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(vf.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	keyBytes, err := hex.DecodeString(vf.KeyHex)
	if err != nil || len(keyBytes) != 32 {
		t.Fatalf("bad key: %v", err)
	}
	var key [32]byte
	copy(key[:], keyBytes)

	for _, v := range vf.Vectors {
		data := patternBytes(v.Len)
		got := Sum(data)
		if hex.EncodeToString(got[:]) != v.Hash {
			t.Errorf("len=%d unkeyed: got %x want %s", v.Len, got, v.Hash)
		}
		gotK := SumKeyed(key, data)
		if hex.EncodeToString(gotK[:]) != v.Keyed {
			t.Errorf("len=%d keyed: got %x want %s", v.Len, gotK, v.Keyed)
		}
	}
}

func TestBlake3KnownEmpty(t *testing.T) {
	want := "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"
	got := Sum(nil)
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("empty: got %x want %s", got, want)
	}
}

func TestHasherMatchesOneShot(t *testing.T) {
	sizes := []int{0, 1, 63, 64, 65, 1023, 1024, 1025, 2048, 4096, 5000, 100000}
	for _, n := range sizes {
		data := patternBytes(n)
		want := Sum(data)
		h := NewHasher()
		// Feed in awkward chunk sizes to exercise chunk boundaries.
		step := 7
		if n < step {
			step = n
		}
		for i := 0; i < n; {
			end := i + step
			if end > n {
				end = n
			}
			h.Write(data[i:end])
			i = end
		}
		got := h.Sum()
		if got != want {
			t.Errorf("len=%d streaming differs from one-shot", n)
		}
	}
}

func TestKeyedHasherMatchesOneShot(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = byte(i)
	}
	data := patternBytes(4096)
	want := SumKeyed(key, data)
	h := NewKeyedHasher(key)
	h.Write(data[:1000])
	h.Write(data[1000:])
	if got := h.Sum(); got != want {
		t.Errorf("keyed streaming differs from one-shot")
	}
}
