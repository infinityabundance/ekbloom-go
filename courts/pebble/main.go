// Command pebble is the EKBLOOM machine-9 Pebble value-log case study.
//
// It builds 20,000 100-byte records from the same skewed/repetitive token
// alphabet as the ycsb court, trains ONE shared order-0 dictionary from a slice
// of them, and stores every value as its machine-9 core in a Pebble DB
// (db.Set(key, core, pebble.NoSync)). It then replays 50,000 80/20-skewed Gets:
// each Get fetches the core, decodes it with a SINGLE reusable DictDecoder built
// once (ekbloom.NewDictDecoder) into a caller-reused buffer (DecodeInto), and
// verifies the decoded bytes equal the original value.
//
// On one representative value it also exercises the sealed-body path
// (WriteBodyWithDict + OpenWithDict) to report the certified worst-case step
// bound and the manifest's expansion metrics.
//
// Output contract:
//   - stdout: exactly one JSON object (machine-readable evidence).
//   - stderr: a human-readable table plus round-trip diagnostics.
//   - file:   ../evidence/pebble-vlog.prom (Prometheus-style exposition).
//
// Usage: go run ./pebble
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/cockroachdb/pebble"
	ekbloom "github.com/infinityabundance/ekbloom-go"
	"github.com/infinityabundance/ekbloom-go/courts/internal/stats"
	"github.com/infinityabundance/ekbloom-go/courts/internal/workload"
)

const (
	records     = 20000
	recordBytes = workload.RecordBytes
	gets        = 50000
	trainFrac   = 4 // 1/trainFrac of the records train the shared dictionary
)

