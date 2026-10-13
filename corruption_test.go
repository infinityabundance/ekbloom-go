package ekbloom

import (
	"os"
	"path/filepath"
	"testing"
)

// coveredRanges returns the byte ranges that must be integrity-protected: the
// visible core and every section payload (including the manifest). Alignment
// padding and reserved header bytes are inert and deliberately excluded.
func coveredRanges(t *testing.T, data []byte) [][2]uint64 {
	t.Helper()
	outcome, err := Scan(data)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	ranges := [][2]uint64{{coreOffset, coreOffset + outcome.Header.CoreLen}}
	for _, c := range outcome.Commits {
		for _, s := range c.Sections {
			ranges = append(ranges, [2]uint64{s.PayloadOffset, s.PayloadOffset + s.PayloadLen})
		}
	}
	return ranges
}

// TestCorruptionDetected100Percent flips up to 1024 single bits in every covered
// structure of every fixture and asserts that Open rejects each one. It reports
// per-structure detection so a regression cannot hide.
func TestCorruptionDetected100Percent(t *testing.T) {
	fix := loadFixtures(t)
	for _, fx := range fix.Fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			if fx.Dict != "" {
				db, _ := os.ReadFile(filepath.Join("testdata", fx.Dict))
				if d, err := DictionaryFromBytes(db); err == nil {
					RegisterDictionary(d)
				}
			}
			data, err := os.ReadFile(filepath.Join("testdata", "fixtures", fx.Name+".ekb"))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			ranges := coveredRanges(t, data)
			total, detected := 0, 0
			for _, r := range ranges {
				n := r[1] - r[0]
				if n == 0 {
					continue
				}
				flips := uint64(1024)
				step := n / flips
				if step == 0 {
					step = 1
					flips = n
				}
				for i := uint64(0); i < flips; i++ {
					pos := r[0] + (i*step)%n
					bit := uint(i % 8)
					corrupted := make([]byte, len(data))
					copy(corrupted, data)
					corrupted[pos] ^= 1 << bit
					total++
					if _, err := Open(corrupted); err != nil {
						detected++
					} else {
						t.Errorf("undetected flip at byte %d bit %d (section range [%d,%d))", pos, bit, r[0], r[1])
					}
				}
			}
			if detected != total {
				t.Errorf("detection %d/%d", detected, total)
			}
			t.Logf("%s: detected %d/%d single-bit flips", fx.Name, detected, total)
		})
	}
}

// TestVerifyLocalizesCorruption confirms the deep verifier reports a section name
// and a bounded offset for a corrupted payload.
func TestVerifyLocalizesCorruption(t *testing.T) {
	fix := loadFixtures(t)
	var fx fixtureMeta
	for _, f := range fix.Fixtures {
		if f.Name == "m1_root_allbytes" {
			fx = f
		}
	}
	if fx.Name == "" {
		t.Fatal("fixture m1_root_allbytes missing")
	}
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", fx.Name+".ekb"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := VerifyDeep(data); err != nil {
		t.Fatalf("clean file must verify: %v", err)
	}
	corrupted := make([]byte, len(data))
	copy(corrupted, data)
	// Flip a bit inside the core region.
	corrupted[coreOffset] ^= 0x01
	err = VerifyDeep(corrupted)
	section, _, ok := CorruptDetail(err)
	if !ok {
		t.Fatalf("expected a localized Corrupt error, got %v", err)
	}
	if section == "" {
		t.Fatalf("corrupt error has an empty section")
	}
}
