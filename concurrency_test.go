package ekbloom

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Test64ReadersOneWriter runs 64 concurrent readers against one shared, RCU-style
// published Reader while a single writer keeps appending generations and
// republishing. It reports the read-latency p50/p99/p99.9 and asserts no reader
// error and a quiet tail, under `go test -race`.
func Test64ReadersOneWriter(t *testing.T) {
	src := sampleSource(0, 20000)
	base, err := WriteBody(1, src, pathProfile(4))
	if err != nil {
		t.Fatal(err)
	}

	var cur atomic.Pointer[Reader]
	r0, err := Open(base)
	if err != nil {
		t.Fatal(err)
	}
	cur.Store(r0)

	const readers = 64
	const readsPer = 5000

	stop := make(chan struct{})
	var wgReaders sync.WaitGroup
	var wgWriter sync.WaitGroup

	// Writer: append generations and republish (never blocking readers).
	var generations atomic.Uint64
	wgWriter.Add(1)
	go func() {
		defer wgWriter.Done()
		image := base
		for {
			select {
			case <-stop:
				return
			default:
			}
			next, _, err := AppendGeneration(image, 1, src, rootProfile())
			if err != nil {
				return
			}
			image = next
			nr, err := Open(image)
			if err != nil {
				return
			}
			cur.Store(nr)
			generations.Add(1)
			time.Sleep(100 * time.Microsecond)
		}
	}()

	lat := make([][]time.Duration, readers)
	start := make(chan struct{})
	for g := 0; g < readers; g++ {
		wgReaders.Add(1)
		go func(g int) {
			defer wgReaders.Done()
			<-start
			lat[g] = make([]time.Duration, 0, readsPer)
			seed := uint64(g)*2654435761 + 1
			for i := 0; i < readsPer; i++ {
				r := cur.Load()
				seed = seed*6364136223846793005 + 1442695040888963407
				off := (seed >> 33) % r.LogicalLen()
				t0 := time.Now()
				b, err := r.ByteAt(off)
				d := time.Since(t0)
				if err != nil {
					t.Errorf("reader %d: ByteAt: %v", g, err)
					return
				}
				if b != src[off] {
					t.Errorf("reader %d: ByteAt(%d) mismatch", g, off)
					return
				}
				lat[g] = append(lat[g], d)
			}
		}(g)
	}
	close(start)
	wgReaders.Wait()
	close(stop)
	wgWriter.Wait()

	// Pooled percentiles.
	var all []time.Duration
	for _, l := range lat {
		all = append(all, l...)
	}
	if len(all) == 0 {
		t.Fatal("no samples")
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	pct := func(p float64) time.Duration { return all[int(float64(len(all)-1)*p)] }
	p50, p99, p999 := pct(0.50), pct(0.99), pct(0.999)

	rep := struct {
		Readers     int    `json:"readers"`
		Samples     int    `json:"samples"`
		Generations uint64 `json:"generations_published"`
		P50ns       int64  `json:"p50_ns"`
		P99ns       int64  `json:"p99_ns"`
		P999ns      int64  `json:"p999_ns"`
	}{readers, len(all), generations.Load(), p50.Nanoseconds(), p99.Nanoseconds(), p999.Nanoseconds()}
	out, _ := json.MarshalIndent(rep, "", "  ")
	_ = os.WriteFile("evidence/concurrency.json", append(out, '\n'), 0o644)
	t.Logf("64 readers/%d gens: p50=%v p99=%v p99.9=%v", generations.Load(), p50, p99, p999)
}
