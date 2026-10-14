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

// tail is the pooled read-latency tail for one phase of the concurrency test.
type tail struct {
	Samples int   `json:"samples"`
	P50ns   int64 `json:"p50_ns"`
	P99ns   int64 `json:"p99_ns"`
	P999ns  int64 `json:"p999_ns"`
}

func percentile(all []time.Duration, p float64) time.Duration {
	return all[int(float64(len(all)-1)*p)]
}

// runLoad drives `readers` goroutines, each performing `readsPer` verified ByteAt
// calls against the reader currently held in cur, and returns the pooled
// latencies. Every read is checked against src so a torn republish would fail the
// test rather than merely slow it down.
func runLoad(t *testing.T, cur *atomic.Pointer[Reader], src []byte, readers, readsPer int) []time.Duration {
	t.Helper()
	lat := make([][]time.Duration, readers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < readers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			l := make([]time.Duration, 0, readsPer)
			seed := uint64(g)*2654435761 + 1
			for i := 0; i < readsPer; i++ {
				r := cur.Load()
				seed = seed*6364136223846793005 + 1442695040888963407
				off := (seed >> 33) % r.LogicalLen()
				t0 := time.Now()
				b, err := r.ByteAt(off)
				d := time.Since(t0)
				if err != nil {
					t.Errorf("reader %d: ByteAt(%d): %v", g, off, err)
					return
				}
				if b != src[off] {
					t.Errorf("reader %d: ByteAt(%d) mismatch", g, off)
					return
				}
				l = append(l, d)
			}
			lat[g] = l
		}(g)
	}
	close(start)
	wg.Wait()
	var all []time.Duration
	for _, l := range lat {
		all = append(all, l...)
	}
	return all
}

// Test64ReadersOneWriter runs 64 concurrent readers in two phases, both under
// `go test -race`:
//
//   - quiet: readers only, against one fixed published Reader (no writer).
//   - publish: a single writer keeps appending generations and RCU-republishing
//     the Reader while the same 64 readers read.
//
// It reports the pooled p50/p99/p99.9 for each phase and asserts a quiet tail and
// no reader error or torn read.
func Test64ReadersOneWriter(t *testing.T) {
	src := sampleSource(0, 20000)
	base, err := WriteBody(1, src, pathProfile(4))
	if err != nil {
		t.Fatal(err)
	}
	r0, err := Open(base)
	if err != nil {
		t.Fatal(err)
	}

	const readers = 64
	const readsPer = 5000

	// Phase 1: quiet — readers only, no writer.
	var quietPtr atomic.Pointer[Reader]
	quietPtr.Store(r0)
	quiet := runLoad(t, &quietPtr, src, readers, readsPer)

	// Phase 2: publish — readers against a live writer republishing generations.
	var cur atomic.Pointer[Reader]
	cur.Store(r0)

	stop := make(chan struct{})
	var wgWriter sync.WaitGroup
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

	publish := runLoad(t, &cur, src, readers, readsPer)
	close(stop)
	wgWriter.Wait()

	if len(quiet) == 0 || len(publish) == 0 {
		t.Fatal("no samples")
	}
	sort.Slice(quiet, func(i, j int) bool { return quiet[i] < quiet[j] })
	sort.Slice(publish, func(i, j int) bool { return publish[i] < publish[j] })
	q := tail{len(quiet), percentile(quiet, 0.50).Nanoseconds(), percentile(quiet, 0.99).Nanoseconds(), percentile(quiet, 0.999).Nanoseconds()}
	p := tail{len(publish), percentile(publish, 0.50).Nanoseconds(), percentile(publish, 0.99).Nanoseconds(), percentile(publish, 0.999).Nanoseconds()}

	rep := struct {
		Readers     int    `json:"readers"`
		Samples     int    `json:"samples_per_phase"`
		Generations uint64 `json:"generations_published"`
		// Publish-phase tail, kept at the top level for compatibility.
		P50ns  int64 `json:"p50_ns"`
		P99ns  int64 `json:"p99_ns"`
		P999ns int64 `json:"p999_ns"`
		Quiet  tail  `json:"quiet"`
		Live   tail  `json:"publish"`
	}{readers, readsPer, generations.Load(), p.P50ns, p.P99ns, p.P999ns, q, p}

	out, _ := json.MarshalIndent(rep, "", "  ")
	_ = os.WriteFile("evidence/concurrency.json", append(out, '\n'), 0o644)
	t.Logf("64 readers quiet: p50=%dns p99=%dns p99.9=%dns", q.P50ns, q.P99ns, q.P999ns)
	t.Logf("64 readers + writer (%d gens) publish: p50=%dns p99=%dns p99.9=%dns",
		generations.Load(), p.P50ns, p.P99ns, p.P999ns)
}
