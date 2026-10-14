// Command point measures the cost of one random 4 KiB point read when the same
// payloads are held in different representations: a raw positional slab, block
// LZ4, per-value Zstandard, an embedded LSM key/value store (Pebble), and the
// EKBLOOM codec (machine 1 root/path and machine 2 root).
//
// EKBLOOM is a codec, not a KV store: the honest comparison is "what does one
// random 4 KiB point read cost, and what does a value occupy", not a claim that
// it beats an LSM. EKBLOOM reads are in-memory; Pebble is on-disk with a block
// cache. Both are reported with their footprint.
//
// The Get p50/p99/p99.9 are the MEDIAN across R = EKB_RUNS runs (default 5; 3 in
// CI), with min/max retained.
//
// Usage: EKB_RUNS=R go run ./point   (writes ../evidence/courts-point.json)
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/cockroachdb/pebble"
	ekbloom "github.com/infinityabundance/ekbloom-go"
	"github.com/infinityabundance/ekbloom-go/courts/internal/stats"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

const (
	values    = 4096  // number of 4 KiB values (16 MiB total)
	valueSize = 4096  // bytes per value
	gets      = 20000 // point reads
	hotFrac   = 5     // 1 of every hotFrac values is "hot" (80/20-ish skew)
)

// value builds a compressible 4 KiB value from a small skewed token alphabet.
func value(seed int) []byte {
	parts := []string{"id=", "user", "region=us-east-1", "role=writer", "status=active",
		"|alpha", "|beta", "payload=", "0123456789", "lorem ipsum dolor sit amet "}
	out := make([]byte, 0, valueSize+16)
	k := uint64(seed)
	for len(out) < valueSize {
		k = k*6364136223846793005 + 1442695040888963407
		out = append(out, parts[(k>>33)%uint64(len(parts))]...)
	}
	return out[:valueSize]
}

type row struct {
	Codec       string  `json:"codec"`
	BytesPerVal float64 `json:"bytes_per_value"`
	P50ns       int64   `json:"get_p50_ns"`
	P99ns       int64   `json:"get_p99_ns"`
	P999ns      int64   `json:"get_p999_ns"`
	TotalBytes  uint64  `json:"total_bytes"`
	P50Min      int64   `json:"get_p50_ns_min"`
	P50Max      int64   `json:"get_p50_ns_max"`
	P99Min      int64   `json:"get_p99_ns_min"`
	P99Max      int64   `json:"get_p99_ns_max"`
	P999Min     int64   `json:"get_p999_ns_min"`
	P999Max     int64   `json:"get_p999_ns_max"`
}

// measure runs decode R = EKB_RUNS times and reduces each run's per-get durations
// to the median across runs (with min/max) for p50/p99/p99.9, in nanoseconds.
func measure(decode func() []time.Duration) (p50, p99, p999 stats.Stats[int64]) {
	runs := stats.Runs()
	a := make([]int64, 0, runs)
	b := make([]int64, 0, runs)
	c := make([]int64, 0, runs)
	for r := 0; r < runs; r++ {
		durs := decode()
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		a = append(a, stats.Pct(durs, 500))
		b = append(b, stats.Pct(durs, 990))
		c = append(c, stats.Pct(durs, 999))
	}
	return stats.Collect(a), stats.Collect(b), stats.Collect(c)
}

