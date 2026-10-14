// Command profstats measures garbage-collection pauses and heap high-water marks
// for a representative EKBLOOM workload, and consolidates the pprof CPU/heap "top"
// tables emitted by scripts/profile.sh into evidence/profile.json.
//
// The workload is soak-style: it round-trips randomized objects through the public
// API (machines 1..9; root / full / sparse(K) / path(D) profiles; 1 B .. 1 MiB
// sources) as WriteBody -> Open -> ReadAt -> ByteAt, and periodically decodes
// machine-9 shared-dictionary records with a single reusable DictDecoder. It keeps
// no per-iteration data, so its resident set stays bounded well under the sandbox's
// 2 GiB cap.
//
// While it runs it samples runtime.MemStats (peak HeapAlloc / HeapInuse and the
// retained, end-of-run heap) and, at the end, the runtime/metrics
// "/gc/pauses:seconds" histogram, from which it derives the GC pause count, total
// and p50/p99/p99.9/max quantiles (bucket midpoints). It then parses the pprof
// text tables (<name>.top, <name>.alloc.top, <name>.inuse.top) written by
// scripts/profile.sh into per-hot-path flat CPU shares, allocation sites and
// retained (inuse) bytes, and writes:
//
//	evidence/profile/gcstats.json   the GC / heap measurement alone
//	evidence/profile.json           the consolidated report (GC + all hot paths)
//
// Usage:
//
//	go run ./internal/tools/profstats \
//	    --dir evidence/profile --out evidence/profile.json \
//	    --path m1-encode=BenchmarkMachineEncode/m1 \
//	    --path byteat-pathbloom=BenchmarkByteAtPathBloom
//
// Everything must run inside the capped sandbox (scripts/sandbox.sh); the tool
// itself only reads artifacts and drives the public API.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"time"

	ekbloom "github.com/infinityabundance/ekbloom-go"
	"github.com/infinityabundance/ekbloom-go/internal/tools/stat"
)

// Default hot-path mapping mirrors scripts/profile.sh.
var defaultPaths = []string{
	"m1-encode=BenchmarkMachineEncode/m1",
	"m2-encode=BenchmarkMachineEncode/m2",
	"m9-encode=BenchmarkMachineEncode/m9",
	"byteat-pathbloom=BenchmarkByteAtPathBloom",
}

// sparseIntervals / pathDepths are the profile parameters the writer accepts.
var (
	sparseIntervals = []uint32{1, 4, 16, 64, 256, 1024, 4096, 65536}
	pathDepths      = []uint16{1, 2, 4, 8, 16, 32}
)

// gcReport is evidence/profile/gcstats.json: the GC-pause and heap measurement.
// The pause quantiles are the median across R = EKB_RUNS runs; the min/max of each
// quantile across runs is retained. Heap peaks are the maximum seen over all runs.
type gcReport struct {
	Seconds         float64 `json:"seconds"`
	Iterations      int64   `json:"iterations"`
	Symbols         int64   `json:"symbols"`
	HeapPeakBytes   uint64  `json:"heap_peak_bytes"`
	HeapInuseBytes  uint64  `json:"heap_inuse_bytes"`
	HeapAllocBytes  uint64  `json:"heap_alloc_bytes"`
	HeapSysBytes    uint64  `json:"heap_sys_bytes"`
	PeakInuseBytes  uint64  `json:"heap_peak_inuse_bytes"`
	NumGC           uint32  `json:"num_gc"`
	PauseTotalNs    uint64  `json:"gc_pause_total_ns"`
	PauseCount      uint64  `json:"gc_pause_count"`
	PauseP50Sec     float64 `json:"gc_pause_p50_s"`
	PauseP99Sec     float64 `json:"gc_pause_p99_s"`
	PauseP999Sec    float64 `json:"gc_pause_p99_9_s"`
	PauseMaxSec     float64 `json:"gc_pause_max_s"`
	PauseP50MinSec  float64 `json:"gc_pause_p50_s_min"`
	PauseP50MaxSec  float64 `json:"gc_pause_p50_s_max"`
	PauseP99MinSec  float64 `json:"gc_pause_p99_s_min"`
	PauseP99MaxSec  float64 `json:"gc_pause_p99_s_max"`
	PauseP999MinSec float64 `json:"gc_pause_p99_9_s_min"`
	PauseP999MaxSec float64 `json:"gc_pause_p99_9_s_max"`
	PauseMaxMinSec  float64 `json:"gc_pause_max_s_min"`
	PauseMaxMaxSec  float64 `json:"gc_pause_max_s_max"`
	Note            string  `json:"note"`
}

