package ekb

import (
	"encoding/json"
	"sort"
	"testing"
	"time"
)

// TestByteAtPercentiles measures single-byte `ByteAt` latency per machine and
// writes evidence/byteat.json (p50/p99/p99.9). Faster machines (path bloom,
// history, direct) are distinguished from the materialised fallback.
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
		durs := make([]int64, samples)
		var seed uint64 = 0x1234_5678
		for i := 0; i < samples; i++ {
			seed = seed*6364136223846793005 + 1442695040888963407
			off := (seed >> 33) % n
			t0 := time.Now()
			if _, err := r.ByteAt(off); err != nil {
				t.Fatalf("%s: ByteAt: %v", name, err)
			}
			durs[i] = time.Since(t0).Nanoseconds()
		}
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		rows = append(rows, row{
			Fixture: name, Machine: r.MachineID(), Logical: n,
			P50ns: durs[len(durs)*50/100], P99ns: durs[len(durs)*99/100], P999ns: durs[len(durs)*999/1000],
			Surface: surface,
		})
	}

	out, _ := json.MarshalIndent(struct {
		Samples int   `json:"samples"`
		Rows    []row `json:"rows"`
	}{samples, rows}, "", "  ")
	if err := writeEvidence("byteat.json", append(out, '\n'), 0o644); err != nil {
		t.Logf("write byteat.json: %v", err)
	}
	for _, r := range rows {
		t.Logf("%-18s m%-2d %-12s p50=%dns p99=%dns p99.9=%dns", r.Fixture, r.Machine, r.Surface, r.P50ns, r.P99ns, r.P999ns)
	}
}
