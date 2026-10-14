package ekb

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/infinityabundance/ekbloom-go/internal/tools/stat"
)

// TestByteAtPercentiles measures single-byte `ByteAt` latency per machine and
// writes evidence/byteat.json. The reported p50/p99/p99.9 are the median across
// R = EKB_RUNS runs (default 5; 3 in CI), with the observed min/max retained so a
// reviewer can see the run-to-run spread. Faster machines (path bloom, history,
// direct) are distinguished from the materialised fallback.
func TestByteAtPercentiles(t *testing.T) {
	fixtures := []string{
		"m1_root_text", "m1_full_rand", "m1_path4_rand", "m1_sparse64_text",
		"m2_root_skew", "m3_root_rand", "m4_root_skew", "m5_root_skew",
		"m6_root_mixed", "m7_root_skew", "m8_root_skew", "m9_root_small",
	}
	const samples = 200_000

	type row struct {
		Fixture string `json:"fixture"`
		Machine uint32 `json:"machine_id"`
		Logical uint64 `json:"logical_len"`
		P50ns   int64  `json:"p50_ns"`
		P99ns   int64  `json:"p99_ns"`
		P999ns  int64  `json:"p99_9_ns"`
		P50Min  int64  `json:"p50_ns_min"`
		P50Max  int64  `json:"p50_ns_max"`
		P99Min  int64  `json:"p99_ns_min"`
		P99Max  int64  `json:"p99_ns_max"`
		P999Min int64  `json:"p99_9_ns_min"`
		P999Max int64  `json:"p99_9_ns_max"`
		Surface string `json:"surface"`
	}
	var rows []row

	for _, name := range fixtures {
		r := openFixture(t, name)
		n := r.LogicalLen()
		if n == 0 {
			continue
		}
		surface := "materialized"
		switch {
		case r.fast.pathHot != nil:
			surface = "path-bloom"
		case r.fast.histStates != nil:
			surface = "history"
		case r.fast.checkpoints != nil:
			surface = "checkpoint"
		case r.directlyAddressable():
			surface = "direct"
		}

		// Each run samples `samples` deterministic pseudo-random offsets; the seed
		// is varied per run so the run-to-run spread also reflects different
		// offsets, not just scheduler noise.
		q := stat.Sample(func(run int) []time.Duration {
			durs := make([]time.Duration, samples)
			seed := uint64(0x1234_5678) + uint64(run)*0x9E37_79B9_7F4A_7C15
			for i := 0; i < samples; i++ {
				seed = seed*6364136223846793005 + 1442695040888963407
				off := (seed >> 33) % n
				t0 := time.Now()
				if _, err := r.ByteAt(off); err != nil {
					t.Fatalf("%s: ByteAt: %v", name, err)
				}
				durs[i] = time.Since(t0)
			}
			return durs
		}, 500, 990, 999)

		rows = append(rows, row{
			Fixture: name, Machine: r.MachineID(), Logical: n,
			P50ns: q[0].Median, P99ns: q[1].Median, P999ns: q[2].Median,
			P50Min: q[0].Min, P50Max: q[0].Max,
			P99Min: q[1].Min, P99Max: q[1].Max,
			P999Min: q[2].Min, P999Max: q[2].Max,
			Surface: surface,
		})
	}

	out, _ := json.MarshalIndent(struct {
		Samples   int    `json:"samples"`
		Runs      int    `json:"runs"`
		Statistic string `json:"statistic"`
		Rows      []row  `json:"rows"`
	}{samples, stat.Runs(), "median-of-runs", rows}, "", "  ")
	if err := writeTimingEvidence("byteat.json", append(out, '\n'), 0o644); err != nil {
		t.Logf("write byteat.json: %v", err)
	}
	for _, r := range rows {
		t.Logf("%-18s m%-2d %-12s p50=%dns(%d..%d) p99=%dns(%d..%d) p99.9=%dns(%d..%d)",
			r.Fixture, r.Machine, r.Surface,
			r.P50ns, r.P50Min, r.P50Max, r.P99ns, r.P99Min, r.P99Max, r.P999ns, r.P999Min, r.P999Max)
	}
}