// report is the machine-readable stdout artifact. Field order fixes the JSON key
// order. get_p50_ns/get_p99_ns are the median across R = EKB_RUNS runs (min/max
// retained); the size/step fields are deterministic.
type report struct {
	Records           int     `json:"records"`
	RecordBytes       int     `json:"record_bytes"`
	Gets              int     `json:"gets"`
	BytesPerRecord    float64 `json:"bytes_per_record"`
	GetP50Ns          float64 `json:"get_p50_ns"`
	GetP99Ns          float64 `json:"get_p99_ns"`
	GetP50NsMin       float64 `json:"get_p50_ns_min"`
	GetP50NsMax       float64 `json:"get_p50_ns_max"`
	GetP99NsMin       float64 `json:"get_p99_ns_min"`
	GetP99NsMax       float64 `json:"get_p99_ns_max"`
	DecodeAllocsPerOp float64 `json:"decode_allocs_per_op"`
	Breaches          uint64  `json:"breaches"`
	WorstCaseSteps    uint64  `json:"worst_case_steps"`
	ExpansionRatio    float64 `json:"expansion_ratio"`
	PhysicalBytes     uint64  `json:"physical_bytes"`
	DictBytes         uint64  `json:"dict_bytes"`
	Runs              int     `json:"runs"`
	Statistic         string  `json:"statistic"`
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func main() {
	// --- records + shared dictionary ----------------------------------------
	src := make([][]byte, records)
	for i := range src {
		src[i] = workload.Record(i)
	}
	trainN := records / trainFrac
	dict, err := ekbloom.TrainDictionary(src[:trainN])
	check(err)
	dictBytes := dict.PhysicalBytes() // 540

	// One reusable decoder for the shared dictionary: the order-0 model is fixed,
	// so its rANS context is built once and reused for every record.
	dec, err := ekbloom.NewDictDecoder(dict)
	check(err)

	// --- Pebble value log: values are machine-9 cores -----------------------
	dir, err := os.MkdirTemp("", "ekbloom-pebble-vlog-")
	check(err)
	defer os.RemoveAll(dir)
	db, err := pebble.Open(filepath.Join(dir, "db"), &pebble.Options{})
	check(err)

	var key [8]byte
	var coreTotal uint64
	for i, v := range src {
		core, err := dict.EncodeRecord(v)
		check(err)
		coreTotal += uint64(len(core))
		binary.BigEndian.PutUint64(key[:], uint64(i))
		check(db.Set(key[:], core, pebble.NoSync))
	}

	// --- steady-state decode allocations ------------------------------------
	// Fetch exactly one core, then measure the decode step in isolation. The
	// reusable decoder writes into a caller-reused buffer, so this is the
	// documented 0-allocation path (and the number the JSON reports).
	binary.BigEndian.PutUint64(key[:], 0)
	core0, closer0, err := db.Get(key[:])
	check(err)
	decodeAllocs := testing.AllocsPerRun(1000, func() {
		dst := decodeDst
		_, _ = dec.DecodeInto(dst, core0)
	})
	closer0.Close()

	// Also measure the whole Get+decode path, so the "zero-alloc decode on Get"
	// claim is visible end to end.
	getAllocs := testing.AllocsPerRun(200, func() {
		binary.BigEndian.PutUint64(key[:], 0)
		b, c, gerr := db.Get(key[:])
		if gerr != nil {
			return
		}
		_, _ = dec.DecodeInto(decodeDst, b)
		c.Close()
	})

	// --- 50,000 skewed Gets: fetch, zero-alloc decode, verify ---------------
	s := uint64(0x5EED1234)
	// Warm the block cache / memtable read path.
	for i := 0; i < 1000; i++ {
		idx := workload.SkewIndex(&s, records)
		binary.BigEndian.PutUint64(key[:], uint64(idx))
		b, c, gerr := db.Get(key[:])
		check(gerr)
		_, _ = dec.DecodeInto(decodeDst, b)
		c.Close()
	}

	ekbloom.ResetBreaches()
	runs := stats.Runs()
	p50v := make([]int64, 0, runs)
	p99v := make([]int64, 0, runs)
	mismatches := 0
	for run := 0; run < runs; run++ {
		rs := uint64(0x5EED1234) + uint64(run)
		durs := make([]time.Duration, gets)
		for i := 0; i < gets; i++ {
			idx := workload.SkewIndex(&rs, records)
			binary.BigEndian.PutUint64(key[:], uint64(idx))
			t0 := time.Now()
			core, closer, gerr := db.Get(key[:])
			if gerr != nil {
				check(gerr)
			}
			n, derr := dec.DecodeInto(decodeDst, core)
			closer.Close()
			durs[i] = time.Since(t0)
			if derr != nil {
				check(derr)
			}
			if string(decodeDst[:n]) != string(src[idx]) {
				mismatches++
			}
		}
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		p50v = append(p50v, stats.Pct(durs, 500))
		p99v = append(p99v, stats.Pct(durs, 990))
	}
	breaches := ekbloom.Breaches()
	sp50, sp99 := stats.Collect(p50v), stats.Collect(p99v)

	// --- sealed-body path on one representative value -----------------------
	v := src[0]
	img, err := ekbloom.WriteBodyWithDict(v, ekbloom.Profile{Kind: ekbloom.ProfileRoot}, dict)
	check(err)
	r, err := ekbloom.OpenWithDict(img, dict)
	check(err)
	steps, err := r.WorstCaseStepsFor(0, uint64(len(v)))
	check(err)
	m := r.Metrics()

	if err := db.Close(); err != nil {
		check(err)
	}

	// --- artifacts ----------------------------------------------------------
	rep := report{
		Records:           records,
		RecordBytes:       recordBytes,
		Gets:              gets,
		BytesPerRecord:    float64(coreTotal) / records,
		GetP50Ns:          float64(sp50.Median),
		GetP99Ns:          float64(sp99.Median),
		GetP50NsMin:       float64(sp50.Min),
		GetP50NsMax:       float64(sp50.Max),
		GetP99NsMin:       float64(sp99.Min),
		GetP99NsMax:       float64(sp99.Max),
		DecodeAllocsPerOp: decodeAllocs,
		Breaches:          breaches,
		WorstCaseSteps:    steps,
		ExpansionRatio:    m.ExpansionRatio,
		PhysicalBytes:     m.PhysicalBytes,
		DictBytes:         dictBytes,
		Runs:              runs,
		Statistic:         "median-of-runs",
	}

	// Human table -> stderr only.
	w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "pebble value-log court (machine-9 shared dictionary, 80/20 skew)")
	fmt.Fprintf(w, "records\t%d\n", records)
	fmt.Fprintf(w, "record_bytes\t%d\n", recordBytes)
	fmt.Fprintf(w, "gets\t%d\n", gets)
	fmt.Fprintf(w, "train_records\t%d\n", trainN)
	fmt.Fprintf(w, "bytes_per_record (codec payload)\t%.4f\n", rep.BytesPerRecord)
	fmt.Fprintf(w, "get p50\t%.0f ns\n", rep.GetP50Ns)
	fmt.Fprintf(w, "get p99\t%.0f ns\n", rep.GetP99Ns)
	fmt.Fprintf(w, "decode allocs/op\t%.4f\n", rep.DecodeAllocsPerOp)
	fmt.Fprintf(w, "get+decode allocs/op\t%.4f\n", getAllocs)
	fmt.Fprintf(w, "roundtrip mismatches\t%d\n", mismatches)
	fmt.Fprintf(w, "allocation breaches\t%d\n", rep.Breaches)
	fmt.Fprintf(w, "worst_case_steps (sealed body)\t%d\n", rep.WorstCaseSteps)
	fmt.Fprintf(w, "expansion_ratio (sealed body)\t%.6f\n", rep.ExpansionRatio)
	fmt.Fprintf(w, "physical_bytes (sealed body)\t%d\n", rep.PhysicalBytes)
	fmt.Fprintf(w, "dict_bytes\t%d\n", rep.DictBytes)
	w.Flush()
	if mismatches != 0 {
		fmt.Fprintf(os.Stderr, "FATAL: %d round-trip mismatches\n", mismatches)
		os.Exit(1)
	}

	// Prometheus-style exposition -> evidence/pebble-vlog.prom.
	if err := writeProm(rep, getAllocs, mismatches, r); err != nil {
		fmt.Fprintln(os.Stderr, "write prom:", err)
	}

	// JSON -> stdout ONLY.
	out, err := json.MarshalIndent(rep, "", "  ")
	check(err)
	fmt.Println(string(out))
}

