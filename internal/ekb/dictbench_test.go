package ekb

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/infinityabundance/ekbloom-go/internal/tools/stat"
)

// dictParts is the small, repetitive token alphabet the reference uses to build
// its ≈100-byte text-like records for the shared-dictionary study.
var dictParts = []string{
	"id=", "user", "kind=", "alpha", "beta", "gamma", "region=", "us-east-1",
	"eu-west-1", "ap-south-1", "score=", "000", "111", "222", "status=", "active",
	"idle", "|",
}

// makeDictRecord builds one text-like record of exactly target bytes, seeded by
// seed — byte-for-byte the reference example's generator.
func makeDictRecord(seed, target int) []byte {
	out := make([]byte, 0, target+16)
	k := uint64(seed)
	for len(out) < target {
		k = k*6364136223846793005 + 1442695040888963407
		out = append(out, dictParts[(k>>33)%uint64(len(dictParts))]...)
	}
	return out[:target]
}

func dictCorpus(n, size int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = makeDictRecord(i, size)
	}
	return out
}

// dictBreakEven returns the first record count at which `shared + a` overtakes
// `b` (both cumulative), or 0 if it never does — the reference's break_even.
func dictBreakEven(shared uint64, a, b []uint64) int {
	sa := shared
	var sb uint64
	for i := range a {
		sa += a[i]
		sb += b[i]
		if sa < sb {
			return i + 1
		}
	}
	return 0
}

func dictP50(d []time.Duration) time.Duration {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	if len(d) == 0 {
		return 0
	}
	return d[len(d)/2]
}

// methodRow is one row of the dictionary study; decode_p50_ns is the median across
// R = EKB_RUNS runs (default 5; 3 in CI), with min/max retained.
type methodRow struct {
	Method       string  `json:"method"`
	TotalBytes   uint64  `json:"total_bytes"`
	BytesPerRec  float64 `json:"bytes_per_record"`
	DecodeP50ns  int64   `json:"decode_p50_ns"`
	DecodeP50Min int64   `json:"decode_p50_ns_min"`
	DecodeP50Max int64   `json:"decode_p50_ns_max"`
	Note         string  `json:"note,omitempty"`
}

