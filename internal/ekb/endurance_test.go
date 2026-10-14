package ekb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEndurance is a long-run writer endurance / fault-injection log for the
// append-only generation path, richer than TestAppendChaos.
//
// Each round appends a generation to a live chain, then damages it with one of a
// taxonomy of faults chosen deterministically from a seed. The faults model the
// real ways a write can fail on a log-structured store:
//
//	trunc_body           truncate at a random offset inside the newest generation
//	                     (ENOSPC/EIO mid-write)
//	trunc_manifest       truncate inside the newest manifest section (ENOSPC
//	                     mid-manifest)
//	trunc_footer         truncate at/inside the commit footer (torn commit)
//	flip_body            flip a bit in the newest non-manifest section payload
//	                     (in-place EIO)
//	flip_section_header  flip a bit in the newest section header (Offset/Length)
//	flip_manifest        flip a bit in the newest manifest payload
//	flip_footer          flip a bit in the newest commit footer
//	old_struct_flip      flip a bit in an OLDER generation's manifest (the chain
//	                     must fall back to the newest generation *before* it, never
//	                     a stale one)
//	old_benign_flip      flip a bit in an OLDER generation's reserved section-header
//	                     Flags field (unvalidated) — the NEWER valid generation must
//	                     still be selected
//	truncate_head        truncate inside the genesis header/core (power loss before
//	                     the first durable block) — the whole image is unrecoverable
//	                     and must be a typed error
//
// Invariants every round: Open either (a) returns exactly the newest surviving
// valid generation (its generation number, its accumulated manifest profile stack,
// and the exact object bytes) or (b) rejects with a typed *ekbloom.Error. Any other
// outcome — a stale/wrong generation, a wrong payload, a non-typed error or a panic
// — is counted as garbage and must be 0. After damage the chain is repaired with
// TruncateToValid and the write continues, so the next round appends on top of the
// recovered prefix.
//
// Note on payload identity: EKBLOOM's AppendGeneration manifests *additional
// structures for the object already stored* — the core is shared and immutable
// across a chain — so the logical bytes are identical in every generation of one
// image. The generation that survives is therefore identified by its generation
// number and its accumulated manifest profile stack (which grows by one entry per
// append), and the harness asserts both, so a stale generation cannot pass. The
// object payload is distinct per chain (derived from the chain/round number) and a
// chain is restarted periodically, so payloads differ across the run too.
//
// It writes evidence/endurance.json and evidence/endurance.log. The round count is
// configurable via EKB_ENDURANCE_ROUNDS (default 2000, or 200 under -short); the
// seed via EKB_ENDURANCE_SEED. A longer run is driven by scripts/endurance.sh.
func TestEndurance(t *testing.T) {
	rounds := enduranceEnvInt("EKB_ENDURANCE_ROUNDS", enduranceDefaultRounds)
	if testing.Short() && os.Getenv("EKB_ENDURANCE_ROUNDS") == "" {
		rounds = enduranceShortRounds
	}
	seed := uint64(enduranceEnvInt("EKB_ENDURANCE_SEED", 1)) ^ 0x9E3779B97F4A7C15

	rng := newEndurRNG(seed)
	kindRNG := newEndurRNG(seed ^ 0xA5A5A5A5A5A5A5A5)
	var log strings.Builder
	fmt.Fprintf(&log, "endurance: start rounds=%d seed=%#x chain_cap=%d src_len=%d\n",
		rounds, seed, enduranceChainCap, enduranceSrcLen)

	rep := enduranceReport{Rounds: rounds, Kinds: map[string]*enduranceKindStat{}}
	for _, k := range enduranceKindNames {
		rep.Kinds[k] = &enduranceKindStat{}
	}

	start := time.Now()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	peakHeap, heapSys := ms.HeapAlloc, ms.HeapSys

	logStride := 1
	if rounds > enduranceLogRoundsMax {
		logStride = rounds / enduranceLogRoundsMax
	}

	var (
		image        []byte
		gens         []enduranceGen
		src          []byte
		coreEnd      uint64
		chain        int
		needNewChain = true
		err          error
	)

	for round := 0; round < rounds; round++ {
		if needNewChain || len(gens) >= enduranceChainCap {
			src = endurancePayload(chain, enduranceSrcLen)
			image, err = WriteBody(1, src, rootProfile())
			if err != nil {
				t.Fatalf("round %d: WriteBody: %v", round, err)
			}
			gens, coreEnd, err = enduranceCollect(image)
			if err != nil {
				t.Fatalf("round %d: collect fresh chain: %v", round, err)
			}
			// Seed the chain with a few generations so that deep-history faults (damage
			// to an older generation while a newer valid one survives) are exercised: a
			// structural round drops the newest generation, so without a seeded depth
			// the chain would hover at 2 and never have an "older" generation to hurt.
			for seedGen := 0; seedGen < enduranceChainMin; seedGen++ {
				interval := enduranceIntervals[int(rng.below(uint64(len(enduranceIntervals))))]
				image, _, err = AppendGeneration(image, 1, src, sparseProfile(interval))
				if err != nil {
					t.Fatalf("round %d: seed append: %v", round, err)
				}
				rep.Generations++
				gens, coreEnd, err = enduranceCollect(image)
				if err != nil {
					t.Fatalf("round %d: collect seeded chain: %v", round, err)
				}
			}
			fmt.Fprintf(&log, "chain %d: payload_len=%d initial_gens=%d\n", chain, len(src), len(gens))
			chain++
			rep.Chains++
			needNewChain = false
		}

		prof := sparseProfile(enduranceIntervals[int(rng.below(uint64(len(enduranceIntervals))))])
		image, _, err = AppendGeneration(image, 1, src, prof)
		if err != nil {
			t.Fatalf("round %d: AppendGeneration: %v", round, err)
		}
		rep.Generations++
		gens, coreEnd, err = enduranceCollect(image)
		if err != nil {
			t.Fatalf("round %d: collect after append: %v", round, err)
		}
		n := len(gens)
		if n < 2 {
			t.Fatalf("round %d: expected >= 2 generations, got %d", round, n)
		}

		clean := image
		cleanEnds := make([]uint64, n)
		for i := range gens {
			cleanEnds[i] = gens[i].end
		}

		kind := enduranceKindNames[int(kindRNG.below(uint64(len(enduranceKindNames))))]
		// old_struct_flip needs a genuinely older (non-genesis, non-newest) generation
		// so that a valid generation survives the damage; on a short chain fall back to
		// a newest-generation fault instead.
		if kind == "old_struct_flip" && n < 3 {
			kind = enduranceKindNames[int(kindRNG.below(7))]
		}
		st := rep.Kinds[kind]
		st.Rounds++

		damage := enduranceApplyDamage(kind, clean, gens, coreEnd, rng)
		line := fmt.Sprintf("round=%d kind=%s gens=%d expected_gen=%d %s", round, kind, n, damage.expect, damage.note)

		if damage.typed {
			r, oerr, panicked := enduranceTryOpen(damage.image)
			switch {
			case panicked:
				rep.Garbage++
				t.Errorf("%s: Open panicked: %v", line, oerr)
			case oerr == nil:
				rep.Garbage++
				t.Errorf("%s: expected typed rejection, got reader gen=%d", line, r.Generation())
			default:
				var e *Error
				if errors.As(oerr, &e) {
					rep.RejectedTyped++
					st.RejectedTyped++
				} else {
					rep.Garbage++
					t.Errorf("%s: non-typed error %v", line, oerr)
				}
			}
			if _, _, rerr := TruncateToValid(damage.image); rerr == nil {
				rep.Garbage++
				t.Errorf("%s: TruncateToValid unexpectedly succeeded on an unrecoverable image", line)
			}
			needNewChain = true
			line += " outcome=rejected_typed"
		} else {
			exp := damage.expect
			r, oerr, panicked := enduranceTryOpen(damage.image)
			switch {
			case panicked:
				rep.Garbage++
				t.Errorf("%s: Open panicked: %v", line, oerr)
				line += " outcome=PANIC"
			case oerr != nil:
				rep.Garbage++
				t.Errorf("%s: expected newest valid gen=%d, got error %v", line, exp, oerr)
				line += " outcome=garbage"
				needNewChain = true
			default:
				ok := true
				if r.Generation() != exp {
					ok = false
					t.Errorf("%s: Open returned generation %d, want %d", line, r.Generation(), exp)
				}
				if !enduranceProfilesEqual(r.Manifest().Profiles, gens[exp-1].profiles) {
					ok = false
					t.Errorf("%s: manifest profile stack %v, want %v", line, r.Manifest().Profiles, gens[exp-1].profiles)
				}
				got, rerr := r.ReadAt(0, uint64(len(src)))
				if rerr != nil || !bytes.Equal(got, src) {
					ok = false
					t.Errorf("%s: recovered payload mismatch (err=%v)", line, rerr)
				}
				if ok {
					rep.RecoveredExact++
					st.RecoveredExact++
					line += " outcome=recovered_exact"
				} else {
					rep.Garbage++
					line += " outcome=garbage"
				}
				if exp < uint64(n) {
					st.DroppedNewer++
				} else {
					st.KeptNewest++
				}
			}

			// Repair to the newest fully-valid generation and continue the chain.
			fixed, fgen, rerr := TruncateToValid(damage.image)
			if rerr != nil {
				rep.Garbage++
				t.Errorf("%s: TruncateToValid: %v", line, rerr)
				needNewChain = true
			} else {
				if fgen != exp {
					rep.Garbage++
					t.Errorf("%s: repaired generation %d, want %d", line, fgen, exp)
				}
				if len(fixed) != int(cleanEnds[exp-1]) {
					rep.Garbage++
					t.Errorf("%s: repaired length %d, want %d", line, len(fixed), cleanEnds[exp-1])
				}
				if !damage.benign && !bytes.Equal(fixed, clean[:cleanEnds[exp-1]]) {
					rep.Garbage++
					t.Errorf("%s: repaired prefix is not byte-identical to the clean generation %d", line, exp)
				}
				image = fixed
				gens = gens[:exp]
			}
		}

		if round%logStride == 0 || damage.typed || rep.Garbage > 0 {
			log.WriteString(line)
			log.WriteByte('\n')
		}

		if round%32 == 0 {
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peakHeap {
				peakHeap = ms.HeapAlloc
			}
			if ms.HeapSys > heapSys {
				heapSys = ms.HeapSys
			}
		}
	}

	runtime.ReadMemStats(&ms)
	if ms.HeapAlloc > peakHeap {
		peakHeap = ms.HeapAlloc
	}
	if ms.HeapSys > heapSys {
		heapSys = ms.HeapSys
	}

	// Concurrent-reader phase: readers read a live image while a writer appends and
	// damages it. Published images are always fully valid, so a torn read is a bug.
	concurrent := enduranceConcurrent(t, endurancePayload(0, enduranceSrcLen))
	rep.Concurrent = concurrent

	rep.PeakHeapBytes = peakHeap
	rep.HeapSysBytes = heapSys
	rep.GCRuns = ms.NumGC
	rep.DurationSeconds = time.Since(start).Seconds()

	fmt.Fprintf(&log, "endurance: done rounds=%d chains=%d generations=%d recovered_exact=%d rejected_typed=%d garbage=%d peak_heap=%dMiB concurrent(readers=%d reads=%d torn=%d errors=%d)\n",
		rep.Rounds, rep.Chains, rep.Generations, rep.RecoveredExact, rep.RejectedTyped, rep.Garbage, peakHeap>>20,
		concurrent.Readers, concurrent.Reads, concurrent.TornReads, concurrent.Errors)

	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')
	if err := writeEvidence("endurance.json", out, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeEvidence("endurance.log", []byte(log.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	if rep.Garbage != 0 {
		t.Fatalf("endurance: %d garbage rounds (must be 0)", rep.Garbage)
	}
	if concurrent.TornReads != 0 || concurrent.Errors != 0 {
		t.Fatalf("endurance: concurrent readers torn=%d errors=%d", concurrent.TornReads, concurrent.Errors)
	}
	t.Logf("endurance: %d rounds, %d chains, %d generations; recovered_exact=%d rejected_typed=%d garbage=%d; peak_heap=%dMiB",
		rep.Rounds, rep.Chains, rep.Generations, rep.RecoveredExact, rep.RejectedTyped, rep.Garbage, peakHeap>>20)
	t.Logf("endurance: concurrent readers=%d reads=%d appends=%d torn=%d errors=%d",
		concurrent.Readers, concurrent.Reads, concurrent.Appends, concurrent.TornReads, concurrent.Errors)
}

const (
	enduranceDefaultRounds    = 2000
	enduranceShortRounds      = 200
	enduranceSrcLen           = 1536
	enduranceChainCap         = 12
	enduranceChainMin         = 4
	enduranceReaders          = 8
	enduranceLogRoundsMax     = 5000
	enduranceConcurrentBudget = 400 * time.Millisecond
)

var enduranceIntervals = []uint32{64, 256, 1024, 4096}

var enduranceKindNames = []string{
	"trunc_body",
	"trunc_manifest",
	"trunc_footer",
	"flip_body",
	"flip_section_header",
	"flip_manifest",
	"flip_footer",
	"old_struct_flip",
	"old_benign_flip",
	"truncate_head",
}

// enduranceKindStat is the per-kind breakdown in evidence/endurance.json.
type enduranceKindStat struct {
	Rounds         int `json:"rounds"`
	RecoveredExact int `json:"recovered_exact"`
	RejectedTyped  int `json:"rejected_typed"`
	DroppedNewer   int `json:"dropped_newer_generation"`
	KeptNewest     int `json:"kept_newest_generation"`
}

// enduranceConcurrentReport is the concurrent-reader phase summary.
type enduranceConcurrentReport struct {
	Readers   int `json:"readers"`
	Reads     int `json:"reads"`
	Appends   int `json:"appends"`
	TornReads int `json:"torn_reads"`
	Errors    int `json:"errors"`
}

// enduranceReport is the evidence/endurance.json payload.
type enduranceReport struct {
	Rounds          int                           `json:"rounds"`
	Chains          int                           `json:"chains"`
	Generations     int                           `json:"generations_appended"`
	RecoveredExact  int                           `json:"recovered_exact"`
	RejectedTyped   int                           `json:"rejected_typed"`
	Garbage         int                           `json:"garbage"`
	Kinds           map[string]*enduranceKindStat `json:"kinds"`
	PeakHeapBytes   uint64                        `json:"peak_heap_bytes"`
	HeapSysBytes    uint64                        `json:"heap_sys_bytes"`
	GCRuns          uint32                        `json:"gc_runs"`
	DurationSeconds float64                       `json:"duration_seconds"`
	Concurrent      enduranceConcurrentReport     `json:"concurrent_readers"`
}

// enduranceGen is the decoded identity of one generation in a chain.
type enduranceGen struct {
	number      uint64
	start       uint64
	end         uint64
	footerStart uint64
	sections    []ScannedSection
	body        []ScannedSection // non-manifest, non-auth sections
	manifest    ScannedSection
	profiles    []string // accumulated manifest profile stack
}

// endurRNG is a small deterministic PRNG so every run with the same seed picks the
// same fault sequence.
type endurRNG struct{ s uint64 }

func newEndurRNG(seed uint64) *endurRNG { return &endurRNG{s: seed} }

func (r *endurRNG) next() uint64 {
	r.s = r.s*6364136223846793005 + 1442695040888963407
	return r.s
}

func (r *endurRNG) below(n uint64) uint64 {
	if n == 0 {
		return 0
	}
	return (r.next() >> 33) % n
}

// enduranceDamage is the outcome of applying one fault.
type enduranceDamage struct {
	image  []byte
	expect uint64 // expected newest-valid generation (0 when typed is true)
	typed  bool   // the whole image is unrecoverable -> typed rejection expected
	benign bool   // the damage is invisible to the format (unvalidated bytes)
	note   string
}

// enduranceApplyDamage damages a clean image in one of the taxonomy's ways. It
// never copies unless the fault is a bit flip (so truncations stay cheap).
func enduranceApplyDamage(kind string, image []byte, gens []enduranceGen, coreEnd uint64, rng *endurRNG) enduranceDamage {
	n := len(gens)
	newest := gens[n-1]
	clone := func() []byte { return append([]byte(nil), image...) }
	flip := func(b []byte, off uint64) { b[off] ^= byte(1) << rng.below(8) }

	switch kind {
	case "trunc_body":
		off := newest.start + rng.below(newest.end-newest.start)
		return enduranceDamage{image: image[:off], expect: uint64(n - 1),
			note: fmt.Sprintf("truncate@%d in newest gen [%d,%d)", off, newest.start, newest.end)}
	case "trunc_manifest":
		m := newest.manifest
		off := m.PayloadOffset + rng.below(m.PayloadLen)
		return enduranceDamage{image: image[:off], expect: uint64(n - 1),
			note: fmt.Sprintf("truncate@%d in newest manifest", off)}
	case "trunc_footer":
		off := newest.footerStart + rng.below(newest.end-newest.footerStart)
		return enduranceDamage{image: image[:off], expect: uint64(n - 1),
			note: fmt.Sprintf("truncate@%d in newest footer", off)}
	case "flip_body":
		sec := enduranceBodySection(newest)
		b := clone()
		flip(b, sec.PayloadOffset+rng.below(sec.PayloadLen))
		return enduranceDamage{image: b, expect: uint64(n - 1),
			note: fmt.Sprintf("flip in newest %s payload", sec.Kind.Name())}
	case "flip_section_header":
		sec := newest.sections[0]
		b := clone()
		flip(b, sec.HeaderOffset+8+rng.below(16)) // Offset [8,16) or Length [16,24)
		return enduranceDamage{image: b, expect: uint64(n - 1),
			note: fmt.Sprintf("flip in newest %s header", sec.Kind.Name())}
	case "flip_manifest":
		m := newest.manifest
		b := clone()
		flip(b, m.PayloadOffset+rng.below(m.PayloadLen))
		return enduranceDamage{image: b, expect: uint64(n - 1), note: "flip in newest manifest payload"}
	case "flip_footer":
		b := clone()
		flip(b, newest.footerStart+rng.below(newest.end-newest.footerStart))
		return enduranceDamage{image: b, expect: uint64(n - 1), note: "flip in newest commit footer"}
	case "old_struct_flip":
		if n >= 3 {
			d := 2 + int(rng.below(uint64(n-2))) // 2..n-1
			g := gens[d-1]
			b := clone()
			flip(b, g.manifest.PayloadOffset+rng.below(g.manifest.PayloadLen))
			return enduranceDamage{image: b, expect: uint64(d - 1),
				note: fmt.Sprintf("structural flip in older gen %d manifest (gen %d also valid)", d, n)}
		}
		g := gens[0]
		b := clone()
		flip(b, g.manifest.PayloadOffset+rng.below(g.manifest.PayloadLen))
		return enduranceDamage{image: b, typed: true, note: "structural flip in gen 1 manifest (unrecoverable)"}
	case "old_benign_flip":
		d := 1 + int(rng.below(uint64(n-1))) // 1..n-1
		g := gens[d-1]
		sec := g.sections[0]
		b := clone()
		flip(b, sec.HeaderOffset+4+rng.below(4)) // Flags [4,8), unvalidated
		return enduranceDamage{image: b, expect: uint64(n), benign: true,
			note: fmt.Sprintf("benign flip in older gen %d %s header flags (gen %d must survive)", d, sec.Kind.Name(), n)}
	default: // truncate_head
		off := rng.below(coreEnd)
		return enduranceDamage{image: image[:off], typed: true,
			note: fmt.Sprintf("truncate@%d in genesis header/core", off)}
	}
}

func enduranceBodySection(g enduranceGen) ScannedSection {
	if len(g.body) > 0 {
		return g.body[0]
	}
	return g.manifest
}

// enduranceCollect decodes every generation of a valid image, in order.
func enduranceCollect(image []byte) ([]enduranceGen, uint64, error) {
	outcome, err := Scan(image)
	if err != nil {
		return nil, 0, err
	}
	gens := make([]enduranceGen, 0, len(outcome.Commits))
	for i := range outcome.Commits {
		c := outcome.Commits[i]
		rec, err := CommitManifest(image, &c)
		if err != nil {
			return nil, 0, err
		}
		g := enduranceGen{
			number:      c.Generation,
			start:       c.Start,
			end:         c.End,
			footerStart: c.End - footerLen,
			sections:    c.Sections,
			profiles:    rec.Profiles,
		}
		for _, s := range c.Sections {
			switch s.Kind {
			case SectionManifest:
				g.manifest = s
			case SectionAuth:
			default:
				g.body = append(g.body, s)
			}
		}
		gens = append(gens, g)
	}
	for i := range gens {
		if gens[i].number != uint64(i+1) {
			return nil, 0, fmt.Errorf("generation %d is number %d, want %d", i, gens[i].number, i+1)
		}
	}
	return gens, outcome.CoreEnd, nil
}

// enduranceTryOpen opens image, converting any panic into a returned error so a
// broken decoder is counted as garbage rather than crashing the harness.
func enduranceTryOpen(image []byte) (r *Reader, err error, panicked bool) {
	defer func() {
		if rec := recover(); rec != nil {
			panicked = true
			err = fmt.Errorf("panic: %v", rec)
		}
	}()
	r, err = Open(image)
	return
}

func enduranceProfilesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// endurancePayload derives a distinct object payload from the chain (round) number.
func endurancePayload(chain, n int) []byte {
	out := make([]byte, n)
	s := uint64(chain)*0x9E3779B97F4A7C15 + 0xD1B54A32D192ED03
	for i := range out {
		s = s*6364136223846793005 + 1442695040888963407
		out[i] = byte(s >> 33)
	}
	return out
}

func enduranceEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// enduranceConcurrent runs k goroutines that continuously Open + ReadAt/ByteAt the
// currently published image (one of them also walks the commit chain) while a
// single writer appends generations and, every few appends, tears the newest one
// and repairs it — publishing only fully-valid images. The readers run for
// enduranceConcurrentBudget so the writer genuinely overlaps them. A torn read of
// a previously-valid generation, a payload mismatch or any error fails the test;
// it is meant to run under -race.
func enduranceConcurrent(t *testing.T, src []byte) enduranceConcurrentReport {
	t.Helper()
	base, err := WriteBody(1, src, rootProfile())
	if err != nil {
		t.Fatal(err)
	}
	r0, err := Open(base)
	if err != nil {
		t.Fatal(err)
	}

	var cur atomic.Pointer[Reader]
	cur.Store(r0)

	stop := make(chan struct{})
	var wgWriter sync.WaitGroup
	var appends atomic.Int64
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
			next, _, err := AppendGeneration(image, 1, src, sparseProfile(64))
			if err != nil {
				t.Errorf("endurance concurrent: AppendGeneration: %v", err)
				return
			}
			// Every few appends, model a torn/partial write (ENOSPC/EIO) and repair
			// it to the newest valid prefix before publishing.
			if appends.Load()%3 == 0 && len(next) > len(base)+1 {
				cut := len(next) - 1 - int(appends.Load()%7)
				repaired, _, err := TruncateToValid(next[:cut])
				if err != nil {
					t.Errorf("endurance concurrent: TruncateToValid: %v", err)
					return
				}
				next = repaired
			}
			image = next
			nr, err := Open(image)
			if err != nil {
				t.Errorf("endurance concurrent: Open: %v", err)
				return
			}
			cur.Store(nr)
			appends.Add(1)
			if appends.Load()%32 == 0 {
				image = base // bound memory: restart the chain periodically
			}
		}
	}()

	var reads, torn, errs atomic.Int64
	start := make(chan struct{})
	var wgReaders sync.WaitGroup
	for g := 0; g < enduranceReaders; g++ {
		wgReaders.Add(1)
		go func(g int) {
			defer wgReaders.Done()
			<-start
			deadline := time.Now().Add(enduranceConcurrentBudget)
			seed := uint64(g)*2654435761 + 1
			for i := 0; time.Now().Before(deadline); i++ {
				r := cur.Load()
				seed = seed*6364136223846793005 + 1442695040888963407
				off := (seed >> 33) % r.LogicalLen()
				if g == enduranceReaders-1 && i%16 == 0 {
					// The iterating reader walks the published commit chain and
					// checks it is a dense 1..k sequence matching the newest gen.
					outcome, err := Scan(r.data)
					if err != nil || len(outcome.Commits) == 0 {
						errs.Add(1)
						t.Errorf("endurance concurrent: iterate Scan: %v", err)
						return
					}
					want := uint64(1)
					for _, c := range outcome.Commits {
						if c.Generation != want {
							torn.Add(1)
							t.Errorf("endurance concurrent: commit generation %d, want %d", c.Generation, want)
							return
						}
						want++
					}
					if want-1 != r.Generation() {
						torn.Add(1)
						t.Errorf("endurance concurrent: newest commit %d, reader generation %d", want-1, r.Generation())
						return
					}
				}
				b, err := r.ByteAt(off)
				if err != nil {
					errs.Add(1)
					t.Errorf("endurance concurrent: ByteAt(%d): %v", off, err)
					return
				}
				if b != src[off] {
					torn.Add(1)
					t.Errorf("endurance concurrent: ByteAt(%d) mismatch", off)
					return
				}
				if i%128 == 0 {
					got, err := r.ReadAt(0, uint64(len(src)))
					if err != nil || !bytes.Equal(got, src) {
						torn.Add(1)
						t.Errorf("endurance concurrent: ReadAt mismatch (err=%v)", err)
						return
					}
				}
				reads.Add(1)
			}
		}(g)
	}
	close(start)
	wgReaders.Wait()
	close(stop)
	wgWriter.Wait()

	return enduranceConcurrentReport{
		Readers:   enduranceReaders,
		Reads:     int(reads.Load()),
		Appends:   int(appends.Load()),
		TornReads: int(torn.Load()),
		Errors:    int(errs.Load()),
	}
}