// decodeDst is the caller-reused decode destination (every record is exactly
// recordBytes long).
var decodeDst = make([]byte, recordBytes)

// writeProm writes the Prometheus-style exposition. The per-object series come
// from ekbloom.MetricsText for the sealed reader; the court's own gauges are
// appended with a stable prefix.
func writeProm(rep report, getAllocs float64, mismatches int, r *ekbloom.Reader) error {
	if err := os.MkdirAll("../evidence", 0o755); err != nil {
		return err
	}
	var b []byte
	b = append(b, "# EKBLOOM machine-9 Pebble value-log court\n"...)
	b = append(b, "# generated by courts/pebble (go run ./pebble)\n"...)
	b = append(b, ekbloom.MetricsText(r)...)
	line := func(format string, args ...any) {
		b = append(b, fmt.Sprintf(format, args...)...)
		b = append(b, '\n')
	}
	line("# TYPE ekbloom_pebble_vlog_bytes_per_record gauge")
	line("ekbloom_pebble_vlog_bytes_per_record %g", rep.BytesPerRecord)
	line("# TYPE ekbloom_pebble_vlog_get_p50_ns gauge")
	line("ekbloom_pebble_vlog_get_p50_ns %g", rep.GetP50Ns)
	line("# TYPE ekbloom_pebble_vlog_get_p99_ns gauge")
	line("ekbloom_pebble_vlog_get_p99_ns %g", rep.GetP99Ns)
	line("# TYPE ekbloom_pebble_vlog_decode_allocs_per_op gauge")
	line("ekbloom_pebble_vlog_decode_allocs_per_op %g", rep.DecodeAllocsPerOp)
	line("ekbloom_pebble_vlog_get_decode_allocs_per_op %g", getAllocs)
	line("ekbloom_pebble_vlog_roundtrip_mismatches %d", mismatches)
	line("ekbloom_pebble_vlog_breaches %d", rep.Breaches)
	line("ekbloom_pebble_vlog_worst_case_steps %d", rep.WorstCaseSteps)
	line("ekbloom_pebble_vlog_expansion_ratio %g", rep.ExpansionRatio)
	line("ekbloom_pebble_vlog_dictionary_bytes %d", rep.DictBytes)
	line("ekbloom_pebble_vlog_records %d", rep.Records)
	line("ekbloom_pebble_vlog_record_bytes %d", rep.RecordBytes)
	line("ekbloom_pebble_vlog_gets %d", rep.Gets)
	return os.WriteFile("../evidence/pebble-vlog.prom", b, 0o644)
}