func main() {
	vals := make([][]byte, values)
	for i := range vals {
		vals[i] = value(i)
	}
	// Deterministic 80/20-skewed access sequence.
	hot := values / hotFrac
	order := make([]int, gets)
	s := uint64(0x9E3779B9)
	for i := range order {
		s = s*6364136223846793005 + 1442695040888963407
		r := (s >> 33) % 100
		if r < 80 {
			order[i] = int((s >> 20) % uint64(hot))
		} else {
			order[i] = int((s >> 20) % uint64(values))
		}
	}

	var rows []row
	add := func(codec string, total uint64, p50, p99, p999 stats.Stats[int64]) {
		rows = append(rows, row{codec, float64(total) / values, p50.Median, p99.Median, p999.Median, total,
			p50.Min, p50.Max, p99.Min, p99.Max, p999.Min, p999.Max})
	}

	// raw positional slab
	{
		slab := make([]byte, 0, values*valueSize)
		for _, v := range vals {
			slab = append(slab, v...)
		}
		decode := func() []time.Duration {
			durs := make([]time.Duration, gets)
			for i, idx := range order {
				t0 := time.Now()
				_ = append([]byte(nil), slab[idx*valueSize:(idx+1)*valueSize]...)
				durs[i] = time.Since(t0)
			}
			return durs
		}
		p50, p99, p999 := measure(decode)
		add("raw-slab", uint64(len(slab)), p50, p99, p999)
	}

	// block LZ4 per value
	{
		var total uint64
		comps := make([][]byte, values)
		dst := make([]byte, lz4.CompressBlockBound(valueSize))
		for i, v := range vals {
			n, err := lz4.CompressBlock(v, dst, nil)
			if err != nil || n == 0 {
				comps[i] = v
			} else {
				comps[i] = append([]byte(nil), dst[:n]...)
			}
			total += uint64(len(comps[i]))
		}
		out := make([]byte, valueSize)
		decode := func() []time.Duration {
			durs := make([]time.Duration, gets)
			for i, idx := range order {
				t0 := time.Now()
				if _, err := lz4.UncompressBlock(comps[idx], out); err != nil {
					_ = append([]byte(nil), comps[idx]...)
				}
				durs[i] = time.Since(t0)
			}
			return durs
		}
		p50, p99, p999 := measure(decode)
		add("lz4-block", total, p50, p99, p999)
	}

	// per-value zstd
	{
		enc, _ := zstd.NewWriter(nil)
		dec, _ := zstd.NewReader(nil)
		var total uint64
		comps := make([][]byte, values)
		for i, v := range vals {
			comps[i] = enc.EncodeAll(v, nil)
			total += uint64(len(comps[i]))
		}
		decode := func() []time.Duration {
			durs := make([]time.Duration, gets)
			for i, idx := range order {
				t0 := time.Now()
				_, _ = dec.DecodeAll(comps[idx], nil)
				durs[i] = time.Since(t0)
			}
			return durs
		}
		p50, p99, p999 := measure(decode)
		add("zstd", total, p50, p99, p999)
	}

	// Pebble (embedded LSM) point Get
	{
		dir, err := os.MkdirTemp("", "ekbloom-pebble-")
		if err != nil {
			fatal(err)
		}
		defer os.RemoveAll(dir)
		db, err := pebble.Open(filepath.Join(dir, "db"), &pebble.Options{})
		if err != nil {
			fatal(err)
		}
		var total uint64
		var key [8]byte
		for i, v := range vals {
			binary.BigEndian.PutUint64(key[:], uint64(i))
			if err := db.Set(key[:], v, pebble.NoSync); err != nil {
				fatal(err)
			}
			total += uint64(len(v))
		}
		// Warm the block cache.
		for _, idx := range order[:2000] {
			binary.BigEndian.PutUint64(key[:], uint64(idx))
			val, closer, err := db.Get(key[:])
			if err != nil {
				fatal(err)
			}
			_ = val
			closer.Close()
		}
		decode := func() []time.Duration {
			durs := make([]time.Duration, gets)
			for i, idx := range order {
				binary.BigEndian.PutUint64(key[:], uint64(idx))
				t0 := time.Now()
				val, closer, err := db.Get(key[:])
				durs[i] = time.Since(t0)
				if err != nil {
					fatal(err)
				}
				_ = val
				closer.Close()
			}
			return durs
		}
		p50, p99, p999 := measure(decode)
		if err := db.Close(); err != nil {
			fatal(err)
		}
		add("pebble", total, p50, p99, p999)
	}

	// EKBLOOM representations: one body whose logical bytes are the concatenation.
	concat := make([]byte, 0, values*valueSize)
	for _, v := range vals {
		concat = append(concat, v...)
	}
	ekb := []struct {
		name string
		prof ekbloom.Profile
		m    uint32
	}{
		{"ekbloom-m1-root", ekbloom.Profile{Kind: ekbloom.ProfileRoot}, 1},
		{"ekbloom-m1-path4", ekbloom.Profile{Kind: ekbloom.ProfilePath, Depth: 4}, 1},
		{"ekbloom-m2-root", ekbloom.Profile{Kind: ekbloom.ProfileRoot}, 2},
	}
	for _, e := range ekb {
		image, err := ekbloom.WriteBody(e.m, concat, e.prof)
		if err != nil {
			fatal(err)
		}
		r, err := ekbloom.Open(image)
		if err != nil {
			fatal(err)
		}
		// Warm (the in-memory reader materialises once on first random access).
		_, _ = r.ReadAt(0, valueSize)
		decode := func() []time.Duration {
			durs := make([]time.Duration, gets)
			for i, idx := range order {
				off := uint64(idx) * valueSize
				t0 := time.Now()
				b, err := r.ReadAt(off, valueSize)
				durs[i] = time.Since(t0)
				if err != nil || len(b) != valueSize {
					fatal(fmt.Errorf("%s: read at %d: %v", e.name, off, err))
				}
			}
			return durs
		}
		p50, p99, p999 := measure(decode)
		add(e.name, r.PhysicalBytes(), p50, p99, p999)
	}

	report := struct {
		Values     int    `json:"values"`
		ValueBytes int    `json:"value_bytes"`
		Gets       int    `json:"gets"`
		Runs       int    `json:"runs"`
		Statistic  string `json:"statistic"`
		Rows       []row  `json:"rows"`
		Note       string `json:"note"`
	}{values, valueSize, gets, stats.Runs(), "median-of-runs", rows,
		"One random 4 KiB point read, same payloads, 80/20 skew. Get p50/p99/p99.9 are the median across EKB_RUNS runs (min/max retained). EKBLOOM is an in-memory codec; Pebble is an on-disk LSM (block cache warmed). Footprint is bytes/value. Single host, scalar Go — a decision aid, not a portable benchmark."}

	out, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile("../evidence/courts-point.json", append(out, '\n'), 0o644); err != nil {
		fatal(err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "codec\tB/value\tGet p50\tGet p99\tGet p99.9")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%.1f\t%d ns\t%d ns\t%d ns\n", r.Codec, r.BytesPerVal, r.P50ns, r.P99ns, r.P999ns)
	}
	w.Flush()
	fmt.Println("\nwrote ../evidence/courts-point.json")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
