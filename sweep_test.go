package ekbloom

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"
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
// exchange for its bytes. It writes evidence/sweep.json.
func TestProfileSweep(t *testing.T) {
	const n = 1 << 20 // 1 MiB
	src := sweepSkewed(n)

	type row struct {
		Profile  string  `json:"profile"`
		Machine  uint32  `json:"machine_id"`
		Physical uint64  `json:"physical_bytes"`
		EV       float64 `json:"E_V"`
		ByteAt50 int64   `json:"byte_at_p50_ns"`
		ByteAt99 int64   `json:"byte_at_p99_ns"`
		Read4k50 int64   `json:"read4k_p50_ns"`
		SeqMBs   float64 `json:"seq_mb_per_s"`
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

		// Single-byte reads (warm the path first).
		_, _ = r.ByteAt(0)
		ba := make([]time.Duration, 4096)
		seed := uint64(0xC0FFEE)
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

		// Bulk sequential throughput.
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

		rows = append(rows, row{
			Profile: sp.name, Machine: sp.machine,
			Physical: r.PhysicalBytes(), EV: float64(r.PhysicalBytes()) / float64(n),
			ByteAt50: ba[len(ba)/2].Nanoseconds(),
			ByteAt99: ba[len(ba)*99/100].Nanoseconds(),
			Read4k50: r4[len(r4)/2].Nanoseconds(),
			SeqMBs:   best,
		})
	}

	report := struct {
		Bytes  int    `json:"logical_bytes"`
		Source string `json:"source"`
		Rows   []row  `json:"rows"`
		Note   string `json:"note"`
	}{n, "sweepSkewed(1 MiB) — 4-symbol 60/25/10/5", rows,
		"E_V = physical_bytes / N on a 4-symbol 60/25/10/5 skewed source. Single host, scalar Go; a decision aid, not a portable benchmark."}

	out, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile("evidence/sweep.json", append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		t.Logf("%-12s phys=%8d E_V=%.3f 1B p50=%4dns 4K p50=%5dns seq=%.0f MB/s",
			r.Profile, r.Physical, r.EV, r.ByteAt50, r.Read4k50, r.SeqMBs)
	}
}
