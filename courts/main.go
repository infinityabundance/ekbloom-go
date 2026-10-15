// Command courts measures the RocksDB-style skewed small-record court: 100-byte
// values, an 80/20 key skew. It compares EKBLOOM machine 9 (shared trained
// dictionary) against raw, block LZ4, plain Zstandard and Zstandard-with-dictionary
// on the same training set, reporting bytes/record and the decode p50.
//
// The decode p50/p99.9 and allocs/get are the MEDIAN across R = EKB_RUNS runs
// (default 5; 3 in CI), with the latency min/max retained.
//
// Usage: EKB_RUNS=R go run .   (writes ../evidence/courts.json)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"text/tabwriter"
	"time"

	ekbloom "github.com/infinityabundance/ekbloom-go"
	"github.com/infinityabundance/ekbloom-go/courts/internal/provenance"
	"github.com/infinityabundance/ekbloom-go/courts/internal/stats"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

const (
	numRecords = 2000
	numTrain   = 500
	recSize    = 100
	gets       = 50000
)

var users = []string{"alice", "bob", "carol", "dave", "erin", "frank", "grace", "heidi"}
var regions = []string{"us-east-1", "us-west-2", "eu-west-1", "eu-central-1", "ap-south-1", "ap-northeast-1"}
var roles = []string{"admin", "reader", "writer", "auditor"}
var flags = []string{"active", "inactive", "pending"}

// lcg is a deterministic PRNG so the court is reproducible.
type lcg uint64

func (s *lcg) next() uint64 {
	*s = lcg(uint64(*s)*6364136223846793005 + 1442695040888963407)
	return uint64(*s) >> 33
}

// makeRecord builds one 100-byte structured value.
func makeRecord(s *lcg) []byte {
	rec := fmt.Sprintf("user=%s|region=%s|role=%s|flag=%s|ver=%d|",
		users[s.next()%uint64(len(users))],
		regions[s.next()%uint64(len(regions))],
		roles[s.next()%uint64(len(roles))],
		flags[s.next()%uint64(len(flags))],
		s.next()%8)
	b := []byte(rec)
	for len(b) < recSize {
		b = append(b, byte('a'+s.next()%4))
	}
	return b[:recSize]
}

// skewIndex picks an index with an 80/20 skew: 80% of gets target the first 20%.
func skewIndex(s *lcg, n int) int {
	hot := n / 5
	if hot < 1 {
		hot = 1
	}
	if s.next()%100 < 80 {
		return int(s.next() % uint64(hot))
	}
	return int(s.next() % uint64(n))
}

type row struct {
	Codec        string  `json:"codec"`
	BytesPerRec  float64 `json:"bytes_per_record"`
	BytesFetched float64 `json:"bytes_fetched_per_get"`
	P50ns        float64 `json:"decode_p50_ns"`
	P999ns       float64 `json:"decode_p99_9_ns"`
	AllocsPerGet float64 `json:"allocs_per_get"`
	Ratio        float64 `json:"ratio_vs_raw"`
	P50Min       float64 `json:"decode_p50_ns_min"`
	P50Max       float64 `json:"decode_p50_ns_max"`
	P999Min      float64 `json:"decode_p99_9_ns_min"`
	P999Max      float64 `json:"decode_p99_9_ns_max"`
}

// mallocs is the cumulative count of heap allocations, used to derive allocs/get
// for each codec's decode loop.
func mallocs() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Mallocs
}

// measureRuns runs decode R = EKB_RUNS times over one codec's prepared cores. Each
// run returns its per-get durations and allocs/get; the results are reduced to the
// median across runs (with min/max) for p50, p99.9 and allocs/get.
func measureRuns(decode func() []time.Duration) (p50s, p999s, allocs stats.Stats[float64]) {
	runs := stats.Runs()
	p50 := make([]float64, 0, runs)
	p999 := make([]float64, 0, runs)
	alloc := make([]float64, 0, runs)
	for r := 0; r < runs; r++ {
		m0 := mallocs()
		durs := decode()
		alloc = append(alloc, float64(mallocs()-m0)/gets)
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		p50 = append(p50, float64(stats.Pct(durs, 500)))
		p999 = append(p999, float64(stats.Pct(durs, 999)))
	}
	return stats.Collect(p50), stats.Collect(p999), stats.Collect(alloc)
}

