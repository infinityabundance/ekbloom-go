// Command soak is a long-running randomized round-trip soak harness for the
// EKBLOOM Go implementation.
//
// Each iteration builds a random object — a random machine id (1..9), a random
// manifestation profile (root / full / sparse(K) / path(D)), a random size
// (1 B .. 4 MiB) and random but varied content (incompressible, skewed,
// repetitive or text-like) — then round-trips it through the public API:
//
//	WriteBody -> Open -> ReadAt(0,N) -> ByteAt(random) -> byte-exact assert
//
// Occasionally it also exercises AppendGeneration + TruncateToValid and the
// machine-9 shared-dictionary path (TrainDictionary / EncodeRecord /
// NewDictDecoder().DecodeInto). It accumulates a cumulative logical-symbol
// counter and per-run totals only — it keeps no per-iteration data, so its
// resident memory stays bounded no matter how many symbols flow through it.
//
// It samples runtime.ReadMemStats periodically and records the peak HeapAlloc
// and HeapSys, plus runtime.NumGC() and the cumulative PauseTotalNs, so a run
// proves that memory stayed bounded (it must survive the 2 GiB sandbox cap).
//
// It stops when SOAK_SYMBOLS (default 1e9) or SOAK_SECONDS (default 120) is
// reached, whichever comes first. On any mismatch it prints the details and
// exits nonzero.
//
// The JSON report is written to evidence/soak.json and echoed on stdout; a
// progress line goes to stderr roughly every 10 seconds.
//
//	sh scripts/soak.sh
//	SOAK_SECONDS=86400 sh scripts/soak.sh
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	ekbloom "github.com/infinityabundance/ekbloom-go"
)

const (
	evidencePath   = "evidence/soak.json"
	maxObjectBits  = 22 // 1 << 22 == 4 MiB
	defaultSymbols = int64(1_000_000_000)
	defaultSeconds = int64(120)
)

// sparseIntervals and pathDepths are the values the writer accepts for the
// sparse and path profiles (see internal/ekb: supportedIntervals / supportedDepths).
var (
	sparseIntervals = []uint32{1, 4, 16, 64, 256, 1024, 4096, 65536}
	pathDepths      = []uint16{1, 2, 4, 8, 16, 32}
)

// report is the evidence/soak.json payload. Field order is the wire order.
type report struct {
	TargetSymbols  int64   `json:"target_symbols"`
	Symbols        int64   `json:"symbols"`
	Iterations     int64   `json:"iterations"`
	Seconds        float64 `json:"seconds"`
	PeakHeapBytes  uint64  `json:"peak_heap_bytes"`
	HeapSysBytes   uint64  `json:"heap_sys_bytes"`
	GCRuns         uint32  `json:"gc_runs"`
	GCPauseTotalNs uint64  `json:"gc_pause_total_ns"`
	Mismatches     int64   `json:"mismatches"`
}

func main() {
	target := envInt64("SOAK_SYMBOLS", defaultSymbols)
	seconds := envInt64("SOAK_SECONDS", defaultSeconds)
	seed := envInt64("SOAK_SEED", 1)

	rng := rand.New(rand.NewSource(seed))
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	start := time.Now()
	lastReport := start

	fmt.Fprintf(os.Stderr, "soak: start target_symbols=%d seconds=%d seed=%d\n", target, seconds, seed)

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	var (
		symbols    int64
		iterations int64
		mismatches int64
		peakHeap   = ms.HeapAlloc
		heapSys    = ms.HeapSys
		failure    error
	)

	for {
		if symbols >= target {
			break
		}
		if !time.Now().Before(deadline) {
			break
		}

		iterations++
		delta, err := runIteration(rng)
		if err != nil {
			mismatches++
			failure = err
			break
		}
		symbols += delta

		// Sample memory periodically. ReadMemStats is a stop-the-world read, so
		// keep it off the hot path; every 16 iterations still catches the peak of
		// the large-object iterations.
		if iterations%16 == 0 {
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peakHeap {
				peakHeap = ms.HeapAlloc
			}
			if ms.HeapSys > heapSys {
				heapSys = ms.HeapSys
			}
		}

		if now := time.Now(); now.Sub(lastReport) >= 10*time.Second {
			fmt.Fprintf(os.Stderr, "soak: progress symbols=%d iterations=%d elapsed=%.0fs peak_heap=%dMiB\n",
				symbols, iterations, now.Sub(start).Seconds(), peakHeap>>20)
			lastReport = now
		}
	}

	// Final sample so the recorded peak/heap-sys include the tail of the run.
	runtime.ReadMemStats(&ms)
	if ms.HeapAlloc > peakHeap {
		peakHeap = ms.HeapAlloc
	}
	if ms.HeapSys > heapSys {
		heapSys = ms.HeapSys
	}

	rep := report{
		TargetSymbols:  target,
		Symbols:        symbols,
		Iterations:     iterations,
		Seconds:        time.Since(start).Seconds(),
		PeakHeapBytes:  peakHeap,
		HeapSysBytes:   heapSys,
		GCRuns:         ms.NumGC,
		GCPauseTotalNs: ms.PauseTotalNs,
		Mismatches:     mismatches,
	}

	out, err := json.Marshal(rep)
	if err != nil {
		fmt.Fprintf(os.Stderr, "soak: marshal report: %v\n", err)
		os.Exit(1)
	}
	out = append(out, '\n')

	if err := writeEvidence(out); err != nil {
		fmt.Fprintf(os.Stderr, "soak: write %s: %v\n", evidencePath, err)
		os.Exit(1)
	}
	// Also echo the report on stdout (scripts/soak.sh captures it to the same file).
	os.Stdout.Write(out)

	if failure != nil {
		fmt.Fprintf(os.Stderr, "soak: MISMATCH after %d symbols / %d iterations: %v\n", symbols, iterations, failure)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "soak: done symbols=%d iterations=%d seconds=%.1f peak_heap=%dMiB gc_runs=%d\n",
		symbols, iterations, rep.Seconds, peakHeap>>20, rep.GCRuns)
}

