package ekb

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type streamMeta struct {
	LogicalLen     uint64 `json:"logical_len"`
	BlockBytes     uint32 `json:"block_bytes"`
	BlockCount     uint64 `json:"block_count"`
	ContainerBytes uint64 `json:"container_bytes"`
	PayloadBytes   uint64 `json:"payload_bytes"`
	SourceHash     string `json:"source_hash"`
}

// TestRustStreamGolden proves the Go stream reader matches the Rust container
// produced by the reference StreamBuilder: same geometry, same source hash, and
// byte-exact sequential and random reads.
func TestRustStreamGolden(t *testing.T) {
	raw, err := os.ReadFile(testdataPath("stream.json"))
	if err != nil {
		t.Fatalf("read stream.json: %v", err)
	}
	var meta streamMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("parse stream.json: %v", err)
	}
	container, err := os.ReadFile(testdataPath("stream", "golden.stream"))
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	src, err := os.ReadFile(testdataPath("stream", "golden.src"))
	if err != nil {
		t.Fatalf("read src: %v", err)
	}
	r, err := OpenStream(container)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if r.Len() != meta.LogicalLen {
		t.Errorf("logical len %d want %d", r.Len(), meta.LogicalLen)
	}
	if r.BlockBytes() != meta.BlockBytes {
		t.Errorf("block bytes %d want %d", r.BlockBytes(), meta.BlockBytes)
	}
	if r.BlockCount() != meta.BlockCount {
		t.Errorf("block count %d want %d", r.BlockCount(), meta.BlockCount)
	}
	if r.PayloadBytes() != meta.PayloadBytes {
		t.Errorf("payload bytes %d want %d", r.PayloadBytes(), meta.PayloadBytes)
	}
	if r.ContainerBytes() != meta.ContainerBytes {
		t.Errorf("container bytes %d want %d", r.ContainerBytes(), meta.ContainerBytes)
	}
	sh := r.SourceHash()
	if hex.EncodeToString(sh[:]) != meta.SourceHash {
		t.Errorf("source hash %x want %s", sh, meta.SourceHash)
	}

	// Byte-exact whole-object read.
	whole := make([]byte, r.Len())
	if err := r.ReadAt(0, whole); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(whole, src) {
		t.Fatalf("whole-object stream read mismatch")
	}
	// Byte-exact sequential read.
	seq, err := OpenStream(container)
	if err != nil {
		t.Fatalf("OpenStream 2: %v", err)
	}
	var seqBuf bytes.Buffer
	tmp := make([]byte, 777)
	for {
		n, err := seq.ReadSequential(tmp)
		if err != nil {
			t.Fatalf("ReadSequential: %v", err)
		}
		if n == 0 {
			break
		}
		seqBuf.Write(tmp[:n])
	}
	if !bytes.Equal(seqBuf.Bytes(), src) {
		t.Fatalf("sequential stream read mismatch")
	}
	// A few random ranges.
	for _, rng := range [][2]uint64{{100, 64}, {4090, 32}, {17900, 100}, {9000, 1}} {
		got := make([]byte, rng[1])
		if err := r.ReadAt(rng[0], got); err != nil {
			t.Fatalf("ReadAt(%d,%d): %v", rng[0], rng[1], err)
		}
		if !bytes.Equal(got, src[rng[0]:rng[0]+rng[1]]) {
			t.Fatalf("range read mismatch at %d", rng[0])
		}
	}
}

// TestStreamGoRoundTrip proves Go writing and Go reading a stream is lossless.
func TestStreamGoRoundTrip(t *testing.T) {
	data := make([]byte, 50000)
	for i := range data {
		data[i] = byte('a' + i%5)
	}
	var sink bytes.Buffer
	b, err := NewStreamBuilder(&sink, StreamOptions{BlockBytes: 4096, Machine: PolicyAuto})
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	if err := b.Write(data[:12345]); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := b.Checkpoint(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := b.Write(data[12345:]); err != nil {
		t.Fatalf("write: %v", err)
	}
	manifest, err := b.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if manifest.LogicalLen != uint64(len(data)) {
		t.Fatalf("logical len %d want %d", manifest.LogicalLen, len(data))
	}
	r, err := OpenStream(sink.Bytes())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got := make([]byte, r.Len())
	if err := r.ReadAt(0, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("round-trip mismatch")
	}
	if r.SourceHash() != manifest.SourceHash {
		t.Fatalf("source hash mismatch")
	}
}
