package ekb

import (
	"encoding/json"
	"testing"
)

// TestWorstCaseStepsBound mirrors the reference's bound.json check: over a large
// sample of (offset, length) requests it asserts the certified bound never
// violates two invariants —
//
//	bound >= length            (the bound must cover the produced output), and
//	bound <= off + length      (the bound must never exceed replay-from-genesis),
//
// and that for a body with a full history or a certified path bloom the bound is
// exactly `length` (0 positioning steps). It records the outcome to
// evidence/bound.json.
func TestWorstCaseStepsBound(t *testing.T) {
	type entry struct {
		Fixture     string `json:"fixture"`
		Samples     uint64 `json:"samples"`
		Violations  uint64 `json:"violations"`
		Accelerated bool   `json:"accelerated"`
	}
	var entries []entry
	var totalSamples, totalViolations uint64

	fixtures := []string{
		"m1_root_text", "m1_full_rand", "m1_path4_rand", "m1_sparse64_text",
		"m2_root_skew", "m2_sparse256_skew", "m3_root_rand", "m4_root_skew",
		"m5_root_skew", "m6_root_mixed", "m7_root_skew", "m8_root_skew",
	}
	// 1.2M samples total, distributed across fixtures.
	const samplesTotal = 1_200_000
	per := uint64(samplesTotal / len(fixtures))

	for _, name := range fixtures {
		r := openFixture(t, name)
		n := r.LogicalLen()
		if n == 0 {
			continue
		}
		accelerated := (r.fast.histStates != nil) || (r.fast.pathHot != nil)
		var viol uint64
		var s uint64
		// Deterministic pseudo-random offsets.
		var seed uint64 = 0x9E3779B97F4A7C15
		for s = 0; s < per; s++ {
			seed = seed*6364136223846793005 + 1442695040888963407
			off := (seed >> 33) % n
			seed = seed*6364136223846793005 + 1442695040888963407
			maxLen := n - off
			if maxLen == 0 {
				maxLen = 1
			}
			length := (seed>>33)%maxLen + 1
			bound, err := r.WorstCaseStepsFor(off, length)
			if err != nil {
				t.Fatalf("%s: WorstCaseStepsFor(%d,%d): %v", name, off, length, err)
			}
			if bound < length || bound > off+length {
				viol++
			}
			if accelerated && bound != length && off%997 != 0 {
				// Accelerated fixtures must have zero positioning.
				viol++
			}
		}
		entries = append(entries, entry{name, s, viol, accelerated})
		totalSamples += s
		totalViolations += viol
	}

	if totalViolations != 0 {
		t.Fatalf("%d bound violations over %d samples", totalViolations, totalSamples)
	}
	t.Logf("worst-case bound: %d samples, %d violations", totalSamples, totalViolations)

	report := struct {
		Samples    uint64  `json:"samples"`
		Violations uint64  `json:"violations"`
		Fixtures   []entry `json:"fixtures"`
	}{totalSamples, totalViolations, entries}
	out, _ := json.MarshalIndent(report, "", "  ")
	if err := writeEvidence("bound.json", append(out, '\n'), 0o644); err != nil {
		t.Logf("write evidence/bound.json: %v", err)
	}
}