// runIteration builds one random object, round-trips it, and returns the number
// of logical symbols it verified. It retains nothing between calls.
func runIteration(rng *rand.Rand) (int64, error) {
	machineID := uint32(1 + rng.Intn(9))
	var profile ekbloom.Profile
	switch rng.Intn(4) {
	case 0:
		profile = ekbloom.Profile{Kind: ekbloom.ProfileRoot}
	case 1:
		profile = ekbloom.Profile{Kind: ekbloom.ProfileFull}
	case 2:
		// Sparse restart points are a machine-1 structure only.
		machineID = ekbloom.MachineNL2
		profile = ekbloom.Profile{Kind: ekbloom.ProfileSparse, Interval: sparseIntervals[rng.Intn(len(sparseIntervals))]}
	default:
		profile = ekbloom.Profile{Kind: ekbloom.ProfilePath, Depth: pathDepths[rng.Intn(len(pathDepths))]}
	}

	size := 1 << rng.Intn(maxObjectBits+1)
	src := makeContent(rng, size)

	image, err := ekbloom.WriteBody(machineID, src, profile)
	if err != nil {
		return 0, fmt.Errorf("WriteBody(machine=%d profile=%s size=%d): %w", machineID, label(profile), size, err)
	}

	reader, err := ekbloom.Open(image)
	if err != nil {
		return 0, fmt.Errorf("Open(machine=%d profile=%s size=%d): %w", machineID, label(profile), size, err)
	}

	n := reader.LogicalLen()
	if n != uint64(len(src)) {
		return 0, fmt.Errorf("logical len %d != source len %d (machine=%d profile=%s)", n, len(src), machineID, label(profile))
	}

	got, err := reader.ReadAt(0, n)
	if err != nil {
		return 0, fmt.Errorf("ReadAt(0,%d) (machine=%d profile=%s): %w", n, machineID, label(profile), err)
	}
	if !bytes.Equal(got, src) {
		return 0, fmt.Errorf("ReadAt mismatch (machine=%d profile=%s size=%d) at first diff %d", machineID, label(profile), size, firstDiff(got, src))
	}

	off := uint64(rng.Int63n(int64(n)))
	b, err := reader.ByteAt(off)
	if err != nil {
		return 0, fmt.Errorf("ByteAt(%d) (machine=%d profile=%s): %w", off, machineID, label(profile), err)
	}
	if b != src[off] {
		return 0, fmt.Errorf("ByteAt mismatch at %d: got %#x want %#x (machine=%d profile=%s)", off, b, src[off], machineID, label(profile))
	}

	symbols := int64(n)

	// Occasionally exercise an additive generation and a torn-tail truncation.
	if rng.Intn(8) == 0 {
		appProfile := appendProfile(rng, machineID)
		image2, gen, err := ekbloom.AppendGeneration(image, machineID, src, appProfile)
		if err != nil {
			return symbols, fmt.Errorf("AppendGeneration(machine=%d profile=%s size=%d): %w", machineID, label(appProfile), size, err)
		}
		r2, err := ekbloom.Open(image2)
		if err != nil {
			return symbols, fmt.Errorf("Open(appended gen=%d machine=%d profile=%s): %w", gen, machineID, label(appProfile), err)
		}
		got2, err := r2.ReadAt(0, n)
		if err != nil || !bytes.Equal(got2, src) {
			return symbols, fmt.Errorf("appended ReadAt mismatch (gen=%d machine=%d profile=%s size=%d err=%v)", gen, machineID, label(appProfile), size, err)
		}
		symbols += int64(n)

		if rng.Intn(2) == 0 {
			trunc, tgen, err := ekbloom.TruncateToValid(image2)
			if err != nil {
				return symbols, fmt.Errorf("TruncateToValid(gen=%d): %w", gen, err)
			}
			if tgen != gen {
				return symbols, fmt.Errorf("TruncateToValid generation %d != appended %d", tgen, gen)
			}
			r3, err := ekbloom.Open(trunc)
			if err != nil {
				return symbols, fmt.Errorf("Open(truncated gen=%d): %w", tgen, err)
			}
			got3, err := r3.ReadAt(0, n)
			if err != nil || !bytes.Equal(got3, src) {
				return symbols, fmt.Errorf("truncated ReadAt mismatch (gen=%d size=%d err=%v)", tgen, size, err)
			}
			symbols += int64(n)
		}
	}

	// Occasionally exercise the machine-9 shared-dictionary path.
	if rng.Intn(16) == 0 {
		dbeyond, err := dictRoundTrip(rng)
		if err != nil {
			return symbols, err
		}
		symbols += dbeyond
	}

	return symbols, nil
}