// topEntry is one row of a `go tool pprof -top` table.
type topEntry struct {
	Function string  `json:"function"`
	Flat     string  `json:"flat"`
	FlatPct  float64 `json:"flat_pct"`
	Cum      string  `json:"cum"`
	CumPct   float64 `json:"cum_pct"`
}

// topTable is a parsed pprof "top" table. TotalValue is nanoseconds for a time
// profile, bytes for a memory profile (see Unit); it is derived from pprof's own
// "Showing nodes accounting for X ... of Y total" line, never fabricated.
type topTable struct {
	Type         string     `json:"type,omitempty"`
	Unit         string     `json:"unit,omitempty"`
	Accounted    string     `json:"accounted,omitempty"`
	AccountedPct float64    `json:"accounted_pct,omitempty"`
	Total        string     `json:"total,omitempty"`
	TotalValue   float64    `json:"total_value,omitempty"`
	Entries      []topEntry `json:"entries"`
	Error        string     `json:"error,omitempty"`
}

// hotPath is one profiled hot path.
type hotPath struct {
	Name            string    `json:"name"`
	Benchmark       string    `json:"benchmark"`
	CPU             *topTable `json:"cpu"`
	AllocSpace      *topTable `json:"alloc_space"`
	InuseSpace      *topTable `json:"inuse_space"`
	TotalAllocBytes float64   `json:"total_alloc_bytes,omitempty"`
	RetainedBytes   float64   `json:"retained_bytes,omitempty"`
}

// report is evidence/profile.json.
type report struct {
	GeneratedBy string            `json:"generated_by"`
	Binaries    map[string]string `json:"binaries"`
	Toolchain   map[string]any    `json:"toolchain"`
	Sandbox     string            `json:"sandbox"`
	Runs        int               `json:"runs"`
	Statistic   string            `json:"statistic"`
	HotPaths    []hotPath         `json:"hot_paths"`
	GC          *gcReport         `json:"gc"`
	Notes       []string          `json:"notes"`
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "profstats: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	dir := flag.String("dir", "evidence/profile", "directory holding the pprof <name>.top artifacts")
	out := flag.String("out", "evidence/profile.json", "consolidated report path")
	seconds := flag.Float64("seconds", 15, "GC workload wall-clock budget, seconds")
	seed := flag.Int64("seed", 1, "deterministic PRNG seed")
	var paths stringList
	flag.Var(&paths, "path", "hot path as name=benchmark (repeatable)")
	flag.Parse()

	specs := []string(paths)
	if len(specs) == 0 {
		specs = defaultPaths
	}

	gc := runWorkload(time.Duration(*seconds*float64(time.Second)), *seed)
	if err := writeJSON(filepath.Join(*dir, "gcstats.json"), gc); err != nil {
		die("write gcstats.json: %v", err)
	}

	rep := report{
		GeneratedBy: "scripts/profile.sh",
		Binaries: map[string]string{
			"test":  filepath.ToSlash(filepath.Join(*dir, "ekbloom.test")),
			"pprof": "go tool pprof (toolchain)",
			"gc":    "internal/tools/profstats",
		},
		Toolchain: map[string]any{
			"go_version": runtime.Version(),
			"goos":       runtime.GOOS,
			"goarch":     runtime.GOARCH,
			"num_cpu":    runtime.NumCPU(),
		},
		Sandbox:   "docker --memory=2g --memory-swap=2g --cpus=2 --pids-limit=512 (scripts/sandbox.sh)",
		Runs:      stat.Runs(),
		Statistic: "median-of-runs",
		GC:        gc,
		Notes: []string{
			"CPU shares and allocation/retained sites are go tool pprof tables over the test-binary profiles; values are pprof's own.",
			"GC pause quantiles are bucket midpoints of runtime/metrics /gc/pauses:seconds (stop-the-world pauses only); each quantile is the median across EKB_RUNS runs, with min/max retained.",
			"Heap peak/retained are runtime.MemStats sampled during the representative workload, not the allocation-profile sample.",
		},
	}

	for _, spec := range specs {
		name, bench := splitSpec(spec)
		hp := hotPath{
			Name:       name,
			Benchmark:  bench,
			CPU:        parseTopTable(filepath.Join(*dir, name+".top")),
			AllocSpace: parseTopTable(filepath.Join(*dir, name+".alloc.top")),
			InuseSpace: parseTopTable(filepath.Join(*dir, name+".inuse.top")),
		}
		if hp.AllocSpace != nil && hp.AllocSpace.Unit == "bytes" {
			hp.TotalAllocBytes = hp.AllocSpace.TotalValue
		}
		if hp.InuseSpace != nil && hp.InuseSpace.Unit == "bytes" {
			hp.RetainedBytes = hp.InuseSpace.TotalValue
		}
		rep.HotPaths = append(rep.HotPaths, hp)
	}

	if err := writeJSON(*out, &rep); err != nil {
		die("write %s: %v", *out, err)
	}

	fmt.Printf("profstats: gc pauses=%d total=%.3fms p99=%.3fµs peak_heap=%.2fMiB retained=%.2fMiB; wrote %s\n",
		gc.PauseCount, float64(gc.PauseTotalNs)/1e6, gc.PauseP99Sec*1e6,
		float64(gc.HeapPeakBytes)/(1<<20), float64(gc.HeapInuseBytes)/(1<<20), *out)
}

