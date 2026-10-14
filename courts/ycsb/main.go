// Command ycsb answers the storage question "where does the machine-9 dictionary
// actually matter?" It replays YCSB-style traces (zipfian keys) over the same
// records held in different representations and reports, per workload, the
// footprint, throughput, and read tail:
//
//   - raw                 — in-memory slab (the latency floor)
//   - machine2-per-record — OrANS-RANS0 with a 512-byte model header per record
//     (the ~590 B/record baseline the dictionary beats)
//   - zstd-dict           — Zstandard with a trained dictionary
//   - ekbloom-m9          — OrANS-DICT: one shared trained dictionary
//
// and the same four payloads stored in a Pebble embedded KV store ("value-log
// adapter": values are encoded by the codec and decoded on Get), to show the
// codec inside a real store.
//
// Workloads: C (read-only), B (95% read / 5% update), A (50/50).
//
// Usage: go run ./ycsb            (writes ../evidence/ycsb.json)
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/cockroachdb/pebble"
	ekbloom "github.com/infinityabundance/ekbloom-go"
	"github.com/klauspost/compress/zstd"
)

const (
	records     = 20000
	recordBytes = 100
	ops         = 50000
	zipfTheta   = 0.99
	trainFrac   = 4 // 1/trainFrac of the records train the dictionary
)

var tokens = []string{"id=", "user", "kind=", "alpha", "beta", "gamma", "region=",
	"us-east-1", "eu-west-1", "ap-south-1", "score=", "000", "111", "222",
	"status=", "active", "idle", "|"}

// record builds one text-like record of exactly recordBytes bytes.
func record(seed int) []byte {
	out := make([]byte, 0, recordBytes+16)
	k := uint64(seed)
	for len(out) < recordBytes {
		k = k*6364136223846793005 + 1442695040888963407
		out = append(out, tokens[(k>>33)%uint64(len(tokens))]...)
	}
	return out[:recordBytes]
}

// codec encodes and decodes one value.
type codec interface {
	encode(v []byte) ([]byte, error)
	decode(b []byte) ([]byte, error)
}

// decoderInto is implemented by codecs that can decode into a caller-provided
// buffer with 0 allocations on the steady path (the reusable machine-9 decoder).
type decoderInto interface {
	decodeInto(dst, b []byte) (int, error)
}

type rawCodec struct{}

func (rawCodec) encode(v []byte) ([]byte, error) { return v, nil }
func (rawCodec) decode(b []byte) ([]byte, error) { return b, nil }

type m2Codec struct{ m ekbloom.Machine }

func (c m2Codec) encode(v []byte) ([]byte, error) { return c.m.Encode(v) }
func (c m2Codec) decode(b []byte) ([]byte, error) {
	return ekbloom.DecodeCore(ekbloom.MachineRans0, b, uint64(recordBytes))
}

type zstdDictCodec struct {
	enc *zstd.Encoder
	dec *zstd.Decoder
}

func (c zstdDictCodec) encode(v []byte) ([]byte, error) { return c.enc.EncodeAll(v, nil), nil }
func (c zstdDictCodec) decode(b []byte) ([]byte, error) { return c.dec.DecodeAll(b, nil) }

type ekbDictCodec struct {
	d   ekbloom.Dictionary
	dec *ekbloom.DictDecoder
}

func (c ekbDictCodec) encode(v []byte) ([]byte, error) { return c.d.EncodeRecord(v) }
func (c ekbDictCodec) decode(b []byte) ([]byte, error) { return c.dec.Decode(b) }

// decodeInto reuses the prepared rANS context and writes straight into dst — the
// 0-alloc steady-state path the court measures.
func (c ekbDictCodec) decodeInto(dst, b []byte) (int, error) {
	return c.dec.DecodeInto(dst, b)
}

// store holds the payloads and serves point reads/updates.
type store interface {
	name() string
	total() uint64
	get(i int) ([]byte, error)
	put(i int, v []byte) error
	close()
}

// memStore is an in-memory slice of encoded values.
type memStore struct {
	n       string
	c       codec
	buf     [][]byte
	bytes   uint64
	scratch []byte // reused decode destination when the codec supports decodeInto
}

