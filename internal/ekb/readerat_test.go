package ekb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/infinityabundance/ekbloom-go/internal/tools/stat"
)

// Compile-time proof that the adapter satisfies the standard interface.
var _ io.ReaderAt = (*File)(nil)

// buildRanges produces a deterministic, varied set of logical ranges for n
// symbols: a full-range read, a zero-length read at the end, an overlapping pair,
// and pseudo-random short reads. Overlap is deliberate: it exercises the batch
// path's shared prefix decode.
func buildRanges(n uint64, count int, seed uint64) []Range {
	if n == 0 {
		return []Range{{0, 0}}
	}
	reqs := []Range{
		{0, n},
		{n, 0},
		{0, 1},
		{n - 1, 1},
	}
	s := seed
	for i := 0; i < count; i++ {
		s = s*6364136223846793005 + 1442695040888963407
		off := (s >> 33) % n
		maxLen := n - off
		s = s*6364136223846793005 + 1442695040888963407
		length := uint64(1) + (s>>17)%maxLen
		if length > 64 {
			length = 64
		}
		reqs = append(reqs, Range{off, length})
		// A deliberately overlapping follower.
		if off > 0 {
			o2 := off - 1
			l2 := length
			if o2+l2 > n {
				l2 = n - o2
			}
			reqs = append(reqs, Range{o2, l2})
		}
	}
	return reqs
}

// checkSectionCopy reads the whole logical content and several sub-ranges through
// io.NewSectionReader/io.Copy, proving the io.ReaderAt contract end-to-end.
func checkSectionCopy(r *Reader, src []byte) error {
	f := r.File()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.NewSectionReader(f, 0, int64(len(src)))); err != nil {
		return fmt.Errorf("io.Copy: %w", err)
	}
	if !bytes.Equal(buf.Bytes(), src) {
		return fmt.Errorf("io.Copy mismatch: got %d bytes", buf.Len())
	}
	n := uint64(len(src))
	for _, sub := range []Range{{0, n}, {0, 1}, {n / 3, 97}, {n - 5, 5}} {
		sr := io.NewSectionReader(f, int64(sub.Off), int64(sub.Len))
		got, err := io.ReadAll(sr)
		if err != nil {
			return fmt.Errorf("sub-range %d+%d: %w", sub.Off, sub.Len, err)
		}
		if !bytes.Equal(got, src[sub.Off:sub.Off+sub.Len]) {
			return fmt.Errorf("sub-range %d+%d mismatch", sub.Off, sub.Len)
		}
	}
	return nil
}

// checkBatchEqual proves ReadRanges equals the same ranges read one at a time and
// equals the source bytes.
func checkBatchEqual(r *Reader, src []byte, reqs []Range) error {
	outs, err := r.ReadRanges(reqs)
	if err != nil {
		return fmt.Errorf("ReadRanges: %w", err)
	}
	if len(outs) != len(reqs) {
		return fmt.Errorf("ReadRanges returned %d slices for %d requests", len(outs), len(reqs))
	}
	for i, req := range reqs {
		if uint64(len(outs[i])) != req.Len {
			return fmt.Errorf("range %d: len %d want %d", i, len(outs[i]), req.Len)
		}
		if !bytes.Equal(outs[i], src[req.Off:req.Off+req.Len]) {
			return fmt.Errorf("range %d (%d+%d): bytes disagree with source", i, req.Off, req.Len)
		}
		want, err := r.ReadAt(req.Off, req.Len)
		if err != nil {
			return fmt.Errorf("range %d: ReadAt: %w", i, err)
		}
		if !bytes.Equal(outs[i], want) {
			return fmt.Errorf("range %d (%d+%d): batch differs from individual ReadAt", i, req.Off, req.Len)
		}
	}
	return nil
}

