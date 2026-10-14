// Command corpusbench measures the concealed-expansion ratio (E_V = physical / N)
// and single-byte read latency of the Go EKBLOOM implementation on a directory of
// real corpus files, writing a JSON report. The same files are measured by the
// Rust counterpart (tools/fixturegen/src/bin/corpusbench.rs) so physical_bytes and
// E_V can be compared machine-for-machine.
//
// Usage: corpusbench <corpus-dir> <out.json>
//
// Environment: EKB_CORPORA_CAP bounds the leading slice taken from each file
// (default 4 MiB, matching the reference's corpus cap).
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	ekbloom "github.com/infinityabundance/ekbloom-go"
	"github.com/infinityabundance/ekbloom-go/internal/blake3"
	"github.com/infinityabundance/ekbloom-go/internal/tools/stat"
)

const samples = 4096

type row struct {
	Corpus      string  `json:"corpus"`
	File        string  `json:"file"`
	Bytes       int     `json:"bytes"`
	SliceBLAKE3 string  `json:"slice_blake3"`
	Machine     uint32  `json:"machine_id"`
	LogicalLen  uint64  `json:"logical_len"`
	Physical    uint64  `json:"physical_bytes"`
	EV          float64 `json:"E_V"`
	P50ns       int64   `json:"byte_at_p50_ns"`
	P99ns       int64   `json:"byte_at_p99_ns"`
	P999ns      int64   `json:"byte_at_p999_ns"`
	P50Min      int64   `json:"byte_at_p50_ns_min"`
	P50Max      int64   `json:"byte_at_p50_ns_max"`
	P99Min      int64   `json:"byte_at_p99_ns_min"`
	P99Max      int64   `json:"byte_at_p99_ns_max"`
	P999Min     int64   `json:"byte_at_p999_ns_min"`
	P999Max     int64   `json:"byte_at_p999_ns_max"`
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

func capBytes() int {
	const def = 4 << 20
	if v := os.Getenv("EKB_CORPORA_CAP"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func main() {
	if len(os.Args) < 3 {
		die("usage: corpusbench <corpus-dir> <out.json>")
	}
	dir, out := os.Args[1], os.Args[2]

	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		die("%v", err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		die("no files under %s", dir)
	}

	cap := capBytes()
	root := ekbloom.Profile{Kind: ekbloom.ProfileRoot}
	machines := []uint32{1, 2}

	var rows []row
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			die("%v", err)
		}
		if len(data) > cap {
			data = data[:cap]
		}
		if len(data) == 0 {
			continue
		}
		rel, _ := filepath.Rel(dir, path)
		corpus := filepath.Base(filepath.Dir(path))
		sum := blake3.Sum(data)

		for _, m := range machines {
			image, err := ekbloom.WriteBody(m, data, root)
			if err != nil {
				die("%s m%d: write: %v", rel, m, err)
			}
			r, err := ekbloom.Open(image)
			if err != nil {
				die("%s m%d: open: %v", rel, m, err)
			}
			// Warm the read path (the in-memory reader materialises once), then
			// sample single-byte reads at deterministic pseudo-random offsets. The
			// reported p50/p99/p99.9 are the median across R = EKB_RUNS runs (default
			// 5; 3 in CI), with the observed min/max retained; physical_bytes/E_V and
			// the BLAKE3 slice hash are deterministic.
			if _, err := r.ByteAt(0); err != nil {
				die("%v", err)
			}
			n := r.LogicalLen()
			q := stat.Sample(func(run int) []time.Duration {
				durs := make([]time.Duration, samples)
				seed := uint64(0x9E3779B97F4A7C15) + uint64(run)*0x9E37_79B9_7F4A_7C15
				for i := 0; i < samples; i++ {
					seed = seed*6364136223846793005 + 1442695040888963407
					off := (seed >> 33) % n
					t0 := time.Now()
					b, err := r.ByteAt(off)
					durs[i] = time.Since(t0)
					if err != nil {
						die("%v", err)
					}
					if b != data[off] {
						die("%s m%d: ByteAt(%d) mismatch", rel, m, off)
					}
				}
				return durs
			}, 500, 990, 999)
			rows = append(rows, row{
				Corpus: corpus, File: rel, Bytes: len(data),
				SliceBLAKE3: fmt.Sprintf("%x", sum),
				Machine:     m, LogicalLen: n, Physical: r.PhysicalBytes(),
				EV:    float64(r.PhysicalBytes()) / float64(n),
				P50ns: q[0].Median, P99ns: q[1].Median, P999ns: q[2].Median,
				P50Min: q[0].Min, P50Max: q[0].Max,
				P99Min: q[1].Min, P99Max: q[1].Max,
				P999Min: q[2].Min, P999Max: q[2].Max,
			})
		}
	}

	report := struct {
		Cap       int    `json:"corpus_cap_bytes"`
		Samples   int    `json:"byte_at_samples"`
		Runs      int    `json:"runs"`
		Statistic string `json:"statistic"`
		Machines  []int  `json:"machines"`
		Rows      []row  `json:"rows"`
		Note      string `json:"note"`
	}{cap, samples, stat.Runs(), "median-of-runs", []int{1, 2}, rows,
		"E_V = physical_bytes / logical_len (lower is smaller). byte_at p50/p99/p99.9 are the median across EKB_RUNS runs (min/max retained). Go numbers; compare against evidence/corpus-rust.json for the same files."}

	blob, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(out, append(blob, '\n'), 0o644); err != nil {
		die("%v", err)
	}
	fmt.Printf("wrote %s (%d rows)\n", out, len(rows))
}
