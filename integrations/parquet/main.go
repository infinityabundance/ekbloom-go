// Command parquet measures the EKBLOOM machine-9 shared-dictionary codec against a
// *real* Parquet dictionary page for the very same column of skewed, repetitive
// strings.
//
// It builds 20,000 ~100-byte structured config/session strings, writes them to a
// genuine Parquet file with github.com/parquet-go/parquet-go (ZSTD compression,
// RLE_DICTIONARY data pages), and then measures — on the identical records — the
// bytes-per-record and read cost of:
//
//   - Parquet (dictionary page + data pages, whole column read back into values);
//   - EKBLOOM machine 9 (one 540-byte shared trained dictionary per file, one
//     header-less core per value via EncodeRecord);
//   - Zstandard with a dictionary trained by zstd.BuildDict on the same records;
//   - raw (uncompressed).
//
// The result is emitted as a single JSON object on stdout; human-readable
// diagnostics go to stderr so stdout stays machine-parseable.
//
// Usage: go run . > ../evidence/parquet-dict.json
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	ekbloom "github.com/infinityabundance/ekbloom-go"
	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
)

const (
	numRecords = 20000 // rows in the column
	recSize    = 100   // fixed logical bytes per record ("~100 B")
	poolSize   = 512   // distinct values the column is drawn from (skewed)
	trainN     = 2000  // records used to train the EKBLOOM + zstd dictionaries
	samples    = 8192  // timed read samples per codec (>= 4096 as required)
)

// lcg is a deterministic PRNG so the measurement is reproducible.
type lcg uint64

func (s *lcg) next() uint64 {
	*s = lcg(uint64(*s)*6364136223846793005 + 1442695040888963407)
	return uint64(*s) >> 33
}

var (
	users   = []string{"alice", "bob", "carol", "dave", "erin", "frank", "grace", "heidi"}
	regions = []string{"us-east-1", "us-west-2", "eu-west-1", "eu-central-1", "ap-south-1", "ap-northeast-1"}
	roles   = []string{"admin", "reader", "writer", "auditor"}
	status  = []string{"active", "inactive", "pending"}
	teams   = []string{"core", "edge", "data", "sre"}
)

// compose builds one exactly recSize-byte structured config string.
func compose(s *lcg) []byte {
	b := []byte(fmt.Sprintf("user=%s|region=%s|role=%s|status=%s|team=%s|v=%d|",
		users[s.next()%uint64(len(users))],
		regions[s.next()%uint64(len(regions))],
		roles[s.next()%uint64(len(roles))],
		status[s.next()%uint64(len(status))],
		teams[s.next()%uint64(len(teams))],
		s.next()%16))
	for len(b) < recSize {
		b = append(b, byte('a'+s.next()%4))
	}
	return b[:recSize]
}

// skewPick picks an index with an 80/20 skew: 80% of draws target the first 20%.
func skewPick(s *lcg, n int) int {
	hot := n / 5
	if hot < 1 {
		hot = 1
	}
	if s.next()%100 < 80 {
		return int(s.next() % uint64(hot))
	}
	return int(s.next() % uint64(n))
}

// rec is the Parquet row type: a single BYTE_ARRAY (UTF8) column named "value".
type rec struct {
	Value string `parquet:"value"`
}

func mallocs() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Mallocs
}

func p50(durs []time.Duration) time.Duration {
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	return durs[len(durs)/2]
}

func encName(e format.Encoding) string {
	switch e {
	case format.Plain:
		return "PLAIN"
	case format.PlainDictionary:
		return "PLAIN_DICTIONARY"
	case format.RLEDictionary:
		return "RLE_DICTIONARY"
	case format.RLE:
		return "RLE"
	case format.BitPacked:
		return "BIT_PACKED"
	case format.DeltaBinaryPacked:
		return "DELTA_BINARY_PACKED"
	case format.DeltaLengthByteArray:
		return "DELTA_LENGTH_BYTE_ARRAY"
	case format.DeltaByteArray:
		return "DELTA_BYTE_ARRAY"
	case format.ByteStreamSplit:
		return "BYTE_STREAM_SPLIT"
	default:
		return fmt.Sprintf("ENCODING_%d", int32(e))
	}
}