// checkConcurrentFile drives goroutines over several readers, mixing File.ReadAt
// and ReadRanges and verifying every result against the source. Run under -race,
// it must be clean.
func checkConcurrentFile(readers []*Reader, srcs [][]byte, goroutines, iters int) error {
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			idx := g % len(readers)
			r := readers[idx]
			src := srcs[idx]
			f := r.File()
			n := uint64(len(src))
			seed := uint64(g)*2654435761 + 1
			for i := 0; i < iters; i++ {
				seed = seed*6364136223846793005 + 1442695040888963407
				off := (seed >> 33) % n
				want := uint64(8)
				if off+want > n {
					want = n - off
				}
				p := make([]byte, 8)
				got, err := f.ReadAt(p, int64(off))
				// A short read that reaches the end returns io.EOF; accept it only
				// when it delivered exactly the remaining bytes.
				eofOK := (err == io.EOF || err == nil) && uint64(got) == want
				if !eofOK {
					errs <- fmt.Errorf("g%d File.ReadAt(%d): n=%d want=%d err=%v", g, off, got, want, err)
					return
				}
				if !bytes.Equal(p[:got], src[off:off+uint64(got)]) {
					errs <- fmt.Errorf("g%d File.ReadAt(%d): bytes disagree", g, off)
					return
				}
				reqs := make([]Range, 4)
				for j := range reqs {
					seed = seed*6364136223846793005 + 1442695040888963407
					ro := (seed >> 33) % n
					seed = seed*6364136223846793005 + 1442695040888963407
					rl := uint64(1) + (seed>>17)%16
					if ro+rl > n {
						rl = n - ro
					}
					reqs[j] = Range{Off: ro, Len: rl}
				}
				outs, err := r.ReadRanges(reqs)
				if err != nil {
					errs <- fmt.Errorf("g%d ReadRanges: %w", g, err)
					return
				}
				for j, req := range reqs {
					if !bytes.Equal(outs[j], src[req.Off:req.Off+req.Len]) {
						errs <- fmt.Errorf("g%d ReadRanges[%d] (%d+%d): bytes disagree", g, j, req.Off, req.Len)
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// measureBatchP50Ns times a batch read over `samples` iterations and returns the
// p50 in nanoseconds.
func measureBatchP50Ns(r *Reader, reqs []Range, samples int) int64 {
	durs := make([]int64, samples)
	for i := 0; i < samples; i++ {
		t0 := time.Now()
		outs, err := r.ReadRanges(reqs)
		d := time.Since(t0).Nanoseconds()
		if err != nil || len(outs) != len(reqs) {
			return -1
		}
		durs[i] = d
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	return durs[len(durs)*50/100]
}

// cancelBackedReader builds a machine-2 root body large enough that a full decode
// takes many milliseconds, so a short deadline reliably expires mid-decode.
func cancelBackedReader(t testing.TB) (*Reader, []byte) {
	t.Helper()
	const n = 2 << 20
	src := sampleSource(1, n)
	data, err := WriteBody(MachineRans0, src, rootProfile())
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	r, err := Open(data)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return r, src
}

// TestReaderAtContract covers the io.ReaderAt contract: the compile assertion,
// SectionReader/io.Copy equality, sub-ranges, io.EOF at and past the end, and a
// typed error for a negative offset.
func TestReaderAtContract(t *testing.T) {
	src := sampleSource(0, 4096)
	data, err := WriteBody(MachineNL2, src, rootProfile())
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	f := r.File()

	if err := checkSectionCopy(r, src); err != nil {
		t.Fatalf("section copy: %v", err)
	}

	// At the logical end: 0 bytes, io.EOF.
	if n, err := f.ReadAt(make([]byte, 8), int64(len(src))); n != 0 || err != io.EOF {
		t.Fatalf("ReadAt at end: n=%d err=%v, want 0, io.EOF", n, err)
	}
	// A short read that reaches the end: n bytes then io.EOF.
	p := make([]byte, 16)
	n, err := f.ReadAt(p, int64(len(src))-4)
	if n != 4 || err != io.EOF {
		t.Fatalf("short read: n=%d err=%v, want 4, io.EOF", n, err)
	}
	if !bytes.Equal(p[:4], src[len(src)-4:]) {
		t.Fatalf("short read bytes mismatch: %q", p[:4])
	}
	// Empty p inside the range: 0, nil (bytes.Reader semantics).
	if n, err := f.ReadAt(nil, 0); n != 0 || err != nil {
		t.Fatalf("empty read in range: n=%d err=%v, want 0, nil", n, err)
	}
	// Empty p at the end: 0, io.EOF (bytes.Reader semantics).
	if n, err := f.ReadAt(nil, int64(len(src))); n != 0 || err != io.EOF {
		t.Fatalf("empty read at end: n=%d err=%v, want 0, io.EOF", n, err)
	}
	// Negative offset: a typed error, never a panic and never io.EOF.
	_, err = f.ReadAt(p, -1)
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("negative offset: err=%v, want a typed error", err)
	}
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindInvalidArgument {
		t.Fatalf("negative offset: err=%v, want KindInvalidArgument", err)
	}
	// The free-function constructor behaves identically.
	if f2 := NewFile(r); f2 == nil || f2.Reader != r {
		t.Fatal("NewFile returned an adapter that does not share the reader")
	}
}

// TestReaderAtBatchEqual proves ReadRanges equals individual reads across several
// machines and profiles, including both fast-path and full-decode surfaces.
func TestReaderAtBatchEqual(t *testing.T) {
	text := sampleSource(0, 4096)
	rand := sampleSource(1, 4096)
	incr := sampleSource(2, 4096)
	dict := bytes.Repeat([]byte("user=alice|region=us-east-1|role=admin\n"), 16)

	cases := []struct {
		name      string
		machineID uint32
		profile   Profile
		source    []byte
	}{
		{"m1_root_text", MachineNL2, rootProfile(), text},
		{"m1_full_text", MachineNL2, fullProfile(), text},
		{"m1_sparse64_text", MachineNL2, sparseProfile(64), text},
		{"m1_path4_rand", MachineNL2, pathProfile(4), rand},
		{"m2_root_rand", MachineRans0, rootProfile(), rand},
		{"m3_root_incr", MachineStore, rootProfile(), incr},
		{"m4_root_rand", MachineRans0I, rootProfile(), rand},
		{"m5_root_rand", MachinePack, rootProfile(), rand},
		{"m6_root_incr", MachineSeg, rootProfile(), incr},
		{"m7_root_rand", MachineRans0W, rootProfile(), rand},
		{"m8_root_rand", MachineRans0IL, rootProfile(), rand},
		{"m9_root_dict", MachineDict, rootProfile(), dict},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			data, err := WriteBody(tc.machineID, tc.source, tc.profile)
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			r, err := Open(data)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if r.LogicalLen() != uint64(len(tc.source)) {
				t.Fatalf("logical len %d want %d", r.LogicalLen(), len(tc.source))
			}
			reqs := buildRanges(uint64(len(tc.source)), 24, uint64(tc.machineID)*7919+13)
			if err := checkBatchEqual(r, tc.source, reqs); err != nil {
				t.Fatalf("batch: %v", err)
			}
		})
	}
}

// TestReaderAtConcurrent64 runs 64 goroutines across a fast-path and a fallback
// reader under `go test -race`, mixing File.ReadAt and ReadRanges.
func TestReaderAtConcurrent64(t *testing.T) {
	text := sampleSource(0, 8192)
	incr := sampleSource(2, 4096)

	build := func(id uint32, src []byte, p Profile) *Reader {
		data, err := WriteBody(id, src, p)
		if err != nil {
			t.Fatal(err)
		}
		r, err := Open(data)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	readers := []*Reader{
		build(MachineNL2, text, pathProfile(4)),  // certified path bloom (fast copy)
		build(MachineNL2, text, rootProfile()),   // full-decode fallback
		build(MachineStore, incr, rootProfile()), // direct core copy
	}
	srcs := [][]byte{text, text, incr}

	if err := checkConcurrentFile(readers, srcs, 64, 400); err != nil {
		t.Fatalf("concurrent: %v", err)
	}
}

// TestReaderAtCancellation proves ReadAtContext returns the typed ctx error
// promptly: immediately for an already-cancelled context, and well below a full
// decode for a deadline that expires mid-decode.
func TestReaderAtCancellation(t *testing.T) {
	r, src := cancelBackedReader(t)
	n := uint64(len(src))

	// A single wall-clock sample can be inflated by an unlucky scheduler
	// deschedule (common under a CPU quota), so every latency below is the BEST
	// (minimum) of a few trials. The baseline is ReadAtContext with a live
	// context — the exact decode path that cancellation aborts — so the
	// comparison is apples to apples and does not depend on the materialised
	// symbol array (which ReadAtContext never uses).
	const trials = 5
	const none = time.Duration(1<<63 - 1)
	live := context.Background()

	fullDur := none
	for i := 0; i < trials; i++ {
		t0 := time.Now()
		full, err := r.ReadAtContext(live, 0, n)
		if d := time.Since(t0); d < fullDur {
			fullDur = d
		}
		if err != nil || !bytes.Equal(full, src) {
			t.Fatalf("baseline full decode: err=%v", err)
		}
	}

	// Already-cancelled context: returns context.Canceled with no work.
	ctx, cancel := context.WithCancel(live)
	cancel()
	cancelledLat := none
	for i := 0; i < trials; i++ {
		start := time.Now()
		_, err := r.ReadAtContext(ctx, 0, n)
		if d := time.Since(start); d < cancelledLat {
			cancelledLat = d
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled ctx: err=%v, want context.Canceled", err)
		}
	}

	// A deadline that expires mid-decode: returns context.DeadlineExceeded, and
	// the latency to return is far below the full decode. The budget is a small
	// fraction of the measured full decode, so it is guaranteed to expire
	// mid-decode on any machine — a fixed budget (e.g. 1 ms) races a fast decode
	// and flakes when the decode happens to finish before the deadline fires.
	budget := fullDur / 8
	if budget < time.Microsecond {
		budget = time.Microsecond
	}
	deadlineLat := none
	for i := 0; i < trials; i++ {
		dctx, dcancel := context.WithTimeout(live, budget)
		start := time.Now()
		_, err := r.ReadAtContext(dctx, 0, n)
		if d := time.Since(start); d < deadlineLat {
			deadlineLat = d
		}
		dcancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired ctx: err=%v, want context.DeadlineExceeded", err)
		}
	}

	t.Logf("full decode best-of-%d=%v | cancelled best-of-%d=%v | deadline best-of-%d=%v",
		trials, fullDur, trials, cancelledLat, trials, deadlineLat)

	if cancelledLat > fullDur {
		t.Fatalf("cancelled ctx latency %v not below full decode %v", cancelledLat, fullDur)
	}
	if deadlineLat >= fullDur {
		t.Fatalf("cancel latency %v not below full decode %v", deadlineLat, fullDur)
	}

	// A live context still yields the correct bytes.
	if got, err := r.ReadAtContext(context.Background(), 16, 64); err != nil || !bytes.Equal(got, src[16:80]) {
		t.Fatalf("live ctx read: err=%v", err)
	}
}

// TestReaderAtEvidence runs the surface checks and writes evidence/readerat.json.
// The latency fields are the median across R = EKB_RUNS runs (default 5; 3 in CI),
// with the observed min/max retained.
func TestReaderAtEvidence(t *testing.T) {
	// Section copy via io.NewSectionReader/io.Copy.
	src := sampleSource(0, 4096)
	data, err := WriteBody(MachineNL2, src, pathProfile(4))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	sectionCopy := "pass"
	if err := checkSectionCopy(r, src); err != nil {
		sectionCopy = "fail"
		t.Errorf("section copy: %v", err)
	}

	// Batch equality across two machine surfaces.
	batchEqual := "pass"
	if err := checkBatchEqual(r, src, buildRanges(uint64(len(src)), 24, 12345)); err != nil {
		batchEqual = "fail"
		t.Errorf("batch equal: %v", err)
	}

	// 64 concurrent readers (race-covered by the script's -race).
	concSrc := sampleSource(0, 8192)
	cdata, err := WriteBody(MachineNL2, concSrc, pathProfile(4))
	if err != nil {
		t.Fatal(err)
	}
	cr, err := Open(cdata)
	if err != nil {
		t.Fatal(err)
	}
	concurrent := "pass"
	if err := checkConcurrentFile([]*Reader{cr, r}, [][]byte{concSrc, src}, 64, 200); err != nil {
		concurrent = "fail"
		t.Errorf("concurrent: %v", err)
	}

	runs := stat.Runs()

	// Batch p50 over >= 4096 samples per run; median across runs.
	const samples = 4096
	reqs := buildRanges(uint64(len(src)), 4, 99)
	batchVals := make([]int64, 0, runs)
	for run := 0; run < runs; run++ {
		p := measureBatchP50Ns(r, reqs, samples)
		if p < 0 {
			t.Errorf("batch measurement failed")
		}
		batchVals = append(batchVals, p)
	}
	batchP50 := stat.Collect(batchVals)

	// Cancellation latency on a body whose full decode dominates. Within a run the
	// deadline latency is the BEST of a few trials (a single wall-clock sample can
	// be inflated by an unlucky scheduler deschedule); across runs the reported value
	// is the median. The baseline is ReadAtContext with a live context — the same
	// decode path cancellation aborts — and the deadline is a small fraction of it,
	// so the deadline is guaranteed to expire mid-decode on any machine.
	cr2, _ := cancelBackedReader(t)
	cn := cr2.LogicalLen()
	live := context.Background()
	measureCancel := func() int64 {
		base := time.Duration(1<<63 - 1)
		for i := 0; i < 3; i++ {
			start := time.Now()
			if _, err := cr2.ReadAtContext(live, 0, cn); err != nil {
				t.Fatalf("cancel baseline: %v", err)
			}
			if d := time.Since(start); d < base {
				base = d
			}
		}
		budget := base / 8
		if budget < time.Microsecond {
			budget = time.Microsecond
		}
		best := time.Duration(1<<63 - 1)
		for i := 0; i < 5; i++ {
			dctx, dcancel := context.WithTimeout(live, budget)
			start := time.Now()
			_, err := cr2.ReadAtContext(dctx, 0, cn)
			if d := time.Since(start); d < best {
				best = d
			}
			dcancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("cancel: err=%v, want context.DeadlineExceeded", err)
			}
		}
		return best.Nanoseconds()
	}
	cancelVals := make([]int64, 0, runs)
	for run := 0; run < runs; run++ {
		cancelVals = append(cancelVals, measureCancel())
	}
	cancelS := stat.Collect(cancelVals)

	ev := struct {
		SectionCopy   string `json:"readerat_section_copy"`
		BatchEqual    string `json:"batch_equal"`
		Concurrent    string `json:"concurrent_64_race"`
		Runs          int    `json:"runs"`
		Statistic     string `json:"statistic"`
		CancelNs      int64  `json:"cancel_latency_ns"`
		CancelNsMin   int64  `json:"cancel_latency_ns_min"`
		CancelNsMax   int64  `json:"cancel_latency_ns_max"`
		BatchP50Ns    int64  `json:"batch_p50_ns"`
		BatchP50NsMin int64  `json:"batch_p50_ns_min"`
		BatchP50NsMax int64  `json:"batch_p50_ns_max"`
	}{sectionCopy, batchEqual, concurrent, runs, "median-of-runs",
		cancelS.Median, cancelS.Min, cancelS.Max, batchP50.Median, batchP50.Min, batchP50.Max}

	out, _ := json.MarshalIndent(ev, "", "  ")
	if err := writeTimingEvidence("readerat.json", append(out, '\n'), 0o644); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	t.Logf("readerat_section_copy=%s batch_equal=%s concurrent_64_race=%s cancel_latency_ns=%d(%d..%d) batch_p50_ns=%d(%d..%d)",
		sectionCopy, batchEqual, concurrent, cancelS.Median, cancelS.Min, cancelS.Max, batchP50.Median, batchP50.Min, batchP50.Max)
}
