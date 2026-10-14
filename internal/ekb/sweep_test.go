package ekb

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/infinityabundance/ekbloom-go/internal/tools/stat"
)

// sweepSkewed builds n bytes over a 4-symbol 60/25/10/5 alphabet — compressible,
// so the profile frontier is meaningful (machine 2 can actually shrink it).
func sweepSkewed(n int) []byte {
	out := make([]byte, n)
	s := uint64(0x1234)
	alpha := []byte("abcd")
	for i := range out {
		s = s*6364136223846793005 + 1442695040888963407
		r := (s >> 33) % 100
		var x int
		switch {
		case r < 60:
			x = 0
		case r < 85:
			x = 1
		case r < 95:
			x = 2
		default:
			x = 3
		}
		out[i] = alpha[x]
	}
	return out
}

// TestProfileSweep records the footprint/latency frontier of the writer profiles
// on one fixed, compressible input, so a reader can see what each profile buys in
// exchange for its bytes. It writes evidence/sweep.json. Every latency/throughput
// value is the median across R = EKB_RUNS runs (default 5; 3 in CI), with the
// observed min/max retained; the deterministic fields (physical_bytes, E_V) are
// measured once.
func TestProfileSweep(t *testing.T) {
	const n = 1 << 20 // 1 MiB
	src := sweepSkewed(n)

	type row struct {
		Profile     string  `json:"profile"`
		Machine     uint32  `json:"machine_id"`
		Physical    uint64  `json:"physical_bytes"`
		EV          float64 `json:"E_V"`
		ByteAt50    int64   `json:"byte_at_p50_ns"`
		ByteAt99    int64   `json:"byte_at_p99_ns"`
		Read4k50    int64   `json:"read4k_p50_ns"`
		SeqMBs      float64 `json:"seq_mb_per_s"`
		ByteAt50Min int64   `json:"byte_at_p50_ns_min"`
		ByteAt50Max int64   `json:"byte_at_p50_ns_max"`
		ByteAt99Min int64   `json:"byte_at_p99_ns_min"`
		ByteAt99Max int64   `json:"byte_at_p99_ns_max"`
		Read4k50Min int64   `json:"read4k_p50_ns_min"`
		Read4k50Max int64   `json:"read4k_p50_ns_max"`
		SeqMBsMin   float64 `json:"seq_mb_per_s_min"`
		SeqMBsMax   float64 `json:"seq_mb_per_s_max"`
	}

	specs := []struct {
		name    string
		machine uint32
		profile Profile
	}{
		{"m1_root", 1, rootProfile()},
		{"m2_root", 2, rootProfile()},
		{"m1_full", 1, fullProfile()},
		{"m1_sparse64", 1, sparseProfile(64)},
		{"m1_path4", 1, pathProfile(4)},
		{"m2_full", 2, fullProfile()},
	}

	runs := stat.Runs()
	var rows []row
	for _, sp := range specs {
		image, err := WriteBody(sp.machine, src, sp.profile)
		if err != nil {
			t.Fatalf("%s: write: %v", sp.name, err)
		}
		r, err := Open(image)
		if err != nil {
			t.Fatalf("%s: open: %v", sp.name, err)
		}
		if got, err := r.ReadAt(0, n); err != nil || len(got) != n {
			t.Fatalf("%s: read: %v", sp.name, err)
		}
		// Warm the path once; the runs below reuse the same reader.
		_, _ = r.ByteAt(0)

		ba50 := make([]int64, 0, runs)
		ba99 := make([]int64, 0, runs)
		r4p50 := make([]int64, 0, runs)
		seqs := make([]float64, 0, runs)
		for run := 0; run < runs; run++ {
			// Single-byte reads.
			ba := make([]time.Duration, 4096)
			seed := uint64(0xC0FFEE) + uint64(run)*0x9E37_79B9_7F4A_7C15
			for i := range ba {
				seed = seed*6364136223846793005 + 1442695040888963407
				off := (seed >> 33) % uint64(n)
				t0 := time.Now()
				if _, err := r.ByteAt(off); err != nil {
					t.Fatalf("%s: byteat: %v", sp.name, err)
				}
				ba[i] = time.Since(t0)
			}
			sort.Slice(ba, func(i, j int) bool { return ba[i] < ba[j] })

			// 4 KiB reads.
			r4 := make([]time.Duration, 512)
			for i := range r4 {
				seed = seed*6364136223846793005 + 1442695040888963407
				off := (seed >> 33) % uint64(n-4096)
				t0 := time.Now()
				if _, err := r.ReadAt(off, 4096); err != nil {
					t.Fatalf("%s: read4k: %v", sp.name, err)
				}
				r4[i] = time.Since(t0)
			}
			sort.Slice(r4, func(i, j int) bool { return r4[i] < r4[j] })

			// Bulk sequential throughput: best of 3 reps within the run, so the
			// per-run value is the achievable rate, not a descheduled outlier.
			best := 0.0
			for rep := 0; rep < 3; rep++ {
				t0 := time.Now()
				if _, err := r.ReadAt(0, n); err != nil {
					t.Fatal(err)
				}
				el := time.Since(t0).Seconds()
				mbps := float64(n) / 1e6 / el
				if mbps > best {
					best = mbps
				}
			}

			ba50 = append(ba50, stat.Pct(ba, 500))
			ba99 = append(ba99, stat.Pct(ba, 990))
			r4p50 = append(r4p50, stat.Pct(r4, 500))
			seqs = append(seqs, best)
		}

		q50 := stat.Collect(ba50)
		q99 := stat.Collect(ba99)
		q4 := stat.Collect(r4p50)
		qs := stat.Collect(seqs)
		rows = append(rows, row{
			Profile: sp.name, Machine: sp.machine,
			Physical: r.PhysicalBytes(), EV: float64(r.PhysicalBytes()) / float64(n),
			ByteAt50: q50.Median, ByteAt99: q99.Median, Read4k50: q4.Median, SeqMBs: qs.Median,
			ByteAt50Min: q50.Min, ByteAt50Max: q50.Max,
			ByteAt99Min: q99.Min, ByteAt99Max: q99.Max,
			Read4k50Min: q4.Min, Read4k50Max: q4.Max,
			SeqMBsMin: qs.Min, SeqMBsMax: qs.Max,
		})
	}

	report := struct {
		Bytes     int    `json:"logical_bytes"`
		Source    string `json:"source"`
		Runs      int    `json:"runs"`
		Statistic string `json:"statistic"`
		Rows      []row  `json:"rows"`
		Note      string `json:"note"`
	}{n, "sweepSkewed(1 MiB) — 4-symbol 60/25/10/5", runs, "median-of-runs", rows,
		"E_V = physical_bytes / N on a 4-symbol 60/25/10/5 skewed source. Latency/throughput are the median across EKB_RUNS runs (min/max retained); physical_bytes/E_V are deterministic. Single host, scalar Go; a decision aid, not a portable benchmark."}

	out, _ := json.MarshalIndent(report, "", "  ")
	if err := writeTimingEvidence("sweep.json", append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		t.Logf("%-12s phys=%8d E_V=%.3f 1B p50=%4dns 4K p50=%5dns seq=%.0f MB/s",
			r.Profile, r.Physical, r.EV, r.ByteAt50, r.Read4k50, r.SeqMBs)
	}
}