type report struct {
	Records                 int     `json:"records"`
	RecordBytes             int     `json:"record_bytes"`
	ParquetBytesPerRecord   float64 `json:"parquet_bytes_per_record"`
	ParquetBytesTotal       int64   `json:"parquet_bytes_total"`
	EkbloomM9BytesPerRecord float64 `json:"ekbloom_m9_bytes_per_record"`
	ZstdDictBytesPerRecord  float64 `json:"zstd_dict_bytes_per_record"`
	RawBytesPerRecord       float64 `json:"raw_bytes_per_record"`
	EkbloomReadP50Ns        float64 `json:"ekbloom_read_p50_ns"`
	ParquetReadP50Ns        float64 `json:"parquet_read_p50_ns"`
	ParquetReadNsPerValue   float64 `json:"parquet_read_ns_per_value"`
	EkbloomReadAllocations  uint64  `json:"ekbloom_read_allocations"`

	Samples                int      `json:"samples"`
	TrainRecords           int      `json:"train_records"`
	DistinctValues         int      `json:"distinct_values"`
	EkbloomDictBytes       int      `json:"ekbloom_dict_bytes"`
	EkbloomM9PayloadBytes  int      `json:"ekbloom_m9_payload_bytes"`
	ZstdDictBytes          int      `json:"zstd_dict_bytes"`
	ZstdPayloadBytes       int      `json:"zstd_payload_bytes"`
	ParquetDictPageBytes   int64    `json:"parquet_dict_page_bytes"`
	ParquetColumnEncodings []string `json:"parquet_column_encodings"`
	ParquetCompression     string   `json:"parquet_compression"`
	ParquetReadAllocations uint64   `json:"parquet_read_allocations"`
	EkbloomAllocsPerOp     float64  `json:"ekbloom_read_allocations_per_op"`
	ParquetAllocsPerOp     float64  `json:"parquet_read_allocations_per_op"`
	Note                   string   `json:"note"`
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "parquet integration:", err)
		os.Exit(1)
	}
}