func splitSpec(spec string) (name, bench string) {
	if i := strings.IndexByte(spec, '='); i >= 0 {
		return spec[:i], spec[i+1:]
	}
	return spec, spec
}

func writeJSON(path string, v any) error {
	if d := filepath.Dir(path); d != "." {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// --- GC / heap measurement ---------------------------------------------------

func runWorkload(budget time.Duration, seed int64) *gcReport {
	rng := rand.New(rand.NewSource(seed))
	start := time.Now()

	// Pre-train one machine-9 dictionary so the decode leg uses the shared-model
	// path (NewDictDecoder + DecodeInto) instead of re-training every record.
	corpus := make([][]byte, 64)
	for i := range corpus {
		corpus[i] = textRecord(rng, 100)
	}
	dict, derr := ekbloom.TrainDictionary(corpus)
	var dec *ekbloom.DictDecoder
	if derr == nil {
		dec, _ = ekbloom.NewDictDecoder(dict)
	}

	// Run the soak R times (R = EKB_RUNS) so the GC-pause quantiles can be reported
	// as the median across runs. The wall-clock budget is SPLIT across the runs, so
	// profile.sh stays time-bounded at any R; each per-run pause histogram is the
	// delta of runtime/metrics /gc/pauses:seconds across that run.
	runs := stat.Runs()
	if runs < 1 {
		runs = 1
	}
	seg := budget / time.Duration(runs)
	if seg < time.Millisecond {
		seg = time.Millisecond
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	peakHeap, peakInuse := ms.HeapAlloc, ms.HeapInuse

	var iterations, symbols int64
	var pauseCount uint64
	p50s := make([]float64, 0, runs)
	p99s := make([]float64, 0, runs)
	p999s := make([]float64, 0, runs)
	maxs := make([]float64, 0, runs)

	for run := 0; run < runs; run++ {
		before, _ := gcHistogram()
		deadline := time.Now().Add(seg)
		for i := 1; time.Now().Before(deadline); i++ {
			n, err := roundtrip(rng)
			if err != nil {
				die("workload: %v", err)
			}
			symbols += n
			if dec != nil && i%4 == 0 {
				symbols += dictDecode(rng, dict, dec)
			}
			if i%16 == 0 {
				runtime.ReadMemStats(&ms)
				if ms.HeapAlloc > peakHeap {
					peakHeap = ms.HeapAlloc
				}
				if ms.HeapInuse > peakInuse {
					peakInuse = ms.HeapInuse
				}
			}
			iterations++
		}
		after, buckets := gcHistogram()
		delta := make([]uint64, len(after))
		for i := range after {
			delta[i] = after[i] - before[i]
		}
		count, _, p50, p99, p999, max := quantilesFrom(delta, buckets)
		pauseCount += count
		p50s = append(p50s, p50)
		p99s = append(p99s, p99)
		p999s = append(p999s, p999)
		maxs = append(maxs, max)
	}
	runtime.ReadMemStats(&ms)
	if ms.HeapAlloc > peakHeap {
		peakHeap = ms.HeapAlloc
	}
	if ms.HeapInuse > peakInuse {
		peakInuse = ms.HeapInuse
	}

	s50, s99, s999, smax := stat.Collect(p50s), stat.Collect(p99s), stat.Collect(p999s), stat.Collect(maxs)
	return &gcReport{
		Seconds:         time.Since(start).Seconds(),
		Iterations:      iterations,
		Symbols:         symbols,
		HeapPeakBytes:   peakHeap,
		HeapInuseBytes:  ms.HeapInuse,
		HeapAllocBytes:  ms.HeapAlloc,
		HeapSysBytes:    ms.HeapSys,
		PeakInuseBytes:  peakInuse,
		NumGC:           ms.NumGC,
		PauseTotalNs:    ms.PauseTotalNs,
		PauseCount:      pauseCount,
		PauseP50Sec:     s50.Median,
		PauseP99Sec:     s99.Median,
		PauseP999Sec:    s999.Median,
		PauseMaxSec:     smax.Median,
		PauseP50MinSec:  s50.Min,
		PauseP50MaxSec:  s50.Max,
		PauseP99MinSec:  s99.Min,
		PauseP99MaxSec:  s99.Max,
		PauseP999MinSec: s999.Min,
		PauseP999MaxSec: s999.Max,
		PauseMaxMinSec:  smax.Min,
		PauseMaxMaxSec:  smax.Max,
		Note: "Stop-the-world GC pauses sampled from runtime/metrics /gc/pauses:seconds over EKB_RUNS runs; " +
			"each quantile is the median across runs (min/max retained); the wall-clock budget is split across runs. " +
			"peak/retained heap from runtime.MemStats during a soak-style roundtrip + machine-9 decode workload.",
	}
}

// gcHistogram reads the runtime's cumulative GC-pause histogram
// (/gc/pauses:seconds) and returns its per-bucket cumulative counts and bucket
// bounds.
func gcHistogram() ([]uint64, []float64) {
	samples := []metrics.Sample{{Name: "/gc/pauses:seconds"}}
	metrics.Read(samples)
	h := samples[0].Value.Float64Histogram()
	return h.Counts, h.Buckets
}

// quantilesFrom derives the pause count and p50/p99/p99.9/max pauses (seconds) from
// a GC-pause histogram (bucket counts + bucket bounds). Quantiles are bucket
// midpoints (a documented histogram estimate, not a fabricated value).
func quantilesFrom(counts []uint64, buckets []float64) (count uint64, total, p50, p99, p999, max float64) {
	mid := func(i int) float64 {
		if i < 0 || i >= len(counts) {
			return 0
		}
		lo, hi := buckets[i], buckets[i+1]
		if math.IsInf(hi, 1) {
			return lo
		}
		return (lo + hi) / 2
	}
	for i, c := range counts {
		count += c
		total += float64(c) * mid(i)
	}
	q := func(frac float64) float64 {
		if count == 0 {
			return 0
		}
		target := frac * float64(count)
		var cum uint64
		for i, c := range counts {
			cum += c
			if float64(cum) >= target {
				return mid(i)
			}
		}
		return mid(len(counts) - 1)
	}
	for i := len(counts) - 1; i >= 0; i-- {
		if counts[i] > 0 {
			max = mid(i)
			break
		}
	}
	return count, total, q(0.50), q(0.99), q(0.999), max
}

// --- representative workload -------------------------------------------------

func roundtrip(rng *rand.Rand) (int64, error) {
	id := uint32(1 + rng.Intn(9))
	var p ekbloom.Profile
	switch rng.Intn(4) {
	case 0:
		p = ekbloom.Profile{Kind: ekbloom.ProfileRoot}
	case 1:
		p = ekbloom.Profile{Kind: ekbloom.ProfileFull}
	case 2:
		id = ekbloom.MachineNL2
		p = ekbloom.Profile{Kind: ekbloom.ProfileSparse, Interval: sparseIntervals[rng.Intn(len(sparseIntervals))]}
	default:
		p = ekbloom.Profile{Kind: ekbloom.ProfilePath, Depth: pathDepths[rng.Intn(len(pathDepths))]}
	}
	size := 1 << rng.Intn(21) // 1 B .. 1 MiB
	src := content(rng, size)

	image, err := ekbloom.WriteBody(id, src, p)
	if err != nil {
		return 0, fmt.Errorf("WriteBody(machine=%d size=%d): %w", id, size, err)
	}
	r, err := ekbloom.Open(image)
	if err != nil {
		return 0, fmt.Errorf("Open(machine=%d size=%d): %w", id, size, err)
	}
	n := r.LogicalLen()
	got, err := r.ReadAt(0, n)
	if err != nil {
		return 0, fmt.Errorf("ReadAt(0,%d): %w", n, err)
	}
	if !bytes.Equal(got, src) {
		return 0, fmt.Errorf("ReadAt mismatch (machine=%d size=%d)", id, size)
	}
	off := uint64(rng.Int63n(int64(n)))
	b, err := r.ByteAt(off)
	if err != nil {
		return 0, fmt.Errorf("ByteAt(%d): %w", off, err)
	}
	if b != src[off] {
		return 0, fmt.Errorf("ByteAt(%d) mismatch: got %#x want %#x", off, b, src[off])
	}
	return int64(n), nil
}

func dictDecode(rng *rand.Rand, dict ekbloom.Dictionary, dec *ekbloom.DictDecoder) int64 {
	rec := textRecord(rng, 32+rng.Intn(200))
	core, err := dict.EncodeRecord(rec)
	if err != nil {
		die("EncodeRecord: %v", err)
	}
	dst := make([]byte, len(rec))
	n, err := dec.DecodeInto(dst, core)
	if err != nil {
		die("DecodeInto: %v", err)
	}
	return int64(n)
}

func content(rng *rand.Rand, n int) []byte {
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
		return textRecord(rng, n)
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

func textRecord(rng *rand.Rand, n int) []byte {
	out := make([]byte, 0, n+16)
	for len(out) < n {
		out = append(out, textTokens[rng.Intn(len(textTokens))]...)
	}
	return out[:n]
}

// --- pprof "top" table parsing ----------------------------------------------

// entryRe captures the five numeric columns and the function name of a -top row.
var entryRe = regexp.MustCompile(`^\s*(\S+)\s+([0-9.]+)%\s+([0-9.]+)%\s+(\S+)\s+([0-9.]+)%\s+(.+)$`)

// numUnitRe splits a pprof value like "0.13s", "200ms", "5.50MB" or "0".
var numUnitRe = regexp.MustCompile(`^([0-9]*\.?[0-9]+)([a-zA-Zµ]*)$`)

func parseTopTable(path string) *topTable {
	b, err := os.ReadFile(path)
	if err != nil {
		return &topTable{Error: err.Error()}
	}
	t := &topTable{Entries: []topEntry{}}
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "Type:"):
			t.Type = strings.TrimSpace(strings.TrimPrefix(line, "Type:"))
		case strings.HasPrefix(line, "Showing nodes accounting for "):
			parseTotals(t, strings.TrimPrefix(line, "Showing nodes accounting for "))
		default:
			if e, ok := parseEntry(line); ok {
				t.Entries = append(t.Entries, e)
			}
		}
	}
	t.TotalValue = parseUnit(t.Total)
	if strings.HasSuffix(t.Total, "B") {
		t.Unit = "bytes"
	} else {
		t.Unit = "time"
	}
	return t
}

// parseTotals parses "780ms, 86.67% of 900ms total" (the " total" suffix stripped).
func parseTotals(t *topTable, s string) {
	s = strings.TrimSuffix(strings.TrimSpace(s), " total")
	i := strings.Index(s, ", ")
	if i < 0 {
		return
	}
	t.Accounted = s[:i]
	tail := s[i+2:]
	j := strings.Index(tail, " of ")
	if j < 0 {
		return
	}
	t.AccountedPct = parseFloat(tail[:j])
	t.Total = tail[j+4:]
}

func parseEntry(line string) (topEntry, bool) {
	m := entryRe.FindStringSubmatch(line)
	if m == nil {
		return topEntry{}, false
	}
	if numUnitRe.FindStringSubmatch(m[1]) == nil {
		return topEntry{}, false
	}
	return topEntry{
		Function: strings.TrimSpace(m[6]),
		Flat:     m[1],
		FlatPct:  parseFloat(m[2]),
		Cum:      m[4],
		CumPct:   parseFloat(m[5]),
	}, true
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "%")), 64)
	return v
}

// parseUnit returns a pprof value in nanoseconds (time units) or bytes (memory
// units); a unitless value is returned as-is.
func parseUnit(s string) float64 {
	m := numUnitRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	switch m[2] {
	case "ns":
		return v
	case "us", "µs":
		return v * 1e3
	case "ms":
		return v * 1e6
	case "s":
		return v * 1e9
	case "kB":
		return v * 1024
	case "MB":
		return v * 1024 * 1024
	case "GB":
		return v * 1024 * 1024 * 1024
	case "TB":
		return v * 1024 * 1024 * 1024 * 1024
	default:
		return v // "B" or unitless
	}
}