func newMemStore(n string, c codec) *memStore {
	s := &memStore{n: n, c: c, buf: make([][]byte, records)}
	if _, ok := c.(decoderInto); ok {
		s.scratch = make([]byte, recordBytes)
	}
	return s
}
func (s *memStore) name() string  { return s.n }
func (s *memStore) total() uint64 { return s.bytes }
func (s *memStore) close()        {}
func (s *memStore) put(i int, v []byte) error {
	b, err := s.c.encode(v)
	if err != nil {
		return err
	}
	s.bytes -= uint64(len(s.buf[i]))
	s.buf[i] = b
	s.bytes += uint64(len(b))
	return nil
}
func (s *memStore) get(i int) ([]byte, error) {
	if di, ok := s.c.(decoderInto); ok {
		n, err := di.decodeInto(s.scratch, s.buf[i])
		if err != nil {
			return nil, err
		}
		return s.scratch[:n], nil
	}
	return s.c.decode(s.buf[i])
}

// pebbleStore is a "value-log adapter": encoded values in a Pebble KV store,
// decoded on Get.
type pebbleStore struct {
	n     string
	c     codec
	db    *pebble.DB
	bytes uint64
}

func newPebbleStore(n string, c codec) (*pebbleStore, error) {
	dir, err := os.MkdirTemp("", "ekbloom-ycsb-")
	if err != nil {
		return nil, err
	}
	db, err := pebble.Open(filepath.Join(dir, "db"), &pebble.Options{})
	if err != nil {
		return nil, err
	}
	return &pebbleStore{n: n, c: c, db: db}, nil
}

var keyBuf [8]byte

func (s *pebbleStore) name() string  { return s.n }
func (s *pebbleStore) total() uint64 { return s.bytes }
func (s *pebbleStore) put(i int, v []byte) error {
	b, err := s.c.encode(v)
	if err != nil {
		return err
	}
	binary.BigEndian.PutUint64(keyBuf[:], uint64(i))
	// Subtract the previous value's contribution if present.
	if old, closer, err := s.db.Get(keyBuf[:]); err == nil {
		s.bytes -= uint64(len(old))
		closer.Close()
	}
	s.bytes += uint64(len(b))
	return s.db.Set(keyBuf[:], b, pebble.NoSync)
}
func (s *pebbleStore) get(i int) ([]byte, error) {
	binary.BigEndian.PutUint64(keyBuf[:], uint64(i))
	b, closer, err := s.db.Get(keyBuf[:])
	if err != nil {
		return nil, err
	}
	cp := append([]byte(nil), b...)
	closer.Close()
	return s.c.decode(cp)
}
func (s *pebbleStore) close() { _ = s.db.Close() }

func zipfCDF(n int, theta float64) []float64 {
	cdf := make([]float64, n)
	zeta := 0.0
	for i := 1; i <= n; i++ {
		zeta += 1 / math.Pow(float64(i), theta)
	}
	acc := 0.0
	for i := 1; i <= n; i++ {
		acc += 1 / math.Pow(float64(i), theta)
		cdf[i-1] = acc / zeta
	}
	return cdf
}