// TestDictionaryBreakEven reproduces the reference's P5 shared-dictionary study:
// on 4096 text-like ~100-byte records, machine 9 (one shared trained dictionary +
// header-less per-record cores) versus per-record machine 2 (a 512-byte model
// header in *every* core). It asserts the shared dictionary wins in space, breaks
// even at the reference's 2 records, and writes evidence/dictionary.json. The
// decode p50s are the median across R = EKB_RUNS runs.
func TestDictionaryBreakEven(t *testing.T) {
	const records, recBytes = 4096, 100
	corpus := dictCorpus(records, recBytes)

	dict, err := TrainDictionary(corpus)
	if err != nil {
		t.Fatal(err)
	}
	RegisterDictionary(dict)

	m2, ok := MachineByID(MachineRans0)
	if !ok {
		t.Fatal("machine 2 not registered")
	}

	dictCore := make([]uint64, records)
	m2Core := make([]uint64, records)
	raw := make([]uint64, records)

	// Framed machine-9 cores (reference framing + header-less core), pre-encoded.
	framed := make([][]byte, records)
	m2cores := make([][]byte, records)

	for i, rec := range corpus {
		m2c, err := m2.Encode(rec)
		if err != nil {
			t.Fatalf("m2 encode %d: %v", i, err)
		}
		m2cores[i] = m2c
		got2, err := decodeAll(m2, m2c, uint64(len(rec)))
		if err != nil || !bytes.Equal(got2, rec) {
			t.Fatalf("m2 round-trip %d failed: %v", i, err)
		}

		core, err := dict.EncodeRecord(rec)
		if err != nil {
			t.Fatalf("dict encode %d: %v", i, err)
		}
		f := frameReference(dict, core)
		framed[i] = f
		if got, err := dict.DecodeRecord(core); err != nil || !bytes.Equal(got, rec) {
			t.Fatalf("dict round-trip %d failed: %v", i, err)
		}

		dictCore[i] = uint64(len(f))
		m2Core[i] = uint64(len(m2c))
		raw[i] = uint64(len(rec))
	}

	sum := func(v []uint64) uint64 {
		var s uint64
		for _, x := range v {
			s += x
		}
		return s
	}
	shared := dict.PhysicalBytes() // 540
	dictTotal := shared + sum(dictCore)
	m2Total := sum(m2Core)
	rawTotal := sum(raw)

	be := dictBreakEven(shared, dictCore, m2Core)
	if be != 2 {
		t.Fatalf("break-even vs machine 2 = %d records, want 2", be)
	}
	if dictTotal >= m2Total {
		t.Fatalf("shared dictionary did not win: %d >= %d", dictTotal, m2Total)
	}

	// Decode latency: one prepared machine-9 context reused across records (the
	// shared-model path), against per-record machine-2 decode (each core rebuilds
	// its model). Encoding is outside the timed region, as in the reference. Each of
	// the R runs yields a per-record p50; the reported p50 is the median across
	// runs, with min/max retained.
	ctx, err := dictContextFromCore(framed[0])
	if err != nil {
		t.Fatal(err)
	}
	measureDict := func() []time.Duration {
		lat := make([]time.Duration, 0, len(framed))
		for _, f := range framed {
			s := Snapshot{State: binary.LittleEndian.Uint32(f[ctx.recordOffset+4:]), Cursor: ctx.headerBytes()}
			t0 := time.Now()
			for i := uint64(0); i < ctx.logicalLen; i++ {
				if _, err := ctx.reveal(f, &s); err != nil {
					t.Fatal(err)
				}
			}
			lat = append(lat, time.Since(t0))
		}
		return lat
	}
	measureM2 := func() []time.Duration {
		lat := make([]time.Duration, 0, len(m2cores))
		for i := range m2cores {
			t0 := time.Now()
			p, err := prepare(m2, m2cores[i])
			if err != nil {
				t.Fatal(err)
			}
			s, err := m2.Init(m2cores[i])
			if err != nil {
				t.Fatal(err)
			}
			for j := uint64(0); j < uint64(len(corpus[i])); j++ {
				if _, err := p.reveal(m2cores[i], &s); err != nil {
					t.Fatal(err)
				}
			}
			lat = append(lat, time.Since(t0))
		}
		return lat
	}

	dictStat := stat.Sample(func(int) []time.Duration { return measureDict() }, 500)[0]
	m2Stat := stat.Sample(func(int) []time.Duration { return measureM2() }, 500)[0]

	report := struct {
		Records             int         `json:"records"`
		RecordSize          int         `json:"record_bytes"`
		Runs                int         `json:"runs"`
		Statistic           string      `json:"statistic"`
		Methods             []methodRow `json:"methods"`
		BreakEvenVsMachine2 int         `json:"break_even_vs_machine2_records"`
		SharedDictBytes     uint64      `json:"shared_dictionary_bytes"`
		Note                string      `json:"note"`
	}{
		Records:             records,
		RecordSize:          recBytes,
		Runs:                stat.Runs(),
		Statistic:           "median-of-runs",
		BreakEvenVsMachine2: be,
		SharedDictBytes:     shared,
	}
	add := func(method string, total uint64, p50 stat.Stats[int64], note string) {
		report.Methods = append(report.Methods, methodRow{
			Method: method, TotalBytes: total, BytesPerRec: float64(total) / records,
			DecodeP50ns: p50.Median, DecodeP50Min: p50.Min, DecodeP50Max: p50.Max, Note: note,
		})
	}
	add("ekbloom-m9", dictTotal, dictStat, "one 540-byte shared dictionary + header-less cores")
	add("machine2-per-record", m2Total, m2Stat, "a 516-byte model header in every core")
	add("raw", rawTotal, stat.Stats[int64]{}, "uncompressed records")
	report.Note = "Reproduces the reference P5 study (docs/DICTIONARY.md). Every row is asserted byte-exact before timing. decode_p50_ns is the median across EKB_RUNS runs (min/max retained)."

	out, _ := json.MarshalIndent(report, "", "  ")
	if err := writeTimingEvidence("dictionary.json", append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("m9 %.1f B/rec vs m2 %.1f B/rec; break-even %d records; m9 decode p50=%dns(%d..%d) m2 p50=%dns(%d..%d)",
		float64(dictTotal)/records, float64(m2Total)/records, be,
		dictStat.Median, dictStat.Min, dictStat.Max, m2Stat.Median, m2Stat.Min, m2Stat.Max)
}

// TestWriteBodyWithDict proves the reference-framed machine-9 write/open pair:
// WriteBodyWithDict emits a core that references a shared dictionary, OpenWithDict
// resolves it, and the whole file reads back byte-exact and verifies.
func TestWriteBodyWithDict(t *testing.T) {
	corpus := dictCorpus(64, 100)
	dict, err := TrainDictionary(corpus)
	if err != nil {
		t.Fatal(err)
	}
	src := dictCorpus(1, 100)[0]

	image, err := WriteBodyWithDict(src, rootProfile(), dict)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyDeep(image); err != nil {
		t.Fatalf("verify: %v", err)
	}
	r, err := OpenWithDict(image, dict)
	if err != nil {
		t.Fatalf("open with dict: %v", err)
	}
	if r.MachineID() != MachineDict {
		t.Fatalf("machine = %d, want 9", r.MachineID())
	}
	got, err := r.ReadAt(0, uint64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("reference-dictionary round-trip mismatch")
	}

	// An unknown/unregistered dictionary id is a typed error, never a wrong-model
	// decode.
	other, err := TrainDictionary([][]byte{[]byte("a completely different corpus for a distinct id")})
	if err != nil {
		t.Fatal(err)
	}
	if other.ID() == dict.ID() {
		t.Fatal("expected distinct dictionary ids")
	}
	core, err := other.EncodeRecord(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dictContextFromCore(frameReference(other, core)); err == nil {
		t.Fatal("unregistered dictionary id accepted")
	}
	RegisterDictionary(other)
	if _, err := dictContextFromCore(frameReference(other, core)); err != nil {
		t.Fatalf("registered dictionary id rejected: %v", err)
	}
}