func main() {
	var s lcg = 0x5eed1234

	// --- build a pool of distinct record strings, then a skewed column ---------
	pool := make([][]byte, 0, poolSize)
	seen := make(map[string]struct{}, poolSize)
	for len(pool) < poolSize {
		v := compose(&s)
		if _, dup := seen[string(v)]; dup {
			continue
		}
		seen[string(v)] = struct{}{}
		pool = append(pool, v)
	}

	records := make([][]byte, numRecords)
	for i := range records {
		records[i] = pool[skewPick(&s, len(pool))]
	}

	// --- write a real Parquet file with dictionary-encoded data pages ---------
	rows := make([]rec, numRecords)
	for i, r := range records {
		rows[i].Value = string(r)
	}

	dir, err := os.MkdirTemp("", "ekbloom-parquet-")
	fatal(err)
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "column.parquet")

	fatal(parquet.WriteFile[rec](path, rows,
		parquet.Compression(&parquet.Zstd),
		parquet.DefaultEncodingFor(parquet.ByteArray, &parquet.RLEDictionary),
	))

	st, err := os.Stat(path)
	fatal(err)
	parquetTotal := st.Size()

	// --- inspect the written file: prove a dictionary page really exists ------
	pf, err := os.Open(path)
	fatal(err)
	pfile, err := parquet.OpenFile(pf, parquetTotal)
	fatal(err)
	md := pfile.Metadata()
	col := md.RowGroups[0].Columns[0]

	var encodings []string
	var haveDict bool
	for _, e := range col.MetaData.Encoding {
		encodings = append(encodings, encName(e))
		if e == format.RLEDictionary || e == format.PlainDictionary {
			haveDict = true
		}
	}
	var dictPageBytes int64
	if col.MetaData.DictionaryPageOffset > 0 {
		dictPageBytes = col.MetaData.DataPageOffset - col.MetaData.DictionaryPageOffset
		haveDict = true
	}
	fatal(pf.Close())
	if !haveDict {
		fatal(fmt.Errorf("parquet column was not dictionary-encoded (encodings=%v)", encodings))
	}

	// --- EKBLOOM machine 9: one shared trained dictionary + one core per value -
	dict, err := ekbloom.TrainDictionary(records[:trainN])
	fatal(err)
	cores := make([][]byte, numRecords)
	var coreTotal int
	for i, r := range records {
		c, err := dict.EncodeRecord(r)
		fatal(err)
		cores[i] = c
		coreTotal += len(c)
	}
	dictBytes := int(dict.PhysicalBytes()) // 540
	m9PerRec := (float64(coreTotal) + float64(dictBytes)) / float64(numRecords)

	// --- Zstandard with a dictionary trained on the same records --------------
	// BuildDict uses History as the raw dictionary content and Contents to train
	// the entropy tables; we seed it with half the distinct values (a compact,
	// representative dictionary rather than the whole value universe).
	dictContent := make([]byte, 0, 256*recSize)
	for i := 0; i < len(pool) && i < 256; i++ {
		dictContent = append(dictContent, pool[i]...)
	}
	zdict, err := zstd.BuildDict(zstd.BuildDictOptions{
		ID:       1,
		Contents: records[:trainN],
		History:  dictContent,
		Level:    zstd.SpeedDefault,
	})
	fatal(err)
	zenc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderDict(zdict))
	fatal(err)
	zdec, err := zstd.NewReader(nil, zstd.WithDecoderDicts(zdict))
	fatal(err)
	var zTotal int
	for i, r := range records {
		z := zenc.EncodeAll(r, nil)
		zTotal += len(z)
		if i%997 == 0 { // spot-check the dictionary round-trips
			got, derr := zdec.DecodeAll(z, nil)
			fatal(derr)
			if string(got) != string(r) {
				fatal(fmt.Errorf("zstd-dict round-trip mismatch at %d", i))
			}
		}
	}
	zenc.Close()
	zdec.Close()
	zPerRec := (float64(zTotal) + float64(len(zdict))) / float64(numRecords)

	// --- raw ------------------------------------------------------------------
	rawPerRec := float64(recSize)

	// --- read cost: EKBLOOM DecodeRecord per value (p50) ----------------------
	// verify correctness once before timing
	for i := 0; i < numRecords; i += 1024 {
		got, err := dict.DecodeRecord(cores[i])
		fatal(err)
		if string(got) != string(records[i]) {
			fatal(fmt.Errorf("ekbloom round-trip mismatch at %d", i))
		}
	}
	ekDurs := make([]time.Duration, samples)
	ekM0 := mallocs()
	for i := 0; i < samples; i++ {
		idx := i % numRecords
		t0 := time.Now()
		out, err := dict.DecodeRecord(cores[idx])
		fatal(err)
		if len(out) != recSize {
			fatal(fmt.Errorf("ekbloom decode length %d != %d", len(out), recSize))
		}
		ekDurs[i] = time.Since(t0)
	}
	ekAllocs := mallocs() - ekM0
	ekP50 := p50(ekDurs)

	// --- read cost: Parquet whole column read back into values (p50) ----------
	_, err = parquet.ReadFile[rec](path) // warm up page cache / decoder
	fatal(err)
	pqDurs := make([]time.Duration, samples)
	pqM0 := mallocs()
	for i := 0; i < samples; i++ {
		t0 := time.Now()
		got, err := parquet.ReadFile[rec](path)
		if err != nil {
			fatal(err)
		}
		if len(got) != numRecords {
			fatal(fmt.Errorf("parquet read %d rows != %d", len(got), numRecords))
		}
		pqDurs[i] = time.Since(t0)
	}
	pqAllocs := mallocs() - pqM0
	pqP50 := p50(pqDurs)

	// --- diagnostics to stderr so stdout stays pure JSON ----------------------
	fmt.Fprintf(os.Stderr, "records=%d record_bytes=%d distinct=%d samples=%d\n",
		numRecords, recSize, len(pool), samples)
	fmt.Fprintf(os.Stderr, "%-22s %10s %10s\n", "codec", "B/record", "read p50")
	fmt.Fprintf(os.Stderr, "%-22s %10.2f %8d ns\n", "parquet(rle_dict)", float64(parquetTotal)/float64(numRecords), pqP50.Nanoseconds())
	fmt.Fprintf(os.Stderr, "%-22s %10.2f %8d ns\n", "ekbloom-m9", m9PerRec, ekP50.Nanoseconds())
	fmt.Fprintf(os.Stderr, "%-22s %10.2f %10s\n", "zstd-dict", zPerRec, "-")
	fmt.Fprintf(os.Stderr, "%-22s %10.2f %10s\n", "raw", rawPerRec, "-")
	fmt.Fprintf(os.Stderr, "parquet column encodings=%v dict_page_bytes=%d codec=%s\n",
		encodings, dictPageBytes, col.MetaData.Codec.String())

	rep := report{
		Records:                 numRecords,
		RecordBytes:             recSize,
		ParquetBytesPerRecord:   float64(parquetTotal) / float64(numRecords),
		ParquetBytesTotal:       parquetTotal,
		EkbloomM9BytesPerRecord: m9PerRec,
		ZstdDictBytesPerRecord:  zPerRec,
		RawBytesPerRecord:       rawPerRec,
		EkbloomReadP50Ns:        float64(ekP50.Nanoseconds()),
		ParquetReadP50Ns:        float64(pqP50.Nanoseconds()),
		ParquetReadNsPerValue:   float64(pqP50.Nanoseconds()) / float64(numRecords),
		EkbloomReadAllocations:  ekAllocs,

		Samples:                samples,
		TrainRecords:           trainN,
		DistinctValues:         len(pool),
		EkbloomDictBytes:       dictBytes,
		EkbloomM9PayloadBytes:  coreTotal,
		ZstdDictBytes:          len(zdict),
		ZstdPayloadBytes:       zTotal,
		ParquetDictPageBytes:   dictPageBytes,
		ParquetColumnEncodings: encodings,
		ParquetCompression:     col.MetaData.Codec.String(),
		ParquetReadAllocations: pqAllocs,
		EkbloomAllocsPerOp:     float64(ekAllocs) / float64(samples),
		ParquetAllocsPerOp:     float64(pqAllocs) / float64(samples),
		Note: "Parquet = a per-column-chunk RLE_DICTIONARY page (dedupes identical " +
			"values, ZSTD-framed). EKBLOOM m9 = one 540 B shared order-0 dictionary per " +
			"file + a header-less core per value (amortised). zstd-dict = zstd.BuildDict " +
			"trained on the same records, dictionary bytes amortised. Read cost is p50: " +
			"EKBLOOM DecodeRecord per value vs. reading the full Parquet column into " +
			"[]string (which includes opening the file).",
	}

	out, err := json.MarshalIndent(rep, "", "  ")
	fatal(err)
	os.Stdout.Write(append(out, '\n'))
}
