package ekbloom

import (
	"io"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestStreamBoundedMemory2GiB builds a 2 GiB logical stream with 64 KiB blocks
// and asserts the builder's peak heap stays bounded by the block size (plus an
// O(blocks) directory), independent of the total length. Opt-in because it moves
// 2 GiB: run with EKB_RUN_SCALE=1.
func TestStreamBoundedMemory2GiB(t *testing.T) {
	if os.Getenv("EKB_RUN_SCALE") == "" {
		t.Skip("set EKB_RUN_SCALE=1 to run the 2 GiB bounded-memory build")
	}
	const total = int64(2) << 30 // 2 GiB

	stop := make(chan struct{})
	var peak uint64
	go func() {
		var ms runtime.MemStats
		for {
			select {
			case <-stop:
				return
			default:
			}
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peak {
				peak = ms.HeapAlloc
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	start := time.Now()
	sb, err := NewStreamBuilder(io.Discard, StreamOptions{BlockBytes: 1 << 16, Machine: PolicyAuto})
	if err != nil {
		t.Fatal(err)
	}
	block := make([]byte, 1<<20)
	for i := range block {
		block[i] = byte('a' + i%4)
	}
	var written int64
	for written < total {
		n := int64(len(block))
		if total-written < n {
			n = total - written
		}
		if err := sb.Write(block[:n]); err != nil {
			t.Fatal(err)
		}
		written += n
	}
	manifest, err := sb.Finish()
	if err != nil {
		t.Fatal(err)
	}
	dur := time.Since(start)
	close(stop)

	if manifest.LogicalLen != uint64(total) {
		t.Fatalf("logical len %d want %d", manifest.LogicalLen, total)
	}
	const limit = 64 << 20 // 64 MiB
	if peak > limit {
		t.Fatalf("peak heap %d MiB exceeds %d MiB bound", peak>>20, limit>>20)
	}
	t.Logf("built 2 GiB stream (blocks=%d) in %s at peak heap %.2f MiB (%.1f MiB/s)",
		manifest.BlockCount, dur, float64(peak)/(1<<20),
		float64(total)/(1<<20)/dur.Seconds())
}