// dictRoundTrip trains a shared order-0 dictionary from a small random corpus,
// encodes one record against it and decodes it back with a reusable DictDecoder.
func dictRoundTrip(rng *rand.Rand) (int64, error) {
	count := 8 + rng.Intn(24)
	records := make([][]byte, count)
	for i := range records {
		records[i] = textContent(rng, 16+rng.Intn(240))
	}
	dict, err := ekbloom.TrainDictionary(records)
	if err != nil {
		return 0, fmt.Errorf("TrainDictionary(%d records): %w", count, err)
	}
	rec := records[rng.Intn(len(records))]
	core, err := dict.EncodeRecord(rec)
	if err != nil {
		return 0, fmt.Errorf("EncodeRecord(%d B): %w", len(rec), err)
	}
	dec, err := ekbloom.NewDictDecoder(dict)
	if err != nil {
		return 0, fmt.Errorf("NewDictDecoder: %w", err)
	}
	dst := make([]byte, len(rec))
	n, err := dec.DecodeInto(dst, core)
	if err != nil {
		return 0, fmt.Errorf("DecodeInto(%d B): %w", len(rec), err)
	}
	if n != len(rec) || !bytes.Equal(dst[:n], rec) {
		return 0, fmt.Errorf("dict round-trip mismatch: n=%d want=%d", n, len(rec))
	}
	return int64(n), nil
}

// appendProfile picks a valid profile for AppendGeneration (root/full, or sparse
// when the machine is 1).
func appendProfile(rng *rand.Rand, machineID uint32) ekbloom.Profile {
	switch rng.Intn(3) {
	case 0:
		return ekbloom.Profile{Kind: ekbloom.ProfileRoot}
	case 1:
		return ekbloom.Profile{Kind: ekbloom.ProfileFull}
	default:
		if machineID == ekbloom.MachineNL2 {
			return ekbloom.Profile{Kind: ekbloom.ProfileSparse, Interval: sparseIntervals[rng.Intn(len(sparseIntervals))]}
		}
		return ekbloom.Profile{Kind: ekbloom.ProfileFull}
	}
}

// makeContent returns n bytes of varied content: incompressible, skewed,
// repetitive or text-like.
func makeContent(rng *rand.Rand, n int) []byte {
	switch rng.Intn(4) {
	case 0:
		b := make([]byte, n)
		rng.Read(b)
		return b
	case 1:
		return skewContent(rng, n)
	case 2:
		return repetitiveContent(rng, n)
	default:
		return textContent(rng, n)
	}
}

func skewContent(rng *rand.Rand, n int) []byte {
	alpha := []byte("abcd")
	out := make([]byte, n)
	for i := range out {
		r := rng.Intn(100)
		var idx int
		switch {
		case r < 60:
			idx = 0
		case r < 85:
			idx = 1
		case r < 95:
			idx = 2
		default:
			idx = 3
		}
		out[i] = alpha[idx]
	}
	return out
}

func repetitiveContent(rng *rand.Rand, n int) []byte {
	plen := 1 + rng.Intn(32)
	pattern := make([]byte, plen)
	for i := range pattern {
		pattern[i] = byte(rng.Intn(256))
	}
	if rng.Intn(2) == 0 {
		v := pattern[0]
		for i := range pattern {
			pattern[i] = v
		}
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = pattern[i%plen]
	}
	return out
}

var textTokens = []string{
	"id=", "user", "alice", "bob", "kind=", "GET", "PUT", "DELETE",
	"region=", "us-east-1", "eu-west-1", "ap-south-1", "score=",
	"status=", "active", "idle", "|", " ", "\n", "0", "1", "2", "3",
}

func textContent(rng *rand.Rand, n int) []byte {
	out := make([]byte, 0, n+16)
	for len(out) < n {
		out = append(out, textTokens[rng.Intn(len(textTokens))]...)
	}
	return out[:n]
}

func label(p ekbloom.Profile) string {
	switch p.Kind {
	case ekbloom.ProfileRoot:
		return "root"
	case ekbloom.ProfileFull:
		return "full"
	case ekbloom.ProfileSparse:
		return "sparse(K=" + strconv.FormatUint(uint64(p.Interval), 10) + ")"
	case ekbloom.ProfilePath:
		return "path(D=" + strconv.FormatUint(uint64(p.Depth), 10) + ")"
	}
	return "kind(" + strconv.Itoa(int(p.Kind)) + ")"
}

func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func writeEvidence(data []byte) error {
	if dir := filepath.Dir(evidencePath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(evidencePath, data, 0o644)
}