func main() {
	var s lcg = 0x1234567

	records := make([][]byte, numRecords)
	for i := range records {
		records[i] = makeRecord(&s)
	}
	train := records[:numTrain]

	// One deterministic 80/20 access trace, shared by every codec and every run so
	// the codecs are compared on the same reads.
	trace := make([]int, gets)
	ts := lcg(0x9E3779B9)
	for i := range trace {
		trace[i] = skewIndex(&ts, numRecords)
	}

	var rows []row
	add := func(codec string, bytesPerRec float64, p50, p999, allocs stats.Stats[float64]) {
		rows = append(rows, row{
			Codec: codec, BytesPerRec: bytesPerRec, BytesFetched: bytesPerRec,
			P50ns: p50.Median, P999ns: p999.Median, AllocsPerGet: allocs.Median,
			P50Min: p50.Min, P50Max: p50.Max, P999Min: p999.Min, P999Max: p999.Max,
		})
	}

	// raw
	{
		var total int
		for _, r := range records {
			total += len(r)
		}
		decode := func() []time.Duration {
			durs := make([]time.Duration, gets)
			for i, idx := range trace {
				t0 := time.Now()
				_ = append([]byte(nil), records[idx]...)
				durs[i] = time.Since(t0)
			}
			return durs
		}
		p50, p999, alloc := measureRuns(decode)
		add("raw", float64(total)/numRecords, p50, p999, alloc)
	}

	// lz4 block
	{
		comps := make([][]byte, numRecords)
		var total int
		dst := make([]byte, lz4.CompressBlockBound(recSize))
		for i, r := range records {
			n, err := lz4.CompressBlock(r, dst, nil)
			if err != nil || n == 0 {
				comps[i] = append([]byte(nil), r...) // incompressible: stored
			} else {
				comps[i] = append([]byte(nil), dst[:n]...)
			}
			total += len(comps[i])
		}
		out := make([]byte, recSize)
		decode := func() []time.Duration {
			durs := make([]time.Duration, gets)
			for i, idx := range trace {
				t0 := time.Now()
				if _, err := lz4.UncompressBlock(comps[idx], out); err != nil {
					_ = append([]byte(nil), comps[idx]...)
				}
				durs[i] = time.Since(t0)
			}
			return durs
		}
		p50, p999, alloc := measureRuns(decode)
		add("lz4", float64(total)/numRecords, p50, p999, alloc)
	}

	// zstd (no dictionary)
	{
		enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
		dec, _ := zstd.NewReader(nil)
		comps := make([][]byte, numRecords)
		var total int
		for i, r := range records {
			comps[i] = enc.EncodeAll(r, nil)
			total += len(comps[i])
		}
		decode := func() []time.Duration {
			durs := make([]time.Duration, gets)
			for i, idx := range trace {
				t0 := time.Now()
				_, _ = dec.DecodeAll(comps[idx], nil)
				durs[i] = time.Since(t0)
			}
			return durs
		}
		p50, p999, alloc := measureRuns(decode)
		add("zstd", float64(total)/numRecords, p50, p999, alloc)
	}

	// zstd with a dictionary trained on the same set (raw content dict)
	{
		dictContent := make([]byte, 0, numTrain*recSize)
		for _, r := range train {
			dictContent = append(dictContent, r...)
		}
		enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderDictRaw(1, dictContent))
		dec, _ := zstd.NewReader(nil, zstd.WithDecoderDictRaw(1, dictContent))
		comps := make([][]byte, numRecords)
		var total int
		for i, r := range records {
			comps[i] = enc.EncodeAll(r, nil)
			total += len(comps[i])
		}
		decode := func() []time.Duration {
			durs := make([]time.Duration, gets)
			for i, idx := range trace {
				t0 := time.Now()
				_, _ = dec.DecodeAll(comps[idx], nil)
				durs[i] = time.Since(t0)
			}
			return durs
		}
		p50, p999, alloc := measureRuns(decode)
		add("zstd-dict", float64(total)/numRecords, p50, p999, alloc)
	}

	// EKBLOOM machine 9 (shared trained order-0 dictionary)
	{
		dict, err := ekbloom.TrainDictionary(train)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		cores := make([][]byte, numRecords)
		var total int
		for i, r := range records {
			core, err := dict.EncodeRecord(r)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			cores[i] = core
			total += len(core)
		}
		decode := func() []time.Duration {
			durs := make([]time.Duration, gets)
			for i, idx := range trace {
				t0 := time.Now()
				if _, err := dict.DecodeRecord(cores[idx]); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
				durs[i] = time.Since(t0)
			}
			return durs
		}
		p50, p999, alloc := measureRuns(decode)
		add("ekbloom-m9", float64(total)/numRecords, p50, p999, alloc)
	}

	// ratio vs raw
	raw := rows[0].BytesPerRec
	for i := range rows {
		rows[i].Ratio = rows[i].BytesPerRec / raw
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "codec\tB/rec\tB/fetch\tvs raw\tp50\tp99.9\tallocs/get\n")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%.1f\t%.1f\t%.3f\t%.0f ns\t%.0f ns\t%.2f\n", r.Codec, r.BytesPerRec, r.BytesFetched, r.Ratio, r.P50ns, r.P999ns, r.AllocsPerGet)
	}
	w.Flush()

	report := struct {
		Records     int    `json:"records"`
		TrainSet    int    `json:"train_records"`
		RecordBytes int    `json:"record_bytes"`
		Gets        int    `json:"gets"`
		Runs        int    `json:"runs"`
		Statistic   string `json:"statistic"`
		Rows        []row  `json:"rows"`
	}{numRecords, numTrain, recSize, gets, stats.Runs(), "median-of-runs", rows}
	out, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile("../evidence/courts.json", provenance.Stamp(out), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write evidence:", err)
	}
	fmt.Println("\nwrote ../evidence/courts.json")
}
