package ekb

import (
	"bytes"
	"os"
	"testing"
)

func testKey() [32]byte {
	var k [32]byte
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

// TestRustAuthenticatedFixture proves Go verifies a Rust-written AUTH tag.
func TestRustAuthenticatedFixture(t *testing.T) {
	data, err := os.ReadFile(testdataPath("auth", "golden_auth.ekb"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	src, err := os.ReadFile(testdataPath("auth", "golden_auth.src"))
	if err != nil {
		t.Fatalf("read src: %v", err)
	}
	key := testKey()

	r, err := OpenAuthenticated(data, key)
	if err != nil {
		t.Fatalf("OpenAuthenticated: %v", err)
	}
	if !r.Authenticated() {
		t.Fatal("reader not marked authenticated")
	}
	got, err := r.ReadAt(0, uint64(len(src)))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("authenticated decode mismatch")
	}

	// Wrong key is rejected.
	var wrong [32]byte
	if _, err := OpenAuthenticated(data, wrong); err == nil {
		t.Fatal("wrong key accepted")
	}
	// Unkeyed Open still succeeds (integrity only).
	if _, err := Open(data); err != nil {
		t.Fatalf("unkeyed open: %v", err)
	}
	// An unauthenticated body is refused by OpenAuthenticated.
	plain, err := WriteBody(1, []byte("hello world"), rootProfile())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAuthenticated(plain, key); err == nil {
		t.Fatal("OpenAuthenticated accepted a body with no AUTH section")
	}
}

// TestGoAuthenticatedRoundTrip proves the Go writer/reader AUTH path.
func TestGoAuthenticatedRoundTrip(t *testing.T) {
	src := []byte("the quick brown fox; authenticated payload")
	key := testKey()
	data, err := WriteBodyAuthenticated(1, src, fullProfile(), key)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	r, err := OpenAuthenticated(data, key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, _ := r.ReadAt(0, uint64(len(src)))
	if !bytes.Equal(got, src) {
		t.Fatal("roundtrip mismatch")
	}
	// Flipping a covered byte fails BOTH integrity and authentication.
	corrupted := append([]byte(nil), data...)
	corrupted[80] ^= 0x01
	if _, err := OpenAuthenticated(corrupted, key); err == nil {
		t.Fatal("corrupted authenticated body accepted")
	}
	// Appending a new generation under the key stays verifiable.
	app, gen, err := AppendGenerationAuthenticated(data, 1, src, rootProfile(), &key)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if gen != 2 {
		t.Fatalf("generation %d want 2", gen)
	}
	if _, err := OpenAuthenticated(app, key); err != nil {
		t.Fatalf("reopen appended: %v", err)
	}
}
