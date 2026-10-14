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
// Usage: go run ./point            (writes ../evidence/courts-point.json)
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

func pct(sorted []time.Duration, perMille int) int64 {
	return sorted[len(sorted)*perMille/1000].Nanoseconds()
}

type row struct {
	Codec       string  `json:"codec"`
	BytesPerVal float64 `json:"bytes_per_value"`
	P50ns       int64   `json:"get_p50_ns"`
	P99ns       int64   `json:"get_p99_ns"`
	P999ns      int64   `json:"get_p999_ns"`
	TotalBytes  uint64  `json:"total_bytes"`
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
	add := func(codec string, total uint64, durs []time.Duration) {
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		rows = append(rows, row{codec, float64(total) / values, pct(durs, 500), pct(durs, 990), pct(durs, 999), total})
	}

	// raw positional slab
	{
		slab := make([]byte, 0, values*valueSize)
		for _, v := range vals {
			slab = append(slab, v...)
		}
		durs := make([]time.Duration, gets)
		for i, idx := range order {
			t0 := time.Now()
			_ = append([]byte(nil), slab[idx*valueSize:(idx+1)*valueSize]...)
			durs[i] = time.Since(t0)
		}
		add("raw-slab", uint64(len(slab)), durs)
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
		durs := make([]time.Duration, gets)
		for i, idx := range order {
			t0 := time.Now()
			if _, err := lz4.UncompressBlock(comps[idx], out); err != nil {
				_ = append([]byte(nil), comps[idx]...)
			}
			durs[i] = time.Since(t0)
		}
		add("lz4-block", total, durs)
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
		durs := make([]time.Duration, gets)
		for i, idx := range order {
			t0 := time.Now()
			_, _ = dec.DecodeAll(comps[idx], nil)
			durs[i] = time.Since(t0)
		}
		add("zstd", total, durs)
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
		if err := db.Close(); err != nil {
			fatal(err)
		}
		add("pebble", total, durs)
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
		add(e.name, r.PhysicalBytes(), durs)
	}

	report := struct {
		Values     int    `json:"values"`
		ValueBytes int    `json:"value_bytes"`
		Gets       int    `json:"gets"`
		Rows       []row  `json:"rows"`
		Note       string `json:"note"`
	}{values, valueSize, gets, rows,
		"One random 4 KiB point read, same payloads, 80/20 skew. EKBLOOM is an in-memory codec; Pebble is an on-disk LSM (block cache warmed). Footprint is bytes/value. Single host, scalar Go — a decision aid, not a portable benchmark."}

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