func sampleZipf(cdf []float64, u float64) int {
	lo, hi := 0, len(cdf)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if cdf[mid] < u {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

type row struct {
	Store       string  `json:"store"`
	Workload    string  `json:"workload"`
	BytesPerR   float64 `json:"bytes_per_record"`
	OpsPerSec   float64 `json:"ops_per_s"`
	ReadP50     int64   `json:"read_p50_ns"`
	ReadP99     int64   `json:"read_p99_ns"`
	AllocsPerOp float64 `json:"allocs_per_op"`
}

func main() {
	src := make([][]byte, records)
	for i := range src {
		src[i] = record(i)
	}
	// Trained dictionary over a slice of the corpus (shared by zstd-dict and m9).
	dictContent := make([]byte, 0, records/trainFrac*recordBytes)
	for i := 0; i < records/trainFrac; i++ {
		dictContent = append(dictContent, src[i]...)
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderDictRaw(1, dictContent))
	check(err)
	dec, err := zstd.NewReader(nil, zstd.WithDecoderDictRaw(1, dictContent))
	check(err)
	m9, err := ekbloom.TrainDictionary(src[:records/trainFrac])
	check(err)
	// One reusable decoder for the shared dictionary: the order-0 model is fixed,
	// so its rANS context is built once and reused for every record.
	m9dec, err := ekbloom.NewDictDecoder(m9)
	check(err)
	m2, _ := ekbloom.MachineByID(ekbloom.MachineRans0)

	build := func() []store {
		var ss []store
		ss = append(ss, newMemStore("raw", rawCodec{}))
		ss = append(ss, newMemStore("machine2-per-record", m2Codec{m2}))
		ss = append(ss, newMemStore("zstd-dict", zstdDictCodec{enc, dec}))
		ss = append(ss, newMemStore("ekbloom-m9", ekbDictCodec{m9, m9dec}))
		for _, cs := range []struct {
			n string
			c codec
		}{
			{"pebble+raw", rawCodec{}},
			{"pebble+zstd-dict", zstdDictCodec{enc, dec}},
			{"pebble+ekbloom-m9", ekbDictCodec{m9, m9dec}},
		} {
			ps, err := newPebbleStore(cs.n, cs.c)
			check(err)
			ss = append(ss, ps)
		}
		return ss
	}
	stores := build()
	for _, s := range stores {
		for i, v := range src {
			check(s.put(i, v))
		}
	}

	// Steady-state decode allocations per op, measured on the same get path the
	// trace uses. The reusable machine-9 decoder reuses its buffer -> 0.
	allocs := make(map[string]float64, len(stores))
	for _, s := range stores {
		allocs[s.name()] = testing.AllocsPerRun(1000, func() { _, _ = s.get(0) })
	}

	// YCSB-style zipfian key trace + op-type trace (deterministic).
	cdf := zipfCDF(records, zipfTheta)
	keys := make([]int, ops)
	kind := make([]float64, ops)
	ks := uint64(0x1234)
	for i := range keys {
		ks = ks*6364136223846793005 + 1442695040888963407
		keys[i] = sampleZipf(cdf, float64((ks>>33)%1_000_000)/1_000_000)
		ks = ks*6364136223846793005 + 1442695040888963407
		kind[i] = float64((ks >> 33) % 100)
	}
	update := func(pct float64) func(float64) bool { return func(k float64) bool { return k < pct } }

	workloads := []struct {
		name  string
		isUpd func(float64) bool
	}{
		{"C-read-only", update(0)},
		{"B-95/5", update(5)},
		{"A-50/50", update(50)},
	}

	var rows []row
	for _, w := range workloads {
		for _, s := range stores {
			// Warm.
			for i := 0; i < 1000; i++ {
				_, _ = s.get(keys[i])
			}
			reads := make([]time.Duration, 0, ops)
			t0 := time.Now()
			for i := 0; i < ops; i++ {
				if w.isUpd(kind[i]) {
					check(s.put(keys[i], record(keys[i]+i+1)))
					continue
				}
				r0 := time.Now()
				got, err := s.get(keys[i])
				d := time.Since(r0)
				if err != nil || len(got) != recordBytes {
					panic(fmt.Sprintf("%s: get(%d): err=%v len=%d", s.name(), keys[i], err, len(got)))
				}
				reads = append(reads, d)
			}
			elapsed := time.Since(t0).Seconds()
			sort.Slice(reads, func(i, j int) bool { return reads[i] < reads[j] })
			rows = append(rows, row{
				Store: s.name(), Workload: w.name,
				BytesPerR:   float64(s.total()) / records,
				OpsPerSec:   float64(ops) / elapsed,
				ReadP50:     reads[len(reads)/2].Nanoseconds(),
				ReadP99:     reads[len(reads)*99/100].Nanoseconds(),
				AllocsPerOp: allocs[s.name()],
			})
		}
	}
	for _, s := range stores {
		s.close()
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "workload\tstore\tB/rec\tops/s\tallocs/op\tread p50\tread p99")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%.1f\t%.0f\t%.0f\t%d ns\t%d ns\n", r.Workload, r.Store, r.BytesPerR, r.OpsPerSec, r.AllocsPerOp, r.ReadP50, r.ReadP99)
	}
	w.Flush()

	report := struct {
		Records    int    `json:"records"`
		RecordSize int    `json:"record_bytes"`
		Ops        int    `json:"ops_per_row"`
		Rows       []row  `json:"rows"`
		Note       string `json:"note"`
	}{records, recordBytes, ops, rows,
		"YCSB-style zipfian (theta=0.99), deterministic trace. Pebble uses NoSync. EKBLOOM is an in-memory codec; the pebble+* rows place the same codec inside an embedded LSM value log. allocs_per_op is testing.AllocsPerRun over the decode path: machine-9 uses the reusable DictDecoder.DecodeInto with a reused buffer (0 allocs), while the other in-memory rows allocate a fresh destination per read. Single host, scalar Go."}
	out, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile("../evidence/ycsb.json", append(out, '\n'), 0o644); err != nil {
		check(err)
	}
	fmt.Println("\nwrote ../evidence/ycsb.json")
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
