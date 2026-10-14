package ekb

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestAppendChaos runs many rounds of "append a generation, then damage the file
// in one of several ways (truncate, flip a payload bit, flip a section-header
// bit)" and asserts the reader always recovers a *valid* generation with the
// correct bytes — or rejects with a typed error — never garbage. It is a bounded
// endurance log for the crash-safety path, written to evidence/chaos.json.
//
// Damage is confined to the newest generation, so the valid prefix always
// survives; the chain is repaired with TruncateToValid between rounds.
func TestAppendChaos(t *testing.T) {
	src := sampleSource(1, 1024)

	type counts struct {
		Rounds         int `json:"rounds"`
		Truncations    int `json:"truncations"`
		PayloadFlips   int `json:"payload_flips"`
		HeaderFlips    int `json:"header_flips"`
		RecoveredExact int `json:"recovered_exact"`
		RejectedTyped  int `json:"rejected_typed"`
	}
	var c counts

	image, err := WriteBody(1, src, rootProfile())
	if err != nil {
		t.Fatal(err)
	}

	seed := uint64(0xDEADBEEF)
	next := func(n uint64) uint64 {
		seed = seed*6364136223846793005 + 1442695040888963407
		return (seed >> 33) % n
	}

	for round := 0; round < 400; round++ {
		before := len(image)
		image, _, err = AppendGeneration(image, 1, src, fullProfile())
		if err != nil {
			t.Fatalf("round %d: append: %v", round, err)
		}
		span := uint64(len(image) - before)
		if span == 0 {
			t.Fatalf("round %d: append produced no bytes", round)
		}
		c.Rounds++

		// Damage only the newest generation, one of three ways.
		switch round % 3 {
		case 0: // truncate inside the newest generation
			image = image[:before+int(next(span))]
			c.Truncations++
		case 1: // flip a bit in the newest generation's body
			image[before+int(next(span))] ^= 1 << (seed % 8)
			c.PayloadFlips++
		default: // flip a bit in the newest section header
			h := span
			if h > 40 {
				h = 40
			}
			image[before+int(next(h))] ^= 1 << (seed % 8)
			c.HeaderFlips++
		}

		// The reader must yield the correct bytes or a typed error — never garbage.
		if r, err := Open(image); err == nil {
			got, err := r.ReadAt(0, uint64(len(src)))
			if err != nil {
				t.Fatalf("round %d: read: %v", round, err)
			}
			if !bytes.Equal(got, src) {
				t.Fatalf("round %d: recovered bytes differ from the source", round)
			}
			c.RecoveredExact++
		} else {
			c.RejectedTyped++
		}

		// Repair to the newest fully-valid prefix and continue the chain.
		image, _, err = TruncateToValid(image)
		if err != nil {
			t.Fatalf("round %d: truncate: %v", round, err)
		}
		if len(image) == 0 {
			t.Fatalf("round %d: no valid generation survived", round)
		}
	}

	out, _ := json.MarshalIndent(c, "", "  ")
	if err := writeEvidence("chaos.json", append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("chaos: %d rounds (%d truncations, %d payload flips, %d header flips); %d recovered exact, %d typed-rejected",
		c.Rounds, c.Truncations, c.PayloadFlips, c.HeaderFlips, c.RecoveredExact, c.RejectedTyped)
}
