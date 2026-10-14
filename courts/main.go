// Command courts measures the RocksDB-style skewed small-record court: 100-byte
// values, an 80/20 key skew. It compares EKBLOOM machine 9 (shared trained
// dictionary) against raw, block LZ4, plain Zstandard and Zstandard-with-dictionary
// on the same training set, reporting bytes/record and the decode p50.
//
// Usage: go run .            (writes ../evidence/courts.json)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	ekbloom "github.com/infinityabundance/ekbloom-go"
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
	Codec       string  `json:"codec"`
	BytesPerRec float64 `json:"bytes_per_record"`
	P50ns       float64 `json:"decode_p50_ns"`
	Ratio       float64 `json:"ratio_vs_raw"`
}

func p50(durs []time.Duration) time.Duration {
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	return durs[len(durs)/2]
}

func main() {
	var s lcg = 0x1234567

	records := make([][]byte, numRecords)
	for i := range records {
		records[i] = makeRecord(&s)
	}
	train := records[:numTrain]

	var rows []row

	// raw
	{
		var total int
		for _, r := range records {
			total += len(r)
		}
		durs := make([]time.Duration, gets)
		for i := 0; i < gets; i++ {
			idx := skewIndex(&s, numRecords)
			t0 := time.Now()
			_ = append([]byte(nil), records[idx]...)
			durs[i] = time.Since(t0)
		}
		rows = append(rows, row{"raw", float64(total) / numRecords, float64(p50(durs).Nanoseconds()), 1.0})
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
		durs := make([]time.Duration, gets)
		out := make([]byte, recSize)
		for i := 0; i < gets; i++ {
			idx := skewIndex(&s, numRecords)
			t0 := time.Now()
			if _, err := lz4.UncompressBlock(comps[idx], out); err != nil {
				_ = append([]byte(nil), comps[idx]...)
			}
			durs[i] = time.Since(t0)
		}
		rows = append(rows, row{"lz4", float64(total) / numRecords, float64(p50(durs).Nanoseconds()), 0})
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
		durs := make([]time.Duration, gets)
		for i := 0; i < gets; i++ {
			idx := skewIndex(&s, numRecords)
			t0 := time.Now()
			_, _ = dec.DecodeAll(comps[idx], nil)
			durs[i] = time.Since(t0)
		}
		rows = append(rows, row{"zstd", float64(total) / numRecords, float64(p50(durs).Nanoseconds()), 0})
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
		durs := make([]time.Duration, gets)
		for i := 0; i < gets; i++ {
			idx := skewIndex(&s, numRecords)
			t0 := time.Now()
			_, _ = dec.DecodeAll(comps[idx], nil)
			durs[i] = time.Since(t0)
		}
		rows = append(rows, row{"zstd-dict", float64(total) / numRecords, float64(p50(durs).Nanoseconds()), 0})
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
		durs := make([]time.Duration, gets)
		for i := 0; i < gets; i++ {
			idx := skewIndex(&s, numRecords)
			t0 := time.Now()
			if _, err := dict.DecodeRecord(cores[idx]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			durs[i] = time.Since(t0)
		}
		rows = append(rows, row{"ekbloom-m9", float64(total) / numRecords, float64(p50(durs).Nanoseconds()), 0})
	}

	// ratio vs raw
	raw := rows[0].BytesPerRec
	for i := range rows {
		rows[i].Ratio = rows[i].BytesPerRec / raw
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "codec\tB/record\tvs raw\tdecode p50\n")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%.1f\t%.3f\t%.0f ns\n", r.Codec, r.BytesPerRec, r.Ratio, r.P50ns)
	}
	w.Flush()

	report := struct {
		Records     int   `json:"records"`
		TrainSet    int   `json:"train_records"`
		RecordBytes int   `json:"record_bytes"`
		Gets        int   `json:"gets"`
		Rows        []row `json:"rows"`
	}{numRecords, numTrain, recSize, gets, rows}
	out, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile("../evidence/courts.json", append(out, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write evidence:", err)
	}
	fmt.Println("\nwrote ../evidence/courts.json")
}
